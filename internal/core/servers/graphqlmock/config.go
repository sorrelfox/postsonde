package graphqlmock

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// CodeInvalidConfig: a configuration the GraphQL mock server cannot use.
// Details: field ("path", "schemaFile", "schemaText", "listLength",
// "nullPercent", "allowOrigin", "overrides"), and for an override
// overrideIndex (0-based) and overrideField ("target", "args", "mode",
// "value", "error", "code").
const CodeInvalidConfig = "graphqlmock.invalid_config"

// Override modes (Override.Mode).
const (
	// ModeValue answers the target with Override.Value, JSON shaped like
	// its type. Members an object leaves out are generated.
	ModeValue = "value"
	// ModeError makes the target a field error with Override.Error: the
	// field is null and the error is in the response's errors.
	ModeError = "error"
)

// Defaults of a new server.
const (
	DefaultPath       = "/graphql"
	DefaultListLength = 2
)

// Limits of a configuration.
const (
	MaxSchemaText  = 32 << 20 // bytes of pasted SDL or introspection JSON, as a schema file
	MaxListLength  = 100
	MaxOverrides   = 1000
	MaxOverrideLen = 1 << 20 // bytes of JSON per override value
	maxPathLen     = 2048
	maxShortText   = 4096
)

// Config is the configuration of a GraphQL mock server (Spec.Config).
type Config struct {
	// Path is the endpoint ("/graphql").
	Path string
	// SchemaFile is the schema served: an SDL file (.graphql, .graphqls,
	// .gql, …) or a saved introspection result (.json), stored as a
	// GraphQL request stores its schema file (relative to the project root
	// with "/", or absolute; Options.ReadFile reads it). When it is "",
	// SchemaText is the schema: pasted SDL, or introspection JSON (text
	// that starts with "{").
	SchemaFile string
	SchemaText string
	// Introspection answers __schema and __type, so clients (the app's
	// own GraphQL editor too) can load the schema from the server.
	Introspection bool
	// ListLength is how many items a generated list has (0 to 100).
	ListLength int
	// NullPercent is how often a nullable field is generated as null (0
	// to 100, chosen deterministically).
	NullPercent int
	// Seed changes the generated data; the same seed, schema and query
	// always give the same response.
	Seed int64
	// AllowOrigin is the CORS origin allowed to call the endpoint from a
	// browser: "*" for any, "" for none.
	AllowOrigin string
	// Overrides are the static responses, first match wins.
	Overrides []Override
}

// Override is a static response for a field, or for every value of a
// scalar or enum type.
type Override struct {
	Enabled bool
	// Target is "Type.field" (a field of an object or interface type) or
	// the name of a scalar or enum type ("DateTime"), whose every value it
	// replaces.
	Target string
	// Args, a JSON object, limits a field override to calls whose
	// arguments have these values; "" matches any call.
	Args string
	// Mode is ModeValue ("" too) or ModeError.
	Mode string
	// Value is the JSON value of ModeValue.
	Value string
	// Error is the message of ModeError, and Code its extensions.code
	// ("" for none).
	Error string
	Code  string
}

// IsField reports whether the override targets a field ("Type.field").
func (o Override) IsField() bool { return strings.Contains(o.Target, ".") }

// DefaultConfig is the configuration of a new GraphQL mock server: an
// example schema pasted in, introspection on.
func DefaultConfig() Config {
	return Config{
		Path:          DefaultPath,
		SchemaText:    ExampleSchema,
		Introspection: true,
		ListLength:    DefaultListLength,
		Overrides:     []Override{},
	}
}

// ExampleSchema is the schema of a new server, so it answers right away.
const ExampleSchema = `type Query {
  "A user by ID."
  user(id: ID!): User
  users(first: Int = 10): [User!]!
  search(text: String!): [SearchResult!]!
}

type Mutation {
  createUser(input: CreateUserInput!): User!
}

interface Node {
  id: ID!
}

type User implements Node {
  id: ID!
  name: String!
  email: String
  role: Role!
  posts: [Post!]!
}

type Post implements Node {
  id: ID!
  title: String!
  publishedAt: DateTime
  author: User!
}

union SearchResult = User | Post

enum Role {
  ADMIN
  EDITOR
  VIEWER
}

input CreateUserInput {
  name: String!
  email: String
  role: Role = VIEWER
}

scalar DateTime
`

// Normalize checks c and returns it with the path, target and mode
// normalized. Whether the overrides fit the schema, and whether their JSON
// is valid, is checked when the server loads the schema (Start,
// Reconfigure, Check), not here.
func Normalize(c Config) (Config, error) {
	c.Path = strings.TrimSpace(c.Path)
	if c.Path == "" {
		c.Path = DefaultPath
	}
	if !strings.HasPrefix(c.Path, "/") || len(c.Path) > maxPathLen || !printable(c.Path) || strings.ContainsAny(c.Path, "?# ") {
		return Config{}, invalidConfig("path", "The path "+strconv.Quote(c.Path)+" is not a valid endpoint path; write one such as /graphql.")
	}
	c.SchemaFile = strings.TrimSpace(c.SchemaFile)
	if len(c.SchemaFile) > maxPathLen || !printable(c.SchemaFile) {
		return Config{}, invalidConfig("schemaFile", "The schema file "+strconv.Quote(c.SchemaFile)+" is not a valid path.")
	}
	if len(c.SchemaText) > MaxSchemaText {
		return Config{}, invalidConfig("schemaText", fmt.Sprintf("The schema text is larger than %d MB.", MaxSchemaText>>20))
	}
	if !utf8.ValidString(c.SchemaText) {
		return Config{}, invalidConfig("schemaText", "The schema text is not valid UTF-8.")
	}
	if c.ListLength < 0 || c.ListLength > MaxListLength {
		return Config{}, invalidConfig("listLength", fmt.Sprintf("Generated lists have 0 to %d items.", MaxListLength))
	}
	if c.NullPercent < 0 || c.NullPercent > 100 {
		return Config{}, invalidConfig("nullPercent", "The share of null values is 0 to 100 percent.")
	}
	c.AllowOrigin = strings.TrimSpace(c.AllowOrigin)
	if len(c.AllowOrigin) > maxShortText || !printable(c.AllowOrigin) {
		return Config{}, invalidConfig("allowOrigin", "The allowed origin must be *, an origin such as http://localhost:3000, or empty.")
	}
	if len(c.Overrides) > MaxOverrides {
		return Config{}, invalidConfig("overrides", fmt.Sprintf("There are %d overrides; at most %d are allowed.", len(c.Overrides), MaxOverrides))
	}
	out := make([]Override, 0, len(c.Overrides))
	for i, o := range c.Overrides {
		o, err := normalizeOverride(i, o)
		if err != nil {
			return Config{}, err
		}
		out = append(out, o)
	}
	c.Overrides = out
	return c, nil
}

func normalizeOverride(i int, o Override) (Override, error) {
	o.Target = strings.TrimSpace(o.Target)
	typ, field, isField := strings.Cut(o.Target, ".")
	if !name(typ) || isField && !name(field) {
		return Override{}, overrideError(i, "target", fmt.Sprintf("Override %d: %q is not a target; write Type.field or the name of a scalar or enum type.", i+1, o.Target))
	}
	switch o.Mode {
	case "":
		o.Mode = ModeValue
	case ModeValue, ModeError:
	default:
		return Override{}, overrideError(i, "mode", fmt.Sprintf("Override %d: %q is not a mode; use value or error.", i+1, o.Mode))
	}
	if o.Mode == ModeError && !isField {
		return Override{}, overrideError(i, "mode", fmt.Sprintf("Override %d: only a field can answer with an error.", i+1))
	}
	if strings.TrimSpace(o.Args) != "" && !isField {
		return Override{}, overrideError(i, "args", fmt.Sprintf("Override %d: only a field override can match arguments.", i+1))
	}
	for _, f := range []struct{ field, text string }{{"args", o.Args}, {"value", o.Value}} {
		if len(f.text) > MaxOverrideLen {
			return Override{}, overrideError(i, f.field, fmt.Sprintf("Override %d: the %s is larger than 1 MiB.", i+1, f.field))
		}
		if !utf8.ValidString(f.text) {
			return Override{}, overrideError(i, f.field, fmt.Sprintf("Override %d: the %s is not valid UTF-8.", i+1, f.field))
		}
	}
	if len(o.Error) > maxShortText || !utf8.ValidString(o.Error) {
		return Override{}, overrideError(i, "error", fmt.Sprintf("Override %d: the error message must be UTF-8 text of at most %d bytes.", i+1, maxShortText))
	}
	o.Code = strings.TrimSpace(o.Code)
	if len(o.Code) > 256 || !printable(o.Code) {
		return Override{}, overrideError(i, "code", fmt.Sprintf("Override %d: the error code must be printable text of at most 256 bytes.", i+1))
	}
	return o, nil
}

// name reports whether s is a GraphQL name.
func name(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c == '_' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || i > 0 && '0' <= c && c <= '9') {
			return false
		}
	}
	return true
}

func printable(s string) bool {
	return utf8.ValidString(s) && !strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

func invalidConfig(field, msg string) *errs.Error {
	return errs.New(errs.InvalidArgument, msg).WithCode(CodeInvalidConfig).WithDetail("field", field)
}

func overrideError(i int, field, msg string) *errs.Error {
	return invalidConfig("overrides", msg).WithDetail("overrideIndex", strconv.Itoa(i)).WithDetail("overrideField", field)
}
