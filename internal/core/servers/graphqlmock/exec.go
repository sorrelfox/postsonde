package graphqlmock

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/graphql/ast"
	"github.com/sorrelfox/postsonde/internal/core/graphql/parser"
	"github.com/sorrelfox/postsonde/internal/core/graphql/schema"
	"github.com/sorrelfox/postsonde/internal/core/graphql/validate"
)

// The executor follows the spec's Execution section: the operation is
// picked (GetOperation), its variables coerced (CoerceVariableValues), the
// root selection set executed (ExecuteSelectionSet, CollectFields with
// @skip and @include, fragments that apply to the object's type, fields
// merged by response key), each field's arguments coerced and its value
// completed against its type (CompleteValue: non-null checks, lists,
// leaves serialized, abstract types resolved to an object type), and field
// errors made null at the nearest nullable position. The values come from
// the overrides and from generated data instead of resolvers.

// Error codes of the response's errors (extensions.code).
const (
	ErrCodeParse      = "GRAPHQL_PARSE_FAILED"
	ErrCodeValidation = "GRAPHQL_VALIDATION_FAILED"
	ErrCodeBadInput   = "BAD_USER_INPUT"
	ErrCodeOperation  = "OPERATION_RESOLUTION_FAILURE"
	ErrCodeBadRequest = "BAD_REQUEST"
	ErrCodeMock       = "MOCK_VALUE_INVALID"
	ErrCodeTooLarge   = "RESPONSE_TOO_LARGE"
)

// maxValues bounds the values of one response: long lists nested a few
// levels deep grow fast.
const maxValues = 250_000

// gqlError is one entry of a response's errors.
type gqlError struct {
	Message   string
	Locations []ast.Position
	// Path is set on field errors: response keys and list indices.
	Path []any
	Code string
}

func (e gqlError) value() object {
	out := object{{"message", e.Message}}
	if len(e.Locations) > 0 {
		locs := make([]any, len(e.Locations))
		for i, p := range e.Locations {
			locs[i] = object{{"line", p.Line}, {"column", p.Column}}
		}
		out = append(out, member{"locations", locs})
	}
	if e.Path != nil {
		out = append(out, member{"path", e.Path})
	}
	if e.Code != "" {
		out = append(out, member{"extensions", object{{"code", e.Code}}})
	}
	return out
}

// result is what executing a request gives.
type result struct {
	// HasData is false for a request error: the response has no data.
	HasData bool
	Data    any
	Errors  []gqlError
	// Operation and OpType name the operation run ("" when none was
	// picked); Introspection is set when it selected __schema or __type.
	Operation     string
	OpType        string
	Introspection bool
}

// body is the response as JSON: errors first, as the spec suggests, then
// data.
func (r result) body() []byte {
	out := object{}
	if len(r.Errors) > 0 {
		list := make([]any, len(r.Errors))
		for i, e := range r.Errors {
			list[i] = e.value()
		}
		out = append(out, member{"errors", list})
	}
	if r.HasData {
		out = append(out, member{"data", r.Data})
	}
	var b bytes.Buffer
	writeJSON(&b, out)
	return b.Bytes()
}

// gqlRequest is a GraphQL request: the document and its parameters.
type gqlRequest struct {
	Query         string
	OperationName string
	Variables     map[string]any
}

// executor runs one request.
type executor struct {
	st    *state
	sch   *schema.Schema
	doc   *ast.Document
	frags map[string]*ast.FragmentDefinition
	vars  map[string]any
	errs  []gqlError

	values   int
	tooLarge bool
}

// fieldError is a field error not reported yet.
type fieldError struct {
	msg  string
	code string
}

func (e *fieldError) Error() string { return e.msg }

// errPropagated is a field error already reported, making the parent
// null.
var errPropagated = errors.New("null propagated")

// execute parses, validates and runs req against st.
func execute(st *state, req gqlRequest, allow func(schema.Operation) *gqlError) result {
	doc, perrs := parser.Parse(req.Query)
	if len(perrs) > 0 {
		out := result{}
		for _, e := range perrs {
			out.Errors = append(out.Errors, gqlError{Message: "Syntax Error: " + e.Message, Locations: []ast.Position{doc.Position(e.Start)}, Code: ErrCodeParse})
			if len(out.Errors) >= 20 {
				break
			}
		}
		return out
	}
	sch := st.loaded.Schema
	var verrs []gqlError
	for _, d := range validate.Validate(sch, doc) {
		if d.Severity != validate.SeverityError {
			continue
		}
		verrs = append(verrs, gqlError{Message: d.Message, Locations: []ast.Position{doc.Position(d.Start)}, Code: ErrCodeValidation})
	}
	if len(verrs) > 0 {
		return result{Errors: verrs}
	}
	e := &executor{st: st, sch: sch, doc: doc, frags: map[string]*ast.FragmentDefinition{}, vars: map[string]any{}}
	var ops []*ast.OperationDefinition
	for _, d := range doc.Definitions {
		switch d := d.(type) {
		case *ast.OperationDefinition:
			ops = append(ops, d)
		case *ast.FragmentDefinition:
			if d.Name != nil {
				if _, dup := e.frags[d.Name.Value]; !dup {
					e.frags[d.Name.Value] = d
				}
			}
		}
	}
	op, opErr := pickOperation(ops, req.OperationName)
	if opErr != nil {
		return result{Errors: []gqlError{*opErr}}
	}
	out := result{OpType: op.Operation.String()}
	if op.Name != nil {
		out.Operation = op.Name.Value
	}
	out.Introspection = selectsIntrospection(op, e.frags)
	if !st.cfg.Introspection && out.Introspection {
		out.Errors = []gqlError{{
			Message:   "GraphQL introspection is turned off on this mock server, but the query asks for __schema or __type.",
			Locations: []ast.Position{doc.Position(op.Start)},
			Code:      ErrCodeValidation,
		}}
		return out
	}
	if allow != nil {
		if gerr := allow(schema.Operation(op.Operation.String())); gerr != nil {
			gerr.Locations = []ast.Position{doc.Position(op.Start)}
			out.Errors = []gqlError{*gerr}
			return out
		}
	}
	if verrs := e.coerceVariables(op, req.Variables); len(verrs) > 0 {
		out.Errors = verrs
		return out
	}
	root := sch.RootType(schema.Operation(op.Operation.String()))
	if root == nil {
		out.Errors = []gqlError{{Message: "The schema has no " + op.Operation.String() + " root type.", Locations: []ast.Position{doc.Position(op.Start)}, Code: ErrCodeValidation}}
		return out
	}
	src := source{seed: rootSeed(st.cfg.Seed, op.Operation.String())}
	data, err := e.selectionSet(root, []*ast.SelectionSet{op.SelectionSet}, src, []any{})
	out.HasData = true
	if err == nil {
		out.Data = data
	}
	out.Errors = e.errs
	return out
}

// pickOperation is GetOperation.
func pickOperation(ops []*ast.OperationDefinition, name string) (*ast.OperationDefinition, *gqlError) {
	if name == "" {
		switch len(ops) {
		case 0:
			return nil, &gqlError{Message: "Must provide an operation.", Code: ErrCodeOperation}
		case 1:
			return ops[0], nil
		}
		return nil, &gqlError{Message: "Must provide operation name if query contains multiple operations.", Code: ErrCodeOperation}
	}
	for _, op := range ops {
		if op.Name != nil && op.Name.Value == name {
			return op, nil
		}
	}
	return nil, &gqlError{Message: "Unknown operation named \"" + name + "\".", Code: ErrCodeOperation}
}

// selectsIntrospection reports whether the operation selects __schema or
// __type at its root (through fragments too).
func selectsIntrospection(op *ast.OperationDefinition, frags map[string]*ast.FragmentDefinition) bool {
	seen := map[string]bool{}
	var walk func(set *ast.SelectionSet) bool
	walk = func(set *ast.SelectionSet) bool {
		if set == nil {
			return false
		}
		for _, sel := range set.Selections {
			switch s := sel.(type) {
			case *ast.Field:
				if s.Name != nil && (s.Name.Value == "__schema" || s.Name.Value == "__type") {
					return true
				}
			case *ast.InlineFragment:
				if walk(s.SelectionSet) {
					return true
				}
			case *ast.FragmentSpread:
				if s.Name == nil || seen[s.Name.Value] {
					continue
				}
				seen[s.Name.Value] = true
				if f := frags[s.Name.Value]; f != nil && walk(f.SelectionSet) {
					return true
				}
			}
		}
		return false
	}
	return walk(op.SelectionSet)
}

// source is what a value is completed from: a JSON value given by an
// override (or the introspection), when has is set, else generated data;
// seed places it in the response either way.
type source struct {
	has  bool
	val  any
	seed uint64
	// required is set inside a non-null type: never generated as null.
	required bool
	// paged is set when a paging argument (first: 5) asks for a generated
	// list of length items.
	paged  bool
	length int
}

// group is the fields of one response key.
type group struct {
	key    string
	fields []*ast.Field
}

// collect is CollectFields over the selection sets of the merged fields.
func (e *executor) collect(obj *schema.Type, sets []*ast.SelectionSet) []*group {
	var groups []*group
	index := map[string]*group{}
	visited := map[string]bool{}
	var walk func(set *ast.SelectionSet)
	walk = func(set *ast.SelectionSet) {
		if set == nil {
			return
		}
		for _, sel := range set.Selections {
			switch s := sel.(type) {
			case *ast.Field:
				if !e.included(s.Directives) || s.Name == nil {
					continue
				}
				key := s.Name.Value
				if s.Alias != nil {
					key = s.Alias.Value
				}
				g := index[key]
				if g == nil {
					g = &group{key: key}
					index[key] = g
					groups = append(groups, g)
				}
				g.fields = append(g.fields, s)
			case *ast.FragmentSpread:
				if s.Name == nil || !e.included(s.Directives) || visited[s.Name.Value] {
					continue
				}
				visited[s.Name.Value] = true
				f := e.frags[s.Name.Value]
				if f == nil || !e.applies(obj, f.TypeCondition) {
					continue
				}
				walk(f.SelectionSet)
			case *ast.InlineFragment:
				if !e.included(s.Directives) || s.TypeCondition != nil && !e.applies(obj, s.TypeCondition) {
					continue
				}
				walk(s.SelectionSet)
			}
		}
	}
	for _, set := range sets {
		walk(set)
	}
	return groups
}

// applies is DoesFragmentTypeApply.
func (e *executor) applies(obj *schema.Type, tc *ast.TypeCondition) bool {
	if tc == nil || tc.Type == nil || tc.Type.Name == nil {
		return true
	}
	return e.sch.IsSubType(tc.Type.Name.Value, obj.Name)
}

// included evaluates @skip and @include.
func (e *executor) included(ds []*ast.Directive) bool {
	for _, d := range ds {
		if d.Name == nil {
			continue
		}
		switch d.Name.Value {
		case "skip":
			if e.directiveIf(d) {
				return false
			}
		case "include":
			if !e.directiveIf(d) {
				return false
			}
		}
	}
	return true
}

func (e *executor) directiveIf(d *ast.Directive) bool {
	if d.Arguments == nil {
		return false
	}
	for _, a := range d.Arguments.Arguments {
		if a.Name == nil || a.Name.Value != "if" {
			continue
		}
		v, _ := e.literal(a.Value, schema.NonNullOf(schema.Named(schema.KindScalar, "Boolean")))
		b, _ := v.(bool)
		return b
	}
	return false
}

// selectionSet executes the merged selection sets on an object of type
// obj. errPropagated means a non-null field failed: the object is null.
func (e *executor) selectionSet(obj *schema.Type, sets []*ast.SelectionSet, src source, path []any) (object, error) {
	groups := e.collect(obj, sets)
	out := make(object, 0, len(groups))
	for _, g := range groups {
		fpath := appendPath(path, g.key)
		v, err := e.field(obj, src, g.fields, fpath)
		if err != nil {
			return nil, err
		}
		out = append(out, member{g.key, v})
	}
	return out, nil
}

func appendPath(path []any, key any) []any {
	out := make([]any, len(path), len(path)+1)
	copy(out, path)
	return append(out, key)
}

// field executes one response key: the value, or null and a reported
// error; errPropagated when it is non-null and failed.
func (e *executor) field(obj *schema.Type, parent source, fields []*ast.Field, path []any) (any, error) {
	f := fields[0]
	name := f.Name.Value
	if name == "__typename" {
		return obj.Name, nil
	}
	def := e.sch.Field(obj.Name, name)
	if def == nil {
		return nil, nil // validation reports unknown fields
	}
	label := obj.Name + "." + name
	v, err := e.fieldValue(obj, parent, def, f, fields, label, path)
	if err == nil {
		return v, nil
	}
	e.report(err, fields, path)
	if def.Type.IsNonNull() {
		return nil, errPropagated
	}
	return nil, nil
}

// report adds err (unless it was reported already) as a field error.
func (e *executor) report(err error, fields []*ast.Field, path []any) {
	if errors.Is(err, errPropagated) {
		return
	}
	fe, ok := err.(*fieldError)
	if !ok {
		fe = &fieldError{msg: err.Error()}
	}
	locs := []ast.Position{e.doc.Position(fields[0].Start)}
	e.errs = append(e.errs, gqlError{Message: fe.msg, Locations: locs, Path: path, Code: fe.code})
}

func (e *executor) fieldValue(obj *schema.Type, parent source, def *schema.Field, f *ast.Field, fields []*ast.Field, label string, path []any) (any, error) {
	args, err := e.arguments(def.Args, f.Arguments)
	if err != nil {
		return nil, err
	}
	name := def.Name
	if obj.Name == e.sch.QueryType && (def == schema.SchemaField || def == schema.TypeField) {
		in := introspector{sch: e.sch}
		var val any
		if def == schema.SchemaField {
			val = in.schemaObject()
		} else {
			n, _ := args["name"].(string)
			val = in.typeByName(n)
		}
		return e.complete(def.Type, fields, label, path, source{has: true, val: val, seed: mix(parent.seed, name)})
	}
	child, err := e.resolve(obj, parent, name, args)
	if err != nil {
		return nil, err
	}
	if !child.has {
		child.length, child.paged = pageSize(f.Arguments, args)
	}
	return e.complete(def.Type, fields, label, path, child)
}

// resolve finds the source of field name of an object: the member of a
// given object, else an override, else generated data.
func (e *executor) resolve(obj *schema.Type, parent source, name string, args map[string]any) (source, error) {
	seed := mix(parent.seed, name, canonical(args))
	if parent.has {
		if m, ok := parent.val.(map[string]any); ok {
			if v, ok := m[name]; ok {
				if lazy, ok := v.(lazyField); ok {
					v = lazy(args)
				}
				return source{has: true, val: v, seed: seed}, nil
			}
		}
	}
	if o := e.st.fieldOverride(obj, name, args); o != nil {
		if o.mode == ModeError {
			return source{}, &fieldError{msg: o.message, code: o.code}
		}
		return source{has: true, val: o.value, seed: seed}, nil
	}
	return source{seed: seed}, nil
}

// complete is CompleteValue.
func (e *executor) complete(t *schema.TypeRef, fields []*ast.Field, label string, path []any, src source) (any, error) {
	if t == nil {
		return nil, nil
	}
	e.values++
	if e.values > maxValues {
		if !e.tooLarge {
			e.tooLarge = true
			return nil, &fieldError{msg: fmt.Sprintf("The response is larger than the mock server makes (%d values): ask for fewer fields, or lower the length of generated lists.", maxValues), code: ErrCodeTooLarge}
		}
		return nil, errPropagated
	}
	if t.Kind == schema.KindNonNull {
		inner := src
		inner.required = true
		v, err := e.complete(t.OfType, fields, label, path, inner)
		if err != nil {
			return nil, err
		}
		if v == nil {
			return nil, &fieldError{msg: "Cannot return null for non-nullable field " + label + ".", code: ErrCodeMock}
		}
		return v, nil
	}
	if src.has && src.val == nil {
		return nil, nil
	}
	if !src.has && !src.required && isNull(src.seed, e.st.cfg.NullPercent) {
		return nil, nil
	}
	if t.Kind == schema.KindList {
		return e.completeList(t, fields, label, path, src)
	}
	named := e.sch.Type(t.Name)
	if named == nil {
		return nil, &fieldError{msg: "The schema does not define the type " + strconv.Quote(t.Name) + " of " + label + ".", code: ErrCodeMock}
	}
	if named.IsLeaf() {
		if !src.has {
			if o := e.st.typeOverride(named.Name); o != nil {
				src = source{has: true, val: o.value, seed: src.seed}
				if o.value == nil {
					return nil, nil
				}
			}
		}
		if !src.has {
			return generateLeaf(named, fieldName(label), src.seed), nil
		}
		v, err := serialize(named, src.val)
		if err != nil {
			return nil, &fieldError{msg: err.Error() + " (the mock value of " + label + ")", code: ErrCodeMock}
		}
		return v, nil
	}
	concrete := named
	if src.has {
		if _, ok := src.val.(map[string]any); !ok {
			return nil, &fieldError{msg: "Expected an object for " + label + " of type " + strconv.Quote(named.Name) + ", but the mock value is " + inspect(src.val) + ".", code: ErrCodeMock}
		}
	}
	if named.IsAbstract() {
		var err error
		if concrete, err = e.resolveType(named, src, label); err != nil {
			return nil, err
		}
	}
	sets := make([]*ast.SelectionSet, 0, len(fields))
	for _, f := range fields {
		sets = append(sets, f.SelectionSet)
	}
	obj, err := e.selectionSet(concrete, sets, src, path)
	if err != nil {
		return nil, err
	}
	return obj, nil
}

func fieldName(label string) string {
	_, f, _ := strings.Cut(label, ".")
	return f
}

func (e *executor) completeList(t *schema.TypeRef, fields []*ast.Field, label string, path []any, src source) (any, error) {
	var items []source
	if src.has {
		list, ok := src.val.([]any)
		if !ok {
			return nil, &fieldError{msg: "Expected a list for " + label + " of type " + strconv.Quote(t.String()) + ", but the mock value is " + inspect(src.val) + ".", code: ErrCodeMock}
		}
		items = make([]source, len(list))
		for i, v := range list {
			items[i] = source{has: true, val: v, seed: mix(src.seed, "#"+strconv.Itoa(i))}
		}
	} else {
		n := e.st.cfg.ListLength
		if src.paged {
			n = src.length
		}
		items = make([]source, n)
		for i := range items {
			items[i] = source{seed: mix(src.seed, "#"+strconv.Itoa(i))}
		}
	}
	out := make([]any, len(items))
	for i, item := range items {
		ipath := appendPath(path, i)
		v, err := e.complete(t.OfType, fields, label, ipath, item)
		if err != nil {
			e.report(err, fields, ipath)
			if t.OfType.IsNonNull() {
				return nil, errPropagated
			}
			v = nil
		}
		out[i] = v
	}
	return out, nil
}

// resolveType picks the object type of an abstract value: the given
// __typename, else one of the possible types by the seed.
func (e *executor) resolveType(abstract *schema.Type, src source, label string) (*schema.Type, error) {
	if m, ok := src.val.(map[string]any); ok && src.has {
		if tn, ok := m["__typename"].(string); ok {
			t := e.sch.Type(tn)
			if t == nil || t.Kind != schema.KindObject || !e.sch.IsPossibleType(abstract.Name, tn) {
				return nil, &fieldError{msg: fmt.Sprintf("The mock value of %s has __typename %q, which is not a possible type of %q.", label, tn, abstract.Name), code: ErrCodeMock}
			}
			return t, nil
		}
	}
	possible := e.sch.PossibleTypes(abstract.Name)
	objects := possible[:0:0]
	for _, t := range possible {
		if t.Kind == schema.KindObject {
			objects = append(objects, t)
		}
	}
	if len(objects) == 0 {
		return nil, &fieldError{msg: fmt.Sprintf("Abstract type %q has no object types, so %s cannot be answered.", abstract.Name, label), code: ErrCodeMock}
	}
	return pick(objects, mix(src.seed, "type")), nil
}

// serialize checks and converts a leaf value given as JSON, as the
// spec's result coercion of the built-in scalars does.
func serialize(t *schema.Type, v any) (any, error) {
	if t.Kind == schema.KindEnum {
		s, ok := v.(string)
		if ok && (t.EnumValue(s) != nil || strings.HasPrefix(t.Name, "__")) {
			return s, nil
		}
		return nil, fmt.Errorf("Enum %q cannot represent value: %s", t.Name, inspect(v))
	}
	switch t.Name {
	case "Int":
		n, ok := number(v)
		if !ok {
			return nil, fmt.Errorf("Int cannot represent non-integer value: %s", inspect(v))
		}
		if n != math.Trunc(n) {
			return nil, fmt.Errorf("Int cannot represent non-integer value: %s", inspect(v))
		}
		if n > math.MaxInt32 || n < math.MinInt32 {
			return nil, fmt.Errorf("Int cannot represent non 32-bit signed integer value: %s", inspect(v))
		}
		return json.Number(strconv.FormatInt(int64(n), 10)), nil
	case "Float":
		n, ok := number(v)
		if !ok || math.IsInf(n, 0) || math.IsNaN(n) {
			return nil, fmt.Errorf("Float cannot represent non numeric value: %s", inspect(v))
		}
		if jn, ok := v.(json.Number); ok {
			return jn, nil
		}
		return json.Number(strconv.FormatFloat(n, 'g', -1, 64)), nil
	case "String":
		switch v := v.(type) {
		case string:
			return v, nil
		case json.Number:
			return v.String(), nil
		case bool:
			return strconv.FormatBool(v), nil
		}
		return nil, fmt.Errorf("String cannot represent value: %s", inspect(v))
	case "Boolean":
		switch v := v.(type) {
		case bool:
			return v, nil
		case json.Number:
			f, err := v.Float64()
			if err == nil {
				return f != 0, nil
			}
		}
		return nil, fmt.Errorf("Boolean cannot represent a non boolean value: %s", inspect(v))
	case "ID":
		switch v := v.(type) {
		case string:
			return v, nil
		case json.Number:
			if _, err := strconv.ParseInt(v.String(), 10, 64); err == nil {
				return v.String(), nil
			}
		}
		return nil, fmt.Errorf("ID cannot represent value: %s", inspect(v))
	}
	// A custom scalar takes any JSON value.
	return v, nil
}

// pagingArgs are the arguments that set the length of a generated list.
var pagingArgs = []string{"first", "last", "limit", "take", "count", "size", "pageSize", "perPage", "top"}

// pageSize is the length a paging argument given in the document asks
// for (a default does not count), at most MaxListLength.
func pageSize(given *ast.ArgumentList, args map[string]any) (int, bool) {
	if given == nil {
		return 0, false
	}
	for _, name := range pagingArgs {
		if !slices.ContainsFunc(given.Arguments, func(a *ast.Argument) bool { return a.Name != nil && a.Name.Value == name }) {
			continue
		}
		n, ok := args[name].(json.Number)
		if !ok {
			continue
		}
		if v, err := strconv.Atoi(n.String()); err == nil && v >= 0 {
			return min(v, MaxListLength), true
		}
	}
	return 0, false
}

// number reads a JSON number, or a string that holds one.
func number(v any) (float64, bool) {
	switch v := v.(type) {
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return f, err == nil && strings.TrimSpace(v) != ""
	case bool:
		if v {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}
