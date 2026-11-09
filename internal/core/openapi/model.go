package openapi

import (
	"encoding/json"
	"strings"
)

// Pos is where an object starts in the source document: 1-based line and
// column (in characters), and its JSON pointer ("#/paths/~1pets/get").
// The zero Pos means unknown.
type Pos struct {
	Line, Column int
	Pointer      string
}

// Entry is a named value of an ordered map: a property, a component, a
// media type, a response (named by its status code) and so on.
type Entry[T any] struct {
	Name  string
	Value T
}

// Entries is an ordered map, in document order.
type Entries[T any] []Entry[T]

// Get returns the value named name and whether it is there.
func (es Entries[T]) Get(name string) (T, bool) {
	for _, e := range es {
		if e.Name == name {
			return e.Value, true
		}
	}
	var zero T
	return zero, false
}

// Names returns the names in order.
func (es Entries[T]) Names() []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.Name
	}
	return out
}

// Extensions are the x- members of an object, as JSON text.
type Extensions = Entries[json.RawMessage]

// Document is a parsed OpenAPI 3.0 or 3.1 document. References are
// resolved: a Schema, Parameter, Response and so on reached through a
// $ref is the same pointer as the component it names, so recursive schemas
// form cycles (see Schema.Recursive) and consumers that walk schemas must
// stop at pointers they have already seen.
type Document struct {
	// OpenAPI is the version string, e.g. "3.0.3" or "3.1.0".
	OpenAPI string
	// Minor is 0 for 3.0.x and 1 for 3.1.x (and later 3.x, read as 3.1).
	Minor int
	// Swagger is "2.0" when the document was converted from Swagger
	// 2.0 (OpenAPI is then "3.0.3"), empty otherwise.
	Swagger           string
	JSONSchemaDialect string
	Info              Info
	// Servers are the document's servers; when the document has none,
	// this is one server with URL "/", as the specification says.
	Servers []Server
	// Paths are the path items in document order.
	Paths []*PathItem
	// Webhooks (3.1) are named path items; PathItem.Path is empty.
	Webhooks     Entries[*PathItem]
	Components   Components
	Security     []SecurityRequirement
	Tags         []Tag
	ExternalDocs *ExternalDocs
	Extensions   Extensions
}

// Is31 reports whether the document is OpenAPI 3.1 (or a later 3.x read
// as 3.1).
func (d *Document) Is31() bool { return d.Minor >= 1 }

// Operations returns every operation of every path, in document order.
func (d *Document) Operations() []*Operation {
	var out []*Operation
	for _, p := range d.Paths {
		out = append(out, p.Operations...)
	}
	return out
}

// Info is the document's metadata.
type Info struct {
	Title          string
	Summary        string // 3.1
	Description    string
	TermsOfService string
	Contact        *Contact
	License        *License
	Version        string
	Extensions     Extensions
}

// Contact is Info.contact.
type Contact struct{ Name, URL, Email string }

// License is Info.license; Identifier is 3.1's SPDX identifier.
type License struct{ Name, Identifier, URL string }

// ExternalDocs points at more documentation.
type ExternalDocs struct{ Description, URL string }

// Tag describes a tag used by operations.
type Tag struct {
	Name         string
	Description  string
	ExternalDocs *ExternalDocs
	Extensions   Extensions
	Pos          Pos
}

// Server is a server URL template with its variables.
type Server struct {
	URL         string
	Description string
	Variables   Entries[*ServerVariable]
	Extensions  Extensions
	Pos         Pos
}

// ServerVariable is a {name} in a server URL.
type ServerVariable struct {
	Default     string
	Enum        []string
	Description string
}

// Expand returns the URL with each {variable} replaced by values[name],
// or by the variable's default when values has no entry for it. Unknown
// variables are left as they are.
func (s Server) Expand(values map[string]string) string {
	var b strings.Builder
	rest := s.URL
	for {
		i := strings.IndexByte(rest, '{')
		if i < 0 {
			b.WriteString(rest)
			return b.String()
		}
		j := strings.IndexByte(rest[i:], '}')
		if j < 0 {
			b.WriteString(rest)
			return b.String()
		}
		name := rest[i+1 : i+j]
		b.WriteString(rest[:i])
		if v, ok := values[name]; ok {
			b.WriteString(v)
		} else if sv, ok := s.Variables.Get(name); ok && sv != nil {
			b.WriteString(sv.Default)
		} else {
			b.WriteString(rest[i : i+j+1])
		}
		rest = rest[i+j+1:]
	}
}

// DefaultURL is Expand(nil): the URL with every variable at its default.
func (s Server) DefaultURL() string { return s.Expand(nil) }

// Methods are the HTTP methods a path item may have (lower case, as the
// document writes them).
var Methods = []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}

// PathItem is the operations on one path (or one webhook or callback
// expression).
type PathItem struct {
	// Path is the key under paths (e.g. "/pets/{petId}"), or the
	// callback expression; empty for webhooks and components.
	Path        string
	Summary     string
	Description string
	// Operations are in document order.
	Operations []*Operation
	// Servers is nil when the path item does not set servers.
	Servers []Server
	// Parameters are the path-level parameters.
	Parameters []*Parameter
	Extensions Extensions
	Pos        Pos
}

// Operation is one HTTP method on a path.
type Operation struct {
	// Method is upper case: "GET", "POST".
	Method string
	// Path is the path item's Path.
	Path        string
	Tags        []string
	Summary     string
	Description string
	OperationID string
	// Parameters are the operation's effective parameters: the path
	// item's, overridden by the operation's own with the same name and
	// location, then the operation's others, in document order.
	Parameters  []*Parameter
	RequestBody *RequestBody
	// Responses are keyed by status code ("200", "4XX") or "default", in
	// document order.
	Responses  Entries[*Response]
	Callbacks  Entries[*Callback]
	Deprecated bool
	// Security is nil when the operation inherits the document's
	// requirements, and empty (not nil) when it removes them.
	Security []SecurityRequirement
	// Servers is nil when the operation does not set servers.
	Servers      []Server
	ExternalDocs *ExternalDocs
	Extensions   Extensions
	Pos          Pos
}

// EffectiveSecurity returns the operation's security requirements, or
// the document's when the operation does not set any.
func (o *Operation) EffectiveSecurity(d *Document) []SecurityRequirement {
	if o.Security != nil {
		return o.Security
	}
	return d.Security
}

// Parameter locations.
const (
	InPath   = "path"
	InQuery  = "query"
	InHeader = "header"
	InCookie = "cookie"
)

// Parameter is a path, query, header or cookie parameter.
type Parameter struct {
	Name            string
	In              string
	Description     string
	Required        bool
	Deprecated      bool
	AllowEmptyValue bool
	// Style is as written; see EffectiveStyle.
	Style string
	// Explode is nil when not written; see EffectiveExplode.
	Explode       *bool
	AllowReserved bool
	Schema        *Schema
	Example       json.RawMessage
	Examples      Entries[*Example]
	// Content is set instead of Schema for complex parameters.
	Content    Entries[*MediaType]
	Extensions Extensions
	Pos        Pos
}

// EffectiveStyle returns Style, or the default for the location: "form"
// for query and cookie, "simple" for path and header.
func (p *Parameter) EffectiveStyle() string {
	if p.Style != "" {
		return p.Style
	}
	if p.In == InQuery || p.In == InCookie {
		return "form"
	}
	return "simple"
}

// EffectiveExplode returns Explode, or its default: true for the form
// style, false otherwise.
func (p *Parameter) EffectiveExplode() bool {
	if p.Explode != nil {
		return *p.Explode
	}
	return p.EffectiveStyle() == "form"
}

// RequestBody is an operation's request body.
type RequestBody struct {
	Description string
	// Content is keyed by media type ("application/json"), in document
	// order.
	Content    Entries[*MediaType]
	Required   bool
	Extensions Extensions
	Pos        Pos
}

// MediaType is the schema and examples for one media type.
type MediaType struct {
	Schema     *Schema
	Example    json.RawMessage
	Examples   Entries[*Example]
	Encoding   Entries[*Encoding]
	Extensions Extensions
	Pos        Pos
}

// Encoding says how one property of a multipart or form body is encoded.
type Encoding struct {
	ContentType   string
	Headers       Entries[*Header]
	Style         string
	Explode       *bool
	AllowReserved bool
}

// Response is one response of an operation.
type Response struct {
	Description string
	Headers     Entries[*Header]
	Content     Entries[*MediaType]
	// Links are kept as JSON text; they are not resolved.
	Links      Entries[json.RawMessage]
	Extensions Extensions
	Pos        Pos
}

// Header is a response or encoding header.
type Header struct {
	Description string
	Required    bool
	Deprecated  bool
	Style       string
	Explode     *bool
	Schema      *Schema
	Example     json.RawMessage
	Examples    Entries[*Example]
	Content     Entries[*MediaType]
	Extensions  Extensions
	Pos         Pos
}

// Example is a named example value.
type Example struct {
	Summary     string
	Description string
	// Value is the example as JSON text; nil when only ExternalValue is
	// given.
	Value         json.RawMessage
	ExternalValue string
	Extensions    Extensions
	Pos           Pos
}

// Callback maps runtime expressions to path items.
type Callback struct {
	Paths []*PathItem
	Pos   Pos
}

// SecurityRequirement is one alternative of a security requirement list:
// every named scheme applies, with its scopes. An empty requirement means
// no security.
type SecurityRequirement = Entries[[]string]

// SecurityScheme is a components.securitySchemes entry.
type SecurityScheme struct {
	// Type is "apiKey", "http", "mutualTLS", "oauth2" or "openIdConnect".
	Type        string
	Description string
	// Name and In are for apiKey.
	Name string
	In   string
	// Scheme ("basic", "bearer", ...) and BearerFormat are for http.
	Scheme           string
	BearerFormat     string
	Flows            *OAuthFlows
	OpenIDConnectURL string
	Extensions       Extensions
	Pos              Pos
}

// OAuthFlows are the OAuth 2 flows of a scheme; nil flows are absent.
type OAuthFlows struct {
	Implicit          *OAuthFlow
	Password          *OAuthFlow
	ClientCredentials *OAuthFlow
	AuthorizationCode *OAuthFlow
}

// OAuthFlow is one OAuth 2 flow.
type OAuthFlow struct {
	AuthorizationURL string
	TokenURL         string
	RefreshURL       string
	// Scopes maps scope names to descriptions.
	Scopes Entries[string]
}

// Components are the document's reusable objects, in document order.
type Components struct {
	Schemas         Entries[*Schema]
	Responses       Entries[*Response]
	Parameters      Entries[*Parameter]
	Examples        Entries[*Example]
	RequestBodies   Entries[*RequestBody]
	Headers         Entries[*Header]
	SecuritySchemes Entries[*SecurityScheme]
	Callbacks       Entries[*Callback]
	PathItems       Entries[*PathItem] // 3.1
	Extensions      Extensions
}

// Schema is a Schema Object: the OpenAPI 3.0 subset of JSON Schema, or
// JSON Schema 2020-12 for 3.1. Both are read into this one form:
//
//   - Type is a list: 3.0's single type, and "null" appended when 3.0's
//     nullable is true; 3.1's type or type array as written.
//   - 3.0's boolean exclusiveMinimum/exclusiveMaximum are converted to
//     3.1's numeric form (the bound moves from Minimum to
//     ExclusiveMinimum).
//   - Example (3.0) and Examples (3.1) are both kept.
//   - A $ref with sibling keywords (3.1) is read as the siblings plus the
//     referenced schema prepended to AllOf; in 3.0 the siblings are
//     ignored, with a diagnostic.
//
// Schemas reached through $ref are shared, so the graph can have cycles.
type Schema struct {
	// Bool is set for the boolean schemas true and false (3.1, and
	// additionalProperties in 3.0); the other fields are then empty.
	Bool *bool
	// Ref is set only on placeholders for references that could not be
	// resolved (external, missing or cyclic ones): the $ref text.
	Ref string
	// Recursive is true when the schema is on a cycle: walking into it
	// through properties, items, allOf and so on can lead back to it.
	Recursive bool

	Type        []string
	Format      string
	Title       string
	Description string
	Default     json.RawMessage
	Example     json.RawMessage
	Examples    []json.RawMessage
	Enum        []json.RawMessage
	Const       json.RawMessage
	Nullable    bool // 3.0 nullable, also folded into Type
	ReadOnly    bool
	WriteOnly   bool
	Deprecated  bool

	// Numbers, as written; "" when absent.
	MultipleOf       json.Number
	Minimum          json.Number
	Maximum          json.Number
	ExclusiveMinimum json.Number
	ExclusiveMaximum json.Number

	MinLength        *int
	MaxLength        *int
	Pattern          string
	ContentMediaType string
	ContentEncoding  string

	Items       *Schema
	PrefixItems []*Schema
	Contains    *Schema
	MinItems    *int
	MaxItems    *int
	UniqueItems bool

	Properties           Entries[*Schema]
	PatternProperties    Entries[*Schema]
	AdditionalProperties *Schema
	PropertyNames        *Schema
	Required             []string
	MinProperties        *int
	MaxProperties        *int

	AllOf []*Schema
	AnyOf []*Schema
	OneOf []*Schema
	Not   *Schema
	If    *Schema
	Then  *Schema
	Else  *Schema

	Discriminator *Discriminator
	XML           *XML
	Defs          Entries[*Schema] // $defs
	ID            string           // $id
	Anchor        string           // $anchor
	Schema        string           // $schema
	Comment       string           // $comment
	ExternalDocs  *ExternalDocs
	Extensions    Extensions
	Pos           Pos
}

// HasType reports whether t is one of the schema's types.
func (s *Schema) HasType(t string) bool {
	for _, x := range s.Type {
		if x == t {
			return true
		}
	}
	return false
}

// Property returns the property schema named name, or nil.
func (s *Schema) Property(name string) *Schema {
	p, _ := s.Properties.Get(name)
	return p
}

// Discriminator selects a oneOf/anyOf alternative by a property.
type Discriminator struct {
	PropertyName string
	// Mapping maps property values to schema references, as written.
	Mapping Entries[string]
}

// XML is a schema's XML hints.
type XML struct {
	Name, Namespace, Prefix string
	Attribute, Wrapped      bool
}
