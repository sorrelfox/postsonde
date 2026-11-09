package http

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	nethttp "net/http"

	"github.com/sorrelfox/postsonde/internal/core/charset"
	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// The image and HTML preview of a response body (HTTP-35, PRV-04): the
// UI renders images from the bytes PreviewBody returns (a blob: URL) and
// HTML in a sandboxed frame without scripts or network access
// (docs/frontend.md#response-preview, docs/privacy.md).

// Preview types (ResponseDTO.PreviewType).
const (
	previewPNG  = "image/png"
	previewJPEG = "image/jpeg"
	previewGIF  = "image/gif"
	previewWebP = "image/webp"
	previewSVG  = "image/svg+xml"
	previewBMP  = "image/bmp"
	previewICO  = "image/x-icon"
	previewHTML = "text/html"
)

// MaxPreviewBytes is the largest body PreviewBody returns: the view holds
// the bytes whole (in a Blob), so a larger image is not previewed.
const MaxPreviewBytes = 16 << 20

// CodePreviewTooLarge: PreviewBody's body is over MaxPreviewBytes.
const CodePreviewTooLarge = "http.preview_too_large"

// previewMediaTypes maps the media types a preview is offered for to
// their preview type.
var previewMediaTypes = map[string]string{
	"image/png":                previewPNG,
	"image/apng":               previewPNG,
	"image/jpeg":               previewJPEG,
	"image/jpg":                previewJPEG,
	"image/pjpeg":              previewJPEG,
	"image/gif":                previewGIF,
	"image/webp":               previewWebP,
	"image/svg+xml":            previewSVG,
	"image/bmp":                previewBMP,
	"image/x-bmp":              previewBMP,
	"image/x-ms-bmp":           previewBMP,
	"image/x-icon":             previewICO,
	"image/vnd.microsoft.icon": previewICO,
	"image/ico":                previewICO,
	"text/html":                previewHTML,
	"application/xhtml+xml":    previewHTML,
}

// previewType returns the preview type of a body with media type mt
// ("" without a Content-Type) whose first bytes are raw: from the media
// type, else sniffed from the bytes (also for the generic
// application/octet-stream); "" when there is no preview for it.
func previewType(mt string, raw []byte) string {
	if p, ok := previewMediaTypes[mt]; ok {
		return p
	}
	if mt != "" && mt != "application/octet-stream" {
		return ""
	}
	return sniffPreview(raw)
}

// sniffPreview recognises an image or HTML body by its first bytes.
func sniffPreview(raw []byte) string {
	head := raw[:min(len(raw), 512)]
	switch nethttp.DetectContentType(head) {
	case "image/png":
		return previewPNG
	case "image/jpeg":
		return previewJPEG
	case "image/gif":
		return previewGIF
	case "image/webp":
		return previewWebP
	case "image/x-icon":
		return previewICO
	case "image/bmp":
		return previewBMP
	}
	_, n := charset.BOM(head)
	text := bytes.ToLower(bytes.TrimLeft(head[n:], " \t\r\n"))
	switch {
	case bytes.HasPrefix(text, []byte("<svg")):
		return previewSVG
	case bytes.HasPrefix(text, []byte("<?xml")) && bytes.Contains(text, []byte("<svg")):
		return previewSVG
	case bytes.HasPrefix(text, []byte("<!doctype html")), bytes.HasPrefix(text, []byte("<html")):
		return previewHTML
	}
	return ""
}

// PreviewBodyInputDTO is what PreviewBody takes.
type PreviewBodyInputDTO struct {
	// BodyID is ResponseDTO.BodyID.
	BodyID string `json:"bodyId"`
}

// BodyPreviewDTO is a kept body's bytes, for the preview.
type BodyPreviewDTO struct {
	// Data is the bytes, Base64 (standard alphabet, padded).
	Data string `json:"data"`
	// Size is their number.
	Size int64 `json:"size"`
}

// PreviewBody returns the bytes of a kept body (ResponseDTO.BodyID) for
// the image preview (HTTP-35), at most MaxPreviewBytes of them.
//
// Errors: http.preview_too_large (invalid_argument) for a larger body,
// http.body_gone (not_found) when the body is no longer kept, io when it
// cannot be read, http.unavailable without a body store.
func (s *HTTPService) PreviewBody(ctx context.Context, in PreviewBodyInputDTO) (BodyPreviewDTO, error) {
	if s.bodies == nil {
		return BodyPreviewDTO{}, errs.New(errs.Unsupported, "Response bodies are not kept.").WithCode(CodeUnavailable)
	}
	if err := ctx.Err(); err != nil {
		return BodyPreviewDTO{}, errs.Wrap(err, errs.KindOf(err), "Reading the body was interrupted.")
	}
	kb, r, ok, err := s.bodies.open(in.BodyID)
	if err != nil {
		return BodyPreviewDTO{}, errs.Wrap(err, errs.IO, "The response body could not be read.")
	}
	if !ok {
		return BodyPreviewDTO{}, bodyGone()
	}
	defer r.Close()
	if kb.size > MaxPreviewBytes {
		return BodyPreviewDTO{}, errs.New(errs.InvalidArgument, "The body is "+formatBytes(kb.size)+", too large to preview (at most "+formatBytes(MaxPreviewBytes)+").").
			WithCode(CodePreviewTooLarge).
			WithHint("Save the body to a file to open it in another app.")
	}
	data, err := io.ReadAll(io.LimitReader(r, MaxPreviewBytes))
	if err != nil {
		return BodyPreviewDTO{}, errs.Wrap(err, errs.IO, "The response body could not be read.")
	}
	return BodyPreviewDTO{Data: base64.StdEncoding.EncodeToString(data), Size: int64(len(data))}, nil
}
