package incremental

import (
	"strconv"
	"strings"
)

// Entry kinds: what one payload, or one entry of its incremental list,
// did.
const (
	// KindInitial is the first payload: the result without the deferred
	// and streamed parts.
	KindInitial = "initial"
	// KindDefer is a deferred fragment's data, merged at its path.
	KindDefer = "defer"
	// KindStream is streamed list items, appended to the list at its
	// path.
	KindStream = "stream"
	// KindPending announces a deferred fragment or stream that will come
	// (the newer format's pending list).
	KindPending = "pending"
	// KindCompleted says a deferred fragment or stream is done (the newer
	// format's completed list), with the errors that ended it early.
	KindCompleted = "completed"
)

// Entry is one thing a payload did.
type Entry struct {
	// Kind is one of the Kind constants.
	Kind string
	// Path is where it applies, as in a GraphQL error's path:
	// "user.friends[2]"; "" for the root.
	Path string
	// Label is the @defer or @stream label, "" when there is none.
	Label string
	// ID is the newer format's id, "" in the older one.
	ID string
	// Items counts the streamed items.
	Items int
	// Errors counts the errors that came with it.
	Errors int
	// Problem says why it could not be applied ("" when it was): an
	// unknown id, a path that is not in the result.
	Problem string
}

// Result merges the payloads of an incremental response into one GraphQL
// response. The zero value is ready to use. It is not safe for concurrent
// use.
//
// Both formats are read:
//
//   - the older one (graphql-js 17 alpha 2, Apollo's
//     deferSpec=20220824, and the first experiments before it):
//     subsequent payloads, or the entries of their "incremental" list,
//     carry a "path" and "data" (a deferred fragment, merged into the
//     object at path) or "items" (streamed items; the path ends with the
//     index of the first one), with an optional "label";
//   - the newer one (graphql-js 17 alpha 3 and later): "pending" announces
//     {id, path, label}, "incremental" entries name an id with "data" and
//     an optional "subPath", or "items" appended to the list at the
//     pending path, and "completed" ends an id, maybe with errors.
//
// "errors" of payloads and entries are appended to the result's errors,
// "extensions" members replace those of the same name, and "hasNext"
// says whether more is to come.
type Result struct {
	data      *value
	hasData   bool
	errors    []*value
	ext       *value
	pending   map[string]pendingEntry
	payloads  int
	hasNext   bool
	sawNext   bool
	completed bool
	// next is the latest payload's hasNext: 0 none, 1 true, 2 false.
	next int8
}

type pendingEntry struct {
	path  []pathElem
	label string
}

// pathElem is a response path step: a field name or a list index.
type pathElem struct {
	name  string
	index int
	isIdx bool
}

// Apply merges one payload, the JSON text of a part. It returns what the
// payload did; an error means the text is not a JSON object, and nothing
// was merged.
func (r *Result) Apply(payload []byte) ([]Entry, error) {
	v, err := parseJSON(payload)
	if err != nil {
		return nil, err
	}
	if v.kind != kindObject {
		return nil, errNotObject
	}
	return r.apply(v), nil
}

// errNotObject: a payload that is JSON but not an object.
var errNotObject = &notObjectError{}

type notObjectError struct{}

func (*notObjectError) Error() string { return "not a JSON object" }

func (r *Result) apply(v *value) []Entry {
	var out []Entry
	first := r.payloads == 0
	r.payloads++
	r.next = 0
	if b, ok := v.get("hasNext").bool(); ok {
		r.hasNext, r.sawNext = b, true
		r.next = 2
		if b {
			r.next = 1
		}
	}
	path := v.get("path")
	if first && path == nil && v.get("id") == nil {
		// The initial result.
		if d := v.get("data"); d != nil {
			r.data, r.hasData = d.clone(), true
		}
		out = append(out, Entry{Kind: KindInitial, Errors: r.addErrors(v.get("errors"))})
	} else {
		n := r.addErrors(v.get("errors"))
		if path != nil && (v.get("data") != nil || v.get("items") != nil) {
			// An older-format subsequent payload: the entry itself.
			e := r.applyEntry(v, nil)
			e.Errors += n
			out = append(out, e)
		} else if n > 0 {
			out = append(out, Entry{Kind: KindCompleted, Errors: n})
		}
	}
	r.mergeExtensions(v.get("extensions"))
	if list := v.get("pending"); list != nil && list.kind == kindArray {
		for _, p := range list.items {
			id := idOf(p.get("id"))
			label, _ := p.get("label").string()
			steps, ok := toPath(p.get("path"))
			e := Entry{Kind: KindPending, ID: id, Label: label, Path: formatPath(steps)}
			if id == "" || !ok {
				e.Problem = "The pending entry has no id or path."
			} else {
				if r.pending == nil {
					r.pending = map[string]pendingEntry{}
				}
				r.pending[id] = pendingEntry{path: steps, label: label}
			}
			out = append(out, e)
		}
	}
	if list := v.get("incremental"); list != nil && list.kind == kindArray {
		for _, it := range list.items {
			out = append(out, r.applyEntry(it, it.get("id")))
		}
	}
	if list := v.get("completed"); list != nil && list.kind == kindArray {
		for _, c := range list.items {
			id := idOf(c.get("id"))
			e := Entry{Kind: KindCompleted, ID: id, Errors: r.addErrors(c.get("errors"))}
			if p, ok := r.pending[id]; ok {
				e.Path, e.Label = formatPath(p.path), p.label
				delete(r.pending, id)
			} else {
				e.Problem = "No pending entry has the id " + strconv.Quote(id) + "."
			}
			out = append(out, e)
		}
	}
	if r.sawNext && !r.hasNext {
		r.completed = true
	}
	return out
}

// idOf reads an id: a string, or a number some servers send.
func idOf(v *value) string {
	if s, ok := v.string(); ok {
		return s
	}
	if v != nil && v.kind == kindLiteral && !v.isNull() {
		return v.raw
	}
	return ""
}

// applyEntry merges one deferred or streamed entry. id is its id in the
// newer format, nil in the older one (where the path is its own).
func (r *Result) applyEntry(e *value, id *value) Entry {
	out := Entry{Errors: r.addErrors(e.get("errors"))}
	r.mergeExtensions(e.get("extensions"))
	items := e.get("items")
	data := e.get("data")
	out.Kind = KindDefer
	if items != nil {
		out.Kind = KindStream
		if items.kind == kindArray {
			out.Items = len(items.items)
		}
	}
	var steps []pathElem
	newer := id != nil
	if newer {
		out.ID = idOf(id)
		p, ok := r.pending[out.ID]
		if !ok {
			out.Problem = "No pending entry has the id " + strconv.Quote(out.ID) + "."
			return out
		}
		out.Label = p.label
		steps = append(steps, p.path...)
		if sub, ok := toPath(e.get("subPath")); ok {
			steps = append(steps, sub...)
		}
	} else {
		out.Label, _ = e.get("label").string()
		var ok bool
		if steps, ok = toPath(e.get("path")); !ok {
			out.Problem = "The entry has no valid path."
			return out
		}
	}
	out.Path = formatPath(steps)
	switch {
	case items != nil:
		if items.kind != kindArray {
			out.Problem = "The items are not a list."
			return out
		}
		out.Problem = r.appendItems(steps, items.items, newer)
	case data != nil:
		if data.isNull() {
			// A deferred fragment whose data is null: its errors say why.
			return out
		}
		out.Problem = r.mergeAt(steps, data)
	default:
		out.Problem = "The entry has neither data nor items."
	}
	return out
}

// addErrors appends the errors of a list and returns how many there were.
func (r *Result) addErrors(list *value) int {
	if list == nil || list.kind != kindArray {
		return 0
	}
	for _, e := range list.items {
		r.errors = append(r.errors, e.clone())
	}
	return len(list.items)
}

// mergeExtensions sets the members of ext on the result's extensions.
func (r *Result) mergeExtensions(ext *value) {
	if ext == nil || ext.kind != kindObject {
		return
	}
	if r.ext == nil {
		r.ext = &value{kind: kindObject}
	}
	for _, m := range ext.members {
		r.ext.set(m.name, m.val.clone())
	}
}

// toPath reads a response path: strings and non-negative integers.
func toPath(v *value) ([]pathElem, bool) {
	if v == nil || v.kind != kindArray {
		return nil, false
	}
	out := make([]pathElem, 0, len(v.items))
	for _, it := range v.items {
		if s, ok := it.string(); ok {
			out = append(out, pathElem{name: s})
		} else if n, ok := it.int(); ok {
			out = append(out, pathElem{index: n, isIdx: true})
		} else {
			return nil, false
		}
	}
	return out, true
}

// formatPath writes a path as the response view writes an error's path:
// user.friends[2].name.
func formatPath(steps []pathElem) string {
	var b strings.Builder
	for _, s := range steps {
		if s.isIdx {
			b.WriteString("[" + strconv.Itoa(s.index) + "]")
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('.')
		}
		b.WriteString(s.name)
	}
	return b.String()
}

// errMissing says a path leads nowhere.
func errMissing(steps []pathElem) string {
	return "The path " + formatPath(steps) + " is not in the result."
}

// lookup returns the value at steps, or nil when it is not there.
func (r *Result) lookup(steps []pathElem) *value {
	cur := r.data
	for _, s := range steps {
		if cur == nil {
			return nil
		}
		if s.isIdx {
			if cur.kind != kindArray || s.index >= len(cur.items) {
				return nil
			}
			cur = cur.items[s.index]
		} else {
			cur = cur.get(s.name)
		}
	}
	return cur
}

// mergeAt merges the object data into the value at steps. A missing last
// step is created (an older-format stream item sent as data at its
// index). It returns a problem, or "".
func (r *Result) mergeAt(steps []pathElem, data *value) string {
	if r.data.isNull() {
		if len(steps) > 0 {
			return errMissing(steps)
		}
		r.data, r.hasData = data.clone(), true
		return ""
	}
	if len(steps) == 0 {
		deepMerge(r.data, data)
		return ""
	}
	parent := r.lookup(steps[:len(steps)-1])
	last := steps[len(steps)-1]
	switch {
	case parent == nil || parent.isNull():
		return errMissing(steps)
	case last.isIdx && parent.kind == kindArray:
		for len(parent.items) <= last.index {
			parent.items = append(parent.items, &value{raw: "null"})
		}
		if t := parent.items[last.index]; t.kind == kindObject && data.kind == kindObject {
			deepMerge(t, data)
		} else {
			parent.items[last.index] = data.clone()
		}
	case !last.isIdx && parent.kind == kindObject:
		if t := parent.get(last.name); t != nil && t.kind == kindObject && data.kind == kindObject {
			deepMerge(t, data)
		} else if t != nil && t.isNull() {
			// null because of an error: the deferred data does not bring it back.
			return errMissing(steps)
		} else {
			parent.set(last.name, data.clone())
		}
	default:
		return errMissing(steps)
	}
	return ""
}

// deepMerge sets src's members on dst: objects merge member by member,
// anything else replaces.
func deepMerge(dst, src *value) {
	if src.kind != kindObject || dst.kind != kindObject {
		return
	}
	for _, m := range src.members {
		if t := dst.get(m.name); t != nil && t.kind == kindObject && m.val.kind == kindObject {
			deepMerge(t, m.val)
		} else if t != nil && t.kind == kindArray && m.val.kind == kindArray {
			// A list selected in the initial result and again in a
			// deferred fragment: its items merge by index.
			for i, it := range m.val.items {
				if i < len(t.items) && t.items[i].kind == kindObject && it.kind == kindObject {
					deepMerge(t.items[i], it)
				} else if i < len(t.items) {
					t.items[i] = it.clone()
				} else {
					t.items = append(t.items, it.clone())
				}
			}
		} else {
			dst.set(m.name, m.val.clone())
		}
	}
}

// appendItems adds streamed items to the list at steps. In the older
// format the path ends with the index of the first item, which places
// them; in the newer one it is the list's, and they are appended.
func (r *Result) appendItems(steps []pathElem, items []*value, newer bool) string {
	start := -1
	listPath := steps
	if !newer && len(steps) > 0 && steps[len(steps)-1].isIdx {
		start = steps[len(steps)-1].index
		listPath = steps[:len(steps)-1]
	}
	list := r.lookup(listPath)
	if list == nil || list.kind != kindArray {
		return errMissing(listPath)
	}
	if start < 0 {
		start = len(list.items)
	}
	for i, it := range items {
		at := start + i
		for len(list.items) < at {
			list.items = append(list.items, &value{raw: "null"})
		}
		if at < len(list.items) {
			list.items[at] = it.clone()
		} else {
			list.items = append(list.items, it.clone())
		}
	}
	return ""
}

// lastNext is the hasNext of the latest payload, nil when it had none.
func (r *Result) lastNext() *bool {
	if r.next == 0 {
		return nil
	}
	b := r.next == 1
	return &b
}

// Payloads counts the payloads applied.
func (r *Result) Payloads() int { return r.payloads }

// HasNext is the latest payload's hasNext: true while the server says
// more will come.
func (r *Result) HasNext() bool { return r.hasNext }

// Completed reports whether a payload said hasNext: false.
func (r *Result) Completed() bool { return r.completed }

// Pending counts the deferred fragments and streams announced (newer
// format) and not completed yet.
func (r *Result) Pending() int { return len(r.pending) }

// Errors counts the errors merged so far.
func (r *Result) Errors() int { return len(r.errors) }

// JSON returns the merged response, two-space indented: "data" (when a
// payload had it), then "errors" (when there are some) and "extensions"
// (when there are some); member order and literals as the server sent
// them. It is "" before a payload was applied.
func (r *Result) JSON() string {
	if r.payloads == 0 {
		return ""
	}
	out := &value{kind: kindObject}
	if r.hasData {
		d := r.data
		if d == nil {
			d = &value{raw: "null"}
		}
		out.set("data", d)
	}
	if len(r.errors) > 0 {
		out.set("errors", &value{kind: kindArray, items: r.errors})
	}
	if r.ext != nil {
		out.set("extensions", r.ext)
	}
	return out.pretty()
}
