package http

import (
	"context"
	"io"
	"strconv"

	"github.com/sorrelfox/postsonde/internal/core/charset"
	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// Large response bodies (HTTP-37): streamed to the body store while they
// arrive, cut at the settings' limit, shown a page at a time.

// Codes of large bodies.
const (
	// CodeBodyLimit: the warning of a body cut at the limit
	// (ResponseDTO.Warnings).
	CodeBodyLimit = "http.body_limit"
	// CodeBodyTooLarge: an assertion on the body was not evaluated, or a
	// filter not run, because the body is over MaxEvalBodyBytes.
	CodeBodyTooLarge = "http.body_too_large"
)

// MaxEvalBodyBytes is the largest body the filter (HTTP-36) and the
// assertions on the body (TEST-01) read: both parse it in memory whole.
const MaxEvalBodyBytes = 256 << 20

// WithMaxBodySize gives the largest response body kept (the settings'
// network.maxResponseBodyMb, HTTP-37), read on every send: a longer body
// is cut there, marked truncated, with a warning. Without it, or when it
// returns 0, the limit is httpclient.DefaultMaxBodyBytes.
func WithMaxBodySize(limit func() int64) Option {
	return func(s *HTTPService) { s.maxBody = limit }
}

// maxBodyBytes returns the limit for a send starting now, 0 for the
// default.
func (s *HTTPService) maxBodyBytes() int64 {
	if s.maxBody == nil {
		return 0
	}
	return max(s.maxBody(), 0)
}

// bodyLimitMessage is the warning of a body cut at limit.
func bodyLimitMessage(limit int64) string {
	return "The body is larger than the " + formatBytes(limit) + " limit, so only its first " + formatBytes(limit) +
		" were kept. Raise the limit in Settings › Network to receive all of it."
}

// formatBytes writes n as the settings count it: "512 MiB", "1.5 GiB",
// "300 KiB", "12 bytes".
func formatBytes(n int64) string {
	unit := func(v int64, shift uint, name string) string {
		whole := v >> shift
		frac := (v - whole<<shift) * 10 >> shift
		if frac == 0 {
			return strconv.FormatInt(whole, 10) + " " + name
		}
		return strconv.FormatInt(whole, 10) + "." + strconv.FormatInt(frac, 10) + " " + name
	}
	switch {
	case n >= 1<<30:
		return unit(n, 30, "GiB")
	case n >= 1<<20:
		return unit(n, 20, "MiB")
	case n >= 1<<10:
		return unit(n, 10, "KiB")
	}
	return strconv.FormatInt(n, 10) + " bytes"
}

// tooLargeToEval says why a body of size bytes is not read whole.
func tooLargeToEval(what string, size int64) string {
	return "The body is " + formatBytes(size) + ", too large to " + what + " (at most " + formatBytes(MaxEvalBodyBytes) + ")."
}

// ReadBodyInputDTO is what ReadBody takes.
type ReadBodyInputDTO struct {
	// BodyID is ResponseDTO.BodyID.
	BodyID string `json:"bodyId"`
	// Offset is the byte of the body the page starts at; it is moved
	// forward to the next whole character (a line of 16 bytes for a hex
	// page, moved back).
	Offset int64 `json:"offset"`
	// Charset is ResponseDTO.Charset, the encoding the bytes are decoded
	// with ("" reads them as UTF-8).
	Charset string `json:"charset"`
	// Hex returns a hex dump of MaxHexBytes instead of text
	// (ResponseDTO.BodyHex).
	Hex bool `json:"hex"`
}

// BodyPageDTO is a part of a kept body, as the view shows it (HTTP-37).
type BodyPageDTO struct {
	// Text is the bytes from Start to End decoded (at most MaxBodyText of
	// them), or their hex dump (at most MaxHexBytes, offsets counted from
	// the start of the body).
	Text string `json:"text"`
	// Start and End are the byte range of the body Text shows.
	Start int64 `json:"start"`
	End   int64 `json:"end"`
	// Size is the size of the whole body.
	Size int64 `json:"size"`
	// Binary is set when some bytes are not valid in the charset (shown
	// as U+FFFD).
	Binary bool `json:"binary"`
}

// ReadBody returns a page of a kept body (ResponseDTO.BodyID) from
// in.Offset (HTTP-37): the view shows the first MaxBodyText bytes with the
// response and asks for the others a page at a time, so a body of any size
// never has to be in the view whole. Pages never split a character: a
// page ends before a character cut at MaxBodyText, and the next page
// starts at its End.
//
// Errors: http.body_gone (not_found) when the body is no longer kept,
// io when it cannot be read, http.unavailable without a body store.
func (s *HTTPService) ReadBody(ctx context.Context, in ReadBodyInputDTO) (BodyPageDTO, error) {
	if s.bodies == nil {
		return BodyPageDTO{}, errs.New(errs.Unsupported, "Response bodies are not kept.").WithCode(CodeUnavailable)
	}
	if err := ctx.Err(); err != nil {
		return BodyPageDTO{}, errs.Wrap(err, errs.KindOf(err), "Reading the body was interrupted.")
	}
	kb, r, ok, err := s.bodies.open(in.BodyID)
	if err != nil {
		return BodyPageDTO{}, errs.Wrap(err, errs.IO, "The response body could not be read.")
	}
	if !ok {
		return BodyPageDTO{}, bodyGone()
	}
	defer r.Close()
	page, err := readPage(r, kb.size, in)
	if err != nil {
		return BodyPageDTO{}, errs.Wrap(err, errs.IO, "The response body could not be read.")
	}
	return page, nil
}

// readPage reads the page of in from r, a body of size bytes.
func readPage(r io.ReadSeeker, size int64, in ReadBodyInputDTO) (BodyPageDTO, error) {
	start := min(max(in.Offset, 0), size)
	if in.Hex {
		start -= start % 16
		data, err := readAt(r, start, MaxHexBytes)
		if err != nil {
			return BodyPageDTO{}, err
		}
		return BodyPageDTO{Text: charset.HexDumpAt(data, start), Start: start, End: start + int64(len(data)), Size: size}, nil
	}
	enc := charset.Canonical(in.Charset)
	if enc == "" {
		enc = charset.UTF8
	}
	if (enc == charset.UTF16LE || enc == charset.UTF16BE) && start%2 == 1 {
		start++
	}
	data, err := readAt(r, start, MaxBodyText+4)
	if err != nil {
		return BodyPageDTO{}, err
	}
	skip := 0
	switch {
	case start == 0:
		if e, n := charset.BOM(data); e != "" {
			enc, skip = e, n
		}
	case enc == charset.UTF8:
		// Not in the middle of a character.
		for skip < len(data) && skip < 3 && data[skip]&0xC0 == 0x80 {
			skip++
		}
	case enc == charset.UTF16LE || enc == charset.UTF16BE:
		// Not on the second half of a surrogate pair.
		if len(data) >= 2 {
			hi := data[0]
			if enc == charset.UTF16LE {
				hi = data[1]
			}
			if hi >= 0xDC && hi < 0xE0 {
				skip = 2
			}
		}
	}
	start += int64(skip)
	data = data[skip:]
	if len(data) > MaxBodyText {
		data = clip(data, enc)
	}
	text, binary := charset.Decode(data, enc)
	return BodyPageDTO{Text: text, Start: start, End: start + int64(len(data)), Size: size, Binary: binary}, nil
}

// readAt reads up to n bytes of r from offset.
func readAt(r io.ReadSeeker, offset int64, n int) ([]byte, error) {
	if _, err := r.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	buf := make([]byte, n)
	got, err := io.ReadFull(r, buf)
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		err = nil
	}
	return buf[:got], err
}
