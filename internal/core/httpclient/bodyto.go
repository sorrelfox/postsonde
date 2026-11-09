package httpclient

import (
	"bufio"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// CodeBodyWriteFailed: SendTo could not write the response body to its
// target (a full disk, a removed directory). The kind comes from the
// cause, io when it has none.
const CodeBodyWriteFailed = "httpclient.body_write_failed"

// ProgressStep is how many body bytes SendTo reads between two calls of
// BodyTarget.Progress.
const ProgressStep = 256 << 10

// Content codings SendTo removes (BodyTarget.Decode, Response.Encoding).
const (
	EncodingGzip    = "gzip"
	EncodingDeflate = "deflate"
)

// BodyTarget is where SendTo writes a response body while it arrives
// (HTTP-37), instead of keeping it in memory as Send does.
type BodyTarget struct {
	// Open is called once, when the final response's headers are in,
	// with the response without its body (as a StreamFunc's head: Proto,
	// StatusCode, Status, Headers, ContentLength and URL). It returns the
	// writer of the body. An error stops the send: SendTo returns it as
	// it is, with a nil response. Required.
	Open func(head *Response) (io.Writer, error)
	// Decode removes a gzip, x-gzip or deflate Content-Encoding while
	// receiving: the writer gets the decompressed bytes. Other codings
	// (br, zstd, several stacked) are written as received, with
	// Response.Encoding "".
	Decode bool
	// Progress, when set, is called from the sending goroutine with the
	// number of body bytes received so far, as sent (compressed), about
	// every ProgressStep bytes and once at the end.
	Progress func(received int64)
}

// SendTo sends r as Send does and writes the response body to the writer
// t.Open returns while it arrives, so a body of any size needs only a
// small buffer (HTTP-37). Settings.Timeout covers the whole body, as for
// Send.
//
// Settings.MaxBodyBytes applies to the bytes written (decompressed with
// t.Decode): once the next byte would pass it, reading stops, and the
// response has BodyTruncated set and Size the bytes received until then.
// The response's Body is empty; DecodedSize counts the bytes written,
// Encoding names the coding removed. A body whose decompression fails
// part way keeps what was decoded, with DecodeError saying why; it is not
// an error of the send.
//
// Errors are Send's, and CodeBodyWriteFailed when the writer fails. When
// the send fails after t.Open, the writer has part of the body: the
// caller discards it.
func (c *Client) SendTo(ctx context.Context, r Request, t BodyTarget) (*Response, error) {
	if t.Open == nil {
		return nil, errs.New(errs.InvalidArgument, "SendTo needs a body target.")
	}
	return c.send(ctx, r, nil, &t)
}

// bodyResult is what copyBody did.
type bodyResult struct {
	// size is the bytes received, written the bytes written.
	size, written int64
	truncated     bool
	encoding      string
	decodeErr     string
}

// errBodyFull stops the copy at the body limit.
var errBodyFull = errors.New("httpclient: body limit reached")

// copyBody copies resp's body to w, decoded when t.Decode says so, up to
// limit bytes written (no limit if limit < 0). readErr is a failed read of
// the body (to map as Send does), writeErr a failed write.
func copyBody(w io.Writer, resp *http.Response, t *BodyTarget, limit int64) (res bodyResult, readErr, writeErr error) {
	wire := &wireReader{r: resp.Body, progress: t.Progress}
	defer wire.report()
	var src io.Reader = wire
	if t.Decode {
		if enc := codingOf(resp.Header); enc != "" {
			dec, err := newDecoder(enc, wire)
			switch {
			case wire.err != nil:
				return bodyResult{size: wire.n}, wire.err, nil
			case errors.Is(err, io.EOF):
				// An empty body (a HEAD or 304 answer) with the header.
				return bodyResult{size: wire.n, encoding: enc}, nil, nil
			case err != nil:
				// Not in the coding it claims: kept as received.
				res.decodeErr = decodeMessage(enc, err)
				src = io.MultiReader(wire.replay(), wire)
			default:
				wire.recorded = nil
				res.encoding = enc
				src = dec
			}
		}
	}
	dst := &limitWriter{w: w, remain: limit}
	buf := make([]byte, 64<<10)
	_, err := io.CopyBuffer(dst, src, buf)
	res.size, res.written = wire.n, dst.n
	switch {
	case errors.Is(err, errBodyFull):
		res.truncated = true
	case wire.err != nil:
		return res, wire.err, nil
	case dst.err != nil:
		return res, nil, dst.err
	case err != nil:
		// The decoder failed on bytes that arrived fine.
		res.decodeErr = decodeMessage(res.encoding, err)
	}
	return res, nil, nil
}

// codingOf returns the one content coding of h that SendTo decodes, or "".
func codingOf(h http.Header) string {
	values := h.Values("Content-Encoding")
	if len(values) != 1 {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(values[0])) {
	case "gzip", "x-gzip":
		return EncodingGzip
	case "deflate":
		return EncodingDeflate
	}
	return ""
}

// newDecoder returns the reader that decodes enc from r. "deflate" is
// zlib data (RFC 9110 section 8.4.1.2), but some servers send raw deflate:
// both are read.
func newDecoder(enc string, r *wireReader) (io.Reader, error) {
	r.record = true
	defer func() { r.record = false }()
	if enc == EncodingGzip {
		return gzip.NewReader(r)
	}
	br := bufio.NewReader(r)
	head, err := br.Peek(2)
	if len(head) == 0 && err != nil {
		return nil, err
	}
	if len(head) == 2 && head[0]&0x0F == 8 && (uint16(head[0])<<8|uint16(head[1]))%31 == 0 {
		return zlib.NewReader(br)
	}
	return flate.NewReader(br), nil
}

// decodeMessage says why decoding enc stopped.
func decodeMessage(enc string, err error) string {
	if enc == "" {
		enc = "compressed"
	}
	msg := err.Error()
	if errors.Is(err, io.ErrUnexpectedEOF) {
		msg = "the data ends early"
	}
	return "The " + enc + " body could not be decompressed: " + msg + "."
}

// wireReader reads the body as received: it counts the bytes, reports
// progress and keeps the read error apart from the decoder's. While
// record is set it keeps what it read, so a body that is not in its
// coding can be written as received.
type wireReader struct {
	r        io.Reader
	n        int64
	err      error
	progress func(int64)
	reported int64
	record   bool
	recorded []byte
}

func (w *wireReader) Read(p []byte) (int, error) {
	n, err := w.r.Read(p)
	w.n += int64(n)
	if w.record {
		w.recorded = append(w.recorded, p[:n]...)
	}
	if err != nil && !errors.Is(err, io.EOF) {
		w.err = err
	}
	if w.progress != nil && w.n-w.reported >= ProgressStep {
		w.reported = w.n
		w.progress(w.n)
	}
	return n, err
}

// replay returns the bytes read while recording, once.
func (w *wireReader) replay() io.Reader {
	b := w.recorded
	w.recorded = nil
	return bytes.NewReader(b)
}

// report sends the final progress.
func (w *wireReader) report() {
	if w.progress != nil && w.n != w.reported {
		w.reported = w.n
		w.progress(w.n)
	}
}

// limitWriter writes up to remain bytes (any number if remain < 0), then
// fails with errBodyFull. err is the underlying writer's error.
type limitWriter struct {
	w      io.Writer
	remain int64
	n      int64
	err    error
}

func (l *limitWriter) Write(p []byte) (int, error) {
	full := false
	if l.remain >= 0 && int64(len(p)) > l.remain {
		p, full = p[:l.remain], true
	}
	n, err := l.w.Write(p)
	l.n += int64(n)
	if l.remain >= 0 {
		l.remain -= int64(n)
	}
	if err != nil {
		l.err = err
		return n, err
	}
	if full {
		return n, errBodyFull
	}
	return n, nil
}

// writeFailed is the error of a body the target could not take.
func writeFailed(err error) error {
	kind := errs.KindOf(err)
	if kind == errs.Internal {
		kind = errs.IO
	}
	return errs.Wrap(err, kind, "The response body could not be written: "+err.Error()+".").
		WithCode(CodeBodyWriteFailed).
		WithHint("Check that the disk is not full.")
}
