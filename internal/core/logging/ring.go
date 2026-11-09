package logging

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Defaults for RingOptions.
const (
	// DefaultRingSize is how many records a Ring keeps by default.
	DefaultRingSize = 2000
	// MaxRingText caps the message and each attribute value a Ring keeps,
	// in bytes. Longer text is cut at a rune boundary and ends with "…".
	MaxRingText = 2048
	// maxRingFields caps the attributes kept per record.
	maxRingFields = 64
)

// Entry is one log record kept by a Ring: already redacted, flattened to
// strings, safe to show in the UI.
type Entry struct {
	// Seq numbers the records a Ring has taken, from 1, over its lifetime
	// (Clear does not reset it).
	Seq     uint64
	Time    time.Time
	Level   slog.Level
	Message string
	// Source names the part of the app that logged it: the "component" or
	// "service" attribute, or "".
	Source string
	// OpID and OpKind are the operation the record belongs to, when known:
	// from the record's context (RingOptions.OpFromContext) or from its
	// "opId"/"conn" and "op" or "domain"+"kind" attributes.
	OpID   string
	OpKind string
	// Fields are the attributes in order, groups flattened to dotted keys.
	Fields []Field
}

// Field is one attribute of an Entry.
type Field struct {
	Key   string
	Value string
}

// RingOptions configures NewRing.
type RingOptions struct {
	// Size is the number of records kept; the oldest go first. Zero means
	// DefaultRingSize.
	Size int
	// Level is the minimum level kept; nil means slog.LevelDebug, so the
	// console shows more than the log file (which stays at info).
	Level slog.Leveler
	// Redactor, if set, is applied again when entries are read, so a
	// secret registered after a record was logged is masked in it too.
	// Records reach the ring through New's Redactor already.
	Redactor *Redactor
	// OpFromContext, if set, returns the operation a record's context
	// belongs to (the app passes netx.UserOpFrom).
	OpFromContext func(ctx context.Context) (id, kind string, ok bool)
}

// Ring keeps the last records logged, for the in-app console (DBG-06). It is
// fed by the slog.Handler from Handler, which New tees next to the file
// output (Options.Ring), under the redaction. It is safe for concurrent use.
type Ring struct {
	level  slog.Leveler
	red    *Redactor
	opFrom func(context.Context) (string, string, bool)

	mu     sync.Mutex
	buf    []Entry // circular, len == size once full
	start  int     // index of the oldest entry when full
	seq    uint64
	subs   map[int]func(Entry)
	nextID int
}

// NewRing returns an empty Ring.
func NewRing(opts RingOptions) *Ring {
	size := opts.Size
	if size <= 0 {
		size = DefaultRingSize
	}
	level := opts.Level
	if level == nil {
		level = slog.LevelDebug
	}
	return &Ring{
		level:  level,
		red:    opts.Redactor,
		opFrom: opts.OpFromContext,
		buf:    make([]Entry, 0, size),
		subs:   map[int]func(Entry){},
	}
}

// Entries returns the kept records, oldest first.
func (r *Ring) Entries() []Entry {
	r.mu.Lock()
	out := make([]Entry, 0, len(r.buf))
	out = append(out, r.buf[r.start:]...)
	out = append(out, r.buf[:r.start]...)
	r.mu.Unlock()
	for i := range out {
		out[i] = r.reread(out[i])
	}
	return out
}

// Clear drops every kept record. Sequence numbers keep counting.
func (r *Ring) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = r.buf[:0]
	r.start = 0
}

// Capacity returns how many records the ring keeps.
func (r *Ring) Capacity() int { return cap(r.buf) }

// LastSeq returns the Seq of the newest record taken, or 0.
func (r *Ring) LastSeq() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seq
}

// Subscribe calls fn with every record taken from now on, in Seq order,
// until the returned function is called. fn runs on the logging goroutine
// while the ring is locked: it must return at once, must not block and must
// not log (a stream.Batcher's Add is the intended subscriber).
func (r *Ring) Subscribe(fn func(Entry)) (unsubscribe func()) {
	r.mu.Lock()
	id := r.nextID
	r.nextID++
	r.subs[id] = fn
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		delete(r.subs, id)
		r.mu.Unlock()
	}
}

// Redact re-applies the ring's Redactor to e, for entries read outside
// Entries (a subscriber's). Without a Redactor it returns e unchanged.
func (r *Ring) Redact(e Entry) Entry { return r.reread(e) }

func (r *Ring) reread(e Entry) Entry {
	if r.red == nil {
		return e
	}
	s := r.red.load()
	if s.repl == nil {
		return e
	}
	e.Message = s.redact(e.Message)
	fields := make([]Field, len(e.Fields))
	for i, f := range e.Fields {
		fields[i] = Field{Key: f.Key, Value: s.redact(f.Value)}
	}
	e.Fields = fields
	return e
}

func (r *Ring) add(e Entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	e.Seq = r.seq
	if len(r.buf) < cap(r.buf) {
		r.buf = append(r.buf, e)
	} else {
		r.buf[r.start] = e
		r.start = (r.start + 1) % len(r.buf)
	}
	for _, fn := range r.subs {
		fn(e)
	}
}

// Handler returns the slog.Handler that feeds the ring. Put it under a
// Redactor (New does, with Options.Ring) so it only sees redacted records.
func (r *Ring) Handler() slog.Handler { return &ringHandler{r: r} }

// ringHandler records into a Ring. prefix is the dotted group path of
// WithGroup calls; attrs were bound with WithAttrs (keys already prefixed).
type ringHandler struct {
	r      *Ring
	prefix string
	attrs  []Field
	bound  opInfo
}

// opInfo is what the attributes say about the record.
type opInfo struct {
	source, opID, op, domain, kind string
}

func (o opInfo) opKind() string {
	switch {
	case o.op != "":
		return o.op
	case o.domain != "" && o.kind != "":
		return o.domain + "." + o.kind
	default:
		return o.domain
	}
}

func (h *ringHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.r.level.Level()
}

func (h *ringHandler) WithAttrs(as []slog.Attr) slog.Handler {
	if len(as) == 0 {
		return h
	}
	nh := *h
	nh.attrs = slices.Clip(h.attrs)
	for _, a := range as {
		nh.attrs = appendAttr(nh.attrs, h.prefix, a, &nh.bound)
	}
	return &nh
}

func (h *ringHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	nh := *h
	nh.prefix = h.prefix + name + "."
	return &nh
}

func (h *ringHandler) Handle(ctx context.Context, rec slog.Record) error {
	info := h.bound
	fields := slices.Clone(h.attrs)
	rec.Attrs(func(a slog.Attr) bool {
		fields = appendAttr(fields, h.prefix, a, &info)
		return true
	})
	if len(fields) > maxRingFields {
		fields = fields[:maxRingFields]
	}
	e := Entry{
		Time:    rec.Time,
		Level:   rec.Level,
		Message: clip(rec.Message),
		Source:  info.source,
		OpID:    info.opID,
		OpKind:  info.opKind(),
		Fields:  fields,
	}
	if h.r.opFrom != nil && ctx != nil {
		if id, kind, ok := h.r.opFrom(ctx); ok {
			if e.OpID == "" {
				e.OpID = id
			}
			if e.OpKind == "" {
				e.OpKind = kind
			}
		}
	}
	h.r.add(e)
	return nil
}

// appendAttr appends a's fields (a group's members, flattened) and notes
// what the top-level operation attributes say.
func appendAttr(fields []Field, prefix string, a slog.Attr, info *opInfo) []Field {
	v := a.Value.Resolve()
	if v.Kind() == slog.KindGroup {
		p := prefix
		if a.Key != "" {
			p += a.Key + "."
		}
		for _, m := range v.Group() {
			fields = appendAttr(fields, p, m, info)
		}
		return fields
	}
	if a.Key == "" {
		return fields
	}
	text := clip(v.String())
	if prefix == "" {
		switch a.Key {
		case "component", "service":
			info.source = text
		case "opId", "conn":
			info.opID = text
		case "op":
			info.op = text
		case "domain":
			info.domain = text
		case "kind":
			info.kind = text
		}
	}
	return append(fields, Field{Key: prefix + a.Key, Value: text})
}

// clip cuts s to MaxRingText bytes at a rune boundary.
func clip(s string) string {
	if len(s) <= MaxRingText {
		return s
	}
	cut := MaxRingText
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.Clone(s[:cut]) + "…"
}
