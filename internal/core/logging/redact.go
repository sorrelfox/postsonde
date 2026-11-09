package logging

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"maps"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"unicode"
	"unicode/utf8"
)

// Mask replaces redacted values.
const Mask = "[REDACTED]"

// MinSecretLen is the minimum length, in characters, of a value AddSecret
// registers.
const MinSecretLen = 4

// maxDepth bounds how deep maps nested in attribute values are expanded.
const maxDepth = 8

var defaultKeys = []string{
	"authorization", "proxy-authorization",
	"cookie", "set-cookie",
	"password", "passwd", "secret", "token",
	"api_key", "apikey", "x-api-key",
	"access_token", "refresh_token", "id_token",
	"client_secret", "private_key",
}

// Redactor masks sensitive attributes and registered secret values in log
// records. It is safe for concurrent use; the zero value is ready to use and
// knows the built-in sensitive keys.
type Redactor struct {
	mu      sync.Mutex
	keys    map[string]struct{} // normalized with normalizeKey
	secrets map[string]struct{}
	version uint64
	snap    atomic.Pointer[snapshot]
}

// snapshot is an immutable view of a Redactor, swapped atomically whenever
// its keys or secrets change so log calls never take a lock.
type snapshot struct {
	version uint64
	keys    map[string]struct{}
	repl    *strings.Replacer // nil when no secrets are registered
}

// NewRedactor returns a Redactor with the built-in sensitive keys and no
// secrets.
func NewRedactor() *Redactor {
	r := &Redactor{}
	r.load()
	return r
}

// AddSecret registers v so it is masked wherever it appears. Values shorter
// than MinSecretLen characters are ignored; AddSecret reports whether v was
// registered.
func (r *Redactor) AddSecret(v string) bool {
	if utf8.RuneCountInString(v) < MinSecretLen {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.initLocked()
	if _, ok := r.secrets[v]; !ok {
		r.secrets[v] = struct{}{}
		r.publishLocked()
	}
	return true
}

// RemoveSecret unregisters a value added with AddSecret.
func (r *Redactor) RemoveSecret(v string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.initLocked()
	if _, ok := r.secrets[v]; ok {
		delete(r.secrets, v)
		r.publishLocked()
	}
}

// AddKey adds a sensitive attribute key. Keys match ignoring case, '-' and
// '_'.
func (r *Redactor) AddKey(k string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.initLocked()
	if n := normalizeKey(k); n != "" {
		if _, ok := r.keys[n]; !ok {
			r.keys[n] = struct{}{}
			r.publishLocked()
		}
	}
}

// IsSensitiveKey reports whether attributes with key k are masked.
func (r *Redactor) IsSensitiveKey(k string) bool { return r.load().sensitive(k) }

// Redact returns s with every registered secret replaced by Mask.
func (r *Redactor) Redact(s string) string { return r.load().redact(s) }

// Attr returns a with its value resolved and redacted.
func (r *Redactor) Attr(a slog.Attr) slog.Attr { return r.load().attr(a, 0) }

// Handler wraps h so that every record's message and attributes, including
// attributes bound with WithAttrs, are redacted before h sees them.
func (r *Redactor) Handler(h slog.Handler) slog.Handler {
	return &redactHandler{r: r, base: h, cache: new(atomic.Pointer[derived])}
}

func (r *Redactor) load() *snapshot {
	if s := r.snap.Load(); s != nil {
		return s
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if s := r.snap.Load(); s != nil {
		return s
	}
	r.initLocked()
	return r.publishLocked()
}

func (r *Redactor) initLocked() {
	if r.keys != nil {
		return
	}
	r.keys = make(map[string]struct{}, len(defaultKeys))
	for _, k := range defaultKeys {
		r.keys[normalizeKey(k)] = struct{}{}
	}
	r.secrets = make(map[string]struct{})
}

func (r *Redactor) publishLocked() *snapshot {
	r.version++
	var pats []string
	for s := range r.secrets {
		pats = append(pats, s, url.QueryEscape(s), url.PathEscape(s))
	}
	// strings.Replacer tries patterns in argument order at each position, so
	// put longer ones first: a secret containing another is masked whole.
	slices.SortFunc(pats, func(a, b string) int {
		return cmp.Or(cmp.Compare(len(b), len(a)), strings.Compare(a, b))
	})
	pats = slices.Compact(pats)
	s := &snapshot{version: r.version, keys: maps.Clone(r.keys)}
	if len(pats) > 0 {
		args := make([]string, 0, 2*len(pats))
		for _, p := range pats {
			args = append(args, p, Mask)
		}
		s.repl = strings.NewReplacer(args...)
	}
	r.snap.Store(s)
	return s
}

func normalizeKey(k string) string {
	return strings.Map(func(r rune) rune {
		if r == '-' || r == '_' {
			return -1
		}
		return unicode.ToLower(r)
	}, k)
}

func (s *snapshot) sensitive(k string) bool {
	_, ok := s.keys[normalizeKey(k)]
	return ok
}

func (s *snapshot) redact(str string) string {
	if s.repl == nil {
		return str
	}
	return s.repl.Replace(str)
}

func (s *snapshot) attrs(as []slog.Attr) []slog.Attr {
	out := make([]slog.Attr, len(as))
	for i, a := range as {
		out[i] = s.attr(a, 0)
	}
	return out
}

func (s *snapshot) attr(a slog.Attr, depth int) slog.Attr {
	a.Value = a.Value.Resolve()
	if a.Key != "" && s.sensitive(a.Key) {
		return slog.String(a.Key, Mask)
	}
	switch a.Value.Kind() {
	case slog.KindString:
		if s.repl != nil {
			a.Value = slog.StringValue(s.repl.Replace(a.Value.String()))
		}
	case slog.KindGroup:
		a.Value = slog.GroupValue(s.attrs(a.Value.Group())...)
	case slog.KindAny:
		a.Value = s.anyValue(a.Value.Any(), depth)
	}
	return a
}

// anyValue redacts a value of kind Any. Maps with string keys become groups
// so their keys are checked; other values are replaced by their redacted
// text only if they contain a secret.
func (s *snapshot) anyValue(x any, depth int) slog.Value {
	if depth < maxDepth {
		if rv := reflect.ValueOf(x); rv.Kind() == reflect.Map && rv.Type().Key().Kind() == reflect.String {
			keys := rv.MapKeys()
			slices.SortFunc(keys, func(a, b reflect.Value) int { return strings.Compare(a.String(), b.String()) })
			attrs := make([]slog.Attr, len(keys))
			for i, k := range keys {
				attrs[i] = s.attr(slog.Any(k.String(), rv.MapIndex(k).Interface()), depth+1)
			}
			return slog.GroupValue(attrs...)
		}
	}
	if s.repl == nil || x == nil {
		return slog.AnyValue(x)
	}
	var text string
	switch v := x.(type) {
	case []string:
		out := make([]string, len(v))
		for i, e := range v {
			out[i] = s.repl.Replace(e)
		}
		return slog.AnyValue(out)
	case error:
		text = v.Error()
	case []byte:
		text = string(v)
	default:
		text = fmt.Sprintf("%+v", x)
	}
	if red := s.repl.Replace(text); red != text {
		return slog.StringValue(red)
	}
	return slog.AnyValue(x)
}

// redactHandler is the slog.Handler returned by Redactor.Handler.
//
// It keeps the WithAttrs and WithGroup calls itself instead of passing them
// straight to base, so attributes bound early are re-redacted when secrets
// change. The derived base handler is cached per Redactor version.
type redactHandler struct {
	r     *Redactor
	base  slog.Handler
	ops   []handlerOp
	cache *atomic.Pointer[derived]
}

// handlerOp is one WithGroup (group set) or WithAttrs (attrs set) call.
type handlerOp struct {
	group string
	attrs []slog.Attr
}

type derived struct {
	version uint64
	h       slog.Handler
}

func (h *redactHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.base.Enabled(ctx, l)
}

func (h *redactHandler) WithAttrs(as []slog.Attr) slog.Handler {
	if len(as) == 0 {
		return h
	}
	return h.with(handlerOp{attrs: slices.Clone(as)})
}

func (h *redactHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return h.with(handlerOp{group: name})
}

func (h *redactHandler) with(op handlerOp) *redactHandler {
	return &redactHandler{
		r:     h.r,
		base:  h.base,
		ops:   append(slices.Clip(h.ops), op),
		cache: new(atomic.Pointer[derived]),
	}
}

func (h *redactHandler) Handle(ctx context.Context, rec slog.Record) error {
	s := h.r.load()
	nr := slog.NewRecord(rec.Time, rec.Level, s.redact(rec.Message), rec.PC)
	rec.Attrs(func(a slog.Attr) bool {
		nr.AddAttrs(s.attr(a, 0))
		return true
	})
	return h.handler(s).Handle(ctx, nr)
}

// handler returns base with the recorded ops applied, redacted with s.
func (h *redactHandler) handler(s *snapshot) slog.Handler {
	if len(h.ops) == 0 {
		return h.base
	}
	if d := h.cache.Load(); d != nil && d.version == s.version {
		return d.h
	}
	inner := h.base
	for _, op := range h.ops {
		if op.group != "" {
			inner = inner.WithGroup(op.group)
		} else {
			inner = inner.WithAttrs(s.attrs(op.attrs))
		}
	}
	h.cache.Store(&derived{version: s.version, h: inner})
	return inner
}
