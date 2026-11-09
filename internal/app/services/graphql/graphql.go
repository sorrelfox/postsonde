// Package graphql is the bound service behind the GraphQL editor's
// schema features: fetching a schema by introspection when the user asks
// (GQL-10) and caching it per endpoint in the workspace's local directory
// (GQL-21), so the editor keeps working offline and after a restart; and
// serving it to the editor: diagnostics (GQL-12), completion (GQL-13) and
// the docs explorer (GQL-14, docs.go). See docs/graphql.md. It also runs
// subscriptions over WebSocket with connection params and a live event
// log (GQL-30, GQL-33, GQL-34, subscribe.go; docs/graphql-subscriptions.md).
//
// Introspection runs only from FetchSchema, which the UI calls for the
// Fetch schema and Refresh buttons and for a request's opt-in schema
// auto-refresh (GQL-19); nothing here fetches on its own. SaveSchemaSDL
// (export.go) saves a schema as SDL (GQL-20). Fetches and subscriptions are operations, so their
// connections carry the user-operation mark the app's dialer requires
// (PRV-05).
package graphql

import (
	"bytes"
	"context"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/graphql/docs"
	"github.com/sorrelfox/postsonde/internal/core/graphql/schema"
	"github.com/sorrelfox/postsonde/internal/core/graphql/schemacache"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
)

// Domain names the GraphQL service and its operations.
const Domain = "graphql"

// Error codes of the service.
const (
	// CodeUnavailable: the service was built without what fetching needs.
	CodeUnavailable = "graphql.unavailable"
	// CodeHTTPStatus: the endpoint answered the introspection query with
	// an HTTP error status and no GraphQL response (kind protocol).
	// Details: status.
	CodeHTTPStatus = "graphql.introspection_http_status"
	// CodeTooLarge: the introspection response was cut at the response
	// size limit (kind protocol).
	CodeTooLarge = "graphql.introspection_too_large"
)

// Poster sends a document to a GraphQL request's endpoint
// (http.GraphQLPoster).
type Poster interface {
	Post(ctx context.Context, query, operationName string) (*httpclient.Response, error)
}

// Endpoint returns the endpoint of GraphQL request id without sending
// anything (http.GraphQLEndpoint).
type Endpoint func(ctx context.Context, id string) (string, error)

// NewPoster prepares posting to the endpoint of GraphQL request id
// (http.NewGraphQLPoster). It fails when the request cannot be built.
type NewPoster func(ctx context.Context, id string) (endpoint string, p Poster, err error)

// LocalDir returns the open workspace's local side-file directory
// (workspace.Manager.LocalDir).
type LocalDir func() (string, error)

// maxMemory bounds the schemas kept in memory; beyond it the map starts
// over (they are reloaded from disk on use).
const maxMemory = 32

// GraphQLService fetches, caches and serves GraphQL schemas.
type GraphQLService struct {
	endpoint  Endpoint
	newPoster NewPoster
	localDir  LocalDir
	ops       *operation.Registry
	logger    *slog.Logger
	now       func() time.Time

	mu sync.Mutex
	// memory holds the schemas read or fetched, by local directory and
	// endpoint; a nil entry means "no cached schema".
	memory map[memoryKey]*schemacache.Entry
	// indexes holds the docs search index of each schema in memory
	// (GQL-14), built on the first search.
	indexes map[*schema.Schema]*docs.Index

	// Schema files (schemafile.go, GQL-11): files holds the files read,
	// by absolute path.
	schemaFile SchemaFileOf
	root       ProjectRoot
	pick       FilePicker
	files      map[string]*fileSchema
	// allowedDirs are the directories schema files may be read from
	// besides the project's (WithAllowedDirs, PRV-10); nil for none.
	allowedDirs func() []string

	// pickSave is the save dialog of SaveSchemaSDL (export.go, GQL-20).
	pickSave SavePicker

	// Subscriptions (subscribe.go).
	newSubscription NewSubscription
	ackTimeout      time.Duration // 0: transportws default
	closeTimeout    time.Duration // 0: subscriptionCloseTimeout
	subs            subscriptions
}

type memoryKey struct{ dir, endpoint string }

// New returns a GraphQLService. endpoint and newPoster come from the HTTP
// service, localDir from the workspace manager; fetches and subscriptions
// (WithSubscriptions) run as operations in ops. A nil function or registry
// makes the methods that need it fail with CodeUnavailable. A nil logger
// means slog.Default().
func New(endpoint Endpoint, newPoster NewPoster, localDir LocalDir, ops *operation.Registry, logger *slog.Logger, opts ...Option) *GraphQLService {
	if logger == nil {
		logger = slog.Default()
	}
	s := &GraphQLService{
		endpoint:  endpoint,
		newPoster: newPoster,
		localDir:  localDir,
		ops:       ops,
		logger:    logger.With("service", Domain),
		now:       time.Now,
		memory:    map[memoryKey]*schemacache.Entry{},
		indexes:   map[*schema.Schema]*docs.Index{},
		files:     map[string]*fileSchema{},
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// ServiceName names the service in Wails logs.
func (s *GraphQLService) ServiceName() string { return Domain }

// SchemaInfo returns the schema of GraphQL request in.RequestID: for a
// request with a schema file (GQL-11), the file's schema, read again when
// it changed on disk, or why it cannot be used; otherwise what is cached
// for the request's endpoint: the schema's type count and age, or Cached
// false. It never sends anything. The Key it returns is what Diagnostics
// and Complete take.
func (s *GraphQLService) SchemaInfo(ctx context.Context, in SchemaInputDTO) (SchemaInfoDTO, error) {
	if err := interrupted(ctx, "Reading the schema"); err != nil {
		return SchemaInfoDTO{}, err
	}
	return s.schemaInfo(ctx, in.RequestID, false)
}

func (s *GraphQLService) schemaInfo(ctx context.Context, id string, reload bool) (SchemaInfoDTO, error) {
	if s.endpoint == nil {
		return SchemaInfoDTO{}, unavailable()
	}
	endpoint, err := s.endpoint(ctx, id)
	if err != nil {
		return SchemaInfoDTO{}, err
	}
	stored, err := s.requestSchemaFile(ctx, id)
	if err != nil {
		return SchemaInfoDTO{}, err
	}
	if stored != "" {
		return s.fileInfo(endpoint, stored, reload), nil
	}
	return toSchemaInfoDTO(endpoint, s.cached(endpoint)), nil
}

// FetchSchema introspects the endpoint of GraphQL request in.RequestID and
// returns the operation's ID at once. The request's headers, auth,
// settings, cookies and environment apply; the query is sent with POST
// whatever the request's mode. The full introspection query is sent
// first; when the server answers it with errors and no schema, the
// conservative query is sent instead. The schema replaces the cached one
// and arrives as the one graphql:op:data item (SchemaInfoDTO); then
// graphql:op:done. A response that is not a schema fails the operation
// (graphql.schema.* codes, CodeHTTPStatus, CodeTooLarge) and keeps the
// cached schema.
//
// It fails at once when the request cannot be built, like HTTPService.Send.
func (s *GraphQLService) FetchSchema(ctx context.Context, in SchemaInputDTO) (operation.StartedDTO, error) {
	if err := interrupted(ctx, "Fetching the schema"); err != nil {
		return operation.StartedDTO{}, err
	}
	if s.newPoster == nil || s.ops == nil {
		return operation.StartedDTO{}, unavailable()
	}
	endpoint, poster, err := s.newPoster(ctx, in.RequestID)
	if err != nil {
		return operation.StartedDTO{}, err
	}
	f := &fetch{s: s, endpoint: endpoint, poster: poster}
	started, err := operation.Start(s.ops, operation.Spec{Domain: Domain, Kind: "introspect", MaxItems: 1}, f.run)
	if err != nil {
		return operation.StartedDTO{}, errs.Wrap(err, errs.Internal, "The schema fetch could not be started.")
	}
	s.logger.Debug("schema fetch started", "opId", started.OpID, "requestId", in.RequestID)
	return started, nil
}

// cached returns the schema of a SchemaInfoDTO.Key: for an endpoint, the
// schema cached in the open workspace, from memory or else from disk; for
// a schema file, the file's schema (schemafile.go). Nil when there is
// none.
func (s *GraphQLService) cached(endpoint string) *schemacache.Entry {
	if endpoint == "" {
		return nil
	}
	if strings.HasPrefix(endpoint, fileKeyPrefix) {
		return s.cachedFile(endpoint)
	}
	dir := s.dir()
	key := memoryKey{dir, endpoint}
	s.mu.Lock()
	e, ok := s.memory[key]
	s.mu.Unlock()
	if ok {
		return e
	}
	e = schemacache.Store{Dir: dir}.Load(endpoint)
	s.remember(key, e)
	return e
}

func (s *GraphQLService) remember(key memoryKey, e *schemacache.Entry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.memory) >= maxMemory {
		clear(s.memory)
	}
	s.memory[key] = e
}

// dir is the open workspace's local directory, "" when none is open.
func (s *GraphQLService) dir() string {
	if s.localDir == nil {
		return ""
	}
	dir, err := s.localDir()
	if err != nil {
		return ""
	}
	return dir
}

// Stages of a fetch, the progress messages.
const (
	StageFull         = "Sending the introspection query"
	StageConservative = "Retrying with the conservative introspection query"
)

// fetch is one running schema fetch.
type fetch struct {
	s        *GraphQLService
	endpoint string
	poster   Poster
}

func (f *fetch) run(ctx context.Context, emit *operation.Emitter[SchemaInfoDTO]) error {
	_ = emit.Progress(0, 0, StageFull)
	query := schemacache.QueryFull
	sch, problems, err := f.introspect(ctx, schema.FullIntrospectionQuery)
	if e, ok := err.(*errs.Error); ok && e.Code == schema.CodeServerErrors {
		_ = emit.Progress(0, 0, StageConservative)
		query = schemacache.QueryConservative
		sch, problems, err = f.introspect(ctx, schema.ConservativeIntrospectionQuery)
	}
	if err != nil {
		return err
	}
	e := &schemacache.Entry{Endpoint: f.endpoint, FetchedAt: f.s.now(), Query: query, Schema: sch, Problems: problems}
	change := schemaChange(f.s.cached(f.endpoint), e)
	dir := f.s.dir()
	if dir != "" {
		if err := (schemacache.Store{Dir: dir}).Save(e); err != nil {
			// The schema is still used for this session.
			f.s.logger.Warn("saving the schema failed", "err", err)
		}
	}
	f.s.remember(memoryKey{dir, f.endpoint}, e)
	info := toSchemaInfoDTO(f.endpoint, e)
	info.Change = change
	return emit.Send(info)
}

// Values of SchemaInfoDTO.Change: how a fetch's schema compares with the
// one cached before it (GQL-19).
const (
	ChangeNew       = "new"
	ChangeChanged   = "changed"
	ChangeUnchanged = "unchanged"
)

// schemaChange compares a fetched schema with the one cached before,
// as introspection JSON: the same JSON is the same schema.
func schemaChange(before, after *schemacache.Entry) string {
	if before == nil || before.Schema == nil {
		return ChangeNew
	}
	a, err1 := before.Schema.MarshalIntrospection()
	b, err2 := after.Schema.MarshalIntrospection()
	if err1 != nil || err2 != nil || !bytes.Equal(a, b) {
		return ChangeChanged
	}
	return ChangeUnchanged
}

// introspect sends query and parses the response as a schema.
func (f *fetch) introspect(ctx context.Context, query string) (*schema.Schema, int, error) {
	resp, err := f.poster.Post(ctx, query, schema.IntrospectionOperationName)
	if err != nil {
		return nil, 0, err
	}
	if resp.BodyTruncated {
		return nil, 0, errs.New(errs.Protocol, "The introspection response is larger than the response size limit.").
			WithCode(CodeTooLarge)
	}
	sch, problems, err := schemacache.Parse(resp.Body)
	if err != nil && resp.StatusCode >= 300 {
		e, ok := err.(*errs.Error)
		if !ok || e.Code != schema.CodeServerErrors {
			return nil, 0, errs.New(errs.Protocol, "The endpoint answered "+resp.Status+" instead of a GraphQL response.").
				WithCode(CodeHTTPStatus).
				WithHint("Check the endpoint URL and the request's headers and auth.").
				WithDetail("status", strconv.Itoa(resp.StatusCode))
		}
	}
	return sch, problems, err
}

func unavailable() error {
	return errs.New(errs.Unsupported, "GraphQL schemas are not available.").WithCode(CodeUnavailable)
}

// interrupted returns a cancelled or timeout error when ctx is done.
func interrupted(ctx context.Context, what string) error {
	if err := ctx.Err(); err != nil {
		return errs.Wrap(err, errs.KindOf(err), what+" was interrupted.")
	}
	return nil
}
