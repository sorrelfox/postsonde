package servers

import (
	"context"
	"io"
	"os"
	"strconv"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/servers/mockhttp"
	"github.com/sorrelfox/postsonde/internal/core/servers/mockopenapi"
)

// Mock routes from an OpenAPI document (MOCK-09): PickOpenAPIFile asks for
// the file, GenerateMockRoutes reads it and returns the generated routes
// merged into the routes being edited, for a preview; the editor applies
// the returned configuration with UpdateConfig. See docs/mock-routes.md.

// Error codes of the OpenAPI routes.
const (
	// CodePickUnavailable: PickOpenAPIFile cannot show a file dialog
	// (tests, headless runs).
	CodePickUnavailable = "servers.pick_unavailable"
	// CodeFileUnreadable: the OpenAPI file cannot be read.
	CodeFileUnreadable = "servers.file_unreadable"
	// CodeFileTooLarge: the OpenAPI file is larger than the reader takes.
	CodeFileTooLarge = "servers.file_too_large"
	// CodeInvalidMode: the merge mode is not "merge" or "replace".
	CodeInvalidMode = "servers.invalid_mode"
)

// FileDialog describes the native dialog for choosing one file.
type FileDialog struct {
	Title      string
	Message    string
	ButtonText string
	// FilterName and FilterPattern ("*.yaml;*.json") limit the files
	// shown; the user can still choose any file where the OS allows.
	FilterName    string
	FilterPattern string
}

// FilePicker shows a native dialog to choose one file and returns its
// path, or "" when the user cancels. The app layer supplies it; tests fake
// it.
type FilePicker func(ctx context.Context, d FileDialog) (string, error)

// WithFilePicker sets the dialog PickOpenAPIFile shows.
func WithFilePicker(pick FilePicker) Option {
	return func(s *ServersService) { s.pick = pick }
}

// MockOpenAPIInputDTO asks for the routes of an OpenAPI document.
type MockOpenAPIInputDTO struct {
	// Path is the document's file (JSON or YAML; OpenAPI 3.0, 3.1 or
	// Swagger 2.0).
	Path string `json:"path"`
	// Prefix is put before every route path ("/v1"); a URL counts as its
	// path. Null uses the path of the document's first server.
	Prefix *string `json:"prefix"`
	// Mode is "merge" (keep the routes in Config and add the generated
	// ones that do not clash; the default) or "replace".
	Mode string `json:"mode"`
	// Config is the mock server's routes now, as the editor has them.
	Config MockHTTPConfigDTO `json:"config"`
}

// MockOpenAPIResultDTO is the preview of the generated routes.
type MockOpenAPIResultDTO struct {
	// Title and Version are the document's info; OpenAPI its version and
	// Swagger "2.0" for a Swagger document read as OpenAPI 3.0.
	Title   string `json:"title"`
	Version string `json:"version"`
	OpenAPI string `json:"openapi"`
	Swagger string `json:"swagger"`
	// DefaultPrefix is the path of the first server; Prefix the one the
	// routes have.
	DefaultPrefix string `json:"defaultPrefix"`
	Prefix        string `json:"prefix"`
	// Routes are the generated routes, one per operation, in document
	// order.
	Routes []MockOpenAPIRouteDTO `json:"routes"`
	// Diagnostics are the reader's and the generator's notes, then the
	// merge's.
	Diagnostics []MockOpenAPIDiagnosticDTO `json:"diagnostics"`
	// Config is the mock server's routes with the generated ones merged
	// in: what UpdateConfig should get when the user applies.
	Config MockHTTPConfigDTO `json:"config"`
	// Added counts the generated routes in Config, Kept the existing
	// routes kept and Removed the existing routes dropped ("replace").
	Added   int `json:"added"`
	Kept    int `json:"kept"`
	Removed int `json:"removed"`
}

// MockOpenAPIRouteDTO is one generated route and the operation it answers.
type MockOpenAPIRouteDTO struct {
	// Route is the route as generated; in Config its ID may differ (an
	// existing route had it).
	Route MockRouteDTO `json:"route"`
	// Operation is "GET /pets/{petId}" as the document writes it.
	Operation   string `json:"operation"`
	OperationID string `json:"operationId"`
	Summary     string `json:"summary"`
	// Response is the documented response the route sends ("200",
	// "default"); "" when there is none.
	Response string `json:"response"`
	// MediaType is the body's documented media type, "" for none.
	MediaType string `json:"mediaType"`
	// Status is "added", "existing" (an existing route answers the same
	// method and path and is kept) or "over_limit".
	Status string `json:"status"`
	// ID is the route's ID in Config, "" when it is not added.
	ID string `json:"id"`
}

// MockOpenAPIDiagnosticDTO is a note or problem about the document, an
// operation or the merge.
type MockOpenAPIDiagnosticDTO struct {
	// Severity is "error", "warning" or "info".
	Severity string `json:"severity"`
	// Code is an openapi.* or mockopenapi.* code.
	Code    string `json:"code"`
	Message string `json:"message"`
	// Operation is "GET /pets" for an operation's note, "" otherwise.
	Operation string `json:"operation"`
	// RouteIndex is the index in Routes, -1 when the note has no route.
	RouteIndex int `json:"routeIndex"`
	// Line and Column are 1-based, 0 when unknown; Pointer is the JSON
	// pointer in the document.
	Line    int    `json:"line"`
	Column  int    `json:"column"`
	Pointer string `json:"pointer"`
}

// maxOpenAPIFile is the largest document read: the reader's own limit.
const maxOpenAPIFile = 32 << 20

// PickOpenAPIFile shows the native dialog to choose an OpenAPI document
// and returns its path, or "" when the user cancels.
func (s *ServersService) PickOpenAPIFile(ctx context.Context) (string, error) {
	if err := interrupted(ctx, "Choosing the OpenAPI file"); err != nil {
		return "", err
	}
	if s.pick == nil {
		return "", errs.New(errs.Unsupported, "Choosing a file is not available here.").WithCode(CodePickUnavailable)
	}
	path, err := s.pick(ctx, FileDialog{
		Title:         "Generate routes from OpenAPI",
		Message:       "Choose an OpenAPI 3 or Swagger 2.0 document (JSON or YAML).",
		ButtonText:    "Choose",
		FilterName:    "OpenAPI documents (*.yaml, *.yml, *.json)",
		FilterPattern: "*.yaml;*.yml;*.json",
	})
	if err != nil {
		return "", errs.Wrap(err, errs.KindOf(err), "The file dialog could not be shown.").WithCode(CodePickUnavailable)
	}
	return path, nil
}

// GenerateMockRoutes reads the OpenAPI document at in.Path and generates
// one route per operation (mockopenapi), merged into in.Config by in.Mode.
// Nothing changes on any server: the editor applies Config itself. A file
// that is not an OpenAPI or Swagger document fails with the reader's error
// (openapi.not_openapi, openapi.invalid_yaml…); problems inside it are
// diagnostics.
func (s *ServersService) GenerateMockRoutes(ctx context.Context, in MockOpenAPIInputDTO) (MockOpenAPIResultDTO, error) {
	if err := interrupted(ctx, "Generating the routes"); err != nil {
		return MockOpenAPIResultDTO{}, err
	}
	mode := mockopenapi.Mode(in.Mode)
	switch mode {
	case "":
		mode = mockopenapi.ModeMerge
	case mockopenapi.ModeMerge, mockopenapi.ModeReplace:
	default:
		return MockOpenAPIResultDTO{}, errs.New(errs.InvalidArgument, strconv.Quote(in.Mode)+" is not a way to add routes; use merge or replace.").
			WithCode(CodeInvalidMode)
	}
	data, err := readOpenAPIFile(in.Path)
	if err != nil {
		return MockOpenAPIResultDTO{}, err
	}
	if err := interrupted(ctx, "Generating the routes"); err != nil {
		return MockOpenAPIResultDTO{}, err
	}
	res, err := mockopenapi.Parse(data, mockopenapi.Options{Prefix: in.Prefix})
	if err != nil {
		return MockOpenAPIResultDTO{}, err
	}
	merged := mockopenapi.Merge(in.Config.toCore(), res.Routes, mode)

	out := MockOpenAPIResultDTO{
		Title: res.Title, Version: res.Version, OpenAPI: res.OpenAPI, Swagger: res.Swagger,
		DefaultPrefix: res.DefaultPrefix, Prefix: res.Prefix,
		Routes:      make([]MockOpenAPIRouteDTO, 0, len(res.Routes)),
		Diagnostics: make([]MockOpenAPIDiagnosticDTO, 0, len(res.Diagnostics)+len(merged.Diagnostics)),
		Added:       merged.Added, Kept: merged.Kept, Removed: merged.Removed,
	}
	for i, r := range res.Routes {
		op := res.Operations[i]
		out.Routes = append(out.Routes, MockOpenAPIRouteDTO{
			Route:     toMockRouteDTO(r),
			Operation: op.Method + " " + op.Path, OperationID: op.OperationID, Summary: op.Summary,
			Response: op.Response, MediaType: op.MediaType,
			Status: merged.Statuses[i], ID: merged.IDs[i],
		})
	}
	for _, d := range append(res.Diagnostics, merged.Diagnostics...) {
		if d.Operation == "" && d.RouteIndex >= 0 && d.RouteIndex < len(res.Operations) {
			op := res.Operations[d.RouteIndex]
			d.Operation = op.Method + " " + op.Path
		}
		out.Diagnostics = append(out.Diagnostics, MockOpenAPIDiagnosticDTO{
			Severity: string(d.Severity), Code: d.Code, Message: d.Message, Operation: d.Operation, RouteIndex: d.RouteIndex,
			Line: d.Pos.Line, Column: d.Pos.Column, Pointer: d.Pos.Pointer,
		})
	}
	if cfg := toConfigDTO(merged.Config).MockHTTP; cfg != nil {
		out.Config = *cfg
	}
	s.logger.Info("mock routes generated from openapi", "operations", len(res.Routes), "added", merged.Added, "mode", string(mode))
	return out, nil
}

// readOpenAPIFile reads a document of at most maxOpenAPIFile bytes.
func readOpenAPIFile(path string) ([]byte, error) {
	if path == "" {
		return nil, errs.New(errs.InvalidArgument, "Choose an OpenAPI file first.").WithCode(CodeFileUnreadable)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errs.Wrap(err, errs.KindOf(err), "The OpenAPI file could not be opened.").WithCode(CodeFileUnreadable).WithDetail("path", path)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxOpenAPIFile+1))
	if err != nil {
		return nil, errs.Wrap(err, errs.IO, "The OpenAPI file could not be read.").WithCode(CodeFileUnreadable).WithDetail("path", path)
	}
	if len(data) > maxOpenAPIFile {
		return nil, errs.New(errs.InvalidArgument, "The OpenAPI file is larger than "+strconv.Itoa(maxOpenAPIFile>>20)+" MiB.").
			WithCode(CodeFileTooLarge).WithDetail("path", path)
	}
	return data, nil
}

// toMockRouteDTO maps one route (as toConfigDTO does).
func toMockRouteDTO(r mockhttp.Route) MockRouteDTO {
	return toConfigDTO(mockhttp.Config{Routes: []mockhttp.Route{r}}).MockHTTP.Routes[0]
}
