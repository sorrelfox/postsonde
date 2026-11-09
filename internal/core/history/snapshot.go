package history

import (
	"encoding/base64"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/httpclient"
)

// MaxBody is how much of each body an entry keeps: the first 64 KiB.
const MaxBody = 64 << 10

// Snapshot returns the snapshot of a body whose first bytes are raw and
// whose full length is size, with the secrets m knows masked (the whole of
// raw is masked before it is clipped to MaxBody, so a secret is never cut
// in half and half kept). masked reports whether anything was hidden. A
// nil m masks nothing.
func Snapshot(raw []byte, size int64, m httpclient.Masker) (b Body, masked bool) {
	if size < int64(len(raw)) {
		size = int64(len(raw))
	}
	data := raw
	if m != nil && len(raw) > 0 {
		if s := m.MaskText(string(raw)); s != string(raw) {
			data, masked = []byte(s), true
		}
	}
	b.Size = size
	if utf8.Valid(data) {
		shown := clipUTF8(data, MaxBody)
		b.Text = string(shown)
		b.Clipped = len(shown) < len(data) || int64(len(raw)) < size
		return b, masked
	}
	shown := data[:min(len(data), MaxBody)]
	b.Text, b.Base64 = base64.StdEncoding.EncodeToString(shown), true
	b.Clipped = len(shown) < len(data) || int64(len(raw)) < size
	return b, masked
}

// Headers converts header fields, their values masked with m (nil masks
// nothing); masked reports whether anything was hidden. Disabled rows
// (httpclient.Header.Enabled false) are left out when enabledOnly is set.
func Headers(hs []httpclient.Header, m httpclient.Masker, enabledOnly bool) (out []Header, masked bool) {
	out = make([]Header, 0, len(hs))
	for _, h := range hs {
		if enabledOnly && !h.Enabled {
			continue
		}
		v := h.Value
		if m != nil {
			v = m.MaskHeader(h.Name, h.Value)
		}
		masked = masked || v != h.Value
		out = append(out, Header{Name: h.Name, Value: v})
	}
	return out, masked
}

// clipUTF8 cuts b to at most n bytes without splitting a UTF-8 sequence.
func clipUTF8(b []byte, n int) []byte {
	if len(b) <= n {
		return b
	}
	b = b[:n]
	for i := len(b) - 1; i >= 0 && i >= len(b)-utf8.UTFMax; i-- {
		if utf8.RuneStart(b[i]) {
			if !utf8.FullRune(b[i:]) {
				b = b[:i]
			}
			break
		}
	}
	return b
}
