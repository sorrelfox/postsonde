package http

import (
	"bytes"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

func TestPreviewType(t *testing.T) {
	t.Parallel()
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	for _, c := range []struct {
		name, ct string
		raw      []byte
		want     string
	}{
		{"png header", "image/png", png, previewPNG},
		{"jpeg header with params", "image/jpeg; q=1", []byte{0xff, 0xd8, 0xff}, previewJPEG},
		{"svg header", "image/svg+xml", []byte("<svg/>"), previewSVG},
		{"ico header", "image/vnd.microsoft.icon", []byte{0, 0, 1, 0}, previewICO},
		{"html header", "text/html; charset=utf-8", []byte("<p>hi"), previewHTML},
		{"xhtml header", "application/xhtml+xml", []byte("<html/>"), previewHTML},
		{"no preview for tiff", "image/tiff", png, ""},
		{"no preview for json", "application/json", []byte("{}"), ""},
		{"sniffed png", "", png, previewPNG},
		{"sniffed png behind octet-stream", "application/octet-stream", png, previewPNG},
		{"sniffed gif", "", []byte("GIF89a......"), previewGIF},
		{"sniffed webp", "", []byte("RIFF\x00\x00\x00\x00WEBPVP8 "), previewWebP},
		{"sniffed bmp", "", []byte("BM\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00"), previewBMP},
		{"sniffed ico", "", []byte{0, 0, 1, 0, 1, 0}, previewICO},
		{"sniffed svg", "", []byte("  <svg xmlns='x'/>"), previewSVG},
		{"sniffed svg after a declaration", "", []byte("<?xml version='1.0'?>\n<svg/>"), previewSVG},
		{"plain xml is not svg", "", []byte("<?xml version='1.0'?><a/>"), ""},
		{"sniffed html", "", []byte("\xef\xbb\xbf<!DOCTYPE html><title>x"), previewHTML},
		{"text", "", []byte("hello"), ""},
	} {
		out := ResponseDTO{ContentType: c.ct}
		setBody(&out, c.raw)
		if out.PreviewType != c.want {
			t.Errorf("%s: PreviewType = %q, want %q", c.name, out.PreviewType, c.want)
		}
	}
}

func TestPreviewBody(t *testing.T) {
	t.Parallel()
	s := New(nil, nil, nil, nil, nil, nil, WithSaveBody(t.TempDir(), nil))
	img := []byte("\x89PNG\r\n\x1a\n\x00\xff binary")
	id := s.bodies.put(img, "x.png")
	p, err := s.PreviewBody(t.Context(), PreviewBodyInputDTO{BodyID: id})
	if err != nil {
		t.Fatal(err)
	}
	got, err := base64.StdEncoding.DecodeString(p.Data)
	if err != nil || !bytes.Equal(got, img) || p.Size != int64(len(img)) {
		t.Errorf("preview = %+v (%v)", p, err)
	}

	// A body over the limit (spooled to disk) is refused, not read.
	big := s.bodies.put(make([]byte, MaxPreviewBytes+1), "big.bin")
	_, err = s.PreviewBody(t.Context(), PreviewBodyInputDTO{BodyID: big})
	var e *errs.Error
	errors.As(err, &e)
	checkErr(t, e, errs.InvalidArgument, CodePreviewTooLarge)

	_, err = s.PreviewBody(t.Context(), PreviewBodyInputDTO{BodyID: "gone"})
	e = nil
	errors.As(err, &e)
	checkErr(t, e, errs.NotFound, CodeBodyGone)

	_, err = New(nil, nil, nil, nil, nil, nil).PreviewBody(t.Context(), PreviewBodyInputDTO{BodyID: id})
	e = nil
	errors.As(err, &e)
	checkErr(t, e, errs.Unsupported, CodeUnavailable)
}
