package schema

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// The introspection JSON shape. Pointers mark values that are null or
// absent in some results (older servers do not send the newer fields).
// Schema.Introspection fills every field, with null where it does not
// apply, like graphql-js does.

// IntrospectionSchema is the value of __schema.
type IntrospectionSchema struct {
	Description      *string                  `json:"description"`
	QueryType        *IntrospectionRootType   `json:"queryType"`
	MutationType     *IntrospectionRootType   `json:"mutationType"`
	SubscriptionType *IntrospectionRootType   `json:"subscriptionType"`
	Types            []IntrospectionType      `json:"types"`
	Directives       []IntrospectionDirective `json:"directives"`
}

// IntrospectionRootType names a root operation type.
type IntrospectionRootType struct {
	Name string `json:"name"`
	Kind string `json:"kind,omitempty"`
}

// IntrospectionType is a named type (__Type).
type IntrospectionType struct {
	Kind        string  `json:"kind"`
	Name        *string `json:"name"`
	Description *string `json:"description"`
	// SpecifiedByURL also decodes the graphql-js 15 spelling specifiedByUrl.
	SpecifiedByURL *string                   `json:"specifiedByURL"`
	IsOneOf        *bool                     `json:"isOneOf"`
	Fields         []IntrospectionField      `json:"fields"`
	InputFields    []IntrospectionInputValue `json:"inputFields"`
	Interfaces     []*IntrospectionTypeRef   `json:"interfaces"`
	EnumValues     []IntrospectionEnumValue  `json:"enumValues"`
	PossibleTypes  []*IntrospectionTypeRef   `json:"possibleTypes"`
}

// IntrospectionField is a __Field.
type IntrospectionField struct {
	Name              string                    `json:"name"`
	Description       *string                   `json:"description"`
	Args              []IntrospectionInputValue `json:"args"`
	Type              *IntrospectionTypeRef     `json:"type"`
	IsDeprecated      bool                      `json:"isDeprecated"`
	DeprecationReason *string                   `json:"deprecationReason"`
}

// IntrospectionInputValue is an __InputValue.
type IntrospectionInputValue struct {
	Name              string                `json:"name"`
	Description       *string               `json:"description"`
	Type              *IntrospectionTypeRef `json:"type"`
	DefaultValue      *string               `json:"defaultValue"`
	IsDeprecated      *bool                 `json:"isDeprecated"`
	DeprecationReason *string               `json:"deprecationReason"`
}

// IntrospectionEnumValue is an __EnumValue.
type IntrospectionEnumValue struct {
	Name              string  `json:"name"`
	Description       *string `json:"description"`
	IsDeprecated      bool    `json:"isDeprecated"`
	DeprecationReason *string `json:"deprecationReason"`
}

// IntrospectionDirective is a __Directive.
type IntrospectionDirective struct {
	Name         string                    `json:"name"`
	Description  *string                   `json:"description"`
	IsRepeatable *bool                     `json:"isRepeatable"`
	Locations    []string                  `json:"locations"`
	Args         []IntrospectionInputValue `json:"args"`
}

// IntrospectionTypeRef is a type reference (a __Type with kind, name and
// ofType only).
type IntrospectionTypeRef struct {
	Kind   string                `json:"kind"`
	Name   *string               `json:"name"`
	OfType *IntrospectionTypeRef `json:"ofType"`
}

// ParseIntrospection converts an introspection result to a Schema. data may
// be a GraphQL response ({"data":{"__schema":…},"errors":…}), a bare
// {"__schema":…} object or the __schema value itself.
//
// Errors are *errs.Error values with a Code* code. With CodeInvalidSchema
// the returned Schema is non-nil and usable; with any other error it is nil.
func ParseIntrospection(data []byte) (*Schema, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, errs.Wrap(err, errs.Parse, "The introspection result is not a JSON object.").
			WithCode(CodeMalformedJSON)
	}

	raw, isResponse := top["__schema"], false
	var serverErrors []string
	if _, ok := top["data"]; ok || top["errors"] != nil {
		isResponse = true
		var resp struct {
			Data *struct {
				Schema json.RawMessage `json:"__schema"`
			} `json:"data"`
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		if err := json.Unmarshal(data, &resp); err != nil {
			return nil, errs.Wrap(err, errs.Parse, "The GraphQL response does not have the expected shape.").
				WithCode(CodeMalformedIntrospection)
		}
		if resp.Data != nil {
			raw = resp.Data.Schema
		}
		for _, e := range resp.Errors {
			serverErrors = append(serverErrors, e.Message)
		}
	} else if raw == nil {
		if _, ok := top["types"]; ok {
			raw = data
		}
	}

	if len(raw) == 0 || string(raw) == "null" {
		if len(serverErrors) > 0 {
			return nil, errs.New(errs.Protocol, "The server returned errors instead of the schema: "+serverErrors[0]).
				WithCode(CodeServerErrors).
				WithHint(HintServerErrors).
				WithDetail("errors", strings.Join(serverErrors, "\n"))
		}
		msg := "The JSON holds no __schema."
		if isResponse {
			msg = "The GraphQL response holds no data.__schema."
		}
		return nil, errs.New(errs.Parse, msg).WithCode(CodeNoSchema)
	}

	var is IntrospectionSchema
	if err := json.Unmarshal(raw, &is); err != nil {
		return nil, errs.Wrap(err, errs.Parse, "The __schema value does not have the shape of an introspection result.").
			WithCode(CodeMalformedIntrospection)
	}
	return FromIntrospection(&is)
}

// FromIntrospection converts a decoded introspection result to a Schema.
// Errors are as for ParseIntrospection.
func FromIntrospection(is *IntrospectionSchema) (*Schema, error) {
	if is == nil {
		return nil, errs.New(errs.Parse, "The JSON holds no __schema.").WithCode(CodeNoSchema)
	}
	c := &converter{declared: make(map[string]Kind, len(is.Types))}
	for _, t := range is.Types {
		if t.Name != nil && Kind(t.Kind).IsNamed() {
			if _, dup := c.declared[*t.Name]; !dup {
				c.declared[*t.Name] = Kind(t.Kind)
			}
		}
	}

	s := &Schema{Description: str(is.Description)}
	s.QueryType = c.root("schema.queryType", is.QueryType, true)
	s.MutationType = c.root("schema.mutationType", is.MutationType, false)
	s.SubscriptionType = c.root("schema.subscriptionType", is.SubscriptionType, false)

	seen := make(map[string]bool, len(is.Types))
	s.Types = make([]*Type, 0, len(is.Types))
	for i := range is.Types {
		t := c.typ(&is.Types[i], i)
		if seen[t.Name] {
			c.add(t.Name, "the type is declared more than once")
		}
		seen[t.Name] = true
		s.Types = append(s.Types, t)
	}

	seenDir := make(map[string]bool, len(is.Directives))
	s.Directives = make([]*Directive, 0, len(is.Directives))
	for i := range is.Directives {
		d := c.directive(&is.Directives[i], i)
		if seenDir[d.Name] {
			c.add("@"+d.Name, "the directive is declared more than once")
		}
		seenDir[d.Name] = true
		s.Directives = append(s.Directives, d)
	}

	s.index()
	if len(c.problems) > 0 {
		return s, errs.Wrap(c.problems, errs.Parse,
			fmt.Sprintf("The schema has %d broken reference(s) or definition(s).", len(c.problems))).
			WithCode(CodeInvalidSchema).
			WithHint(HintInvalidSchema).
			WithDetail("problems", strconv.Itoa(len(c.problems)))
	}
	return s, nil
}

type converter struct {
	declared map[string]Kind
	problems Problems
}

func (c *converter) add(path, format string, args ...any) {
	c.problems = append(c.problems, Problem{Path: path, Message: fmt.Sprintf(format, args...)})
}

// kindOf returns the kind of a declared or built-in type.
func (c *converter) kindOf(name string) (Kind, bool) {
	if k, ok := c.declared[name]; ok {
		return k, true
	}
	if t := builtins.types[name]; t != nil {
		return t.Kind, true
	}
	return "", false
}

func (c *converter) root(path string, r *IntrospectionRootType, required bool) string {
	if r == nil {
		if required {
			c.add(path, "the schema has no query type")
		}
		return ""
	}
	k, ok := c.declared[r.Name]
	switch {
	case r.Name == "":
		c.add(path, "the root type has no name")
	case !ok:
		c.add(path, "references undeclared type %q", r.Name)
	case k != KindObject:
		c.add(path, "root type %q is %s, not OBJECT", r.Name, k)
	}
	return r.Name
}

func (c *converter) typ(it *IntrospectionType, i int) *Type {
	t := &Type{
		Kind:           Kind(it.Kind),
		Name:           str(it.Name),
		Description:    str(it.Description),
		SpecifiedByURL: str(it.SpecifiedByURL),
		IsOneOf:        it.IsOneOf != nil && *it.IsOneOf,
	}
	path := t.Name
	if path == "" {
		path = fmt.Sprintf("types[%d]", i)
		c.add(path, "the type has no name")
	}
	if !t.Kind.IsNamed() {
		c.add(path, "unknown type kind %q", it.Kind)
	}

	applies := func(field string, n int, kinds ...Kind) bool {
		for _, k := range kinds {
			if t.Kind == k {
				return true
			}
		}
		if n > 0 {
			c.add(path, "%s types cannot have %s", t.Kind, field)
		}
		return false
	}

	if applies("fields", len(it.Fields), KindObject, KindInterface) {
		t.Fields = make([]*Field, 0, len(it.Fields))
		for j := range it.Fields {
			t.Fields = append(t.Fields, c.field(path, &it.Fields[j]))
		}
		c.checkUnique(path, "field", len(t.Fields), func(j int) string { return t.Fields[j].Name })
	}
	if applies("interfaces", len(it.Interfaces), KindObject, KindInterface) {
		t.Interfaces = c.namedRefs(path+" implements", it.Interfaces, KindInterface)
	}
	if applies("possibleTypes", len(it.PossibleTypes), KindUnion, KindInterface) {
		t.PossibleTypes = c.namedRefs(path+" possibleTypes", it.PossibleTypes, KindObject)
	}
	if applies("enumValues", len(it.EnumValues), KindEnum) {
		t.EnumValues = make([]*EnumValue, 0, len(it.EnumValues))
		for _, ev := range it.EnumValues {
			if ev.Name == "" {
				c.add(path, "an enum value has no name")
			}
			t.EnumValues = append(t.EnumValues, &EnumValue{
				Name:        ev.Name,
				Description: str(ev.Description),
				Deprecation: Deprecation{IsDeprecated: ev.IsDeprecated, DeprecationReason: str(ev.DeprecationReason)},
			})
		}
		c.checkUnique(path, "enum value", len(t.EnumValues), func(j int) string { return t.EnumValues[j].Name })
	}
	if applies("inputFields", len(it.InputFields), KindInputObject) {
		t.InputFields = c.inputValues(path, ".", "", it.InputFields)
	}
	if t.SpecifiedByURL != "" && t.Kind != KindScalar {
		c.add(path, "%s types cannot have specifiedByURL", t.Kind)
	}
	if t.IsOneOf && t.Kind != KindInputObject {
		c.add(path, "%s types cannot be oneOf", t.Kind)
	}
	return t
}

func (c *converter) checkUnique(path, what string, n int, name func(int) string) {
	seen := make(map[string]bool, n)
	for j := range n {
		nm := name(j)
		if seen[nm] {
			c.add(path, "%s %q is declared more than once", what, nm)
		}
		seen[nm] = true
	}
}

func (c *converter) field(owner string, f *IntrospectionField) *Field {
	path := owner + "." + f.Name
	if f.Name == "" {
		c.add(owner, "a field has no name")
	}
	return &Field{
		Name:        f.Name,
		Description: str(f.Description),
		Args:        c.inputValues(owner+"."+f.Name, "(", ":)", f.Args),
		Type:        c.ref(path, f.Type, false),
		Deprecation: Deprecation{IsDeprecated: f.IsDeprecated, DeprecationReason: str(f.DeprecationReason)},
	}
}

// inputValues converts arguments (sep "(", suffix ":)") or input fields
// (sep ".", no suffix).
func (c *converter) inputValues(owner, sep, suffix string, ivs []IntrospectionInputValue) []*InputValue {
	out := make([]*InputValue, 0, len(ivs))
	for _, iv := range ivs {
		path := owner + sep + iv.Name + suffix
		if iv.Name == "" {
			c.add(owner, "an argument or input field has no name")
		}
		out = append(out, &InputValue{
			Name:         iv.Name,
			Description:  str(iv.Description),
			Type:         c.ref(path, iv.Type, true),
			DefaultValue: iv.DefaultValue,
			Deprecation: Deprecation{
				IsDeprecated:      iv.IsDeprecated != nil && *iv.IsDeprecated,
				DeprecationReason: str(iv.DeprecationReason),
			},
		})
	}
	c.checkUnique(owner, "argument or input field", len(out), func(j int) string { return out[j].Name })
	return out
}

func (c *converter) directive(id *IntrospectionDirective, i int) *Directive {
	d := &Directive{
		Name:         id.Name,
		Description:  str(id.Description),
		IsRepeatable: id.IsRepeatable != nil && *id.IsRepeatable,
		Locations:    make([]DirectiveLocation, 0, len(id.Locations)),
	}
	path := "@" + id.Name
	if id.Name == "" {
		path = fmt.Sprintf("directives[%d]", i)
		c.add(path, "the directive has no name")
	}
	for _, l := range id.Locations {
		d.Locations = append(d.Locations, DirectiveLocation(l))
	}
	d.Args = c.inputValues(path, "(", ":)", id.Args)
	return d
}

// namedRefs converts interfaces or possibleTypes: named references to types
// of kind want. Names are kept even when they do not resolve.
func (c *converter) namedRefs(path string, refs []*IntrospectionTypeRef, want Kind) []string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		if r == nil || r.Name == nil || *r.Name == "" {
			c.add(path, "a reference has no type name")
			continue
		}
		name := *r.Name
		out = append(out, name)
		k, ok := c.kindOf(name)
		switch {
		case !ok:
			c.add(path, "references undeclared type %q", name)
		case k != want:
			c.add(path, "%q is %s, not %s", name, k, want)
		case r.Kind != "" && Kind(r.Kind) != k:
			c.add(path, "reference to %q says %s, but the type is %s", name, r.Kind, k)
		}
	}
	return out
}

// ref converts a type reference of a field (input false) or of an argument
// or input field (input true). It returns nil only when the reference is
// missing.
func (c *converter) ref(path string, r *IntrospectionTypeRef, input bool) *TypeRef {
	if r == nil {
		c.add(path, "the type is missing")
		return nil
	}
	top := c.convertRef(path, r)
	named := top.NamedType()
	if named == nil || named.Name == "" {
		return top
	}
	k, ok := c.kindOf(named.Name)
	switch {
	case !ok:
		c.add(path, "references undeclared type %q", named.Name)
	case k != named.Kind:
		c.add(path, "reference to %q says %s, but the type is %s", named.Name, named.Kind, k)
	case input && !k.IsInput():
		c.add(path, "%q is %s, which is not an input type", named.Name, k)
	case !input && !k.IsOutput():
		c.add(path, "%q is %s, which is not an output type", named.Name, k)
	}
	return top
}

func (c *converter) convertRef(path string, r *IntrospectionTypeRef) *TypeRef {
	t := &TypeRef{Kind: Kind(r.Kind), Name: str(r.Name)}
	switch {
	case t.Kind.IsWrapper():
		if r.OfType == nil {
			c.add(path, "the type reference is truncated at a %s wrapper; raise the introspection query's type depth", t.Kind)
			return t
		}
		if t.Kind == KindNonNull && r.OfType.Kind == string(KindNonNull) {
			c.add(path, "NON_NULL wraps NON_NULL")
		}
		t.Name = ""
		t.OfType = c.convertRef(path, r.OfType)
	case t.Kind.IsNamed():
		if t.Name == "" {
			c.add(path, "a %s reference has no name", t.Kind)
		}
		if r.OfType != nil {
			c.add(path, "a %s reference cannot have ofType", t.Kind)
		}
	default:
		c.add(path, "unknown type kind %q in a type reference", r.Kind)
	}
	return t
}

// Introspection converts s back to the introspection shape, as returned by
// a current graphql-js server for the full query: null where a field does
// not apply to a kind, empty descriptions and reasons as null.
func (s *Schema) Introspection() *IntrospectionSchema {
	is := &IntrospectionSchema{
		Description:      ptr(s.Description),
		QueryType:        s.rootOut(s.QueryType),
		MutationType:     s.rootOut(s.MutationType),
		SubscriptionType: s.rootOut(s.SubscriptionType),
		Types:            make([]IntrospectionType, 0, len(s.Types)),
		Directives:       make([]IntrospectionDirective, 0, len(s.Directives)),
	}
	for _, t := range s.Types {
		is.Types = append(is.Types, s.typeOut(t))
	}
	for _, d := range s.Directives {
		locs := make([]string, len(d.Locations))
		for i, l := range d.Locations {
			locs[i] = string(l)
		}
		rep := d.IsRepeatable
		is.Directives = append(is.Directives, IntrospectionDirective{
			Name:         d.Name,
			Description:  ptr(d.Description),
			IsRepeatable: &rep,
			Locations:    locs,
			Args:         inputValuesOut(d.Args),
		})
	}
	return is
}

// MarshalIntrospection encodes s as {"__schema": …}, the form the schema
// cache stores. ParseIntrospection reads it back.
func (s *Schema) MarshalIntrospection() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(struct {
		Schema *IntrospectionSchema `json:"__schema"`
	}{s.Introspection()}); err != nil {
		return nil, errs.Wrap(err, errs.Internal, "Could not encode the schema.")
	}
	return buf.Bytes(), nil
}

func (s *Schema) rootOut(name string) *IntrospectionRootType {
	if name == "" {
		return nil
	}
	return &IntrospectionRootType{Name: name, Kind: string(KindObject)}
}

func (s *Schema) typeOut(t *Type) IntrospectionType {
	name := t.Name
	it := IntrospectionType{
		Kind:        string(t.Kind),
		Name:        &name,
		Description: ptr(t.Description),
	}
	switch t.Kind {
	case KindScalar:
		it.SpecifiedByURL = ptr(t.SpecifiedByURL)
	case KindObject, KindInterface:
		it.Fields = make([]IntrospectionField, 0, len(t.Fields))
		for _, f := range t.Fields {
			it.Fields = append(it.Fields, IntrospectionField{
				Name:              f.Name,
				Description:       ptr(f.Description),
				Args:              inputValuesOut(f.Args),
				Type:              refOut(f.Type),
				IsDeprecated:      f.IsDeprecated,
				DeprecationReason: ptr(f.DeprecationReason),
			})
		}
		it.Interfaces = s.namedRefsOut(t.Interfaces, KindInterface)
		if t.Kind == KindInterface {
			it.PossibleTypes = s.namedRefsOut(t.PossibleTypes, KindObject)
		}
	case KindUnion:
		it.PossibleTypes = s.namedRefsOut(t.PossibleTypes, KindObject)
	case KindEnum:
		it.EnumValues = make([]IntrospectionEnumValue, 0, len(t.EnumValues))
		for _, v := range t.EnumValues {
			it.EnumValues = append(it.EnumValues, IntrospectionEnumValue{
				Name:              v.Name,
				Description:       ptr(v.Description),
				IsDeprecated:      v.IsDeprecated,
				DeprecationReason: ptr(v.DeprecationReason),
			})
		}
	case KindInputObject:
		oneOf := t.IsOneOf
		it.IsOneOf = &oneOf
		it.InputFields = inputValuesOut(t.InputFields)
	}
	return it
}

// namedRefsOut writes interface or possible type names as references,
// with the kind of the type when it resolves and want otherwise.
func (s *Schema) namedRefsOut(names []string, want Kind) []*IntrospectionTypeRef {
	out := make([]*IntrospectionTypeRef, 0, len(names))
	for _, n := range names {
		k := want
		if t := s.Type(n); t != nil {
			k = t.Kind
		}
		out = append(out, &IntrospectionTypeRef{Kind: string(k), Name: &n})
	}
	return out
}

func inputValuesOut(vs []*InputValue) []IntrospectionInputValue {
	out := make([]IntrospectionInputValue, 0, len(vs))
	for _, v := range vs {
		dep := v.IsDeprecated
		out = append(out, IntrospectionInputValue{
			Name:              v.Name,
			Description:       ptr(v.Description),
			Type:              refOut(v.Type),
			DefaultValue:      v.DefaultValue,
			IsDeprecated:      &dep,
			DeprecationReason: ptr(v.DeprecationReason),
		})
	}
	return out
}

func refOut(t *TypeRef) *IntrospectionTypeRef {
	if t == nil {
		return nil
	}
	r := &IntrospectionTypeRef{Kind: string(t.Kind), OfType: refOut(t.OfType)}
	if !t.Kind.IsWrapper() {
		name := t.Name
		r.Name = &name
	}
	return r
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func ptr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
