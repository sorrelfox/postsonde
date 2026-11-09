package protojson

import (
	"slices"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/desc"
)

// CompletionContext says what the cursor is on.
type CompletionContext string

const (
	// CompleteNone: nothing to suggest (inside a number, after a value,
	// or in a part of the text whose type is unknown).
	CompleteNone CompletionContext = "none"
	// CompleteKey: an object key, a field name in a message.
	CompleteKey CompletionContext = "key"
	// CompleteValue: a value, such as an enum name or true.
	CompleteValue CompletionContext = "value"
)

// CompletionKind is what an item is.
type CompletionKind string

const (
	ItemField     CompletionKind = "field"
	ItemExtension CompletionKind = "extension"
	ItemEnumValue CompletionKind = "enum-value"
	ItemKeyword   CompletionKind = "keyword" // true, false, null
	ItemType      CompletionKind = "type"    // an Any type URL
	ItemMapKey    CompletionKind = "map-key" // "true" or "false" of a bool-keyed map
)

// CompletionItem is one suggestion.
type CompletionItem struct {
	Label string
	Kind  CompletionKind
	// Detail is the short type ("string", "repeated Tag", "map<string,
	// int32>") of a field, the number of an enum value.
	Detail string
	// Type is the full name of the field's message or enum type, if any.
	Type string
	// Documentation is the element's comment in the .proto.
	Documentation string
	Deprecated    bool
	// Number is the field or enum value number.
	Number int32
	// Oneof is the oneof a field belongs to (not a synthetic one).
	Oneof string
	// InsertText replaces the range From–To; Suffix follows it (": {}"
	// after a key), with the cursor SuffixCursor bytes into Suffix.
	InsertText   string
	Suffix       string
	SuffixCursor int
	// Retrigger is true when the position after the insertion has
	// suggestions of its own (an enum field's value), so the editor may
	// ask again at once.
	Retrigger bool
}

// Completion is the result of Complete. From and To are byte offsets.
type Completion struct {
	Context CompletionContext
	From    int
	To      int
	// Prefix is what is typed before the cursor in the replaced range,
	// without the opening quote.
	Prefix string
	// Owner is where the suggestions belong: the message whose fields are
	// suggested, or the field whose values are.
	Owner string
	Items []CompletionItem
}

// Complete returns the suggestions at byte offset of the message JSON
// text of type md (GRPC-17): the fields of the message the cursor is in
// that are not set yet (by JSON name; by .proto name when that is what is
// typed; extensions of pool by "[full.name]"), and the enum value names,
// true, false and null that fit at a value. The text need not be valid
// JSON: an unfinished key, value or object is read as far as it goes.
// Nested messages, lists, maps, oneofs (a set member hides the others)
// and the well-known types (Any by its "@type", Struct and Value as free
// JSON, wrappers by the wrapped kind) are followed. pool may be nil (no
// extensions or Any types).
func Complete(pool *desc.Pool, md *desc.Message, text string, offset int) Completion {
	offset = min(max(offset, 0), len(text))
	c := completer{pool: pool, text: text, pos: offset}
	return c.run(md)
}

// ctok is a token of the lenient scan: one of {}[]:, or a string ('s')
// or a bare word ('w': a literal, a number or anything unquoted).
type ctok struct {
	kind       byte
	start, end int
	str        string // a string's content, unescaped when possible
	closed     bool   // a string that has its closing quote
}

// lenientTokens splits text into tokens without failing: a string without
// its closing quote ends at the end of the line.
func lenientTokens(text string) []ctok {
	var out []ctok
	for i := 0; i < len(text); {
		c := text[i]
		switch c {
		case ' ', '\t', '\r', '\n':
			i++
		case '{', '}', '[', ']', ':', ',':
			out = append(out, ctok{kind: c, start: i, end: i + 1})
			i++
		case '"':
			j := i + 1
			closed := false
			for j < len(text) {
				if text[j] == '\\' && j+1 < len(text) && text[j+1] != '\n' {
					j += 2
					continue
				}
				if text[j] == '"' {
					closed = true
					j++
					break
				}
				if text[j] == '\n' {
					break
				}
				j++
			}
			t := ctok{kind: 's', start: i, end: j, closed: closed}
			if closed {
				if s, err := strconv.Unquote(text[i:j]); err == nil {
					t.str = s
				} else {
					t.str = text[i+1 : j-1]
				}
			} else {
				t.str = text[i+1 : j]
			}
			out = append(out, t)
			i = j
		default:
			j := i
			for j < len(text) && !strings.ContainsRune(" \t\r\n{}[]:,\"", rune(text[j])) {
				j++
			}
			out = append(out, ctok{kind: 'w', start: i, end: j, str: text[i:j]})
			i = j
		}
	}
	return out
}

type shapeKind uint8

const (
	shNone      shapeKind = iota
	shMessage             // an object of md's fields (Any and Empty too)
	shMap                 // an object of map field fd's entries
	shList                // an array of repeated field fd's elements
	shScalar              // a value of non-message field fd
	shWKScalar            // a Timestamp, Duration, FieldMask or wrapper md
	shJSON                // any JSON (google.protobuf.Value)
	shStruct              // an object of any JSON values
	shListValue           // an array of any JSON values
	shTypeURL             // the "@type" of an Any
)

// shape is the type expected at a place in the JSON.
type shape struct {
	k  shapeKind
	md *desc.Message
	fd *desc.Field
	// elem: an element of a list or a map value, where null is not
	// accepted.
	elem bool
	// of is the map field of a map value.
	of *desc.Field
}

func fieldShape(fd *desc.Field, elem bool) shape {
	if fd == nil || checkField(fd) != nil {
		return shape{}
	}
	switch {
	case !elem && fd.IsMap():
		return shape{k: shMap, fd: fd}
	case !elem && fd.Cardinality == desc.Repeated:
		return shape{k: shList, fd: fd}
	case fd.Kind == desc.KindMessage || fd.Kind == desc.KindGroup:
		s := messageShape(fd.Message)
		s.fd, s.elem = fd, elem
		return s
	}
	return shape{k: shScalar, fd: fd, elem: elem}
}

func messageShape(md *desc.Message) shape {
	if md == nil {
		return shape{}
	}
	if isWellKnown(md) && checkWellKnownShape(md) == nil {
		switch md.FullName {
		case structName:
			return shape{k: shStruct, md: md}
		case listValueName:
			return shape{k: shListValue, md: md}
		case valueName:
			return shape{k: shJSON, md: md}
		case anyName, emptyName:
			return shape{k: shMessage, md: md}
		}
		return shape{k: shWKScalar, md: md}
	}
	if md.MapEntry || md.MessageSetWireFormat {
		return shape{}
	}
	return shape{k: shMessage, md: md}
}

const (
	stExpectValue = iota // after '{'-less start, ':' or ',' in an array
	stExpectKey          // after '{' or ',' in an object
	stAfterKey           // after a key, before ':'
	stAfterValue         // after a value
)

type cframe struct {
	obj     bool
	sh      shape
	state   int
	key     string          // the last key read
	keys    map[string]bool // keys set in the object, as written
	typeURL string          // the "@type" of an Any
}

type completer struct {
	pool *desc.Pool
	text string
	pos  int
}

// lookupField finds the field a key names in md, as the parser does.
func (c *completer) lookupField(md *desc.Message, key string) *desc.Field {
	if strings.HasPrefix(key, "[") && strings.HasSuffix(key, "]") {
		if c.pool == nil || strings.HasPrefix(key, "[.") {
			return nil
		}
		fd := c.pool.FindExtension(key[1 : len(key)-1])
		if fd != nil && fd.Extendee == md {
			return fd
		}
		return nil
	}
	if fd := md.FieldByJSONName(key); fd != nil {
		return fd
	}
	for _, f := range md.Fields {
		if textName(f) == key {
			return f
		}
	}
	return nil
}

// anyTarget is the type an Any's "@type" names, or nil.
func (c *completer) anyTarget(typeURL string) *desc.Message {
	if c.pool == nil {
		return nil
	}
	name := typeURL[strings.LastIndexByte(typeURL, '/')+1:]
	if name == "" || name[0] == '.' {
		return nil
	}
	return c.pool.FindMessage(name)
}

// childShape is the shape of the value of key in object frame f.
func (c *completer) childShape(f *cframe, key string) shape {
	switch f.sh.k {
	case shMessage:
		md := f.sh.md
		if md.FullName == anyName {
			if key == "@type" {
				return shape{k: shTypeURL}
			}
			target := c.anyTarget(f.typeURL)
			if target == nil {
				return shape{}
			}
			ts := messageShape(target)
			if ts.k != shMessage {
				if key == "value" {
					return ts
				}
				return shape{}
			}
			md = target
		}
		return fieldShape(c.lookupField(md, key), false)
	case shMap:
		s := fieldShape(f.sh.fd.MapValue(), true)
		s.of = f.sh.fd
		return s
	case shStruct, shJSON:
		return shape{k: shJSON}
	}
	return shape{}
}

// elemShape is the shape of an element of array frame f.
func elemShape(f *cframe) shape {
	switch f.sh.k {
	case shList:
		return fieldShape(f.sh.fd, true)
	case shListValue, shJSON:
		return shape{k: shJSON, elem: true}
	}
	return shape{}
}

// valueShape is the shape expected at a value position in f.
func (c *completer) valueShape(f *cframe) shape {
	if f.obj {
		return c.childShape(f, f.key)
	}
	return elemShape(f)
}

func (c *completer) run(md *desc.Message) Completion {
	toks := lenientTokens(c.text)
	// The token the cursor is in: a string or a word being typed.
	cur := -1
	for i, t := range toks {
		if t.start >= c.pos {
			break
		}
		inString := t.kind == 's' && (c.pos < t.end || !t.closed && c.pos <= t.end)
		inWord := t.kind == 'w' && c.pos <= t.end
		if inString || inWord {
			cur = i
			break
		}
	}

	root := &cframe{sh: messageShape(md), state: stExpectValue}
	root.sh.elem = true // no null for the whole message
	stack := []*cframe{root}
	var at *cframe // the frame at the cursor
	atState := 0
	snap := func() {
		at = stack[len(stack)-1]
		atState = at.state
	}
	// value is called for a value token in the top frame.
	value := func() {
		top := stack[len(stack)-1]
		top.state = stAfterValue
	}
	for i, t := range toks {
		if at == nil && (i == cur || cur < 0 && t.start >= c.pos) {
			snap()
		}
		top := stack[len(stack)-1]
		switch t.kind {
		case '{', '[':
			var sh shape
			switch {
			case top == root:
				if top.state == stExpectValue {
					sh = top.sh
				}
			case top.state == stExpectValue:
				sh = c.valueShape(top)
			}
			top.state = stAfterValue
			f := &cframe{obj: t.kind == '{', sh: sh, state: stExpectValue}
			if f.obj {
				f.state = stExpectKey
				f.keys = map[string]bool{}
			}
			stack = append(stack, f)
		case '}', ']':
			wantObj := t.kind == '}'
			for j := len(stack) - 1; j > 0; j-- {
				if stack[j].obj == wantObj {
					stack = stack[:j]
					break
				}
			}
		case ':':
			if top.obj && top.state == stAfterKey {
				top.state = stExpectValue
			}
		case ',':
			if top.obj {
				top.state = stExpectKey
			} else if top != root {
				top.state = stExpectValue
			}
		case 's', 'w':
			if top.obj && (top.state == stExpectKey || top.state == stAfterValue) && (t.kind == 's' || i == cur) {
				top.key = t.str
				if i != cur {
					top.keys[t.str] = true
				}
				top.state = stAfterKey
				continue
			}
			if top.obj && top.state == stExpectValue && top.key == "@type" && t.kind == 's' && i != cur {
				top.typeURL = t.str
			}
			value()
		}
	}
	if at == nil {
		snap()
	}

	out := Completion{Context: CompleteNone, From: c.pos, To: c.pos}
	var tok *ctok
	if cur >= 0 {
		tok = &toks[cur]
		out.From, out.To = tok.start, tok.end
		if tok.kind == 's' {
			out.Prefix = c.text[tok.start+1 : c.pos]
		} else {
			out.Prefix = c.text[tok.start:c.pos]
		}
	}

	switch {
	case at.obj && (atState == stExpectKey || atState == stAfterValue && tok != nil):
		out.Context = CompleteKey
		c.keys(&out, at)
	case atState == stExpectValue && (at != root || tok != nil):
		sh := at.sh
		if at != root {
			sh = c.valueShape(at)
		}
		if sh.k != shNone {
			out.Context = CompleteValue
			c.values(&out, sh, tok)
		}
	}
	if len(out.Items) == 0 {
		out.Context = CompleteNone
	}
	return out
}

// match ranks name against prefix: 0 starts with it, 1 starts with it
// ignoring case, 2 contains it ignoring case, -1 no match.
func match(name, prefix string) int {
	switch {
	case strings.HasPrefix(name, prefix):
		return 0
	case strings.HasPrefix(strings.ToLower(name), strings.ToLower(prefix)):
		return 1
	case strings.Contains(strings.ToLower(name), strings.ToLower(prefix)):
		return 2
	}
	return -1
}

type ranked struct {
	rank int
	item CompletionItem
}

func sortRanked(rs []ranked) []CompletionItem {
	slices.SortStableFunc(rs, func(a, b ranked) int { return a.rank - b.rank })
	out := make([]CompletionItem, len(rs))
	for i, r := range rs {
		out[i] = r.item
	}
	return out
}

// followedByColon reports whether the next thing after the range is ':'.
func (c *completer) followedByColon(to int) bool {
	rest := strings.TrimLeft(c.text[to:], " \t\r\n")
	return strings.HasPrefix(rest, ":")
}

// keys suggests the keys of object frame f.
func (c *completer) keys(out *Completion, f *cframe) {
	colon := c.followedByColon(out.To)
	var rs []ranked
	add := func(name string, rank int, it CompletionItem, sh shape) {
		it.Label = name
		it.InsertText = strconv.Quote(name)
		if !colon {
			it.Suffix, it.SuffixCursor = keySuffix(sh)
		}
		it.Retrigger = !colon && hasSuggestions(sh)
		rs = append(rs, ranked{rank, it})
	}
	switch f.sh.k {
	case shMessage:
		md := f.sh.md
		out.Owner = md.FullName
		if md.FullName == anyName {
			if !f.keys["@type"] {
				if r := match("@type", out.Prefix); r >= 0 {
					add("@type", r, CompletionItem{Kind: ItemField, Detail: "string",
						Documentation: "The type of the packed message: type.googleapis.com/ followed by its full name."}, shape{k: shTypeURL})
				}
			}
			target := c.anyTarget(f.typeURL)
			if target == nil {
				break
			}
			if ts := messageShape(target); ts.k != shMessage {
				if !f.keys["value"] {
					if r := match("value", out.Prefix); r >= 0 {
						add("value", r, CompletionItem{Kind: ItemField, Detail: target.Name, Type: target.FullName,
							Documentation: "The " + target.FullName + " in its JSON form."}, ts)
					}
				}
				break
			}
			md = target
			out.Owner = md.FullName
		}
		c.fieldItems(md, f, out.Prefix, add)
	case shMap:
		out.Owner = f.sh.fd.FullName
		if key := f.sh.fd.MapKey(); key != nil && key.Kind == desc.KindBool {
			for _, k := range []string{"true", "false"} {
				if f.keys[k] {
					continue
				}
				if r := match(k, out.Prefix); r >= 0 {
					add(k, r, CompletionItem{Kind: ItemMapKey, Detail: "bool"}, fieldShape(f.sh.fd.MapValue(), true))
				}
			}
		}
	}
	out.Items = sortRanked(rs)
}

// fieldItems adds the fields of md not set in f, and md's extensions.
func (c *completer) fieldItems(md *desc.Message, f *cframe, prefix string, add func(string, int, CompletionItem, shape)) {
	set := func(fd *desc.Field) bool {
		return f.keys[fd.JSONName] || f.keys[textName(fd)] || f.keys["["+fd.FullName+"]"]
	}
	usedOneofs := map[*desc.Oneof]bool{}
	for _, fd := range md.Fields {
		if fd.Oneof != nil && !fd.Oneof.Synthetic && set(fd) {
			usedOneofs[fd.Oneof] = true
		}
	}
	for _, fd := range md.Fields {
		if checkField(fd) != nil || set(fd) || fd.Oneof != nil && usedOneofs[fd.Oneof] {
			continue
		}
		name, r := fd.JSONName, match(fd.JSONName, prefix)
		// The .proto name when that is what is typed.
		if tn := textName(fd); tn != name && prefix != "" {
			if rt := match(tn, prefix); rt >= 0 && (r < 0 || rt < r) {
				name, r = tn, rt
			}
		}
		if r < 0 {
			continue
		}
		add(name, r, fieldItem(fd, ItemField), fieldShape(fd, false))
	}
	if c.pool == nil {
		return
	}
	for _, fd := range c.pool.Extensions(md) {
		if checkField(fd) != nil || set(fd) || !md.InExtensionRange(fd.Number) {
			continue
		}
		name := "[" + fd.FullName + "]"
		r := match(name, prefix)
		if r < 0 {
			r = match(fd.FullName, prefix)
		}
		if r < 0 {
			continue
		}
		add(name, r, fieldItem(fd, ItemExtension), fieldShape(fd, false))
	}
}

func fieldItem(fd *desc.Field, kind CompletionKind) CompletionItem {
	it := CompletionItem{
		Kind:          kind,
		Detail:        fieldTypeLabel(fd),
		Documentation: docComment(&fd.Base),
		Deprecated:    fd.Deprecated,
		Number:        fd.Number,
	}
	if fd.IsMap() {
		if v := fd.MapValue(); v != nil {
			it.Type = typeFullName(v)
		}
	} else {
		it.Type = typeFullName(fd)
	}
	if fd.Oneof != nil && !fd.Oneof.Synthetic {
		it.Oneof = fd.Oneof.Name
	}
	return it
}

func typeFullName(fd *desc.Field) string {
	switch {
	case fd.Message != nil:
		return fd.Message.FullName
	case fd.Enum != nil:
		return fd.Enum.FullName
	}
	return ""
}

// typeLabel is the short type of a single value of fd.
func typeLabel(fd *desc.Field) string {
	switch {
	case fd == nil:
		return "?"
	case fd.Message != nil:
		return fd.Message.Name
	case fd.Enum != nil:
		return fd.Enum.Name
	}
	return fd.Kind.String()
}

// fieldTypeLabel is "string", "repeated Tag" or "map<string, int32>".
func fieldTypeLabel(fd *desc.Field) string {
	switch {
	case fd.IsMap():
		return "map<" + typeLabel(fd.MapKey()) + ", " + typeLabel(fd.MapValue()) + ">"
	case fd.Cardinality == desc.Repeated:
		return "repeated " + typeLabel(fd)
	}
	return typeLabel(fd)
}

// docComment is an element's leading comment, else its trailing one, with
// each line trimmed.
func docComment(b *desc.Base) string {
	text := b.Comments.Leading
	if strings.TrimSpace(text) == "" {
		text = b.Comments.Trailing
	}
	lines := strings.Split(strings.TrimSpace(text), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	return strings.Join(lines, "\n")
}

// keySuffix is what follows an inserted key for a value of shape sh, and
// where the cursor goes in it: inside the braces, brackets or quotes.
func keySuffix(sh shape) (string, int) {
	switch sh.k {
	case shMessage, shMap, shStruct:
		return ": {}", 3
	case shList, shListValue:
		return ": []", 3
	case shTypeURL:
		return `: ""`, 3
	case shWKScalar:
		if k, ok := wrapperKinds[sh.md.FullName]; ok {
			if k == desc.KindString || k == desc.KindBytes {
				return `: ""`, 3
			}
			return ": ", 2
		}
		return `: ""`, 3 // Timestamp, Duration, FieldMask
	case shScalar:
		switch sh.fd.Kind {
		case desc.KindString, desc.KindBytes, desc.KindEnum:
			if !isNullValueField(sh.fd) {
				return `: ""`, 3
			}
		}
	}
	return ": ", 2
}

// hasSuggestions reports whether a value of shape sh, as keySuffix leaves
// it, has suggestions: enum names, true and false, or fields.
func hasSuggestions(sh shape) bool {
	switch sh.k {
	case shMessage:
		return sh.md.FullName == anyName || len(sh.md.Fields) > 0
	case shList:
		el := fieldShape(sh.fd, true)
		return el.k == shScalar && (el.fd.Kind == desc.KindEnum || el.fd.Kind == desc.KindBool) || el.k == shMessage && hasSuggestions(el)
	case shScalar:
		return sh.fd.Kind == desc.KindEnum || sh.fd.Kind == desc.KindBool
	case shJSON, shTypeURL:
		return true
	case shWKScalar:
		return wrapperKinds[sh.md.FullName] == desc.KindBool
	}
	return false
}

// values suggests the values of shape sh.
func (c *completer) values(out *Completion, sh shape, tok *ctok) {
	var rs []ranked
	quoted := tok != nil && tok.kind == 's'
	add := func(label string, quote bool, it CompletionItem) {
		// A literal is not typed in quotes, a name is.
		if quoted && !quote || !quote && out.Prefix == label {
			return
		}
		prefix := out.Prefix
		r := match(label, prefix)
		if r < 0 {
			return
		}
		it.Label = label
		it.InsertText = label
		if quote {
			it.InsertText = strconv.Quote(label)
		}
		rs = append(rs, ranked{r, it})
	}
	keyword := func(words ...string) {
		for _, w := range words {
			detail := "bool"
			if w == "null" {
				detail = "unset"
			}
			add(w, false, CompletionItem{Kind: ItemKeyword, Detail: detail})
		}
	}
	if sh.of != nil {
		out.Owner = sh.of.FullName
	} else if sh.fd != nil {
		out.Owner = sh.fd.FullName
	}
	switch sh.k {
	case shScalar:
		fd := sh.fd
		switch {
		case isNullValueField(fd):
			keyword("null")
		case fd.Kind == desc.KindEnum:
			for _, ev := range fd.Enum.Values {
				add(ev.Name, true, CompletionItem{Kind: ItemEnumValue, Detail: strconv.Itoa(int(ev.Number)),
					Type: fd.Enum.FullName, Documentation: docComment(&ev.Base), Deprecated: ev.Deprecated, Number: ev.Number})
			}
			if !sh.elem {
				keyword("null")
			}
		case fd.Kind == desc.KindBool:
			keyword("true", "false")
			if !sh.elem {
				keyword("null")
			}
		}
	case shWKScalar:
		if wrapperKinds[sh.md.FullName] == desc.KindBool {
			keyword("true", "false")
		}
		if !sh.elem {
			keyword("null")
		}
	case shJSON:
		keyword("true", "false", "null")
	case shMessage, shMap, shList, shStruct, shListValue:
		if !sh.elem && sh.fd != nil {
			keyword("null")
		}
	case shTypeURL:
		out.Owner = anyName
		if c.pool == nil {
			break
		}
		for _, f := range c.pool.Files() {
			desc.Walk(f, func(d desc.Descriptor) bool {
				md, ok := d.(*desc.Message)
				if !ok || md.MapEntry {
					return true
				}
				url := "type.googleapis.com/" + md.FullName
				r := match(url, out.Prefix)
				if r < 0 {
					r = match(md.FullName, out.Prefix)
				}
				if r >= 0 {
					rs = append(rs, ranked{r, CompletionItem{Label: url, Kind: ItemType, Detail: md.Name, Type: md.FullName,
						Documentation: docComment(&md.Base), Deprecated: md.Deprecated, InsertText: strconv.Quote(url)}})
				}
				return true
			})
		}
	}
	out.Items = sortRanked(rs)
}
