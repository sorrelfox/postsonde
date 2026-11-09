package incremental

import (
	"bytes"
)

// multipartParser splits a multipart/mixed body into its parts' bodies as
// the bytes arrive. A part is complete as soon as the delimiter after it
// is in: servers send "\r\n--boundary" right after each payload, so the
// payload is shown before the next one starts (mime/multipart would wait
// for the bytes after the delimiter). Lines may end with CRLF or LF.
type multipartParser struct {
	delim []byte // "\n--" + boundary
	buf   []byte
	// inPart is set once the first delimiter was seen: bytes before it
	// are the preamble.
	inPart bool
	// atDelim is set when buf starts right after a delimiter, before it
	// is known whether it is the close delimiter.
	atDelim bool
	// closed is set after the close delimiter: the rest is the epilogue.
	closed bool
	// scan is where the search for the next delimiter goes on, so a
	// large part arriving in many chunks is scanned once.
	scan int
}

func newMultipartParser(boundary string) *multipartParser {
	// A leading "\n" lets the first delimiter, at the very start of the
	// body, match like the others.
	return &multipartParser{delim: []byte("\n--" + boundary), buf: []byte("\n")}
}

// feed adds bytes and returns the bodies of the parts they completed.
func (p *multipartParser) feed(chunk []byte) [][]byte {
	if p.closed {
		return nil
	}
	p.buf = append(p.buf, chunk...)
	var out [][]byte
	for {
		if p.atDelim {
			if len(p.buf) < 2 {
				return out
			}
			if p.buf[0] == '-' && p.buf[1] == '-' {
				p.closed, p.buf = true, nil
				return out
			}
			p.atDelim, p.inPart = false, true
		}
		i := bytes.Index(p.buf[p.scan:], p.delim)
		if i < 0 {
			p.scan = max(0, len(p.buf)-len(p.delim)+1)
			return out
		}
		i += p.scan
		p.scan = 0
		if p.inPart {
			if body, ok := partBody(p.buf[:i]); ok {
				out = append(out, bytes.Clone(body))
			}
		}
		// Copied, so the parts before are not kept alive.
		p.buf = bytes.Clone(p.buf[i+len(p.delim):])
		p.atDelim = true
	}
}

// close ends the body: a part that was not followed by a delimiter is
// returned when it holds something.
func (p *multipartParser) close() [][]byte {
	if p.closed {
		return nil
	}
	p.closed = true
	buf := p.buf
	p.buf = nil
	if !p.inPart || p.atDelim {
		return nil
	}
	if body, ok := partBody(buf); ok {
		return [][]byte{body}
	}
	return nil
}

// partBody returns the body of a part: the text after the delimiter's
// line and the header lines, up to the blank line; without a blank line
// the whole text counts as the body when it looks like JSON. Parts
// without a body (whitespace only) are skipped.
func partBody(seg []byte) ([]byte, bool) {
	// The rest of the delimiter line (transport padding).
	nl := bytes.IndexByte(seg, '\n')
	if nl < 0 {
		return nil, false
	}
	rest := seg[nl+1:]
	body := rest
	for {
		nl := bytes.IndexByte(rest, '\n')
		line := rest
		if nl >= 0 {
			line = rest[:nl]
		}
		if len(bytes.TrimRight(line, "\r")) == 0 {
			if nl < 0 {
				body = nil
			} else {
				body = rest[nl+1:]
			}
			break
		}
		if nl < 0 || bytes.HasPrefix(bytes.TrimSpace(line), []byte("{")) {
			// No blank line: a part without headers.
			break
		}
		rest = rest[nl+1:]
	}
	body = bytes.TrimRight(body, "\r")
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, false
	}
	return body, true
}
