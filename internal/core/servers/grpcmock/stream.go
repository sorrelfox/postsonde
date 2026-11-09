package grpcmock

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
	"time"

	"github.com/sorrelfox/postsonde/internal/core/grpc/transport"
	"github.com/sorrelfox/postsonde/internal/core/servers"
	"github.com/sorrelfox/postsonde/internal/core/servers/httpbase"
)

// MaxRecvMessageSize is the largest request message the server reads, as
// grpc-go's default.
const MaxRecvMessageSize = 4 << 20

// errStopping ends the calls in progress when the server stops.
var errStopping = errors.New("the server is stopping")

// stream is the server side of one call: the request body read as gRPC
// frames, the response headers, messages and status written to the
// ResponseWriter. It is used by one goroutine at a time for writing;
// reading may happen on another.
type stream struct {
	w  http.ResponseWriter
	rc *http.ResponseController
	fr *transport.FrameReader
	// ctx ends with the call: the client went away, the grpc-timeout
	// passed, or the server is stopping (the cause is errStopping).
	ctx    context.Context
	cancel context.CancelCauseFunc

	// deadline is when the grpc-timeout passes; zero without one.
	deadline time.Time

	gzip bool // requests are gzip-compressed (grpc-encoding)
	// webType is the gRPC-Web content type of the request (answered with
	// the same), "" for gRPC; webText is set for grpc-web-text.
	webType string
	webText bool

	headerSent bool
	header     transport.Metadata
	// sentHeader is the header block as sent, for the traffic.
	sentHeader http.Header
}

// newStream prepares the call r. The body of a gRPC-Web call is read
// whole first: gRPC-Web is half-duplex, and an HTTP/1.1 handler cannot
// read the body once it has written the response.
// A grpc-timeout (timeout > 0) bounds the call. Call s.cancel when the
// call ends.
func newStream(w http.ResponseWriter, r *http.Request, stop context.Context, timeout time.Duration, webType string, webText bool) (*stream, error) {
	s := &stream{
		w:       w,
		rc:      http.NewResponseController(w),
		gzip:    strings.EqualFold(r.Header.Get("Grpc-Encoding"), "gzip"),
		webType: webType,
		webText: webText,
	}
	ctx, cancel := context.WithCancelCause(r.Context())
	s.ctx, s.cancel = ctx, cancel
	if timeout > 0 {
		s.deadline = time.Now().Add(timeout)
		tctx, tcancel := context.WithTimeoutCause(ctx, timeout, context.DeadlineExceeded)
		s.ctx = tctx
		s.cancel = func(cause error) {
			cancel(cause)
			tcancel()
		}
	}
	stopAfter := context.AfterFunc(stop, func() { cancel(errStopping) })
	context.AfterFunc(ctx, func() { stopAfter() })
	if webType != "" {
		body := io.Reader(r.Body)
		if webText {
			body = transport.NewWebTextReader(body)
		}
		all, err := io.ReadAll(io.LimitReader(body, 64<<20))
		if err != nil {
			return s, err
		}
		s.fr = transport.NewFrameReader(bytes.NewReader(all), MaxRecvMessageSize)
		return s, nil
	}
	s.fr = transport.NewFrameReader(r.Body, MaxRecvMessageSize)
	return s, nil
}

// recv reads the next request message; io.EOF when the client
// half-closed.
func (s *stream) recv() ([]byte, error) {
	if err := s.ctx.Err(); err != nil {
		return nil, context.Cause(s.ctx)
	}
	f, err := s.fr.Next()
	if err != nil {
		if s.ctx.Err() != nil {
			return nil, context.Cause(s.ctx)
		}
		return nil, err
	}
	if !f.Compressed {
		return f.Payload, nil
	}
	if !s.gzip {
		return nil, errors.New("a compressed message without grpc-encoding")
	}
	zr, err := gzip.NewReader(bytes.NewReader(f.Payload))
	if err != nil {
		return nil, err
	}
	b, err := io.ReadAll(io.LimitReader(zr, MaxRecvMessageSize+1))
	if err == nil && len(b) > MaxRecvMessageSize {
		return nil, &transport.FrameTooLargeError{Size: uint32(min(len(b), 1<<31)), Max: MaxRecvMessageSize}
	}
	return b, err
}

// sendHeader sends the response headers now.
func (s *stream) sendHeader() error {
	if s.headerSent {
		return nil
	}
	s.headerSent = true
	h := s.w.Header()
	s.setContentType(h)
	writeMetadata(h, "", s.header)
	s.sentHeader = h.Clone()
	s.w.WriteHeader(http.StatusOK)
	return s.rc.Flush()
}

func (s *stream) setContentType(h http.Header) {
	if s.webType != "" {
		h.Set("Content-Type", s.webType)
		return
	}
	h.Set("Content-Type", transport.ContentTypeGRPC)
}

// send writes one message, uncompressed, and flushes it.
func (s *stream) send(msg []byte) error {
	return s.sendRaw(transport.AppendFrame(nil, false, msg))
}

// sendRaw writes bytes to the body after the headers and flushes them;
// for grpc-web-text they are Base64-encoded first.
func (s *stream) sendRaw(b []byte) error {
	if err := s.sendHeader(); err != nil {
		return err
	}
	if s.webText {
		b = []byte(base64.StdEncoding.EncodeToString(b))
	}
	if _, err := s.w.Write(b); err != nil {
		return err
	}
	return s.rc.Flush()
}

// finish writes the status and trailers: in HTTP trailers, as a
// trailers-only response when no headers were sent, or in a gRPC-Web
// trailer frame. The header block as sent is kept for the traffic.
func (s *stream) finish(st transport.Status, trailers transport.Metadata) {
	status := func(h http.Header, prefix string) {
		h.Set(prefix+transport.HeaderStatus, strconv.FormatUint(uint64(st.Code), 10))
		if st.Message != "" {
			h.Set(prefix+transport.HeaderMessage, transport.EncodeStatusMessage(st.Message))
		}
		writeMetadata(h, prefix, trailers)
	}
	if s.webType != "" && s.headerSent {
		t := http.Header{}
		status(t, "")
		_ = s.sendRaw(transport.AppendTrailerFrame(nil, t))
		return
	}
	h := s.w.Header()
	if s.headerSent {
		status(h, http.TrailerPrefix)
		return
	}
	// Trailers-only: the status and the header metadata in one block.
	s.headerSent = true
	s.setContentType(h)
	writeMetadata(h, "", s.header)
	status(h, "")
	s.sentHeader = h.Clone()
	s.w.WriteHeader(http.StatusOK)
}

// wait pauses for d, or until the call ends.
func (s *stream) wait(d time.Duration) error {
	if d <= 0 {
		return s.ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-s.ctx.Done():
		return context.Cause(s.ctx)
	}
}

// writeMetadata adds md to h, "-bin" values Base64-encoded.
func writeMetadata(h http.Header, prefix string, md transport.Metadata) {
	for _, e := range md {
		v := e.Value
		if transport.IsBinaryKey(e.Key) {
			v = transport.EncodeBinaryValue([]byte(v))
		}
		h.Add(prefix+e.Key, v)
	}
}

// metadataHeaders lists md for the traffic, "-bin" values in Base64.
func metadataHeaders(md transport.Metadata) []servers.Header {
	out := make([]servers.Header, 0, len(md))
	for _, e := range md {
		v := e.Value
		if transport.IsBinaryKey(e.Key) {
			v = transport.EncodeBinaryValue([]byte(v))
		}
		out = append(out, servers.Header{Name: e.Key, Value: v})
	}
	return out
}

// sentHeaders lists the header block as sent, for the traffic.
func (s *stream) sentHeaders() []servers.Header {
	return httpbase.HeaderList(s.sentHeader)
}
