package servers

import (
	"context"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/request"
	core "github.com/sorrelfox/postsonde/internal/core/servers"
	"github.com/sorrelfox/postsonde/internal/core/servers/graphqlmock"
)

// The DTOs and methods of the GraphQL mock server (SSRV-08): its schema
// (a file, or pasted SDL or introspection JSON), the settings of the
// generated data and the static overrides; DescribeGraphQLMock loads the
// schema the editor shows, and the GraphQL detail of the traffic. See
// docs/servers.md.

// GraphQLMockConfigDTO is the configuration of a graphql_mock server.
type GraphQLMockConfigDTO struct {
	// Path is the endpoint ("/graphql").
	Path string `json:"path"`
	// SchemaFile is a stored path (relative to the project root, or
	// absolute): an SDL file or an introspection result (.json). When it
	// is "", SchemaText (pasted SDL or introspection JSON) is the schema.
	SchemaFile string `json:"schemaFile"`
	SchemaText string `json:"schemaText"`
	// Introspection answers __schema and __type.
	Introspection bool `json:"introspection"`
	// ListLength is the length of generated lists (0–100), NullPercent how
	// often a nullable field is null (0–100), Seed what varies the data.
	ListLength  int   `json:"listLength"`
	NullPercent int   `json:"nullPercent"`
	Seed        int64 `json:"seed"`
	// AllowOrigin is the CORS origin allowed, "*" for any, "" for none.
	AllowOrigin string                   `json:"allowOrigin"`
	Overrides   []GraphQLMockOverrideDTO `json:"overrides"`
}

// GraphQLMockOverrideDTO is a static response.
type GraphQLMockOverrideDTO struct {
	Enabled bool `json:"enabled"`
	// Target is "Type.field" or a scalar or enum type.
	Target string `json:"target"`
	// Args is a JSON object the field's arguments must match, "" for any.
	Args string `json:"args"`
	// Mode is "value" or "error".
	Mode string `json:"mode"`
	// Value is JSON text; Error and Code the field error of "error".
	Value string `json:"value"`
	Error string `json:"error"`
	Code  string `json:"code"`
}

// GraphQLDTO is the GraphQL mock server's detail of a traffic event.
type GraphQLDTO struct {
	// Request numbers the requests of one run.
	Request int64 `json:"request"`
	// Operation is the operation's name, OperationType "query" or
	// "mutation" ("" when none was run).
	Operation     string `json:"operation"`
	OperationType string `json:"operationType"`
	// Variables are the request's variables as JSON text, "" for none.
	Variables     string `json:"variables"`
	Introspection bool   `json:"introspection"`
	// Errors and HasData describe the response, on its event.
	Errors  int  `json:"errors"`
	HasData bool `json:"hasData"`
}

// GraphQLMockDescriptionDTO is what the editor shows about a GraphQL mock
// server's schema.
type GraphQLMockDescriptionDTO struct {
	// Source is "file", "text" or "" (no schema); Format "sdl" or
	// "introspection".
	Source string `json:"source"`
	Format string `json:"format"`
	// Types counts the declared types; the others count root fields.
	Types              int `json:"types"`
	QueryFields        int `json:"queryFields"`
	MutationFields     int `json:"mutationFields"`
	SubscriptionFields int `json:"subscriptionFields"`
	// Targets are what overrides can answer, root fields first.
	Targets []GraphQLMockTargetDTO `json:"targets"`
	// Problems are the schema's problems and the overrides that do not fit
	// it (overrideIndex -1 for a schema problem).
	Problems []GraphQLMockProblemDTO `json:"problems"`
	// SDL is the schema printed as SDL.
	SDL string `json:"sdl"`
}

// GraphQLMockTargetDTO is a field or a scalar or enum type.
type GraphQLMockTargetDTO struct {
	Target string `json:"target"`
	// Type is the field's type ("[User!]!") or "SCALAR" / "ENUM".
	Type string   `json:"type"`
	Args []string `json:"args"`
	Root bool     `json:"root"`
}

// GraphQLMockProblemDTO is an override the server skips, or a schema
// problem.
type GraphQLMockProblemDTO struct {
	OverrideIndex int    `json:"overrideIndex"`
	Field         string `json:"field"`
	Message       string `json:"message"`
}

func (c GraphQLMockConfigDTO) toCore() graphqlmock.Config {
	out := graphqlmock.Config{
		Path: c.Path, SchemaFile: c.SchemaFile, SchemaText: c.SchemaText, Introspection: c.Introspection,
		ListLength: c.ListLength, NullPercent: c.NullPercent, Seed: c.Seed, AllowOrigin: c.AllowOrigin,
		Overrides: make([]graphqlmock.Override, 0, len(c.Overrides)),
	}
	for _, o := range c.Overrides {
		out.Overrides = append(out.Overrides, graphqlmock.Override{
			Enabled: o.Enabled, Target: o.Target, Args: o.Args, Mode: o.Mode, Value: o.Value, Error: o.Error, Code: o.Code,
		})
	}
	return out
}

func toGraphQLMockConfigDTO(c graphqlmock.Config) *GraphQLMockConfigDTO {
	out := &GraphQLMockConfigDTO{
		Path: c.Path, SchemaFile: c.SchemaFile, SchemaText: c.SchemaText, Introspection: c.Introspection,
		ListLength: c.ListLength, NullPercent: c.NullPercent, Seed: c.Seed, AllowOrigin: c.AllowOrigin,
		Overrides: make([]GraphQLMockOverrideDTO, 0, len(c.Overrides)),
	}
	for _, o := range c.Overrides {
		out.Overrides = append(out.Overrides, GraphQLMockOverrideDTO{
			Enabled: o.Enabled, Target: o.Target, Args: o.Args, Mode: o.Mode, Value: o.Value, Error: o.Error, Code: o.Code,
		})
	}
	return out
}

func toGraphQLDTO(g *core.GraphQLInfo) *GraphQLDTO {
	if g == nil {
		return nil
	}
	return &GraphQLDTO{
		Request: int64(g.Request), Operation: g.Operation, OperationType: g.OperationType, Variables: g.Variables,
		Introspection: g.Introspection, Errors: g.Errors, HasData: g.HasData,
	}
}

func toGraphQLMockDescriptionDTO(d graphqlmock.Description) GraphQLMockDescriptionDTO {
	out := GraphQLMockDescriptionDTO{
		Source: d.Source, Format: d.Format, Types: d.Types,
		QueryFields: d.QueryFields, MutationFields: d.MutationFields, SubscriptionFields: d.SubscriptionFields,
		Targets: make([]GraphQLMockTargetDTO, 0, len(d.Targets)), Problems: make([]GraphQLMockProblemDTO, 0, len(d.Problems)),
		SDL: d.SDL,
	}
	for _, t := range d.Targets {
		out.Targets = append(out.Targets, GraphQLMockTargetDTO{Target: t.Target, Type: t.Type, Args: nonNil(t.Args), Root: t.Root})
	}
	for _, p := range d.Problems {
		out.Problems = append(out.Problems, GraphQLMockProblemDTO{OverrideIndex: p.OverrideIndex, Field: p.Field, Message: p.Message})
	}
	return out
}

// readSchemaFile reads a GraphQL mock's stored schema file: relative to
// the open project's root, from the project folder or an allowed
// directory only (PRV-10, fileaccess.not_allowed), at most 32 MB.
func (s *ServersService) readSchemaFile(stored string) ([]byte, error) {
	p := s.filePolicy()
	p.Subject = "The schema file"
	p.MaxSize = graphqlmock.MaxSchemaText
	return p.ReadFile(stored)
}

// DescribeGraphQLMock loads the schema of a GraphQL mock configuration
// as the server would (the file read as at start) and checks its
// overrides, for the editor. Nothing changes on any server. A schema that
// does not load is the error (graphqlmock.schema_failed, fileaccess.*);
// overrides that do not fit are problems.
func (s *ServersService) DescribeGraphQLMock(ctx context.Context, config GraphQLMockConfigDTO) (GraphQLMockDescriptionDTO, error) {
	if err := interrupted(ctx, "Loading the schema"); err != nil {
		return GraphQLMockDescriptionDTO{}, err
	}
	d, err := graphqlmock.Describe(config.toCore(), s.readSchemaFile)
	if err != nil {
		return GraphQLMockDescriptionDTO{}, err
	}
	return toGraphQLMockDescriptionDTO(d), nil
}

// PickGraphQLSchemaFile shows the native dialog to choose a GraphQL mock's
// schema file (SDL or introspection JSON) and returns the path to store:
// relative to the project root with "/" when inside the project, else
// absolute; "" when the user cancels.
func (s *ServersService) PickGraphQLSchemaFile(ctx context.Context) (string, error) {
	if err := interrupted(ctx, "Choosing the schema file"); err != nil {
		return "", err
	}
	if s.pick == nil {
		return "", errs.New(errs.Unsupported, "Choosing a file is not available here.").WithCode(CodePickUnavailable)
	}
	root := s.filePolicy().ProjectRoot
	path, err := s.pick(ctx, FileDialog{
		Title:         "Choose the schema to serve",
		Message:       "Choose a GraphQL schema (SDL) or a saved introspection result (JSON).",
		ButtonText:    "Choose",
		FilterName:    "GraphQL schemas (*.graphql, *.graphqls, *.gql, *.json)",
		FilterPattern: "*.graphql;*.graphqls;*.gql;*.sdl;*.json",
	})
	if err != nil {
		return "", errs.Wrap(err, errs.KindOf(err), "The file dialog could not be shown.").WithCode(CodePickUnavailable)
	}
	if path == "" {
		return "", nil
	}
	return request.StoredFilePath(root, path), nil
}
