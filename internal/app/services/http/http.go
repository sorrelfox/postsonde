// Package http is the bound service that sends HTTP requests (HTTP-20,
// HTTP-21), and GraphQL requests over HTTP (GQL-02): Send builds the request from its saved file (the edits not
// written yet included), with the active environment's variables, the
// resolved auth and the project's cookie jar, and sends it with
// internal/core/httpclient as an operation (docs/events.md). The UI
// cancels it with OpsService.Cancel. See docs/http-client.md.
package http

import (
	"context"
	"crypto/x509"
	"io"
	"log/slog"
	"net/http/httptrace"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/app/services/collection"
	"github.com/sorrelfox/postsonde/internal/core/cookies"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/fileaccess"
	"github.com/sorrelfox/postsonde/internal/core/graphql/incremental"
	"github.com/sorrelfox/postsonde/internal/core/history"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netrc"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/oauth2"
	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/runtimevars"
	coresettings "github.com/sorrelfox/postsonde/internal/core/settings"
	"github.com/sorrelfox/postsonde/internal/core/snippet"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

// Domain names the HTTP service and its operations.
const Domain = "http"

// CodeUnavailable: the service was built without what sending needs.
const CodeUnavailable = "http.unavailable"

// CodeSSERequest: Send was asked to send an SSE request, which is
// connected to instead (kind invalid_argument). Details: requestId.
const CodeSSERequest = "http.sse_request"

// Loader returns what sending request id needs from the open project
// (collection.LoadHTTPSource).
type Loader func(ctx context.Context, id string) (collection.HTTPSource, error)

// CookieJars gives the cookie jar of the open project: the app's shared
// *cookies.Jars of internal/app/services/cookies, so the cookie manager
// and every send use one jar (docs/cookies.md).
type CookieJars interface {
	Jar() (*cookies.Jar, error)
}

// HTTPService sends HTTP requests for the UI.
type HTTPService struct {
	load           Loader
	authLoad       Loader // the auth of a request of any kind (WithAuthLoader)
	loadVariables  VariablesLoader
	defaultTimeout func() time.Duration
	ops            *operation.Registry
	dialer         netx.Dialer
	jars           CookieJars
	logger         *slog.Logger
	// bodies keeps the latest bodies for SaveBody and pickSave shows the
	// save dialog (WithSaveBody); nil without.
	bodies   *bodyStore
	pickSave SavePicker
	// verifyTLS is the app's network.verifyTls, read on every send
	// (WithVerifyTLS); nil verifies.
	verifyTLS func() bool
	// requireTLS refuses every send, stream or subscription that turns
	// certificate verification off (WithRequireTLSVerification, SET-02).
	requireTLS bool
	// roots gives the CA pool to verify servers against (WithTrust); nil
	// uses the system's.
	roots RootsFunc
	// clientCerts picks the client certificate of each https connection
	// (WithClientCerts); nil sends none.
	clientCerts httpclient.ClientCertFunc
	// filters keeps the last body parsed by FilterBody.
	filters filterCache
	// history records every send (WithHistory); nil records nothing.
	history HistoryTarget
	// maxBody gives the response body limit of the settings, read on
	// every send (WithMaxBodySize); nil keeps httpclient's default.
	maxBody func() int64
	// version is the app's version, for exported files (WithVersion).
	version string
	// exchangeSecrets masks saved exchanges again (WithExchangeSecrets).
	exchangeSecrets Secrets
	// netrc is where a netrc auth finds its file (WithNetrc); the zero
	// value is the real environment and file system.
	netrc netrc.Env
	// allowedDirs are the directories project-named files ({{$file}}, a
	// JWT key or netrc file, a schema file) may be read from besides the
	// project's (WithAllowedDirs, files.go); nil for none.
	allowedDirs func() []string
	// tokens keeps the tokens of oauth2 auths (WithOAuth2Store; an
	// oauth2.MemoryStore by default, oauth2.go); now is the clock of their
	// expiry, nil for time.Now.
	tokens oauth2.Store
	now    func() time.Time
	// oauth is the rest of what oauth2 auths need (oauth2.go).
	oauth oauth2State
	// runtime holds the runtime variables of the open project
	// (WithRuntimeVariables, runtime.go); nil for none.
	runtime *runtimevars.Registry
	// lastResponses keeps the last response of each request sent, for
	// {{$response}} references (DYN-06, responseref.go); responseHistory
	// opens the history they read after a restart (WithResponseHistory).
	lastResponses   lastResponses
	responseHistory ResponseHistory

	mu      sync.Mutex
	clients map[clientKey]*httpclient.Client
}

// RootsFunc returns the certificate authorities trusted for TLS: nil for
// the system's, or the system's plus the custom CA bundles (NET-04,
// tlsconf.Trust.Roots). An error means a configured bundle cannot be used.
type RootsFunc func() (*x509.CertPool, error)

// clientKey tells the cached clients apart: verification off, or the
// pool they verify against (nil for the system's).
type clientKey struct {
	insecure bool
	roots    *x509.CertPool
}

// New returns an HTTPService that loads requests with load, reads the
// global default timeout (network.defaultTimeoutMs) from defaultTimeout on
// every send, runs sends as operations in ops, connects through dialer and
// takes cookies from jars. A nil load, ops or dialer makes Send fail; nil
// defaultTimeout means no default; nil jars sends no jar cookies. A nil
// logger means slog.Default(). Without WithSaveBody, SaveBody fails.
func New(load Loader, defaultTimeout func() time.Duration, ops *operation.Registry, dialer netx.Dialer, jars CookieJars, logger *slog.Logger, opts ...Option) *HTTPService {
	if logger == nil {
		logger = slog.Default()
	}
	s := &HTTPService{
		load:           load,
		defaultTimeout: defaultTimeout,
		ops:            ops,
		dialer:         dialer,
		jars:           jars,
		logger:         logger.With("service", Domain),
		clients:        map[clientKey]*httpclient.Client{},
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// ServiceName names the service in Wails logs.
func (s *HTTPService) ServiceName() string { return Domain }

// ServiceShutdown closes the idle connections and drops the kept bodies,
// removing their spool files. Running sends are operations, which the
// shared registry cancels at shutdown.
func (s *HTTPService) ServiceShutdown() error {
	if s.bodies != nil {
		s.bodies.close()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.clients {
		c.CloseIdleConnections()
	}
	return nil
}

// Send starts sending request in.RequestID and returns its operation ID at
// once. Progress arrives as http:op:progress (the stage: connecting,
// sending, waiting, receiving), the response as one http:op:data item
// (ResponseDTO; a GraphQL request answered with incremental delivery,
// GQL-08, sends partial responses with Incremental.Receiving set before
// it), then http:op:done: "ok" with a response of any status,
// "error" with the send's error (a timeout is httpclient.timeout with the
// timeout in its message and details), "cancelled" after
// OpsService.Cancel.
//
// It fails at once, without sending anything, when the request cannot be
// built: an unknown ID, an undefined {{variable}}, a missing path
// parameter, an invalid header, a body file that does not exist, and the
// other errors of request.BuildHTTP. Undefined variables of all fields are
// reported together as one template.unresolved error (details.names), so
// the UI can ask whether to send anyway (ENV-11); with in.AllowUnresolved
// they are sent as written instead. Other template problems (a cycle, an
// empty name) always refuse.
//
// {{$prompt LABEL}} references (DYN-07) take their values from in.Prompts;
// the labels without one are reported together first, as one
// template.prompt error whose details.prompts (JSON: label, default,
// secret) says what the UI asks for before sending again with the answers.
// A secret prompt's value is masked in the timeline and the history.
func (s *HTTPService) Send(ctx context.Context, in SendInputDTO) (operation.StartedDTO, error) {
	if err := ctx.Err(); err != nil {
		return operation.StartedDTO{}, errs.Wrap(err, errs.KindOf(err), "Sending the request was interrupted.")
	}
	if s.ops == nil {
		return operation.StartedDTO{}, errs.New(errs.Unsupported, "Sending HTTP requests is not available.").WithCode(CodeUnavailable)
	}
	o := buildOptions{allowUnresolved: in.AllowUnresolved, prompts: in.Prompts}
	snd, run, err := s.prepareSend(ctx, in.RequestID, o, s.historyRecord())
	if err != nil {
		return operation.StartedDTO{}, err
	}
	started, err := operation.Start(s.ops, operation.Spec{Domain: Domain, Kind: "send", MaxItems: 1}, run)
	if err != nil {
		return operation.StartedDTO{}, errs.Wrap(err, errs.Internal, "The request could not be started.")
	}
	s.logger.Debug("http request started", "opId", started.OpID, "requestId", in.RequestID, "method", snd.build.Request.Method)
	return started, nil
}

// prepareSend prepares request id for Send: built at once, so that what
// stops it is reported before the operation starts, and the operation's
// function. When a {{$response}} reference needs its request sent first
// (DYN-06), that build is only a plan: the operation sends those
// requests, reporting StageSendingFirst, and builds the request again
// with their responses, failing with the build's error.
func (s *HTTPService) prepareSend(ctx context.Context, id string, o buildOptions, record func(history.Entry)) (*send, func(context.Context, *operation.Emitter[ResponseDTO]) error, error) {
	plan := &responseFlow{mode: responsesPlan}
	o.responses = plan
	snd, err := s.prepare(ctx, id, o, record)
	if !plan.needsSending() {
		if err != nil {
			return nil, nil, err
		}
		return snd, snd.run, nil
	}
	// Values to ask for and undefined names are reported now, as the UI
	// asks about them; other errors may come from a stand-in value and
	// are left to the build after the requests were sent.
	if code := codeOf(err); code == template.CodePrompt || code == template.CodeUnresolved || code == CodeSSERequest {
		return nil, nil, err
	}
	if snd == nil {
		snd = &send{}
	}
	run := func(ctx context.Context, emit *operation.Emitter[ResponseDTO]) error {
		o.responses = &responseFlow{mode: responsesSend, stage: func(msg string) { _ = emit.Progress(0, 0, msg) }}
		real, err := s.prepare(ctx, id, o, record)
		if err != nil {
			return err
		}
		return real.run(ctx, emit)
	}
	return snd, run, nil
}

// prepare loads and builds request id for one send, as Send does, with
// record as its history recorder (nil records nothing). It fails as Send
// does, and for an SSE request.
func (s *HTTPService) prepare(ctx context.Context, id string, o buildOptions, record func(history.Entry)) (*send, error) {
	src, x, b, err := s.build(ctx, id, o)
	if err != nil {
		return nil, err
	}
	if src.SSE {
		return nil, errs.New(errs.InvalidArgument, "An SSE request is connected to, not sent.").
			WithCode(CodeSSERequest).
			WithHint("Connect to the event stream from the request's editor.").
			WithDetail("requestId", id)
	}

	client, err := s.client(b.InsecureSkipVerify, b.Request.URL)
	if err != nil {
		return nil, err
	}
	var rec *jarRecorder
	if s.jars != nil {
		if jar, err := s.jars.Jar(); err != nil {
			s.logger.Warn("http request sent without cookies", "err", err)
		} else if jar != nil {
			// Recorded per send, so the Cookies tab can say what the jar
			// did with each Set-Cookie value (HTTP-32).
			rec = &jarRecorder{jar: jar}
			client = client.WithCookieJar(rec)
		}
	}
	// The timeline shows the request as sent, with the secrets it carries
	// masked (DBG-01, docs/privacy.md).
	secrets := secretsOf(x, b)
	masker := httpclient.NewSecretMasker(secrets)
	// The assertions are rendered now, with the variables the request was
	// built with; their secrets are masked in the results (TEST-01).
	checks, checkSecrets := prepareChecks(x.resolver, assertionsOf(src))
	// So are the expressions of the extraction rules (TEST-05).
	extractions, extractSecrets := prepareExtractions(x.resolver, extractionsOf(src))
	checkSecrets = append(checkSecrets, extractSecrets...)
	snd := &send{svc: s, client: client, build: b, jar: rec, bodies: s.bodies, maxBody: s.maxBodyBytes(), masker: masker, secrets: secrets,
		checks: checks, checkSecrets: checkSecrets, checkMasker: httpclient.NewSecretMasker(append(slices.Clone(secrets), checkSecrets...)), schemaFiles: s.filePolicy(src),
		extractions: extractions, runtimeStore: func() (*runtimevars.Store, error) { return s.runtimeVariables(src.Workspace) },
		record: record, meta: sendMeta{kind: history.KindHTTP, requestID: id, requestName: src.Request.Name}, root: workspaceRoot(src)}
	if setter, ok := o.runVars.(runVarSetter); ok {
		snd.runVars = setter
	}
	// What the history keeps to restore the request (NAV-03). Runs set
	// the recorder after prepare, so this is there for them too.
	snd.meta.environment = environmentName(x.resolver.Scopes)
	if src.GraphQL != nil {
		snd.meta.template = history.GraphQLTemplate(*src.GraphQL)
	} else {
		snd.meta.template = history.HTTPTemplate(src.Request)
	}
	if src.GraphQL != nil {
		snd.meta.kind, snd.meta.requestName = history.KindGraphQL, src.GraphQL.Name
	}
	return snd, nil
}

// workspaceRoot is the root of the project src belongs to, or "".
func workspaceRoot(src collection.HTTPSource) string {
	if src.Workspace != nil {
		return src.Workspace.Root
	}
	return ""
}

// buildOptions says how build treats the references a request's scopes
// cannot resolve alone.
type buildOptions struct {
	// allowUnresolved leaves undefined {{variables}} as written ("Send
	// anyway", ENV-11).
	allowUnresolved bool
	// prompts answer the {{$prompt}} references, by label (DYN-07).
	prompts map[string]string
	// preview is for what sends nothing (a curl command, the header
	// preview, an export): unanswered prompts stay as written, listed with
	// the undefined names, instead of failing the build.
	preview bool
	// deferNetrc leaves a netrc auth's file unread (AUTH-19): for the
	// header preview, which is computed after every edit, not asked for.
	deferNetrc bool
	// deferFiles leaves {{$file}} references unread and as written
	// (DYN-08), for the same reason.
	deferFiles bool
	// runVars are the variables of a folder run (runner.Vars), above
	// every other scope; nil outside a run.
	runVars template.Scope
	// templated keeps every {{reference}} as written and builds with
	// snippet.TemplateAuth: the templated snippet (GEN-12, snippet.go).
	templated bool
	// record keeps what each render substituted (expansion.results), for
	// the resolution view (DBG-08, resolution.go).
	record bool
	// responses says what {{$response}} references may do (DYN-06,
	// responseref.go); nil reads the stored responses only.
	// responseChain and responseNames are the requests (IDs and names)
	// being built for which this one is sent first, outermost first.
	responses                    *responseFlow
	responseChain, responseNames []string
}

// previewBuild builds a request for a preview: undefined variables and
// prompts stay as written.
var previewBuild = buildOptions{allowUnresolved: true, preview: true}

// headerPreviewBuild is previewBuild without reading a netrc file or the
// files of {{$file}} references.
var headerPreviewBuild = buildOptions{allowUnresolved: true, preview: true, deferNetrc: true, deferFiles: true}

// build loads request id (an HTTP, GraphQL or SSE request) and builds the
// request it sends, with its {{variables}} rendered by the returned
// expansion. It fails as Send does.
func (s *HTTPService) build(ctx context.Context, id string, o buildOptions) (collection.HTTPSource, *expansion, request.HTTPBuild, error) {
	if s.load == nil || s.dialer == nil {
		return collection.HTTPSource{}, nil, request.HTTPBuild{}, errs.New(errs.Unsupported, "Sending HTTP requests is not available.").WithCode(CodeUnavailable)
	}
	src, err := s.load(ctx, id)
	if err != nil {
		return collection.HTTPSource{}, nil, request.HTTPBuild{}, err
	}
	chain, err := s.chainOf(src)
	if err != nil {
		return collection.HTTPSource{}, nil, request.HTTPBuild{}, err
	}
	if o.runVars != nil {
		chain = append(chain, o.runVars)
	}
	x := &expansion{resolver: template.Resolver{Scopes: chain, Prompts: o.prompts}, allowUnresolved: o.allowUnresolved, askPrompts: !o.preview, record: o.record}
	if !o.deferFiles {
		x.resolver.Files = s.fileReader(src)
	}
	x.resolver.Responses = s.responseReaderFor(ctx, src, id, o)
	var def time.Duration
	if s.defaultTimeout != nil {
		def = s.defaultTimeout()
	}
	opts := request.HTTPOptions{Expand: x.expand, Root: workspaceRoot(src), AllowedDirs: s.allowed(), DefaultTimeout: def, DefaultInsecure: s.defaultInsecure(), FolderHeaders: src.FolderHeaders, Netrc: s.netrcOptions(o.deferNetrc)}
	auth := src.Auth
	if o.templated {
		opts.Expand = snippet.KeepReferences
		auth, _ = snippet.TemplateAuth(auth)
	}
	var b request.HTTPBuild
	if src.GraphQL != nil {
		// GraphQL over HTTP (GQL-02): POST with a JSON body, or GET. An
		// operation set to run over SSE (GQL-32, GQL-35) asks for an event
		// stream, which the response reads like an incremental one.
		var transport string
		transport, _, err = request.GraphQLTransportOf(*src.GraphQL)
		switch {
		case err != nil:
		case transport == projectfile.GraphQLTransportSSE:
			b, err = request.BuildGraphQLSSE(*src.GraphQL, auth, opts)
		default:
			b, err = request.BuildGraphQL(*src.GraphQL, auth, opts)
		}
	} else {
		b, err = request.BuildHTTP(src.Request, auth, opts)
	}
	// Undefined variables of every field are reported together, before
	// any other error: a build error may only follow from the braces left
	// in the text (an invalid header name, JSON that no longer parses).
	// Values to ask for come before them: the answers may be all it takes.
	if perr := x.promptErr(); perr != nil {
		return collection.HTTPSource{}, nil, request.HTTPBuild{}, perr
	}
	if uerr := x.unresolvedErr(); uerr != nil {
		return collection.HTTPSource{}, nil, request.HTTPBuild{}, uerr
	}
	if err != nil {
		return collection.HTTPSource{}, nil, request.HTTPBuild{}, err
	}
	if o.preview {
		// A copy or preview never asks the token endpoint (oauth2.go).
		s.previewOAuth2(&b)
	}
	return src, x, b, nil
}

// WithVerifyTLS gives the app's TLS verification default (network.verifyTls,
// HTTP-23), read on every send; a request's own settings.verifyTls
// overrides it. Without it, requests without their own setting verify.
func WithVerifyTLS(verify func() bool) Option {
	return func(s *HTTPService) { s.verifyTLS = verify }
}

// WithRequireTLSVerification makes every send, SSE stream, schema fetch
// and GraphQL subscription that turns certificate verification off (its
// own settings.verifyTls false) fail before it connects, with
// settings.tls_verification_required: the admin policy's
// requireTlsVerification (SET-02).
func WithRequireTLSVerification(require bool) Option {
	return func(s *HTTPService) { s.requireTLS = require }
}

// checkTLSPolicy returns the error for an insecure connection the admin
// policy forbids, or nil.
func (s *HTTPService) checkTLSPolicy(insecure bool, protocol string) error {
	if insecure && s.requireTLS {
		return coresettings.TLSVerificationRequiredError(protocol)
	}
	return nil
}

// defaultInsecure reports whether the app setting turns TLS verification
// off for requests without their own setting (WithVerifyTLS).
func (s *HTTPService) defaultInsecure() bool {
	return s.verifyTLS != nil && !s.verifyTLS()
}

// WithTrust gives the CA pool every verified connection uses (NET-04),
// read on every send: the custom CA bundles of the settings on top of the
// system roots. Without it, the system roots only.
func WithTrust(roots RootsFunc) Option {
	return func(s *HTTPService) { s.roots = roots }
}

// WithClientCerts gives the per-host client certificates (AUTH-30): every
// https hop of a send, a GraphQL request or subscription, an SSE stream
// or a schema fetch sends the certificate pick returns for its host and
// port. An error from pick fails the send before it connects.
func WithClientCerts(pick httpclient.ClientCertFunc) Option {
	return func(s *HTTPService) { s.clientCerts = pick }
}

// client returns the client for the TLS setting and the trusted CAs, built
// on first use, so connections are reused across sends. When the CA
// bundles change, the clients of the old pool are dropped (their idle
// connections closed).
//
// A CA bundle that cannot be used fails an https URL; a plain http one is
// sent with the system roots (a redirect to https then trusts only them).
func (s *HTTPService) client(insecure bool, url string) (*httpclient.Client, error) {
	if err := s.checkTLSPolicy(insecure, "HTTP"); err != nil {
		return nil, err
	}
	var roots *x509.CertPool
	if !insecure && s.roots != nil {
		pool, err := s.roots()
		switch {
		case err == nil:
			roots = pool
		case isHTTPS(url):
			return nil, err
		default:
			s.logger.Warn("custom CA certificates not used", "err", err)
		}
	}
	key := clientKey{insecure: insecure, roots: roots}
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.clients[key]; ok {
		return c, nil
	}
	for k, c := range s.clients {
		if k.insecure == insecure {
			c.CloseIdleConnections()
			delete(s.clients, k)
		}
	}
	c := httpclient.New(s.dialer, httpclient.TransportOptions{InsecureSkipVerify: insecure, RootCAs: roots, ClientCerts: s.clientCerts})
	s.clients[key] = c
	return c, nil
}

// isHTTPS reports whether url uses TLS.
func isHTTPS(url string) bool {
	return len(url) >= 6 && strings.EqualFold(url[:6], "https:")
}

// Stages of a send, the progress messages.
const (
	StageConnecting = "Connecting"
	StageSending    = "Sending the request"
	StageWaiting    = "Waiting for the response"
	StageReceiving  = "Receiving the response"
)

// send is one running send.
type send struct {
	// svc gets an oauth2 auth's token (authorizeOAuth2); nil in tests
	// that build a send by hand.
	svc    *HTTPService
	client *httpclient.Client
	build  request.HTTPBuild
	// jar is the send's cookie jar, nil for none.
	jar *jarRecorder
	// bodies keeps the body for SaveBody, nil for none.
	bodies *bodyStore
	// maxBody is the body limit of the app settings (WithMaxBodySize), 0
	// for the request's own (httpclient.DefaultMaxBodyBytes).
	maxBody int64
	// masker hides the secrets of the request in the timeline and the
	// history; nil masks the Authorization fields only.
	masker *httpclient.SecretMasker
	// secrets are the values masker hides, which the history masks too.
	secrets []string
	// checks are the request's enabled assertions, evaluated against the
	// response (TEST-01); checkMasker hides the secrets in their results.
	checks       []checkItem
	checkSecrets []string
	checkMasker  *httpclient.SecretMasker
	// schemaFiles is what the schema files of matches_schema assertions
	// are read through: the project root, which relative paths are
	// resolved against, and the allowed directories (PRV-10).
	schemaFiles fileaccess.Policy
	// extractions are the request's enabled extraction rules (TEST-05),
	// applied to the response; runtimeStore opens the runtime variables
	// of the request's project, and runVars are the variables of the
	// folder run the send is part of (nil outside a run).
	extractions  []extractItem
	runtimeStore func() (*runtimevars.Store, error)
	runVars      runVarSetter
	// record writes the send to the request history (NAV-02), nil for
	// none; meta is what it says about the request.
	record func(history.Entry)
	meta   sendMeta
	// root is the project's folder, which the response is kept under for
	// {{$response}} references.
	root string
}

// run is the operation: report the stages, send, and send the response as
// the one data item. Its error is the send's.
func (x *send) run(ctx context.Context, emit *operation.Emitter[ResponseDTO]) error {
	// An incremental GraphQL response (GQL-08) is shown while it arrives:
	// partial responses first, the final one last.
	partial := func(dto ResponseDTO) { _ = emit.Send(dto) }
	ex, err := x.exchange(ctx, func(done, total int64, msg string) { _ = emit.Progress(done, total, msg) }, partial)
	if err != nil {
		return err
	}
	if x.bodies != nil {
		// Kept for SaveBody, FilterBody and ReadBody (HTTP-34, HTTP-36,
		// HTTP-37); the DTO only has the decoded, clipped text.
		ex.dto.BodyID = x.bodies.keep(ex.body, ex.dto.FileName)
		// With the rest of the exchange, for SaveExchange (EXP-06).
		x.keepExchange(ex.dto.BodyID, ex)
	}
	if x.record != nil {
		// Named now, so the view can save it as an example (HTTP-38).
		ex.dto.HistoryID = history.NewID()
	}
	err = emit.Send(ex.dto)
	// Recorded after the response went out, masked like the timeline
	// (docs/history.md).
	x.recordResponse(ex.started, ex.resp, ex.dto)
	return err
}

// exchanged is a send that got a response: its DTO, without BodyID, and
// the body's spool, closed, which the caller keeps or aborts.
type exchanged struct {
	dto     ResponseDTO
	resp    *httpclient.Response
	body    *spool
	started time.Time
}

// exchange sends the request and turns the response into its DTO, with
// the assertions checked, reporting the stages and the bytes received
// through progress. A send that fails (cancelled, timed out) is recorded
// in the history here and its error returned; one that got a response is
// recorded by the caller (recordResponse).
//
// A GraphQL request answered with incremental delivery (GQL-08) gets the
// payloads and their merged result in the DTO's Incremental; while the
// body arrives, partial (when not nil) gets a partial response after new
// payloads, at most once per PartialInterval, never after exchange
// returned.
func (x *send) exchange(ctx context.Context, progress func(done, total int64, msg string), partial func(ResponseDTO)) (exchanged, error) {
	stage := func(msg string) { progress(0, 0, msg) }
	// An oauth2 auth's token first (AUTH-08): stored, or got from the
	// token endpoint now, as part of this operation.
	token, err := x.authorize(ctx, stage)
	if err != nil {
		x.recordFailure(ctx, time.Now(), err)
		return exchanged{}, err
	}
	stage(StageConnecting)
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		GotConn:              func(httptrace.GotConnInfo) { stage(StageSending) },
		WroteRequest:         func(httptrace.WroteRequestInfo) { stage(StageWaiting) },
		GotFirstResponseByte: func() { stage(StageReceiving) },
	})
	var masker httpclient.Masker = &httpclient.SecretMasker{}
	if x.masker != nil {
		masker = x.masker
	}
	req := x.build.Request
	if x.maxBody > 0 {
		req.Settings.MaxBodyBytes = x.maxBody
	}
	limit := req.Settings.MaxBodyBytes
	if limit == 0 {
		limit = httpclient.DefaultMaxBodyBytes
	}
	// The body goes to the spool while it arrives (HTTP-37): in memory
	// while small, then to a file, with the bytes received as progress.
	sp := x.bodies.newSpool()
	var total int64
	var collector *incremental.Collector
	var partials *throttle
	started := time.Now()
	target := httpclient.BodyTarget{
		Open: func(head *httpclient.Response) (io.Writer, error) {
			total = max(head.ContentLength, 0)
			progress(0, total, StageReceiving)
			if x.meta.kind != history.KindGraphQL {
				return sp, nil
			}
			var poke func()
			if partial != nil {
				partials = &throttle{interval: PartialInterval}
				poke = partials.poke
			}
			collector = incremental.NewCollector(contentType(head.Headers), started, poke)
			if collector == nil {
				return sp, nil
			}
			if partials != nil {
				partials.fn = func() {
					inc := toIncrementalDTO(collector.Snapshot(), true)
					partial(partialResponseDTO(x.build, head, time.Since(started), collector.Written(), inc, masker))
				}
			}
			return io.MultiWriter(sp, collector), nil
		},
		Decode:   true,
		Progress: func(n int64) { progress(n, total, StageReceiving) },
	}
	resp, err := x.client.SendTo(ctx, req, target)
	if partials != nil {
		// No partial response after the final one (or the error).
		partials.stop()
	}
	if err != nil {
		// Cancelled, timed out or failed while receiving: the part
		// received is dropped with its file.
		sp.abort()
		// A transport error can repeat the URL, secrets and all ("Get
		// \"https://…?key=…\": EOF"); it reaches the UI, the log and the
		// console, so it is masked like the timeline (docs/privacy.md).
		err = errs.Redact(timeoutError(err, x.build), x.masker.MaskText)
		x.recordFailure(ctx, started, err)
		return exchanged{}, err
	}
	sp.close()
	// The view, the history and the small bodies' checks read the head.
	resp.Body = sp.headBytes()
	// The extraction rules set their variables now (TEST-05), before the
	// DTO is made: a secret value set is masked from here on, in the
	// timeline, the results and the history, like a secret variable's.
	extractions, extracted := x.runExtractions(ctx, resp, sp)
	if len(extracted) > 0 {
		x.secrets = append(x.secrets, extracted...)
		x.checkSecrets = append(x.checkSecrets, extracted...)
		x.masker = httpclient.NewSecretMasker(x.secrets)
		x.checkMasker = httpclient.NewSecretMasker(append(slices.Clone(x.secrets), x.checkSecrets...))
		masker = x.masker
	}
	// Kept for {{$response}} references to it (DYN-06).
	x.rememberResponse(resp, sp)
	dto := toResponseDTO(x.build, resp, x.jar, time.Now(), masker)
	dto.Extractions = extractions
	if token != nil {
		now := time.Now()
		if x.svc != nil {
			now = x.svc.clock()
		}
		offset := time.Duration(0)
		if first, ok := token.started(); ok {
			offset = started.Sub(first)
		}
		dto.Timeline = withTokenExchange(dto.Timeline, token, masker, offset, now)
		if x.svc != nil && x.svc.droppedOnUnauthorized(token, resp.StatusCode) {
			dto.Timeline.Events = append(dto.Timeline.Events, TimelineEventDTO{AtMs: ms(resp.Duration), Name: EventOAuth2TokenDropped,
				Detail: "the server refused the stored token; the next send gets a new one"})
		}
	}
	dto.BodyLimit = limit
	if collector != nil {
		collector.Close()
		dto.Incremental = toIncrementalDTO(collector.Snapshot(), false)
	}
	if resp.BodyTruncated {
		dto.Warnings = append(dto.Warnings, WarningDTO{Code: CodeBodyLimit, Message: bodyLimitMessage(limit)})
	}
	// Checked against the whole body; a failed assertion does not fail
	// the send (TEST-06).
	var checkMasker httpclient.Masker = masker
	if x.checkMasker != nil {
		checkMasker = x.checkMasker
	}
	dto.Tests = runChecks(ctx, x.checks, resp, sp, checkMasker, schemaLoader(x.schemaFiles))
	return exchanged{dto: dto, resp: resp, body: sp, started: started}, nil
}

// timeoutError adds the timeout that stopped the request, and where it
// was set, to httpclient's timeout error (HTTP-21); other errors are
// returned as they are.
func timeoutError(err error, b request.HTTPBuild) error {
	e, ok := err.(*errs.Error)
	if !ok || e.Code != httpclient.CodeTimeout || b.Request.Settings.Timeout <= 0 {
		return err
	}
	d := b.Request.Settings.Timeout
	c := *e
	switch b.TimeoutSource {
	case request.TimeoutFromRequest:
		c.Message = e.Message + " The request's timeout is " + request.FormatTimeout(d) + "."
		c.Hint = "Increase the timeout in the request's Settings tab, or check that the server is responding."
	default:
		c.Message = e.Message + " The default timeout is " + request.FormatTimeout(d) + "."
		c.Hint = "Increase the default timeout in Settings, set a timeout for this request in its Settings tab, or check that the server is responding."
	}
	return c.WithDetail("timeoutMs", strconv.FormatInt(d.Milliseconds(), 10)).
		WithDetail("timeoutSource", b.TimeoutSource)
}
