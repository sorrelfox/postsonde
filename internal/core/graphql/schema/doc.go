// Package schema is PostSonde's model of a GraphQL schema: object,
// interface, union, enum, input object and scalar types, directives,
// descriptions and deprecations, built from an introspection result or
// from SDL (ParseSDL, FromSDL: sdl.go, which also validates the type
// system).
//
// The model feeds the docs explorer, autocomplete, validation and, later,
// schema diffs and SDL export. It keeps everything the introspection result
// says, in declaration order, and cross-references types by name so that it
// has no pointer cycles and encodes to JSON as is.
//
//	// body: the server's response to FullIntrospectionQuery
//	s, err := schema.ParseIntrospection(body)
//	if s == nil {
//		return err // not JSON, no __schema, or the server's errors
//	}
//	// err may still report broken references; s is usable (see below).
//	f := s.Field("Query", "__typename") // meta fields included
//	fmt.Println(f.Type)                 // String!
//	for _, t := range s.PossibleTypes("SearchResult") { … }
//
// Two introspection queries are provided: FullIntrospectionQuery asks for
// everything the current spec defines (schema description, specifiedByURL,
// isRepeatable, isOneOf, deprecated arguments and input fields), and
// ConservativeIntrospectionQuery asks only what servers built on older
// GraphQL libraries accept. Servers reject unknown introspection fields, so
// a client tries the full query and falls back to the conservative one.
// IntrospectionQuery builds other combinations.
//
// ParseIntrospection accepts a GraphQL response ({"data":{"__schema":…}}),
// a bare {"__schema":…} object or the __schema value itself. Every type
// reference is checked: a reference to an undeclared type, a kind that does
// not match the declared type, an output type used as an input (or the
// reverse), a truncated wrapper, a duplicate name. These problems are
// returned as an *errs.Error with code CodeInvalidSchema wrapping Problems,
// together with a non-nil Schema in which the broken references resolve to
// nil, so the editor keeps working against a slightly broken server.
//
// Lookups fall back to the built-in scalars (Int, Float, String, Boolean,
// ID), the introspection types (__Schema, __Type, …) and the spec
// directives (@skip, @include, @deprecated, @specifiedBy, @oneOf) when the
// result does not declare them, without adding them to Types.
//
// Schema.Introspection converts the model back to the introspection shape
// (the full query's shape, nulls where a field does not apply), which is
// what the schema cache stores and what the round-trip tests compare
// against the fixtures in testdata.
//
// A Schema is read-only once built and safe for concurrent reads. Package
// schema imports only the standard library, internal/core/errs and, for
// SDL, the GraphQL ast and parser packages.
package schema
