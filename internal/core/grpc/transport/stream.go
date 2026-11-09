package transport

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// Stream is one call in progress. Send and CloseSend may run concurrently
// with Recv and Header; Send must not run concurrently with itself, nor
// Recv with itself.
type Stream struct {
	ctx     context.Context
	cancel  context.CancelFunc
	address string
	sec     security
	gzip    bool
	maxRecv int

	sendMu     sync.Mutex
	pw         *io.PipeWriter // native gRPC: the request body
	web        *webCall       // gRPC-Web: the request waiting for CloseSend
	sendClosed bool

	ready chan struct{} // closed when resp or the round trip error is set
	resp  *http.Response

	recvMu   sync.Mutex
	started  bool
	fr       *FrameReader
	encoding string // grpc-encoding of the response

	mu      sync.Mutex
	done    bool
	err     error // nil: ended with OK
	status  Status
	trailer Metadata
}

// webCall is the request of a gRPC-Web call, sent whole at CloseSend.
type webCall struct {
	tr   *http.Transport
	req  *http.Request
	text bool         // grpc-web-text: the body is base64
	body bytes.Buffer // the frames sent so far
	// once starts the round trip, or ends a call cancelled before it.
	once sync.Once
}

func (s *Stream) roundTrip(tr *http.Transport, req *http.Request, stopCloser func() bool) {
	resp, err := tr.RoundTrip(req)
	if err != nil {
		stopCloser()
		sec := s.sec
		sec.roundTrip = true
		s.finish(transportErr(s.ctx, err, s.address, sec), nil)
		// Unblock a Send waiting on the pipe; RoundTrip closes the body too.
		req.Body.Close()
		close(s.ready)
		return
	}
	s.resp = resp
	context.AfterFunc(s.ctx, func() { resp.Body.Close() })
	close(s.ready)
}

// Send writes one message. It blocks until the transport has taken the
// bytes (HTTP/2 flow control applies). When the call has already ended it
// returns io.EOF; Recv then gives the reason. Messages are compressed when
// the call uses gzip.
func (s *Stream) Send(msg []byte) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if s.sendClosed {
		return errs.New(errs.InvalidArgument, "The request stream is already closed.").WithCode(CodeSendClosed)
	}
	if s.ctx.Err() != nil {
		return io.EOF // ended, cancelled or past its deadline
	}
	compressed := false
	if s.gzip {
		var b bytes.Buffer
		zw := gzip.NewWriter(&b)
		if _, err := zw.Write(msg); err != nil {
			return errs.Wrap(err, errs.Internal, "Compressing the message failed.").WithCode(CodeCompression)
		}
		if err := zw.Close(); err != nil {
			return errs.Wrap(err, errs.Internal, "Compressing the message failed.").WithCode(CodeCompression)
		}
		msg, compressed = b.Bytes(), true
	}
	if uint64(len(msg)) > MaxFramePayload {
		return errs.New(errs.InvalidArgument, "The message is larger than gRPC allows (4 GiB).").
			WithCode(CodeSendTooLarge).WithDetail("size", strconv.Itoa(len(msg)))
	}
	if s.web != nil {
		s.web.body.Write(AppendFrame(make([]byte, 0, FrameHeaderLen+len(msg)), compressed, msg))
		return nil
	}
	if _, err := s.pw.Write(AppendFrame(make([]byte, 0, FrameHeaderLen+len(msg)), compressed, msg)); err != nil {
		return io.EOF
	}
	return nil
}

// CloseSend ends the request stream (half-close). Later Sends fail.
func (s *Stream) CloseSend() error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if s.sendClosed {
		return nil
	}
	s.sendClosed = true
	if w := s.web; w != nil {
		w.once.Do(func() {
			body := w.body.Bytes()
			if w.text {
				body = []byte(base64.StdEncoding.EncodeToString(body))
			}
			w.req.Body = io.NopCloser(bytes.NewReader(body))
			w.req.ContentLength = int64(len(body))
			w.req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
			go s.roundTrip(w.tr, w.req, func() bool { return true })
		})
		return nil
	}
	return s.pw.Close()
}

// Close cancels the call if it has not ended and releases it. Recv then
// returns a Cancelled status (or the status the call ended with).
func (s *Stream) Close() {
	s.cancel()
}

// Header waits for the response headers and returns them as metadata
// (every field, lower-case keys in sorted order, "-bin" values decoded).
// It returns the call's error when the call failed before any headers.
func (s *Stream) Header() (Metadata, error) {
	<-s.ready
	if s.resp == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		return nil, s.err
	}
	return metadataFromHeader(s.resp.Header), nil
}

// Trailer returns the trailers once Recv has returned an error; nil
// before. For a trailers-only response these are the response headers.
func (s *Stream) Trailer() Metadata {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.trailer
}

// Status returns the final status once Recv has returned an error; ok is
// false before.
func (s *Stream) Status() (st Status, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status, s.done
}

// Recv reads the next message. At the end of the call it returns io.EOF
// for an OK status and otherwise an *errs.Error wrapping a *StatusError
// (StatusFromError); every later call returns the same.
func (s *Stream) Recv() ([]byte, error) {
	<-s.ready
	s.recvMu.Lock()
	defer s.recvMu.Unlock()
	if done, err := s.result(); done {
		return nil, err
	}
	if !s.started {
		s.started = true
		if s.checkHeaders() {
			_, err := s.result()
			return nil, err
		}
	}
	f, err := s.fr.Next()
	if err != nil {
		return nil, s.fail(s.frameErr(err))
	}
	if f.Trailer {
		return nil, s.webTrailer(f)
	}
	msg := f.Payload
	if f.Compressed {
		if msg, err = s.decompress(msg); err != nil {
			return nil, s.fail(err)
		}
	}
	return msg, nil
}

// checkHeaders validates the response headers. It reports true when the
// call is over: a non-gRPC response, or a trailers-only response.
func (s *Stream) checkHeaders() bool {
	h := s.resp.Header
	if s.resp.StatusCode != http.StatusOK {
		code := CodeForHTTPStatus(s.resp.StatusCode)
		msg := "unexpected HTTP status code received from server: " + strconv.Itoa(s.resp.StatusCode) + " (" + http.StatusText(s.resp.StatusCode) + ")"
		if ct, _ := headerValue(h, HeaderContentType); !validContentType(ct) {
			msg += "; transport: received unexpected content-type " + strconv.Quote(ct)
		}
		e := statusErr(Status{Code: code, Message: msg}, nil, CodeHTTPStatus,
			"The server at "+s.address+" answered with HTTP "+strconv.Itoa(s.resp.StatusCode)+" instead of a gRPC response.", HintPlaintext).
			WithDetail("http_status", strconv.Itoa(s.resp.StatusCode))
		s.fail(e)
		return true
	}
	if st, found := parseStatus(h); found {
		// Trailers-only: the headers are the trailers. Accepted with any
		// content-type, since the status is explicit.
		s.end(st, metadataFromHeader(h))
		return true
	}
	ct, _ := headerValue(h, HeaderContentType)
	if s.web != nil {
		ok, text := webContentType(ct)
		if !ok {
			st := Status{Code: Unknown, Message: "transport: received unexpected content-type " + strconv.Quote(ct)}
			s.fail(statusErr(st, nil, CodeNotGRPC, "The server at "+s.address+" did not answer with application/grpc-web.", HintWeb))
			return true
		}
		s.encoding, _ = headerValue(h, HeaderEncoding)
		body := io.Reader(s.resp.Body)
		if text {
			body = NewWebTextReader(body)
		}
		s.fr = NewWebFrameReader(body, s.maxRecv)
		return false
	}
	if !validContentType(ct) {
		st := Status{Code: Unknown, Message: "transport: received unexpected content-type " + strconv.Quote(ct)}
		s.fail(statusErr(st, nil, CodeNotGRPC, "The server at "+s.address+" did not answer with application/grpc.", HintPlaintext))
		return true
	}
	s.encoding, _ = headerValue(h, HeaderEncoding)
	s.fr = NewFrameReader(s.resp.Body, s.maxRecv)
	return false
}

// webTrailer ends a gRPC-Web call with the status in its trailer frame.
// Anything after the trailer frame is ignored.
func (s *Stream) webTrailer(f Frame) error {
	block := f.Payload
	if f.Compressed {
		var err error
		if block, err = s.decompress(block); err != nil {
			return s.fail(err)
		}
	}
	h, err := parseTrailerBlock(block)
	if err != nil {
		st := Status{Code: Internal, Message: "grpc-web: " + err.Error()}
		return s.fail(statusErr(st, err, CodeMalformedFrame, "The server sent a malformed gRPC-Web trailer frame.", ""))
	}
	st, found := parseStatus(h)
	if !found {
		st = Status{Code: Unknown, Message: "server sent trailers without grpc-status"}
		e := statusErr(st, nil, CodeMissingStatus, "The server ended the call without a gRPC status.", "")
		s.finish(e, metadataFromHeader(h))
		return e
	}
	s.end(st, metadataFromHeader(h))
	_, err = s.result()
	return err
}

// frameErr maps an error from reading the next frame; io.EOF is the end
// of the body, where the trailers decide.
func (s *Stream) frameErr(err error) error {
	var tooLarge *FrameTooLargeError
	var badFlag *FrameFlagError
	switch {
	case errors.Is(err, io.EOF) && s.ctx.Err() == nil:
		return s.endOfBody()
	case errors.As(err, &tooLarge):
		return s.tooLarge(int64(tooLarge.Size))
	case errors.As(err, &badFlag):
		st := Status{Code: Internal, Message: badFlag.Error()}
		return statusErr(st, err, CodeMalformedFrame, "The server sent a malformed gRPC message.", "")
	case errors.Is(err, errWebText) && s.ctx.Err() == nil:
		st := Status{Code: Internal, Message: err.Error()}
		return statusErr(st, err, CodeMalformedFrame, "The server sent a gRPC-Web text body that is not valid base64.", "")
	case errors.Is(err, ErrFrameTruncated) && s.ctx.Err() == nil:
		// A truncated frame at a clean end of the body: the server ended
		// the stream mid-message.
		st := Status{Code: Internal, Message: "stream ended in the middle of a message"}
		return statusErr(st, err, CodeMalformedFrame, "The server ended the stream in the middle of a message.", "")
	}
	return transportErr(s.ctx, err, s.address, s.sec)
}

func (s *Stream) tooLarge(size int64) error {
	st := Status{Code: ResourceExhausted, Message: "grpc: received message larger than max (" + strconv.FormatInt(size, 10) + " vs. " + strconv.Itoa(s.maxRecv) + ")"}
	return statusErr(st, nil, CodeRecvTooLarge,
		"The server sent a message of "+strconv.FormatInt(size, 10)+" bytes, over the limit of "+strconv.Itoa(s.maxRecv)+" bytes.", HintTooLarge).
		WithDetail("size", strconv.FormatInt(size, 10)).
		WithDetail("max", strconv.Itoa(s.maxRecv))
}

// endOfBody reads the status from the trailers once the body has ended.
func (s *Stream) endOfBody() error {
	tr := s.resp.Trailer
	st, found := parseStatus(tr)
	if !found {
		if countValues(tr) == 0 {
			st = Status{Code: Internal, Message: "server closed the stream without sending trailers"}
		} else {
			st = Status{Code: Unknown, Message: "server sent trailers without grpc-status"}
		}
		e := statusErr(st, nil, CodeMissingStatus, "The server ended the call without a gRPC status.", "")
		s.finish(e, metadataFromHeader(tr))
		return e
	}
	s.end(st, metadataFromHeader(tr))
	_, err := s.result()
	return err
}

func (s *Stream) decompress(msg []byte) ([]byte, error) {
	enc := strings.ToLower(strings.TrimSpace(s.encoding))
	if enc != CompressionGzip {
		if enc != "" && enc != CompressionIdentity {
			text := "grpc: decompressor is not installed for grpc-encoding " + strconv.Quote(s.encoding)
			return nil, statusErr(Status{Code: Unimplemented, Message: text}, nil, CodeUnsupportedEncoding,
				"The server compressed its response with "+strconv.Quote(s.encoding)+", which PostSonde cannot decompress (it supports gzip).", HintUnsupportedEncoding).
				WithDetail("encoding", s.encoding)
		}
		text := "grpc: compressed flag set with identity or empty encoding"
		return nil, statusErr(Status{Code: Internal, Message: text}, nil, CodeCompression,
			"The server sent a compressed message that cannot be decompressed ("+text+").", "")
	}
	zr, err := gzip.NewReader(bytes.NewReader(msg))
	if err == nil {
		r := io.Reader(zr)
		if s.maxRecv >= 0 {
			r = io.LimitReader(zr, int64(s.maxRecv)+1)
		}
		var out []byte
		if out, err = io.ReadAll(r); err == nil {
			if s.maxRecv >= 0 && len(out) > s.maxRecv {
				return nil, s.tooLarge(int64(len(out)))
			}
			return out, nil
		}
	}
	st := Status{Code: Internal, Message: "grpc: failed to decompress the received message: " + err.Error()}
	return nil, statusErr(st, err, CodeCompression, "The server sent a gzip message that could not be decompressed.", "")
}

// end finishes the call with a status from the server.
func (s *Stream) end(st Status, trailer Metadata) {
	if st.OK() {
		s.finish(nil, trailer)
		return
	}
	s.finish(serverStatusErr(st), trailer)
}

// fail finishes the call with err and returns the recorded error.
func (s *Stream) fail(err error) error {
	s.finish(err, nil)
	_, err = s.result()
	return err
}

// finish records the outcome once; the first one wins. It cancels the
// call, which resets the HTTP/2 stream if it is still open.
func (s *Stream) finish(err error, trailer Metadata) {
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		return
	}
	s.done = true
	s.err = err
	s.status = StatusFromError(err)
	s.trailer = trailer
	s.mu.Unlock()
	s.cancel()
}

func (s *Stream) result() (done bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.done {
		return false, nil
	}
	if s.err == nil {
		return true, io.EOF
	}
	return true, s.err
}

func countValues(h http.Header) int {
	n := 0
	for _, vv := range h {
		n += len(vv)
	}
	return n
}
