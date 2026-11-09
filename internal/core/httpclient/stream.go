package httpclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync/atomic"
)

// StreamFunc reads a response body as it arrives, for SendStream. head is
// the final response without its body: Proto, StatusCode, Status, Headers,
// ContentLength and URL are set, the rest is empty. body yields the body
// bytes as received (never decompressed) and ends with io.EOF; any other
// error from it is an *errs.Error as Send returns for the body (a
// cancelled or broken stream). fn must return when body fails; what it
// returns is SendStream's error.
type StreamFunc func(head *Response, body io.Reader) error

// SendStream sends r as Send does and hands the response body to fn as it
// arrives, instead of reading it whole: for event streams (SSE-01) and
// other responses that do not end on their own. Settings.Timeout bounds
// connecting, sending and waiting for the response headers only; once
// they are in, the body is read until it ends, fn returns or ctx is
// cancelled. Settings.MaxBodyBytes does not apply.
//
// fn is called for a response of any status. SendStream returns the
// response once fn has returned, with an empty Body, Size the number of
// body bytes fn read and the timing and timeline of the whole exchange,
// together with fn's error. When the request fails before the response
// headers, fn is not called and the error is as Send's, with a nil
// response.
func (c *Client) SendStream(ctx context.Context, r Request, fn StreamFunc) (*Response, error) {
	if fn == nil {
		fn = func(*Response, io.Reader) error { return nil }
	}
	return c.send(ctx, r, fn, nil)
}

// stream runs fn on the body of resp and returns the number of body bytes
// read and fn's error.
func (c *Client) stream(ctx context.Context, resp *http.Response, host string, fn StreamFunc) (int64, error) {
	head := streamHead(resp)
	body := &streamBody{ctx: ctx, r: resp.Body, host: host}
	err := fn(head, body)
	return body.n.Load(), err
}

// streamHead is resp without its body, for a StreamFunc or a BodyTarget.
func streamHead(resp *http.Response) *Response {
	return &Response{
		Proto:         resp.Proto,
		StatusCode:    resp.StatusCode,
		Status:        resp.Status,
		Headers:       headerList(resp.Header),
		Body:          []byte{},
		ContentLength: resp.ContentLength,
		URL:           displayURL(resp.Request.URL),
	}
}

// streamBody is a response body handed to a StreamFunc: it counts the
// bytes read and maps read errors as Send does.
type streamBody struct {
	ctx  context.Context
	r    io.Reader
	host string
	n    atomic.Int64
}

func (b *streamBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	b.n.Add(int64(n))
	if err != nil && !errors.Is(err, io.EOF) {
		return n, mapError(b.ctx, err, stageBody, b.host)
	}
	return n, err
}
