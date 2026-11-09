package http

import (
	"context"
	"encoding/base64"
	"io"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/har"
	"github.com/sorrelfox/postsonde/internal/core/history"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
)

// Saving a response's whole exchange (EXP-06, docs/har.md#a-responses-exchange):
// the request as sent and the full response, as a HAR 1.2 file with one
// entry, masked.

// Error codes of SaveExchange, besides SaveBody's http.body_gone and
// http.unavailable.
const (
	// CodeExchangeTooLarge: the kept body is larger than MaxExchangeBody
	// (kind unsupported). Details: size, limit.
	CodeExchangeTooLarge = "http.exchange_too_large"
)

// MaxExchangeBody is the largest response body SaveExchange writes: a
// HAR file holds the body as JSON text, so it is masked and encoded in
// memory. SaveBody saves larger ones.
const MaxExchangeBody = MaxEvalBodyBytes

// Secrets returns the secret values the app keeps (the stored ones:
// typed auth secrets, secret variables, OAuth 2.0 tokens, app-wide
// passphrases), read when called.
type Secrets func(ctx context.Context) ([]string, error)

// WithExchangeSecrets masks saved exchanges with the values secrets
// returns too, besides the secrets of the send (docs/har.md). Without it
// only the send's are masked.
func WithExchangeSecrets(secrets Secrets) Option {
	return func(s *HTTPService) { s.exchangeSecrets = secrets }
}

// keptExchange is what SaveExchange needs of a send besides its kept
// body: the exchange as a history entry, masked with the send's secrets,
// the request body whole as the client recorded it and the response body
// left out, and the send's masker for that body. It lives in memory only,
// with the kept body, and goes with it.
type keptExchange struct {
	entry  history.Entry
	masker httpclient.Masker
}

// keepExchange attaches the exchange of a send that got a response to its
// kept body id.
func (x *send) keepExchange(id string, ex exchanged) {
	if x.bodies == nil || id == "" {
		return
	}
	m := x.responseMasker(ex.resp)
	e := responseEntry(x.meta, x.build, ex.started, ex.resp, ex.dto, m)
	sent := ex.resp.Request
	if len(ex.resp.Redirects) > 0 && ex.resp.Redirects[0].Request != nil {
		sent = ex.resp.Redirects[0].Request
	}
	if sent != nil {
		var masked bool
		e.Request.Body, masked = wholeBody(sent.Body, sent.BodySize, m)
		e.Masked = e.Masked || masked
	}
	e.Response.Body = history.Body{}
	x.bodies.attach(id, &keptExchange{entry: e, masker: m})
}

// wholeBody is history.Snapshot without the history's clipping: raw
// masked with m, as text when it is UTF-8, else in Base64; Clipped when
// size counts more bytes than raw holds.
func wholeBody(raw []byte, size int64, m httpclient.Masker) (b history.Body, masked bool) {
	size = max(size, int64(len(raw)))
	data := raw
	if m != nil && len(raw) > 0 {
		if s := m.MaskText(string(raw)); s != string(raw) {
			data, masked = []byte(s), true
		}
	}
	b.Size, b.Clipped = size, int64(len(raw)) < size
	if utf8.Valid(data) {
		b.Text = string(data)
	} else {
		b.Text, b.Base64 = base64.StdEncoding.EncodeToString(data), true
	}
	return b, masked
}

// SaveExchangeInputDTO is what SaveExchange takes.
type SaveExchangeInputDTO struct {
	// BodyID is ResponseDTO.BodyID of the response.
	BodyID string `json:"bodyId"`
	// Name names the HAR page and the suggested file: the request's name.
	Name string `json:"name"`
}

// SaveExchangeResultDTO is what SaveExchange returns.
type SaveExchangeResultDTO struct {
	// Saved is false when the user cancelled the dialog.
	Saved bool   `json:"saved"`
	Path  string `json:"path"`
	// Size is the size of the file written.
	Size int64 `json:"size"`
	// Masked is set when secrets were replaced by ••••••.
	Masked bool `json:"masked"`
}

// SaveExchange asks where to save the whole exchange of a response
// (ResponseDTO.BodyID) with the native save dialog and writes it there as
// a HAR 1.2 file with one entry (EXP-06): the request as sent (the first
// hop: method, URL, every header field written, the body as recorded, at
// most httpclient.MaxRecordedBody bytes of it) and the full response
// (status, headers, the whole body as kept, decompressed, not clipped
// like the history's 64 KiB), with the timings of the final hop. It is
// masked with the send's secrets, as the timeline and the history are,
// then again with every secret the app keeps (WithExchangeSecrets), in
// the body too; there is no "with secrets" choice. Nothing is written
// when the user cancels.
//
// Errors: http.body_gone once the response is no longer kept,
// http.exchange_too_large for a body over MaxExchangeBody,
// http.export_failed when the file cannot be written, http.unavailable
// without a save dialog.
func (s *HTTPService) SaveExchange(ctx context.Context, in SaveExchangeInputDTO) (SaveExchangeResultDTO, error) {
	if s.bodies == nil || s.pickSave == nil {
		return SaveExchangeResultDTO{}, errs.New(errs.Unsupported, "Saving exchanges is not available.").WithCode(CodeUnavailable)
	}
	f, masked, err := s.exchangeHAR(ctx, in)
	if err != nil {
		return SaveExchangeResultDTO{}, err
	}
	data, err := har.Encode(f)
	if err != nil {
		return SaveExchangeResultDTO{}, errs.Wrap(err, errs.Internal, "The HAR file could not be made.")
	}
	path, err := s.pickSave(ctx, SaveDialog{
		Title:      "Save exchange as HAR",
		Message:    "Save the request as sent and the whole response as an HTTP Archive, secrets masked.",
		ButtonText: "Save",
		Filename:   har.FileName(exchangeTitle(in.Name)),
	})
	if err != nil {
		return SaveExchangeResultDTO{}, errs.Wrap(err, errs.Internal, "The save dialog could not be shown.")
	}
	if path == "" {
		return SaveExchangeResultDTO{Masked: masked}, nil
	}
	if err := har.WriteFile(path, data); err != nil {
		kind := errs.KindOf(err)
		if kind == errs.Internal {
			kind = errs.IO
		}
		return SaveExchangeResultDTO{}, errs.Wrap(err, kind, "The exchange could not be saved to "+path+".").WithCode(CodeExportFailed).
			WithDetail("path", path).WithHint("Check that the folder exists and can be written to, and that the disk is not full.")
	}
	s.logger.Info("exchange saved as HAR", "bytes", len(data), "masked", masked)
	return SaveExchangeResultDTO{Saved: true, Path: path, Size: int64(len(data)), Masked: masked}, nil
}

// exchangeTitle is the HAR page title of an exchange of request name.
func exchangeTitle(name string) string {
	if strings.TrimSpace(name) == "" {
		return "exchange"
	}
	return name
}

// exchangeHAR builds the HAR file of SaveExchange.
func (s *HTTPService) exchangeHAR(ctx context.Context, in SaveExchangeInputDTO) (*har.File, bool, error) {
	kb, r, ok, err := s.bodies.open(in.BodyID)
	if err != nil {
		return nil, false, errs.Wrap(err, errs.IO, "The response body could not be read.").WithCode(CodeExportFailed)
	}
	if !ok || kb.exchange == nil {
		if ok {
			r.Close()
		}
		return nil, false, bodyGone()
	}
	defer r.Close()
	if kb.size > MaxExchangeBody {
		return nil, false, errs.New(errs.Unsupported, "The response body is too large to save in a HAR file.").
			WithCode(CodeExchangeTooLarge).
			WithHint("Save the body to a file on its own instead.").
			WithDetail("size", strconv.FormatInt(kb.size, 10)).WithDetail("limit", strconv.Itoa(MaxExchangeBody))
	}
	raw, err := io.ReadAll(io.LimitReader(r, MaxExchangeBody))
	if err != nil {
		return nil, false, errs.Wrap(err, errs.IO, "The response body could not be read.").WithCode(CodeExportFailed)
	}
	// A copy: the kept entry stays as it was for the next save.
	e := kb.exchange.entry
	e.Request.Headers = slices.Clone(e.Request.Headers)
	resp := *e.Response
	resp.Headers = slices.Clone(resp.Headers)
	var bodyMasked bool
	resp.Body, bodyMasked = wholeBody(raw, int64(len(raw)), kb.exchange.masker)
	e.Response = &resp
	e.Masked = e.Masked || bodyMasked

	// Again with every secret the app keeps now, as copied logs are.
	if s.exchangeSecrets != nil {
		values, err := s.exchangeSecrets(ctx)
		if err != nil {
			// A side file that cannot be read: its secrets cannot be
			// sent either (docs/privacy.md#copied-and-saved-logs).
			s.logger.Warn("reading the secrets to mask an exchange with", "err", err)
		}
		if len(values) > 0 && maskEntry(&e, httpclient.NewSecretMasker(values)) {
			e.Masked = true
		}
	}
	f := har.NewFile(s.version)
	page := f.AddPage(exchangeTitle(in.Name), e.Time)
	entry := har.FromExchange(e)
	entry.Pageref = page
	f.Log.Entries = append(f.Log.Entries, entry)
	return f, e.Masked, nil
}

// maskEntry masks every value of e with m: the URLs, header values and
// bodies (a Base64 body in its decoded bytes). It reports whether
// anything was hidden.
func maskEntry(e *history.Entry, m httpclient.Masker) bool {
	masked := false
	text := func(s *string) {
		if v := m.MaskText(*s); v != *s {
			*s, masked = v, true
		}
	}
	headers := func(hs []history.Header) {
		for i := range hs {
			if v := m.MaskHeader(hs[i].Name, hs[i].Value); v != hs[i].Value {
				hs[i].Value, masked = v, true
			}
		}
	}
	body := func(b *history.Body) {
		if !b.Base64 {
			text(&b.Text)
			return
		}
		raw, err := base64.StdEncoding.DecodeString(b.Text)
		if err != nil {
			return
		}
		if v := m.MaskText(string(raw)); v != string(raw) {
			b.Text, masked = base64.StdEncoding.EncodeToString([]byte(v)), true
		}
	}
	text(&e.URL)
	text(&e.Request.URL)
	text(&e.Error)
	headers(e.Request.Headers)
	body(&e.Request.Body)
	if e.Response != nil {
		text(&e.Response.FinalURL)
		headers(e.Response.Headers)
		body(&e.Response.Body)
	}
	return masked
}
