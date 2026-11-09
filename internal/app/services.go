package app

import (
	"context"
	"crypto/x509"
	"errors"
	"log/slog"
	"path/filepath"
	"runtime/debug"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/sorrelfox/postsonde/internal/app/apperr"
	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/app/services/collection"
	"github.com/sorrelfox/postsonde/internal/app/services/console"
	appcookies "github.com/sorrelfox/postsonde/internal/app/services/cookies"
	"github.com/sorrelfox/postsonde/internal/app/services/datadir"
	"github.com/sorrelfox/postsonde/internal/app/services/demo"
	"github.com/sorrelfox/postsonde/internal/app/services/environments"
	"github.com/sorrelfox/postsonde/internal/app/services/format"
	graphqlservice "github.com/sorrelfox/postsonde/internal/app/services/graphql"
	"github.com/sorrelfox/postsonde/internal/app/services/grpc"
	historyservice "github.com/sorrelfox/postsonde/internal/app/services/history"
	httpservice "github.com/sorrelfox/postsonde/internal/app/services/http"
	"github.com/sorrelfox/postsonde/internal/app/services/localdata"
	"github.com/sorrelfox/postsonde/internal/app/services/logexport"
	"github.com/sorrelfox/postsonde/internal/app/services/network"
	"github.com/sorrelfox/postsonde/internal/app/services/ops"
	"github.com/sorrelfox/postsonde/internal/app/services/proxyscopes"
	"github.com/sorrelfox/postsonde/internal/app/services/runner"
	"github.com/sorrelfox/postsonde/internal/app/services/runtimevars"
	"github.com/sorrelfox/postsonde/internal/app/services/secrets"
	"github.com/sorrelfox/postsonde/internal/app/services/servers"
	"github.com/sorrelfox/postsonde/internal/app/services/settings"
	socketioservice "github.com/sorrelfox/postsonde/internal/app/services/socketio"
	"github.com/sorrelfox/postsonde/internal/app/services/sse"
	"github.com/sorrelfox/postsonde/internal/app/services/system"
	"github.com/sorrelfox/postsonde/internal/app/services/tcp"
	"github.com/sorrelfox/postsonde/internal/app/services/themes"
	"github.com/sorrelfox/postsonde/internal/app/services/udp"
	"github.com/sorrelfox/postsonde/internal/app/services/websocket"
	"github.com/sorrelfox/postsonde/internal/app/services/window"
	"github.com/sorrelfox/postsonde/internal/app/services/workspace"
	"github.com/sorrelfox/postsonde/internal/app/streamcheck"
	"github.com/sorrelfox/postsonde/internal/core/appsecrets"
	"github.com/sorrelfox/postsonde/internal/core/cookies"
	coreenvironments "github.com/sorrelfox/postsonde/internal/core/environments"
	"github.com/sorrelfox/postsonde/internal/core/graphql/transportws"
	corehistory "github.com/sorrelfox/postsonde/internal/core/history"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	corelogexport "github.com/sorrelfox/postsonde/internal/core/logexport"
	"github.com/sorrelfox/postsonde/internal/core/netlog"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/oauth2/tokenstore"
	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/requestsecrets"
	corerunner "github.com/sorrelfox/postsonde/internal/core/runner"
	"github.com/sorrelfox/postsonde/internal/core/runner/runstore"
	coreruntimevars "github.com/sorrelfox/postsonde/internal/core/runtimevars"
	"github.com/sorrelfox/postsonde/internal/core/secretstore"
	"github.com/sorrelfox/postsonde/internal/core/servers/servertls"
	coresettings "github.com/sorrelfox/postsonde/internal/core/settings"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
	"github.com/sorrelfox/postsonde/internal/core/tlsconf"
	coreworkspace "github.com/sorrelfox/postsonde/internal/core/workspace"
)

// wailsModule is the module path of Wails v3, looked up in the build info.
const wailsModule = "github.com/wailsapp/wails/v3"

// services builds the Go services bound to the frontend: one per domain,
// each in internal/app/services/<domain>. Adding one here (and running
// `wails3 task bindings`) generates its TypeScript bindings; see
// docs/bindings.md.
//
// Register each service with application.NewServiceWithOptions and
// serviceOptions, and keep those calls in this function: the bindings
// generator finds services by statically following them from main.
func services(cfg Config) []application.Service {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	info, _ := debug.ReadBuildInfo()
	build := system.Build{
		AppName:      appName,
		Version:      cfg.Version,
		WailsVersion: moduleVersion(info, wailsModule),
	}

	// One registry for every long-running operation, so OpsService can
	// cancel any of them by ID (docs/events.md).
	operations := operation.NewRegistry(emitEvent, logger)

	// Reads settings.json now; problems are logged and reported to the UI
	// by SettingsService.Problems (docs/settings.md). The admin policy
	// (SET-02) locks settings: every reader below gets the locked values
	// through settingsStore.Settings(), and requireTLS refuses a request or
	// connection of any protocol that turns certificate verification off.
	settingsStore := coresettings.Open(cfg.SettingsPath, logger, coresettings.WithPolicy(cfg.Policy))
	requireTLS := cfg.Policy.RequireTLSVerification

	// The open workspace and its local side-file directory. Later services
	// that need either get this manager (docs/workspace.md).
	workspaces := coreworkspace.New(cfg.DataDir)

	// The app's windows (APP-06, windows.go): every one shows the open
	// project, and is titled with its name. Without an application (tests)
	// no window opens.
	windows := cfg.windows
	if windows == nil {
		windows = newWindowHost(nil)
	}
	windows.title = func() string {
		if w := workspaces.Current(); w != nil {
			return windowTitle(w.Name())
		}
		return windowTitle("")
	}

	// The master password of the local secrets (SEC-03): every
	// secret file under the data directory (the stores below, the
	// request, environment and runtime variable secrets) is read and
	// written through this vault, encrypted once the user turns it on
	// (docs/privacy.md#encrypted-secrets). Registered before anything
	// reads a secret. Every change of it drops the OAuth 2.0 tokens held
	// in memory, so none stays usable while the secrets are locked. With
	// the key store "keychain" the key is kept in the OS keychain
	// too, and the secrets unlock from it at startup.
	var oauth2Tokens *tokenstore.Store
	var vault *secretstore.Vault
	var secretsKeychain *secrets.Keychain
	if cfg.DataDir != "" {
		if cfg.Keychain != nil {
			secretsKeychain = secrets.NewKeychain(cfg.Keychain, settingsStore)
		}
		secretsChanged := secrets.Changed(emitEvent, settingsStore, secretsKeychain)
		vault = secretstore.Open(cfg.DataDir, secrets.VaultOptions(settingsStore, secretsKeychain, func(st secretstore.Status) {
			if oauth2Tokens != nil {
				oauth2Tokens.Forget()
			}
			secretsChanged(st)
		}))
		secretstore.Register(vault)
	}

	// App-wide secrets kept out of settings.json: the passphrases of the
	// client certificates and the manual proxy's password, in
	// <data>/secrets/app.json (docs/privacy.md).
	appSecrets := appsecrets.New(cfg.DataDir)
	// The OAuth 2.0 tokens oauth2 auths obtain (AUTH-13), kept in the
	// open project's local directory, never in the project
	// (docs/auth.md#token-store).
	oauth2Tokens = tokenstore.New(workspaces.LocalDir, tokenstore.WithOnError(func(err error) {
		logger.Warn("oauth2 token store", "err", err)
	}))
	// The connection log (PRV-06): every dial through the shared dialer,
	// refused ones included, for the network activity indicator.
	connLog := netlog.New(netlog.Options{})
	// The CAs every TLS client trusts: the system's plus the custom CA
	// bundles of the settings (NET-04), read per connection and cached
	// until the list or a file changes (docs/network.md).
	trust := tlsconf.NewTrust(func() []string { return settingsStore.Settings().Network.CABundles })
	// The dialer every protocol client connects through (docs/network.md):
	// directly, or through the proxy of the active environment, of the
	// open project, or of the settings or ALL_PROXY (SOCKS-04, SOCKS-08,
	// NET-02), with its bypass list (SOCKS-09), read on every dial. An
	// https:// proxy's certificate is verified with the same trust.
	// The OS's proxy settings (NET-01) for the "system" mode, read when
	// that mode needs them and kept until a settings change or the
	// refresh timer (below) reads them again.
	systemProxy := newSystemProxy(cfg.SystemProxy)
	resolveProxy := proxyResolver(func() coresettings.Network { return settingsStore.Settings().Network }, appSecrets, cfg.Getenv,
		proxyScopes(workspaces), systemProxy.config)
	dialer, proxyRouter := newRoutedDialer(logger, connLog, dialerConfig{
		Proxy:   proxyConfigOf(resolveProxy),
		RootCAs: trust.Roots,
	})
	// The per-host client certificates (AUTH-30): every TLS client picks
	// the one of the host and port it connects to, read per connection and
	// cached until the entry, its passphrase or a file changes
	// (docs/network.md#client-certificates).
	clientCerts := tlsconf.NewClientCerts(
		func() []tlsconf.ClientCertEntry { return settingsStore.Settings().Network.ClientCerts },
		func(id string) (string, error) { return appSecrets.Get(appsecrets.ClientCertPassphraseKey(id)) })
	pickClientCert := func(addr string) (*httpclient.ClientCert, error) {
		c, err := clientCerts.ForAddress(addr)
		if err != nil || c == nil {
			return nil, err
		}
		return &httpclient.ClientCert{Certificate: c.Certificate, Description: c.Describe()}, nil
	}

	// Raw TCP connections belong to the open project's requests, so they
	// are closed whenever the open workspace changes (docs/tcp.md). TLS
	// over TCP (TCP-02) uses the same trust and client certificates as
	// the other protocols.
	tcpService := tcp.New(dialer, emitEvent, logger, tcp.WithTrust(trust.Roots), tcp.WithClientCerts(pickClientCert),
		tcp.WithRequireTLSVerification(requireTLS))
	// The open project's cookie jars (its shared jar and, with separate
	// cookies per environment, one per environment; CK-03): one registry
	// and one Jars for every protocol and the cookie manager, so a cookie
	// edited in the manager is the one the next request sends, and a switch
	// of project or environment switches every kind of send
	// (docs/cookies.md).
	cookieRegistry := cookies.NewRegistry(cookies.Options{
		OnSaveError: func(err error) { logger.Warn("saving cookies failed", "err", err) },
	})
	cookieJars := appcookies.NewJars(cookieRegistry, workspaces)
	// Imports (IMP-10) choose their file with the native open dialog.
	// The open project's files are polled for changes made outside the app
	// (STO-05, docs/collection.md#external-changes).
	// Postman exports (EXP-03) save with the native save dialog.
	// Polling pauses while every window is hidden; edits from several
	// windows are versioned (collection/windows.go).
	collectionService := collection.New(workspaces, emitEvent, logger, collection.WithFilePicker(pickImportFile),
		collection.WithWatch(collection.DefaultWatchInterval),
		collection.WithWindowCount(windows.Count),
		collection.WithSaveDialog(func(ctx context.Context, d collection.SaveDialog) (string, error) {
			return saveFile(ctx, httpservice.SaveDialog(d))
		}))
	// The {{variable}} scopes of a request of any kind, as an HTTP send
	// renders them (docs/templates.md): the editor's chips of every kind
	// and WebSocket connects and sends (WS-11).
	loadVariables := func(ctx context.Context, id string) (collection.VariablesSource, error) {
		return collection.LoadVariablesSource(collectionService, ctx, id)
	}
	// The open project's runtime variables (ENV-15), above every scope of
	// the project files: one session per opening of a project, persisted
	// ones in its local directory (docs/environments.md#runtime-variables).
	runtimeVars := coreruntimevars.NewRegistry(workspaces.Current, runtimevars.Changed(emitEvent))
	requestScopes := func(ctx context.Context, id string) (template.Chain, error) {
		src, err := loadVariables(ctx, id)
		if err != nil {
			return nil, err
		}
		chain, err := httpservice.ScopeChain(src)
		if err != nil {
			return nil, err
		}
		return httpservice.RuntimeScope(runtimeVars, src.Workspace, chain)
	}
	// The auth of WebSocket, Socket.IO and gRPC handshakes (AUTH-31): the
	// request's own or inherited auth, built by the HTTP service (below)
	// with its secrets, key and netrc files and OAuth 2.0 tokens
	// (docs/auth.md#auth-on-every-protocol-auth-31).
	var httpService *httpservice.HTTPService
	handshakeAuth := func(ctx context.Context, in request.HandshakeInput) (request.Handshake, error) {
		return httpservice.HandshakeAuth(ctx, httpService, in)
	}
	// The assertions of stream requests (TEST-08), as they are now and
	// rendered with the request's variables, checked when a gRPC call,
	// WebSocket or Socket.IO connection or SSE stream ends and on demand
	// (docs/assertions.md#streams).
	streamChecks := func(ctx context.Context, id string) (streamcheck.Checks, error) {
		return httpservice.StreamChecks(ctx, httpService, id)
	}
	// WebSocket connections too; the handshake uses the open project's
	// cookie jar, saved when the workspace changes and on shutdown
	// (docs/websocket.md, docs/cookies.md).
	wsService := websocket.New(dialer, websocket.WorkspaceJars(cookieJars), emitEvent, logger,
		websocket.WithScopes(requestScopes), websocket.WithTrust(trust.Roots), websocket.WithClientCerts(pickClientCert),
		websocket.WithRequireTLSVerification(requireTLS), websocket.WithAuth(handshakeAuth), websocket.WithChecks(streamChecks))
	// Socket.IO connections run as operations and use the same cookie jar
	// and trust settings (docs/socketio.md).
	sioService := socketioservice.New(operations, dialer, websocket.WorkspaceJars(cookieJars), logger,
		socketioservice.WithScopes(requestScopes), socketioservice.WithTrust(trust.Roots), socketioservice.WithClientCerts(pickClientCert),
		socketioservice.WithRequireTLSVerification(requireTLS), socketioservice.WithAuth(handshakeAuth),
		socketioservice.WithChecks(streamChecks))
	// UDP sockets (UDP-01) belong to the open project's requests like TCP
	// connections; through a SOCKS5 proxy their datagrams go through its
	// UDP ASSOCIATE relay (SOCKS-07, docs/udp.md).
	udpService := udp.New(dialer, emitEvent, logger, udp.WithScopes(udp.Scopes(requestScopes)))
	workspaceEmit := func(name string, data any) {
		if name == workspace.EventChanged {
			// Another project has other proxy scopes (SOCKS-08).
			proxyRouter.Refresh(context.Background())
			tcp.OnWorkspaceChanged(tcpService)
			udp.OnWorkspaceChanged(udpService)
			websocket.OnWorkspaceChanged(wsService)
			socketioservice.OnWorkspaceChanged(sioService)
			windows.retitle()
		}
		emitEvent(name, data)
	}

	// New windows, and a second start of PostSonde (APP-06): the window
	// service opens windows through the host and hands each its project.
	windowService := window.New(windows, workspaces, emitEvent, logger)
	windows.setOpenAt(func(id uint, path string) { window.OpenAt(windowService, id, path) })
	windows.setLaunch(func(args []string, workingDir string) { window.Launch(windowService, args, workingDir) })
	windows.onClosed(func(id uint) {
		collection.WindowClosed(collectionService, id)
		window.Closed(windowService, id)
	})

	// The request history (NAV-02): every HTTP and GraphQL send, and every
	// replayed webhook request, recorded with secrets masked in the open
	// project's local directory (docs/history.md).
	// Entries export as HAR (EXP-04) to a file the user picks. Response
	// examples made from them (HTTP-38) are masked again with every secret
	// kept, as copied logs are.
	historyStores := corehistory.NewRegistry(corehistory.Limits{})
	// Clearing a request's history, or all of it, also drops the
	// responses {{$response}} references read (DYN-06): a cleared
	// response is not used any more.
	var httpServiceRef *httpservice.HTTPService
	historyEmit := func(name string, data any) {
		if ev, ok := data.(historyservice.ChangedEventDTO); ok && ev.Action == historyservice.ActionCleared {
			httpservice.ForgetResponses(httpServiceRef, ev.RequestID)
		}
		emitEvent(name, data)
	}
	historyService := historyservice.New(historyStores, workspaces.LocalDir, historyEmit, logger,
		historyservice.WithHARExport(cfg.Version, func(ctx context.Context, d historyservice.SaveDialog) (string, error) {
			return saveFile(ctx, httpservice.SaveDialog(d))
		}),
		historyservice.WithSecrets(historyservice.Secrets(knownSecrets(workspaces, appSecrets, runtimeVars, oauth2Tokens.Values))))

	// The directories besides the open project's that files named in
	// project data may be read from (PRV-10), read whenever a file is.
	allowedDirs := func() []string { return settingsStore.Settings().Privacy.AllowedDirs }

	// HTTP requests are built from the collection's view of the request
	// (unsaved edits included), take the default timeout from the settings
	// on every send and send and store cookies through the open project's
	// jar, cookieJars (docs/http-client.md).
	httpService = httpservice.New(
		func(ctx context.Context, id string) (collection.HTTPSource, error) {
			return collection.LoadHTTPSource(collectionService, ctx, id)
		},
		func() time.Duration {
			return time.Duration(settingsStore.Settings().Network.DefaultTimeoutMs) * time.Millisecond
		},
		operations, dialer, cookieJars, logger,
		// Response bodies for "Save to file" (HTTP-34): large ones are
		// spooled under the data directory, never elsewhere unless the
		// user picks the path (docs/privacy.md).
		httpservice.WithSaveBody(responseSpoolDir(cfg.DataDir), saveFile),
		// The largest body kept (HTTP-37), read per send; bodies stream
		// to the spool directory while they arrive.
		httpservice.WithMaxBodySize(func() int64 {
			return int64(settingsStore.Settings().Network.MaxResponseBodyMb) << 20
		}),
		// The global TLS verification switch (HTTP-23), read per send.
		httpservice.WithVerifyTLS(func() bool { return settingsStore.Settings().Network.VerifyTLS }),
		// The admin policy's requireTlsVerification (SET-02): sends,
		// streams, schema fetches and subscriptions of requests that turn
		// verification off are refused.
		httpservice.WithRequireTLSVerification(requireTLS),
		// The custom CA bundles (NET-04), for sends and schema fetches.
		httpservice.WithTrust(trust.Roots),
		// The per-host client certificates (AUTH-30), for every https hop
		// and GraphQL subscription.
		httpservice.WithClientCerts(pickClientCert),
		httpservice.WithVariablesLoader(loadVariables),
		// The auth of a request of any kind, for the Auth tab's tools and
		// the handshakes of the other protocols (AUTH-31).
		httpservice.WithAuthLoader(func(ctx context.Context, id string) (collection.HTTPSource, error) {
			return collection.LoadAuthSource(collectionService, ctx, id)
		}),
		// Runtime variables (ENV-15) come after the folders' variables.
		httpservice.WithRuntimeVariables(runtimeVars),
		// The directories {{$file}}, JWT key, netrc and schema files may
		// be read from besides the project's (PRV-10).
		httpservice.WithAllowedDirs(allowedDirs),
		// The version written in exported HAR files (EXP-04).
		httpservice.WithVersion(cfg.Version),
		// Saved exchanges (EXP-06) are masked again with every secret
		// kept, as copied logs are.
		httpservice.WithExchangeSecrets(httpservice.Secrets(knownSecrets(workspaces, appSecrets, runtimeVars, oauth2Tokens.Values))),
		// Every send, successful or not, goes into the history.
		httpservice.WithHistory(historyservice.Recorder(historyService)),
		// {{$response}} references (DYN-06) read the history when a
		// request was not sent since the app started.
		httpservice.WithResponseHistory(func() (*corehistory.Store, error) {
			dir, err := workspaces.LocalDir()
			if err != nil {
				return nil, err
			}
			return historyStores.Store(dir)
		}),
		// OAuth 2.0 tokens survive a restart (AUTH-13), and the Auth tab
		// hears when one changes.
		httpservice.WithOAuth2Store(oauth2Tokens),
		httpservice.WithOAuth2Events(emitEvent),
		// The authorization code grant's sign-in (AUTH-07) opens the
		// system browser; the code comes back to a loopback listener.
		httpservice.WithOAuth2Browser(openURL))
	httpServiceRef = httpService
	// GraphQL schemas: fetched through the HTTP service's pipeline (the
	// request's headers, auth and environment) only when the user asks,
	// cached in the open project's local directory (docs/graphql.md).
	graphqlService := graphqlservice.New(
		func(ctx context.Context, id string) (string, error) {
			return httpservice.GraphQLEndpoint(ctx, httpService, id)
		},
		func(ctx context.Context, id string) (string, graphqlservice.Poster, error) {
			p, err := httpservice.NewGraphQLPoster(ctx, httpService, id)
			if err != nil {
				return "", nil, err
			}
			return p.Endpoint, p, nil
		},
		workspaces.LocalDir, operations, logger,
		// Schemas from local SDL files (GQL-11): the request's schemaFile,
		// resolved against the open project's root.
		graphqlservice.WithSchemaFiles(
			func(ctx context.Context, id string) (string, error) {
				src, err := collection.LoadHTTPSource(collectionService, ctx, id)
				if err != nil || src.GraphQL == nil {
					return "", err
				}
				return src.GraphQL.SchemaFile, nil
			},
			func() string {
				if w := workspaces.Current(); w != nil {
					return w.Root
				}
				return ""
			},
			pickSchemaFile),
		graphqlservice.WithAllowedDirs(allowedDirs),
		// Save schema as SDL (GQL-20): the native save dialog.
		graphqlservice.WithSaveDialog(func(ctx context.Context, d graphqlservice.SaveDialog) (string, error) {
			return saveFile(ctx, httpservice.SaveDialog(d))
		}),
		// Subscriptions over WebSocket or SSE (GQL-30..GQL-35): the
		// handshake or the SSE request is built like a send (headers,
		// auth, TLS setting, cookies, environment) and connects through
		// the app's dialer.
		graphqlservice.WithSubscriptions(func(ctx context.Context, id string, allowUnresolved bool) (graphqlservice.SubscriptionTarget, error) {
			sub, err := httpservice.NewGraphQLSubscription(ctx, httpService, id, allowUnresolved)
			if err != nil {
				return graphqlservice.SubscriptionTarget{}, err
			}
			return graphQLSubscriptionTarget(sub), nil
		}))

	// The data directory setting and portable mode (SET-03, APP-04).
	dataDirOpts := []datadir.Option{datadir.WithFolderPicker(pickDataFolder), datadir.WithReveal(revealPath)}
	if cfg.Relaunch != nil {
		dataDirOpts = append(dataDirOpts, datadir.WithRestart(requestRestart))
	}

	// Server-Sent Events streams: built and sent like HTTP requests, read
	// as they arrive (docs/sse.md).
	sseService := sse.New(
		func(ctx context.Context, id string, allowUnresolved bool) (sse.Target, error) {
			st, err := httpservice.NewStream(ctx, httpService, id, allowUnresolved)
			if err != nil {
				return sse.Target{}, err
			}
			return sse.Target{Stream: st, Method: st.Method, URL: st.URL, Reconnect: sse.ReconnectFrom(st.Reconnect), Checks: st.Checks, Mask: st.Mask}, nil
		},
		operations, logger, sse.WithChecks(streamChecks))

	// gRPC calls, with the app's trust and client certificates.
	grpcService := grpc.New(workspaces, operations, dialer, pickPaths, logger, grpc.WithTrust(trust.Roots), grpc.WithClientCerts(pickClientCert),
		grpc.WithRequireTLSVerification(requireTLS), grpc.WithAllowedDirs(allowedDirs),
		// The request's auth in the call's metadata (AUTH-31).
		grpc.WithAuth(handshakeAuth, grpc.Scopes(requestScopes)),
		grpc.WithChecks(streamChecks))
	// A change of the proxy in effect closes the idle kept-alive
	// connections of every pool, so none opened with the old route is
	// reused (SOCKS-05, docs/network.md#pools-and-proxy-changes). The
	// router notices it on the next dial, and after every settings change.
	proxyRouter.OnChange(func() {
		httpservice.OnProxyChanged(httpService)
		grpc.OnProxyChanged(grpcService)
		logger.Info("proxy changed: idle connections closed")
	})

	opts := serviceOptions(logger)
	return []application.Service{
		application.NewServiceWithOptions(system.New(build, logger), opts),
		application.NewServiceWithOptions(ops.New(operations, logger), opts),
		application.NewServiceWithOptions(demo.New(operations, logger), opts),
		application.NewServiceWithOptions(settings.New(settingsStore, refreshProxyOnSettings(proxyRouter, systemProxy, emitEvent), logger,
			settings.WithDesktop(settings.Desktop{Open: openPath, Reveal: revealPath}),
			settings.WithDataDir(cfg.DataDir),
			// Adding a custom CA bundle (NET-04) or a client certificate
			// (AUTH-30), whose passphrases go to appSecrets.
			settings.WithFilePicker(pickFile),
			settings.WithSecrets(appSecrets),
			// The proxy section shows what ALL_PROXY holds (SOCKS-04)
			// and tests the proxy (SOCKS-11).
			settings.WithProxyEnv(cfg.Getenv),
			// The "system" mode shows the OS's proxies (NET-01).
			settings.WithSystemProxy(systemProxy.settings),
			settings.WithProxyTester(proxyTester(proxyRouter, connLog, logger)),
			// The file pickers' allowlist check (PRV-10) counts the open
			// project's folder as allowed.
			settings.WithProjectRoot(func() string {
				if w := workspaces.Current(); w != nil {
					return w.Root
				}
				return ""
			})), opts),
		application.NewServiceWithOptions(datadir.New(cfg.DataLocation, logger, dataDirOpts...), opts),
		// The master password of the local secrets (SEC-03): on and
		// off, unlock, lock now, the idle lock, and the reset of a
		// forgotten password; the key in the OS keychain.
		application.NewServiceWithOptions(secrets.New(vault, settingsStore, logger,
			secrets.WithKeychain(secretsKeychain), secrets.WithEmit(emitEvent)), opts),
		// The project's and its environments' proxies (SOCKS-08) on the
		// settings screen; a change closes the idle pools at once.
		// Its timer reads the OS proxy settings again every
		// systemProxyRefresh, so a change there closes the idle pools too.
		application.NewServiceWithOptions(proxyscopes.New(workspaces, resolveProxy, refreshProxyOn(proxyRouter, emitEvent), logger,
			proxyscopes.WithRefresh(systemProxyRefresh, systemProxy.refresh(proxyRouter))), opts),
		// Custom themes (UI-05): theme files in <data>/themes, validated
		// in Go; the UI applies the chosen one (docs/themes.md).
		application.NewServiceWithOptions(themes.New(cfg.DataDir, logger, themes.WithOpen(openPath)), opts),
		// While other windows are open, the project cannot be switched
		// or closed (docs/windows.md).
		application.NewServiceWithOptions(workspace.New(workspaces, pickFolder, workspaceEmit, logger, workspace.WithReveal(revealPath),
			workspace.WithWindows(windows.others)), opts),
		// New windows and the calling window's ID (APP-06).
		application.NewServiceWithOptions(windowService, opts),
		application.NewServiceWithOptions(collectionService, opts),
		application.NewServiceWithOptions(environments.New(workspaces, logger, environments.WithEmit(refreshProxyOn(proxyRouter, emitEvent)),
			// Its writes are the app's own, not outside changes (STO-05).
			environments.WithWritten(func() { collection.EnvironmentsWritten(collectionService) }),
			// Environment exports (EXP-05) save with the native save dialog.
			environments.WithExport(func(ctx context.Context, d environments.SaveDialog) (string, error) {
				return saveFile(ctx, httpservice.SaveDialog(d))
			}, cfg.Version),
			// The global variables editor (ENV-02) writes postsonde.json
			// through the collection's tree, which holds that file.
			environments.WithGlobals(environments.Globals{
				Load: func(ctx context.Context) ([]projectfile.Variable, bool, error) {
					g, err := collection.LoadGlobalVariables(collectionService, ctx)
					return g.Variables, g.ReadOnly, err
				},
				Save: func(ctx context.Context, vars []projectfile.Variable) ([]projectfile.Variable, error) {
					g, err := collection.SaveGlobalVariables(collectionService, ctx, vars)
					return g.Variables, err
				},
			})), opts),
		// The Runtime section of the Environments view (ENV-15).
		application.NewServiceWithOptions(runtimevars.New(workspaces, runtimeVars, logger), opts),
		application.NewServiceWithOptions(format.New(logger), opts),
		// The cookie manager (CK-02) over the open project's jar.
		application.NewServiceWithOptions(appcookies.New(cookieJars, logger), opts),
		// Local servers; ServiceShutdown stops them all when the app quits.
		// A webhook receiver's requests are replayed through the HTTP
		// service's send pipeline, as http operations (MOCK-08). Servers
		// saved in a project folder are read and written through the
		// collection service (SRV-04). Mock routes are generated from an
		// OpenAPI file chosen in the native dialog (MOCK-09). TLS servers
		// (SRV-05) get generated certificates from the local CA in the data
		// directory, and certificate passphrases are app secrets.
		application.NewServiceWithOptions(servers.New(emitEvent, logger, servers.WithReplayer(func(req httpclient.Request) (operation.StartedDTO, error) {
			return httpservice.SendPrepared(httpService, req)
		}), servers.WithProject(servers.CollectionProject(collectionService)), servers.WithFilePicker(pickOpenAPIFile),
			servers.WithTLS(servertls.NewAuthority(cfg.DataDir), appSecrets),
			servers.WithTrustedBundles(func() []string { return settingsStore.Settings().Network.CABundles }),
			servers.WithReveal(revealPath),
			servers.WithAllowedDirs(allowedDirs),
			// Mock routes made from responses (MOCK-03) are masked again
			// with every secret kept, as response examples are.
			servers.WithSecrets(servers.Secrets(knownSecrets(workspaces, appSecrets, runtimeVars, oauth2Tokens.Values))),
			// gRPC mock servers (SSRV-07) resolve relative .proto paths against
			// the open project, as gRPC requests do.
			servers.WithProjectRoot(func() string {
				if w := workspaces.Current(); w != nil {
					return w.Root
				}
				return ""
			})), opts),
		application.NewServiceWithOptions(tcpService, opts),
		// UDP sockets (UDP-01, SOCKS-07).
		application.NewServiceWithOptions(udpService, opts),
		application.NewServiceWithOptions(wsService, opts),
		// Socket.IO connections (SIO-01, SIO-03…SIO-07).
		application.NewServiceWithOptions(sioService, opts),
		// Describes the .proto setups of gRPC requests (docs/grpc.md).
		application.NewServiceWithOptions(grpcService, opts),
		// Sends HTTP requests (HTTP-20, HTTP-21).
		application.NewServiceWithOptions(httpService, opts),
		// The request history (NAV-02).
		application.NewServiceWithOptions(historyService, opts),
		// GraphQL schema fetch and cache (GQL-10, GQL-21).
		application.NewServiceWithOptions(graphqlService, opts),
		// Server-Sent Events streams (SSE-01, SSE-02).
		application.NewServiceWithOptions(sseService, opts),
		// The folder runner (RUN-01): the folder's requests, in the
		// tree's order, sent through the HTTP service (docs/runner.md).
		// Data files of data-driven runs (RUN-05) are chosen in the
		// native open dialog. Runs that end are kept in the open
		// project's local directory, masked like copied logs, and saved as
		// reports where the user picks (RUN-08, RUN-09).
		application.NewServiceWithOptions(runner.New(func(ctx context.Context, id string) (corerunner.Plan, error) {
			return collection.RunPlan(collectionService, ctx, id)
		}, httpService, operations, logger,
			runner.WithDataFilePicker(func(ctx context.Context, d runner.FileDialog) (string, error) {
				return pickFile(ctx, settings.FileDialog{Title: d.Title, Message: d.Message, ButtonText: d.ButtonText,
					FilterName: d.FilterName, FilterPattern: d.FilterPattern})
			}),
			runner.WithRunHistory(workspaces.LocalDir, runstore.Limits{}),
			runner.WithSecrets(runner.Secrets(knownSecrets(workspaces, appSecrets, runtimeVars))),
			runner.WithReportDialog(func(ctx context.Context, d runner.SaveDialog) (string, error) {
				return saveFile(ctx, httpservice.SaveDialog(d))
			})), opts),
		// The in-app console: the recent log records, redacted, streamed
		// as batched events (DBG-06).
		application.NewServiceWithOptions(console.New(cfg.Console, emitEvent, logger), opts),
		// The network activity indicator and connection log (PRV-06).
		application.NewServiceWithOptions(network.New(connLog, emitEvent, logger), opts),
		// Settings, Privacy, Local data (PRV-13): measures and clears the
		// history, run history, cookies, OAuth 2.0 tokens, cached schemas
		// and runtime variables of the open project or of every project,
		// telling the services that keep them in memory first.
		application.NewServiceWithOptions(localdata.New(workspaces, cfg.DataDir, logger, localDataHooks(localDataDeps{
			workspaces: workspaces, history: historyService, historyStore: historyStores, http: httpService, cookies: cookieRegistry,
			tokens: oauth2Tokens, graphql: graphqlService, runtimeVars: runtimeVars, emit: emitEvent,
		})...), opts),
		// Copying and saving timeline and log entries (DBG-07): masked
		// with every secret the app keeps, saved where the user picks.
		application.NewServiceWithOptions(logexport.New(knownSecrets(workspaces, appSecrets, runtimeVars, oauth2Tokens.Values), func(ctx context.Context, d logexport.SaveDialog) (string, error) {
			return saveFile(ctx, httpservice.SaveDialog(d))
		}, logger), opts),
	}
}

// knownSecrets returns the secret values a copied or saved log is masked
// with (DBG-07, docs/privacy.md#copied-and-saved-logs): those of the open
// project's local side files (request, folder and global auth and
// variables, environments, persisted runtime variables), its secret
// runtime variables of the session, and the app-wide ones (certificate
// passphrases), read on every call so a secret typed a moment ago counts;
// then those of more (the stored OAuth 2.0 tokens).
func knownSecrets(workspaces *coreworkspace.Manager, app *appsecrets.Store, runtimeVars *coreruntimevars.Registry, more ...func() []string) logexport.Secrets {
	return func(context.Context) ([]string, error) {
		var dirs []string
		if local, err := workspaces.LocalDir(); err == nil {
			dirs = append(dirs,
				filepath.Join(local, filepath.FromSlash(requestsecrets.Dir)),
				filepath.Join(local, filepath.FromSlash(coreenvironments.SecretsDir)),
				filepath.Join(local, filepath.FromSlash(coreruntimevars.SecretsDir)))
		}
		values, err := corelogexport.ReadSecrets(dirs...)
		values = append(values, runtimeVars.Secrets()...)
		appValues, appErr := app.Values()
		values = append(values, appValues...)
		for _, fn := range more {
			values = append(values, fn()...)
		}
		return values, errors.Join(err, appErr)
	}
}

// graphQLSubscriptionTarget hands a GraphQL request built for WebSocket or
// SSE to the GraphQL service.
func graphQLSubscriptionTarget(sub *httpservice.GraphQLSubscription) graphqlservice.SubscriptionTarget {
	t := graphqlservice.SubscriptionTarget{Transport: sub.Transport, OperationType: sub.OperationType}
	if sub.SSE != nil {
		t.SSE = &graphqlservice.SSETarget{
			Method: sub.SSE.Method,
			URL:    sub.SSE.URL,
			Body:   sub.SSEBody,
			Open: func(ctx context.Context, fn httpclient.StreamFunc) (*httpclient.Response, error) {
				return sub.SSE.Open(ctx, "", fn)
			},
		}
		return t
	}
	t.WS = sub.WS
	t.ConnectionParams = sub.Build.ConnectionParams
	t.Request = transportws.SubscribeRequest{
		Query:         sub.Build.Query,
		OperationName: sub.Build.OperationName,
		Variables:     sub.Build.Variables,
		Extensions:    sub.Build.Extensions,
	}
	return t
}

// responseSpoolDir is where HTTPService spools large response bodies
// kept for saving: <data>/tmp/response-bodies, emptied at start and
// shutdown. "" (no data directory) keeps them in memory.
func responseSpoolDir(dataDir string) string {
	if dataDir == "" {
		return ""
	}
	return filepath.Join(dataDir, "tmp", "response-bodies")
}

// dialerConfig configures newDialer.
type dialerConfig struct {
	// Direct configures the direct dialer; its Resolver also resolves
	// host names for a socks5 proxy (tests set one).
	Direct netx.Options
	// Proxy returns the proxy in effect for a dial (proxySource); nil
	// means none.
	Proxy func(context.Context) (netx.ProxyConfig, error)
	// RootCAs are the CAs an https:// proxy's certificate is verified
	// against (tlsconf.Trust.Roots); nil means the system's.
	RootCAs func() (*x509.CertPool, error)
}

// newDialer builds the dialer shared by every protocol client. Its guard
// refuses, and logs, every dial whose context is not marked as a user
// operation (netx.RequireUserOp, PRV-05): operations started through the
// registry are marked by operation.Start, TCP and WebSocket connections by
// their Connect. Inside the guard a netx.ProxyRouter sends each dial
// directly or through the proxy in effect (SOCKS-04, NET-02), so the
// guard and the connection log see the target as the client asked for
// it. The dialer is a netx.ForwardProxier: the HTTP client sends http://
// requests to an HTTP proxy in absolute form, dialing them through the
// same chain.
//
// connLog, when not nil, records every dial outside the guard, so the
// connection log shows refused dials too (PRV-06).
func newDialer(logger *slog.Logger, connLog *netlog.Log, cfg dialerConfig) netx.Dialer {
	d, _ := newRoutedDialer(logger, connLog, cfg)
	return d
}

// newRoutedDialer is newDialer that also returns its proxy router, for
// the proxy connection test (SOCKS-11).
func newRoutedDialer(logger *slog.Logger, connLog *netlog.Log, cfg dialerConfig) (netx.Dialer, *netx.ProxyRouter) {
	direct := netx.NewDirect(cfg.Direct)
	router := netx.NewProxyConfigRouter(direct, cfg.Proxy, netx.ProxyOptions{Resolver: cfg.Direct.Resolver, Now: cfg.Direct.Now, RootCAs: cfg.RootCAs})
	chain := netx.Chain(netx.Routed(router.Route), connLog.Middleware(), netx.Guarded(netx.RequireUserOp(logger)))
	return netx.NewForwardProxier(chain, router.ForwardProxy), router
}

// serviceOptions are the Wails options every bound service is registered
// with. MarshalError turns each error a method returns into an
// apperr.ErrorDTO, which the frontend receives as the rejected promise's
// `cause` (docs/errors.md).
//
// It has to be set per service: in Wails v3.0.0 Bindings.Add
// combines a service's MarshalError with Wails' default marshaler only, so
// application.Options.MarshalError is never used for bound methods.
func serviceOptions(logger *slog.Logger) application.ServiceOptions {
	return application.ServiceOptions{MarshalError: apperr.Marshaler(logger)}
}

// moduleVersion returns the version of module path linked into the binary,
// following replace directives, or "" if info does not list it.
func moduleVersion(info *debug.BuildInfo, path string) string {
	if info == nil {
		return ""
	}
	for _, m := range info.Deps {
		if m.Path != path {
			continue
		}
		if m.Replace != nil && m.Replace.Version != "" {
			return m.Replace.Version
		}
		return m.Version
	}
	return ""
}
