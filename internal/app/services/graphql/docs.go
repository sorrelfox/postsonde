package graphql

import (
	"cmp"
	"context"
	"slices"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/graphql/docs"
	"github.com/sorrelfox/postsonde/internal/core/graphql/schema"
)

// The docs explorer (GQL-14) reads the cached schema piece by piece: the
// overview (roots, the names of all types, directives), one type at a time,
// and a search over every type and member. So a schema of thousands of
// types is never shipped to the UI whole, and everything works offline.

// MaxDocsHits bounds the hits of DocsSearch.
const MaxDocsHits = 200

// schemaFor returns the cached schema for key, or nil.
func (s *GraphQLService) schemaFor(key string) *schema.Schema {
	if key == "" {
		return nil
	}
	if e := s.cached(key); e != nil {
		return e.Schema
	}
	return nil
}

// index returns the search index of sch, built on first use and kept in
// memory like the schema.
func (s *GraphQLService) index(sch *schema.Schema) *docs.Index {
	s.mu.Lock()
	idx, ok := s.indexes[sch]
	s.mu.Unlock()
	if ok {
		return idx
	}
	idx = docs.NewIndex(sch)
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.indexes) >= maxMemory {
		clear(s.indexes)
	}
	s.indexes[sch] = idx
	return idx
}

// DocsOverview returns the docs explorer's start page for the cached
// schema in.Key: its description, root types, every type's name and kind
// (introspection types left out, sorted by name) and its directives. It
// never sends anything; Schema is false when nothing is cached.
func (s *GraphQLService) DocsOverview(ctx context.Context, in DocsInputDTO) (DocsOverviewDTO, error) {
	out := DocsOverviewDTO{Roots: []DocsRootDTO{}, Types: []DocsTypeNameDTO{}, Directives: []DocsDirectiveDTO{}}
	if err := interrupted(ctx, "Reading the schema docs"); err != nil {
		return out, err
	}
	sch := s.schemaFor(in.Key)
	if sch == nil {
		return out, nil
	}
	out.Schema = true
	out.Description = sch.Description
	for _, op := range []schema.Operation{schema.OpQuery, schema.OpMutation, schema.OpSubscription} {
		if t := sch.RootType(op); t != nil {
			out.Roots = append(out.Roots, DocsRootDTO{Operation: string(op), Type: t.Name})
		}
	}
	seen := make(map[string]bool, len(sch.Types))
	for _, t := range sch.Types {
		if seen[t.Name] || strings.HasPrefix(t.Name, "__") {
			continue
		}
		seen[t.Name] = true
		out.Types = append(out.Types, DocsTypeNameDTO{Name: t.Name, Kind: string(t.Kind)})
	}
	slices.SortFunc(out.Types, func(a, b DocsTypeNameDTO) int { return cmp.Compare(a.Name, b.Name) })
	for _, d := range sch.Directives {
		dd := DocsDirectiveDTO{Name: d.Name, Description: d.Description, Locations: make([]string, 0, len(d.Locations)), Args: inputValues(sch, d.Args), Repeatable: d.IsRepeatable}
		for _, l := range d.Locations {
			dd.Locations = append(dd.Locations, string(l))
		}
		out.Directives = append(out.Directives, dd)
	}
	return out, nil
}

// DocsType returns the type in.Name of the cached schema in.Key with its
// fields and their arguments, input fields or enum values, descriptions
// and deprecations, and the types it relates to. Built-in scalars and
// introspection types are found even when the server did not list them.
// Found is false for an unknown type or without a schema.
func (s *GraphQLService) DocsType(ctx context.Context, in DocsTypeInputDTO) (DocsTypeDTO, error) {
	out := DocsTypeDTO{
		Name: in.Name, Interfaces: []string{}, PossibleTypes: []string{}, Implementations: []string{},
		Fields: []DocsFieldDTO{}, InputFields: []DocsInputValueDTO{}, EnumValues: []DocsEnumValueDTO{},
	}
	if err := interrupted(ctx, "Reading the schema docs"); err != nil {
		return out, err
	}
	sch := s.schemaFor(in.Key)
	if sch == nil {
		return out, nil
	}
	t := sch.Type(in.Name)
	if t == nil {
		return out, nil
	}
	out.Found = true
	out.Kind = string(t.Kind)
	out.Description = t.Description
	out.SpecifiedByURL = t.SpecifiedByURL
	out.IsOneOf = t.IsOneOf
	switch t.Name {
	case sch.QueryType:
		out.Root = string(schema.OpQuery)
	case sch.MutationType:
		out.Root = string(schema.OpMutation)
	case sch.SubscriptionType:
		out.Root = string(schema.OpSubscription)
	}
	out.Interfaces = append(out.Interfaces, t.Interfaces...)
	if t.IsAbstract() {
		for _, p := range sch.PossibleTypes(t.Name) {
			out.PossibleTypes = append(out.PossibleTypes, p.Name)
		}
	}
	if t.Kind == schema.KindInterface {
		for _, impl := range sch.Implementations(t.Name) {
			out.Implementations = append(out.Implementations, impl.Name)
		}
	}
	for _, f := range t.Fields {
		name, kind := named(sch, f.Type)
		out.Fields = append(out.Fields, DocsFieldDTO{
			Name:              f.Name,
			Description:       f.Description,
			Args:              inputValues(sch, f.Args),
			Type:              f.Type.String(),
			TypeName:          name,
			TypeKind:          kind,
			Deprecated:        f.IsDeprecated,
			DeprecationReason: deprecationReason(f.Deprecation),
		})
	}
	out.InputFields = inputValues(sch, t.InputFields)
	for _, v := range t.EnumValues {
		out.EnumValues = append(out.EnumValues, DocsEnumValueDTO{
			Name:              v.Name,
			Description:       v.Description,
			Deprecated:        v.IsDeprecated,
			DeprecationReason: deprecationReason(v.Deprecation),
		})
	}
	return out, nil
}

// DocsSearch finds types, fields, arguments, input fields and enum values
// of the cached schema in.Key whose name matches in.Query (docs.Index:
// case-insensitive, prefix, word start, substring, then in-order letters;
// "Type.member" searches the members of matching types). It returns the
// best in.Limit hits (at most MaxDocsHits) and how many matched. The
// index is built on the first search and kept with the schema.
func (s *GraphQLService) DocsSearch(ctx context.Context, in DocsSearchInputDTO) (DocsSearchDTO, error) {
	out := DocsSearchDTO{Items: []DocsHitDTO{}}
	if err := interrupted(ctx, "Searching the schema docs"); err != nil {
		return out, err
	}
	sch := s.schemaFor(in.Key)
	if sch == nil {
		return out, nil
	}
	out.Schema = true
	limit := in.Limit
	if limit <= 0 || limit > MaxDocsHits {
		limit = MaxDocsHits
	}
	hits, count := s.index(sch).Search(in.Query, limit)
	out.Count = count
	for _, h := range hits {
		out.Items = append(out.Items, DocsHitDTO{
			Kind:        string(h.Kind),
			Name:        h.Name,
			Type:        h.Type,
			Member:      h.Member,
			Parent:      h.Parent,
			Detail:      h.Detail,
			Description: h.Description,
			Deprecated:  h.Deprecated,
		})
	}
	return out, nil
}

// named returns the name and kind of the named type at the core of ref;
// the kind is the schema's when it knows the type.
func named(sch *schema.Schema, ref *schema.TypeRef) (string, string) {
	n := ref.NamedType()
	if n == nil {
		return "", ""
	}
	if t := sch.Type(n.Name); t != nil {
		return t.Name, string(t.Kind)
	}
	return n.Name, string(n.Kind)
}

func inputValues(sch *schema.Schema, vs []*schema.InputValue) []DocsInputValueDTO {
	out := make([]DocsInputValueDTO, 0, len(vs))
	for _, v := range vs {
		name, kind := named(sch, v.Type)
		dto := DocsInputValueDTO{
			Name:              v.Name,
			Description:       v.Description,
			Type:              v.Type.String(),
			TypeName:          name,
			TypeKind:          kind,
			Deprecated:        v.IsDeprecated,
			DeprecationReason: deprecationReason(v.Deprecation),
		}
		if v.DefaultValue != nil {
			dto.DefaultValue, dto.HasDefault = *v.DefaultValue, true
		}
		out = append(out, dto)
	}
	return out
}

// deprecationReason is the reason of a deprecated item ("" otherwise).
func deprecationReason(d schema.Deprecation) string {
	if !d.IsDeprecated {
		return ""
	}
	return d.DeprecationReason
}
