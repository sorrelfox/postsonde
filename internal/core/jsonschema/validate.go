package jsonschema

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// state is one validation.
type state struct {
	ctx    interface{ Err() error }
	lim    Limits
	annots bool
	format bool
	nodes  map[string]*node

	steps     int
	errs      []Error
	truncated bool
	quiet     int
	fatal     error

	inst   []pathToken
	kw     []string
	scope  []*resource
	active map[activeKey]bool
}

// activeKey is a schema reached through a reference while it is being
// applied to the value at instance depth: reaching it again at the same
// depth, before going deeper into the value, never ends.
type activeKey struct {
	n     *node
	depth int
}

// annot is what a schema evaluated of an object or array, for
// unevaluatedProperties and unevaluatedItems.
type annot struct {
	props     map[string]bool
	allProps  bool
	items     int // the first items evaluated by prefixItems
	allItems  bool
	contained map[int]bool
}

func (a *annot) merge(b *annot) {
	if a == nil || b == nil {
		return
	}
	a.allProps = a.allProps || b.allProps
	for k := range b.props {
		if a.props == nil {
			a.props = map[string]bool{}
		}
		a.props[k] = true
	}
	a.items = max(a.items, b.items)
	a.allItems = a.allItems || b.allItems
	for k := range b.contained {
		if a.contained == nil {
			a.contained = map[int]bool{}
		}
		a.contained[k] = true
	}
}

func (a *annot) markProp(name string) {
	if a == nil {
		return
	}
	if a.props == nil {
		a.props = map[string]bool{}
	}
	a.props[name] = true
}

// stopEarly: the rest of a failing schema need not be checked (a quiet
// evaluation, or no room for more errors).
func (st *state) stopEarly() bool {
	return st.quiet > 0 || st.truncated || st.fatal != nil
}

func (st *state) step(n int) bool {
	st.steps += n
	if st.lim.MaxSteps > 0 && st.steps > st.lim.MaxSteps {
		st.fatal = errs.New(errs.Unsupported, "Validating against the JSON schema needs too much work on this value.").
			WithCode(CodeTooComplex).
			WithHint("Simplify the schema (fewer nested anyOf and oneOf), or check a smaller part of the value.").
			WithDetail("maxSteps", strconv.Itoa(st.lim.MaxSteps))
		return false
	}
	if st.steps&1023 < n {
		if err := st.ctx.Err(); err != nil {
			st.fatal = errs.Wrap(err, errs.KindOf(err), "Validating against the JSON schema was interrupted.")
			return false
		}
	}
	return true
}

// fail records an error of keyword at n for the current value.
func (st *state) fail(n *node, keyword, msg string) {
	if st.quiet > 0 {
		return
	}
	if len(st.errs) >= st.lim.MaxErrors {
		st.truncated = true
		return
	}
	kwPath := pointer(st.kw)
	schemaPath := fragment(n.loc)
	if keyword != "false" {
		kwPath += "/" + escapeToken(keyword)
		schemaPath += "/" + escapeToken(keyword)
	}
	st.errs = append(st.errs, Error{
		InstancePath: instancePointer(st.inst),
		Path:         jsonPath(st.inst),
		KeywordPath:  kwPath,
		SchemaPath:   schemaPath,
		Keyword:      keyword,
		Message:      msg,
	})
}

func instancePointer(tokens []pathToken) string {
	var b strings.Builder
	for _, t := range tokens {
		b.WriteByte('/')
		b.WriteString(escapeToken(t.name))
	}
	return b.String()
}

// child applies n to v, a value at inst below the current one (nil: the
// current one), through the keyword tokens kw.
func (st *state) child(n *node, v any, depth int, inst *pathToken, kw ...string) (bool, *annot) {
	st.kw = append(st.kw, kw...)
	if inst != nil {
		st.inst = append(st.inst, *inst)
	}
	ok, a := st.validate(n, v, depth+1)
	st.kw = st.kw[:len(st.kw)-len(kw)]
	if inst != nil {
		st.inst = st.inst[:len(st.inst)-1]
	}
	return ok, a
}

// quietly applies n to the current value without recording errors.
func (st *state) quietly(n *node, v any, depth int, kw ...string) (bool, *annot) {
	st.quiet++
	ok, a := st.child(n, v, depth, nil, kw...)
	st.quiet--
	return ok, a
}

func (st *state) follow(target *node, v any, depth int, word string) (bool, *annot) {
	key := activeKey{target, len(st.inst)}
	if st.active[key] {
		st.fatal = errs.New(errs.Unsupported, "The JSON schema refers to "+fragment(target.loc)+" again while applying it to the same value, which never ends.").
			WithCode(CodeTooDeep).
			WithHint("Check the schema's "+word+" cycles: each round must go into a property or an item.").
			WithDetail("location", fragment(target.loc))
		return false, nil
	}
	st.active[key] = true
	ok, a := st.child(target, v, depth, nil, word)
	delete(st.active, key)
	return ok, a
}

func (st *state) validate(n *node, v any, depth int) (bool, *annot) {
	if st.fatal != nil || !st.step(1) {
		return false, nil
	}
	if depth > st.lim.MaxDepth {
		st.fatal = errs.New(errs.Unsupported, "The JSON schema nests more than "+strconv.Itoa(st.lim.MaxDepth)+" schemas deep on this value.").
			WithCode(CodeTooDeep).
			WithDetail("maxDepth", strconv.Itoa(st.lim.MaxDepth))
		return false, nil
	}
	if n.boolean != nil {
		if *n.boolean {
			return true, nil
		}
		st.fail(n, "false", st.falseMessage())
		return false, nil
	}
	if len(st.scope) == 0 || st.scope[len(st.scope)-1] != n.res {
		st.scope = append(st.scope, n.res)
		defer func() { st.scope = st.scope[:len(st.scope)-1] }()
	}
	var ann *annot
	if st.annots {
		ann = &annot{}
	}
	ok := true
	bad := func() bool {
		ok = false
		return st.stopEarly()
	}

	if n.ref != nil {
		sub, a := st.follow(n.ref, v, depth, "$ref")
		if !sub && bad() {
			return false, nil
		}
		if sub {
			ann.merge(a)
		}
		if n.refIsOnly {
			return ok, ann
		}
	}
	if n.dynRef != nil {
		target := n.dynRef
		if n.dynAnchor != "" {
			for _, r := range st.scope {
				if loc, found := r.dynAnchors[n.dynAnchor]; found {
					if t := st.nodes[loc]; t != nil {
						target = t
					}
					break
				}
			}
		}
		sub, a := st.follow(target, v, depth, n.dynRefWord)
		if !sub && bad() {
			return false, nil
		}
		if sub {
			ann.merge(a)
		}
	}

	if len(n.types) > 0 && !typeMatches(n.types, v) {
		st.fail(n, "type", "must be "+joinOr(n.types)+", not "+typeOf(v))
		if bad() {
			return false, nil
		}
	}
	if n.hasEnum && !inEnum(n.enum, v) {
		st.fail(n, "enum", enumMessage(n.enum))
		if bad() {
			return false, nil
		}
	}
	if n.hasConst && !equal(n.constV, v) {
		st.fail(n, "const", "must equal "+show(n.constV))
		if bad() {
			return false, nil
		}
	}

	switch typeOf(v) {
	case "number":
		if !st.checkNumber(n, v) && bad() {
			return false, nil
		}
	case "string":
		if !st.checkString(n, v.(string)) && bad() {
			return false, nil
		}
	case "array":
		if !st.checkArray(n, v.([]any), depth, ann) && bad() {
			return false, nil
		}
	case "object":
		o, _ := asObject(v)
		if !st.checkObject(n, o, depth, ann) && bad() {
			return false, nil
		}
	}

	// In-place applicators.
	for i, s := range n.allOf {
		sub, a := st.child(s, v, depth, nil, "allOf", strconv.Itoa(i))
		if !sub && bad() {
			return false, nil
		}
		if sub {
			ann.merge(a)
		}
	}
	if len(n.anyOf) > 0 {
		passed := false
		for i, s := range n.anyOf {
			sub, a := st.quietly(s, v, depth, "anyOf", strconv.Itoa(i))
			if st.fatal != nil {
				return false, nil
			}
			if sub {
				passed = true
				ann.merge(a)
				if !st.annots {
					break
				}
			}
		}
		if !passed {
			st.fail(n, "anyOf", "must match at least one of the "+strconv.Itoa(len(n.anyOf))+" schemas in anyOf")
			if bad() {
				return false, nil
			}
		}
	}
	if len(n.oneOf) > 0 {
		var matched []string
		var first *annot
		for i, s := range n.oneOf {
			sub, a := st.quietly(s, v, depth, "oneOf", strconv.Itoa(i))
			if st.fatal != nil {
				return false, nil
			}
			if sub {
				matched = append(matched, strconv.Itoa(i))
				first = a
				if len(matched) > 1 && !st.annots {
					break
				}
			}
		}
		switch len(matched) {
		case 1:
			ann.merge(first)
		case 0:
			st.fail(n, "oneOf", "must match exactly one of the "+strconv.Itoa(len(n.oneOf))+" schemas in oneOf, but matches none")
		default:
			st.fail(n, "oneOf", "must match exactly one of the schemas in oneOf, but matches "+strings.Join(matched, " and "))
		}
		if len(matched) != 1 && bad() {
			return false, nil
		}
	}
	if n.not != nil {
		sub, _ := st.quietly(n.not, v, depth, "not")
		if st.fatal != nil {
			return false, nil
		}
		if sub {
			st.fail(n, "not", "must not match the schema in not")
			if bad() {
				return false, nil
			}
		}
	}
	if n.ifS != nil {
		cond, a := st.quietly(n.ifS, v, depth, "if")
		if st.fatal != nil {
			return false, nil
		}
		var branch *node
		word := "then"
		if cond {
			ann.merge(a)
			branch = n.thenS
		} else {
			branch, word = n.elseS, "else"
		}
		if branch != nil {
			sub, a := st.child(branch, v, depth, nil, word)
			if !sub && bad() {
				return false, nil
			}
			if sub {
				ann.merge(a)
			}
		}
	}
	for _, d := range n.dependentSchemas {
		o, isObj := asObject(v)
		if !isObj {
			break
		}
		if _, has := o.Get(d.name); !has {
			continue
		}
		sub, a := st.child(d.n, v, depth, nil, d.word, d.name)
		if !sub && bad() {
			return false, nil
		}
		if sub {
			ann.merge(a)
		}
	}

	// Unevaluated keywords see what every other keyword evaluated.
	if n.unevaluatedProperties != nil {
		if o, isObj := asObject(v); isObj && !ann.allProps {
			for i := range o.Len() {
				name, mv := o.Member(i)
				if ann.props[name] {
					continue
				}
				sub, _ := st.child(n.unevaluatedProperties, mv, depth, &pathToken{name: name}, "unevaluatedProperties")
				if !sub && bad() {
					return false, nil
				}
			}
			ann.allProps = true
		}
	}
	if n.unevaluatedItems != nil {
		if arr, isArr := v.([]any); isArr && !ann.allItems {
			for i := ann.items; i < len(arr); i++ {
				if ann.contained[i] {
					continue
				}
				sub, _ := st.child(n.unevaluatedItems, arr[i], depth, &pathToken{name: strconv.Itoa(i), index: true}, "unevaluatedItems")
				if !sub && bad() {
					return false, nil
				}
			}
			ann.allItems = true
		}
	}
	if !ok {
		return false, nil
	}
	return true, ann
}

// falseMessage is the message of a false schema, naming the keyword that
// led to it.
func (st *state) falseMessage() string {
	k := len(st.kw)
	if k > 0 {
		switch st.kw[k-1] {
		case "additionalProperties", "unevaluatedProperties", "items", "additionalItems", "unevaluatedItems", "not", "then", "else", "$ref":
			return "is not allowed (" + st.kw[k-1] + " is false)"
		}
	}
	if k > 1 {
		switch st.kw[k-2] {
		case "properties", "patternProperties", "prefixItems", "items", "dependentSchemas", "dependencies":
			return "is not allowed (" + st.kw[k-2] + ")"
		}
	}
	return "is not allowed here (the schema is false)"
}

func typeMatches(types []string, v any) bool {
	t := typeOf(v)
	for _, want := range types {
		if want == t {
			return true
		}
		if want == "integer" && t == "number" {
			if n, _ := asNumber(v); n.isInt() {
				return true
			}
		}
	}
	return false
}

func joinOr(list []string) string {
	switch len(list) {
	case 1:
		return list[0]
	case 2:
		return list[0] + " or " + list[1]
	}
	return strings.Join(list[:len(list)-1], ", ") + " or " + list[len(list)-1]
}

func inEnum(enum []any, v any) bool {
	for _, e := range enum {
		if equal(e, v) {
			return true
		}
	}
	return false
}

func enumMessage(enum []any) string {
	if len(enum) == 1 {
		return "must equal " + show(enum[0])
	}
	if len(enum) <= 6 {
		parts := make([]string, len(enum))
		for i, e := range enum {
			parts[i] = show(e)
		}
		return "must be one of " + strings.Join(parts, ", ")
	}
	return "must be one of the " + strconv.Itoa(len(enum)) + " values in enum"
}

func (st *state) checkNumber(n *node, v any) bool {
	x, _ := asNumber(v)
	ok := true
	check := func(bound *number, keyword string, pass func(c int) bool, words string) {
		if bound == nil || !ok && st.stopEarly() {
			return
		}
		if !pass(x.cmp(*bound)) {
			st.fail(n, keyword, "must be "+words+" "+bound.String()+", not "+x.String())
			ok = false
		}
	}
	check(n.minimum, "minimum", func(c int) bool { return c >= 0 }, "at least")
	check(n.maximum, "maximum", func(c int) bool { return c <= 0 }, "at most")
	check(n.exclMin, "exclusiveMinimum", func(c int) bool { return c > 0 }, "greater than")
	check(n.exclMax, "exclusiveMaximum", func(c int) bool { return c < 0 }, "less than")
	if n.multipleOf != nil && (ok || !st.stopEarly()) && !x.multipleOf(*n.multipleOf) {
		st.fail(n, "multipleOf", "must be a multiple of "+n.multipleOf.String()+", not "+x.String())
		ok = false
	}
	return ok
}

func (st *state) checkString(n *node, s string) bool {
	ok := true
	if n.minLength >= 0 || n.maxLength >= 0 {
		st.step(len(s) / 64)
		l := utf8.RuneCountInString(s)
		if n.minLength >= 0 && l < n.minLength {
			st.fail(n, "minLength", fmt.Sprintf("must have at least %s, not %d", plural(n.minLength, "character"), l))
			ok = false
		}
		if n.maxLength >= 0 && l > n.maxLength && (ok || !st.stopEarly()) {
			st.fail(n, "maxLength", fmt.Sprintf("must have at most %s, not %d", plural(n.maxLength, "character"), l))
			ok = false
		}
	}
	if n.pattern != nil && (ok || !st.stopEarly()) {
		st.step(len(s) / 16)
		if !n.pattern.MatchString(s) {
			st.fail(n, "pattern", "must match the pattern "+show(n.patternSrc))
			ok = false
		}
	}
	if st.format && n.format != "" && (ok || !st.stopEarly()) {
		st.step(len(s) / 64)
		if why := checkFormat(n.format, s); why != "" {
			st.fail(n, "format", why)
			ok = false
		}
	}
	return ok
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

func (st *state) checkArray(n *node, arr []any, depth int, ann *annot) bool {
	ok := true
	bad := func() bool {
		ok = false
		return st.stopEarly()
	}
	if len(arr) < n.minItems {
		st.fail(n, "minItems", fmt.Sprintf("must have at least %s, not %d", plural(n.minItems, "item"), len(arr)))
		if bad() {
			return false
		}
	}
	if n.maxItems >= 0 && len(arr) > n.maxItems {
		st.fail(n, "maxItems", fmt.Sprintf("must have at most %s, not %d", plural(n.maxItems, "item"), len(arr)))
		if bad() {
			return false
		}
	}
	if n.uniqueItems && len(arr) > 1 {
		seen := make(map[string]int, len(arr))
		for i, e := range arr {
			var b strings.Builder
			canonical(&b, e)
			if !st.step(1 + b.Len()/64) {
				return false
			}
			if j, dup := seen[b.String()]; dup {
				st.fail(n, "uniqueItems", fmt.Sprintf("must have unique items, but items %d and %d are equal", j, i))
				if bad() {
					return false
				}
				break
			}
			seen[b.String()] = i
		}
	}
	for i, s := range n.prefixItems {
		if i >= len(arr) {
			break
		}
		sub, _ := st.child(s, arr[i], depth, &pathToken{name: strconv.Itoa(i), index: true}, n.prefixWord, strconv.Itoa(i))
		if !sub && bad() {
			return false
		}
	}
	if ann != nil {
		ann.items = max(ann.items, min(len(n.prefixItems), len(arr)))
	}
	if n.items != nil {
		for i := len(n.prefixItems); i < len(arr); i++ {
			sub, _ := st.child(n.items, arr[i], depth, &pathToken{name: strconv.Itoa(i), index: true}, n.itemsWord)
			if !sub && bad() {
				return false
			}
		}
		if ann != nil {
			ann.allItems = true
		}
	}
	if n.contains != nil {
		count := 0
		for i, e := range arr {
			sub, _ := st.quietlyAt(n.contains, e, depth, i)
			if st.fatal != nil {
				return false
			}
			if sub {
				count++
				if ann != nil {
					if ann.contained == nil {
						ann.contained = map[int]bool{}
					}
					ann.contained[i] = true
				} else if n.maxContains < 0 && count >= n.minContains {
					break
				}
			}
		}
		switch {
		case count < n.minContains:
			word := "contains"
			if n.minContains != 1 {
				word = "minContains"
			}
			st.fail(n, word, fmt.Sprintf("must contain at least %s matching the contains schema, not %d", plural(n.minContains, "item"), count))
			if bad() {
				return false
			}
		case n.maxContains >= 0 && count > n.maxContains:
			st.fail(n, "maxContains", fmt.Sprintf("must contain at most %s matching the contains schema, not %d", plural(n.maxContains, "item"), count))
			if bad() {
				return false
			}
		}
	}
	return ok
}

// quietlyAt applies n to item i of the current array without recording
// errors.
func (st *state) quietlyAt(n *node, v any, depth, i int) (bool, *annot) {
	st.quiet++
	ok, a := st.child(n, v, depth, &pathToken{name: strconv.Itoa(i), index: true}, "contains")
	st.quiet--
	return ok, a
}

func (st *state) checkObject(n *node, o object, depth int, ann *annot) bool {
	ok := true
	bad := func() bool {
		ok = false
		return st.stopEarly()
	}
	count := o.Len()
	if count < n.minProps {
		st.fail(n, "minProperties", fmt.Sprintf("must have at least %s, not %d", plural(n.minProps, "property"), count))
		if bad() {
			return false
		}
	}
	if n.maxProps >= 0 && count > n.maxProps {
		st.fail(n, "maxProperties", fmt.Sprintf("must have at most %s, not %d", plural(n.maxProps, "property"), count))
		if bad() {
			return false
		}
	}
	for _, name := range n.required {
		if _, has := o.Get(name); !has {
			st.fail(n, "required", "missing required property "+quote(name))
			if bad() {
				return false
			}
		}
	}
	for _, d := range n.dependentRequired {
		if _, has := o.Get(d.name); !has {
			continue
		}
		for _, name := range d.required {
			if _, has := o.Get(name); !has {
				st.fail(n, d.word, "property "+quote(d.name)+" requires property "+quote(name))
				if bad() {
					return false
				}
			}
		}
	}
	for _, p := range n.properties {
		mv, has := o.Get(p.name)
		if !has {
			continue
		}
		sub, _ := st.child(p.n, mv, depth, &pathToken{name: p.name}, "properties", p.name)
		if !sub && bad() {
			return false
		}
		ann.markProp(p.name)
	}
	needMembers := len(n.patternProps) > 0 || n.additional != nil || n.propertyNames != nil
	if !needMembers {
		return ok
	}
	for i := range count {
		name, mv := o.Member(i)
		if !st.step(1) {
			return false
		}
		if n.propertyNames != nil {
			st.quiet++
			sub, _ := st.validate(n.propertyNames, name, depth+1)
			st.quiet--
			if st.fatal != nil {
				return false
			}
			if !sub {
				st.fail(n, "propertyNames", "property name "+quote(name)+" does not match the propertyNames schema")
				if bad() {
					return false
				}
			}
		}
		matched := false
		for _, pp := range n.patternProps {
			if !pp.re.MatchString(name) {
				continue
			}
			matched = true
			sub, _ := st.child(pp.n, mv, depth, &pathToken{name: name}, "patternProperties", pp.src)
			if !sub && bad() {
				return false
			}
			ann.markProp(name)
		}
		if n.additional == nil || matched || hasProperty(n.properties, name) {
			continue
		}
		sub, _ := st.child(n.additional, mv, depth, &pathToken{name: name}, "additionalProperties")
		if !sub && bad() {
			return false
		}
		ann.markProp(name)
	}
	return ok
}

func hasProperty(props []namedNode, name string) bool {
	for _, p := range props {
		if p.name == name {
			return true
		}
	}
	return false
}
