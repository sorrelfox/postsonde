package httpclient

import (
	"bytes"
	"errors"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// errHeadWritten stops Preview's write once the header block is complete,
// so the body is not read to its end.
var errHeadWritten = errors.New("httpclient: head written")

// Preview returns the header fields Send would write for r on HTTP/1.1,
// in the order it would write them, without connecting (HTTP-09): the
// request is prepared by the code Send uses (so a Host row, the body's
// Content-Length or Transfer-Encoding, its Content-Type and URL userinfo
// apply as they do on a send), jar's cookies are added as Send adds them
// (unless r.Settings.SkipSendingCookies; nil jar adds none), and the head
// is written by net/http's request writer, the one the transport uses.
// The body is opened, and read at most up to a few KiB, then closed.
//
// What it cannot show: the Authorization field of Digest auth (computed
// from the server's challenge), the headers of redirect hops, and the
// HTTP/2 form (lower-case names, pseudo-header fields). It fails as Send
// fails before connecting (an invalid URL, header or body).
func Preview(r Request, jar CookieJar) ([]Header, error) {
	p, err := prepare(r)
	if err != nil {
		return nil, err
	}
	req := p.req
	if jar != nil && !p.settings.SkipSendingCookies {
		req = addJarCookies(req, &sendState{settings: p.settings, jar: jar})
	}
	w := &headWriter{}
	err = req.Write(w)
	if req.Body != nil {
		// Write closes the body; a second Close is harmless and covers
		// an early error.
		_ = req.Body.Close()
	}
	if !w.done {
		if err == nil {
			err = errors.New("httpclient: incomplete request head")
		}
		return nil, errs.Wrap(err, errs.KindOf(err), "The request to "+p.host+" could not be written.")
	}
	return parseHead(w.buf.Bytes()), nil
}

// headWriter keeps what is written up to the blank line that ends the
// header block, then fails the write.
type headWriter struct {
	buf  bytes.Buffer
	done bool
}

func (w *headWriter) Write(b []byte) (int, error) {
	if w.done {
		return 0, errHeadWritten
	}
	w.buf.Write(b)
	if i := bytes.Index(w.buf.Bytes(), []byte("\r\n\r\n")); i >= 0 {
		w.buf.Truncate(i + 2)
		w.done = true
		return len(b), errHeadWritten
	}
	return len(b), nil
}

// parseHead returns the fields of a request head: the lines after the
// request line, each "Name: value".
func parseHead(head []byte) []Header {
	lines := strings.Split(strings.TrimSuffix(string(head), "\r\n"), "\r\n")
	out := make([]Header, 0, len(lines))
	for _, line := range lines[1:] {
		name, value, _ := strings.Cut(line, ":")
		out = append(out, Header{Name: name, Value: strings.TrimPrefix(value, " "), Enabled: true})
	}
	return out
}
