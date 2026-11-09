package transport

import (
	"bufio"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
)

// Protocol is how a call goes on the wire (CallOptions.Protocol).
type Protocol string

// Protocols.
const (
	// ProtocolGRPC is gRPC over HTTP/2 (h2c or TLS with ALPN h2). The empty
	// Protocol means the same.
	ProtocolGRPC Protocol = "grpc"
	// ProtocolGRPCWeb is gRPC-Web with binary framing
	// (application/grpc-web+proto) over HTTP/1.1, or HTTP/2 when TLS
	// negotiates it. The trailers come in a trailer frame.
	ProtocolGRPCWeb Protocol = "grpc-web"
	// ProtocolGRPCWebText is gRPC-Web with the body in base64
	// (application/grpc-web-text+proto).
	ProtocolGRPCWebText Protocol = "grpc-web-text"
)

// Web reports whether p is one of the gRPC-Web protocols.
func (p Protocol) Web() bool { return p == ProtocolGRPCWeb || p == ProtocolGRPCWebText }

// gRPC-Web content types and headers.
const (
	ContentTypeGRPCWeb     = "application/grpc-web"
	ContentTypeGRPCWebText = "application/grpc-web-text"
	HeaderAccept           = "accept"
	HeaderXGRPCWeb         = "x-grpc-web"
)

// webContentType reports whether ct is a gRPC-Web content type
// (application/grpc-web or application/grpc-web-text, optionally with
// "+subtype" or ";params"), and whether it is the text (base64) one.
func webContentType(ct string) (ok, text bool) {
	if rest, found := cutPrefixFold(ct, ContentTypeGRPCWebText); found && (rest == "" || rest[0] == '+' || rest[0] == ';') {
		return true, true
	}
	if rest, found := cutPrefixFold(ct, ContentTypeGRPCWeb); found && (rest == "" || rest[0] == '+' || rest[0] == ';') {
		return true, false
	}
	return false, false
}

// WebContentType reports whether ct is a gRPC-Web content type, and
// whether it is the text (base64) one; for servers.
func WebContentType(ct string) (ok, text bool) { return webContentType(ct) }

// errWebText is wrapped by the errors of a base64 body that does not decode.
var errWebText = errors.New("grpc-web-text body is not valid base64")

// NewWebTextReader decodes a grpc-web-text body: standard base64, where
// every write of the sender may be padded on its own, so padding may
// appear in the middle; line breaks and spaces are skipped. Invalid input
// is an error wrapping a sentinel the transport maps to a malformed
// message.
func NewWebTextReader(r io.Reader) io.Reader {
	return &webTextReader{r: bufio.NewReader(r)}
}

type webTextReader struct {
	r   *bufio.Reader
	q   [4]byte // the quantum being collected
	n   int     // bytes in q
	out [3]byte // decoded bytes not yet returned
	o   []byte
	err error
}

func (w *webTextReader) Read(p []byte) (int, error) {
	n := 0
	for n < len(p) {
		if len(w.o) > 0 {
			c := copy(p[n:], w.o)
			w.o = w.o[c:]
			n += c
			continue
		}
		if w.err != nil {
			break
		}
		// Return what is decoded before blocking for more input.
		if n > 0 && w.r.Buffered() == 0 {
			break
		}
		b, err := w.r.ReadByte()
		if err != nil {
			w.err = err
			if err == io.EOF && w.n > 0 {
				// An unpadded last quantum.
				k, derr := base64.RawStdEncoding.Decode(w.out[:], w.q[:w.n])
				w.n = 0
				if derr != nil {
					w.err = errors.Join(errWebText, derr)
				} else {
					w.o = w.out[:k]
					w.err = io.EOF
				}
			}
			continue
		}
		switch b {
		case ' ', '\t', '\r', '\n':
			continue
		}
		w.q[w.n] = b
		w.n++
		if w.n == 4 {
			k, derr := base64.StdEncoding.Decode(w.out[:], w.q[:])
			w.n = 0
			if derr != nil {
				w.err = errors.Join(errWebText, derr)
				continue
			}
			w.o = w.out[:k]
		}
	}
	if n > 0 {
		return n, nil
	}
	return 0, w.err
}

// AppendTrailerFrame appends a gRPC-Web trailer frame holding h: one
// "key: value\r\n" line per value, keys in lower case and sorted.
func AppendTrailerFrame(dst []byte, h http.Header) []byte {
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
	var block []byte
	for _, k := range keys {
		for _, v := range h[k] {
			block = append(block, strings.ToLower(k)...)
			block = append(block, ": "...)
			block = append(block, v...)
			block = append(block, "\r\n"...)
		}
	}
	dst = AppendFrame(dst, false, block)
	dst[len(dst)-len(block)-FrameHeaderLen] |= flagTrailer
	return dst
}

// parseTrailerBlock reads the header lines of a gRPC-Web trailer frame.
// Lines end in CRLF (a bare LF is accepted); empty lines are skipped.
func parseTrailerBlock(b []byte) (http.Header, error) {
	h := http.Header{}
	for line := range strings.SplitSeq(string(b), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		k = strings.TrimSpace(k)
		if !ok || k == "" || strings.ContainsAny(k, " \t") {
			return nil, errors.New("malformed trailer line " + quoteShort(line))
		}
		h.Add(http.CanonicalHeaderKey(k), strings.TrimSpace(v))
	}
	return h, nil
}

func quoteShort(s string) string {
	if len(s) > 60 {
		s = s[:60]
	}
	return strconv.Quote(s)
}
