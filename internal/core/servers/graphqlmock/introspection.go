package graphqlmock

import (
	"github.com/sorrelfox/postsonde/internal/core/graphql/schema"
)

// The introspection meta fields (__schema, __type) answer from the schema
// model: their values are objects of the introspection types whose list
// and type members are lazyFields, so a client's query (the app's
// FullIntrospectionQuery, graphql-js's getIntrospectionQuery, …) walks as
// deep as it asks and no deeper. The types list the schema's own types and
// then the built-in scalars and introspection types it does not declare,
// as a graphql-js server does; the directives likewise.

type introspector struct {
	sch *schema.Schema
}

// schemaObject is the value of __schema.
func (in introspector) schemaObject() map[string]any {
	s := in.sch
	root := func(name string) any {
		if name == "" {
			return nil
		}
		if t := s.Type(name); t != nil {
			return in.typeObject(t)
		}
		return nil
	}
	return map[string]any{
		"description": nullable(s.Description),
		"types": lazyField(func(map[string]any) any {
			out := make([]any, 0, len(s.Types)+16)
			for _, t := range s.Types {
				out = append(out, in.typeObject(t))
			}
			for _, t := range schema.Builtins().Types {
				if !s.HasType(t.Name) {
					out = append(out, in.typeObject(t))
				}
			}
			return out
		}),
		"queryType":        lazyField(func(map[string]any) any { return root(s.QueryType) }),
		"mutationType":     lazyField(func(map[string]any) any { return root(s.MutationType) }),
		"subscriptionType": lazyField(func(map[string]any) any { return root(s.SubscriptionType) }),
		"directives": lazyField(func(map[string]any) any {
			out := make([]any, 0, len(s.Directives)+5)
			seen := map[string]bool{}
			for _, d := range s.Directives {
				seen[d.Name] = true
				out = append(out, in.directiveObject(d))
			}
			for _, d := range schema.Builtins().Directives {
				if !seen[d.Name] {
					out = append(out, in.directiveObject(d))
				}
			}
			return out
		}),
	}
}

// typeByName is the value of __type(name:): nil for an unknown type.
func (in introspector) typeByName(name string) any {
	t := in.sch.Type(name)
	if t == nil {
		return nil
	}
	return in.typeObject(t)
}

func includeDeprecated(args map[string]any) bool {
	b, _ := args["includeDeprecated"].(bool)
	return b
}

// typeObject is the __Type of a named type.
func (in introspector) typeObject(t *schema.Type) map[string]any {
	obj := map[string]any{
		"kind":           string(t.Kind),
		"name":           t.Name,
		"description":    nullable(t.Description),
		"specifiedByURL": nil,
		"isOneOf":        nil,
		"ofType":         nil,
		"fields":         nil,
		"interfaces":     nil,
		"possibleTypes":  nil,
		"enumValues":     nil,
		"inputFields":    nil,
	}
	switch t.Kind {
	case schema.KindScalar:
		obj["specifiedByURL"] = nullable(t.SpecifiedByURL)
	case schema.KindObject, schema.KindInterface:
		obj["fields"] = lazyField(func(args map[string]any) any {
			all := includeDeprecated(args)
			out := make([]any, 0, len(t.Fields))
			for _, f := range t.Fields {
				if all || !f.IsDeprecated {
					out = append(out, in.fieldObject(f))
				}
			}
			return out
		})
		obj["interfaces"] = lazyField(func(map[string]any) any { return in.named(t.Interfaces) })
		if t.Kind == schema.KindInterface {
			obj["possibleTypes"] = lazyField(func(map[string]any) any { return in.possible(t.Name) })
		}
	case schema.KindUnion:
		obj["possibleTypes"] = lazyField(func(map[string]any) any { return in.possible(t.Name) })
	case schema.KindEnum:
		obj["enumValues"] = lazyField(func(args map[string]any) any {
			all := includeDeprecated(args)
			out := make([]any, 0, len(t.EnumValues))
			for _, v := range t.EnumValues {
				if all || !v.IsDeprecated {
					out = append(out, map[string]any{
						"name":              v.Name,
						"description":       nullable(v.Description),
						"isDeprecated":      v.IsDeprecated,
						"deprecationReason": deprecationReason(v.Deprecation),
					})
				}
			}
			return out
		})
	case schema.KindInputObject:
		obj["isOneOf"] = t.IsOneOf
		obj["inputFields"] = lazyField(func(args map[string]any) any { return in.inputValues(t.InputFields, includeDeprecated(args)) })
	}
	return obj
}

func (in introspector) named(names []string) []any {
	out := make([]any, 0, len(names))
	for _, n := range names {
		if t := in.sch.Type(n); t != nil {
			out = append(out, in.typeObject(t))
		}
	}
	return out
}

func (in introspector) possible(name string) []any {
	types := in.sch.PossibleTypes(name)
	out := make([]any, 0, len(types))
	for _, t := range types {
		out = append(out, in.typeObject(t))
	}
	return out
}

// refObject is the __Type of a type reference: a LIST or NON_NULL
// wrapper, or the named type.
func (in introspector) refObject(r *schema.TypeRef) any {
	if r == nil {
		return nil
	}
	if !r.Kind.IsWrapper() {
		if t := in.sch.Type(r.Name); t != nil {
			return in.typeObject(t)
		}
		// A broken reference of an introspection result: named, kind as
		// given, nothing else.
		return map[string]any{"kind": string(r.Kind), "name": r.Name, "description": nil, "specifiedByURL": nil, "isOneOf": nil,
			"ofType": nil, "fields": nil, "interfaces": nil, "possibleTypes": nil, "enumValues": nil, "inputFields": nil}
	}
	of := r.OfType
	return map[string]any{
		"kind": string(r.Kind), "name": nil, "description": nil, "specifiedByURL": nil, "isOneOf": nil,
		"fields": nil, "interfaces": nil, "possibleTypes": nil, "enumValues": nil, "inputFields": nil,
		"ofType": lazyField(func(map[string]any) any { return in.refObject(of) }),
	}
}

func (in introspector) fieldObject(f *schema.Field) map[string]any {
	return map[string]any{
		"name":              f.Name,
		"description":       nullable(f.Description),
		"args":              lazyField(func(args map[string]any) any { return in.inputValues(f.Args, includeDeprecated(args)) }),
		"type":              lazyField(func(map[string]any) any { return in.refObject(f.Type) }),
		"isDeprecated":      f.IsDeprecated,
		"deprecationReason": deprecationReason(f.Deprecation),
	}
}

func (in introspector) inputValues(vs []*schema.InputValue, all bool) []any {
	out := make([]any, 0, len(vs))
	for _, v := range vs {
		if !all && v.IsDeprecated {
			continue
		}
		var def any
		if v.DefaultValue != nil {
			def = *v.DefaultValue
		}
		out = append(out, map[string]any{
			"name":              v.Name,
			"description":       nullable(v.Description),
			"type":              lazyField(func(map[string]any) any { return in.refObject(v.Type) }),
			"defaultValue":      def,
			"isDeprecated":      v.IsDeprecated,
			"deprecationReason": deprecationReason(v.Deprecation),
		})
	}
	return out
}

func (in introspector) directiveObject(d *schema.Directive) map[string]any {
	locs := make([]any, len(d.Locations))
	for i, l := range d.Locations {
		locs[i] = string(l)
	}
	return map[string]any{
		"name":         d.Name,
		"description":  nullable(d.Description),
		"isRepeatable": d.IsRepeatable,
		"locations":    locs,
		"args":         lazyField(func(args map[string]any) any { return in.inputValues(d.Args, includeDeprecated(args)) }),
	}
}

// deprecationReason is null for a member that is not deprecated, as
// graphql-js answers.
func deprecationReason(d schema.Deprecation) any {
	if !d.IsDeprecated {
		return nil
	}
	return nullable(d.DeprecationReason)
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
