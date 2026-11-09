// Package servers is the bound service for the local servers (SRV-01,
// SRV-03, MOCK-08): define, start and stop servers and follow their
// traffic. The logic is internal/core/servers; server definitions live in
// memory for this app run, except those saved in a project folder (SRV-04,
// project.go). See docs/servers.md.
package servers

import (
	"context"
	"log/slog"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/appsecrets"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/realtime/wsframe"
	core "github.com/sorrelfox/postsonde/internal/core/servers"
	"github.com/sorrelfox/postsonde/internal/core/servers/echo"
	"github.com/sorrelfox/postsonde/internal/core/servers/graphqlmock"
	"github.com/sorrelfox/postsonde/internal/core/servers/grpcmock"
	"github.com/sorrelfox/postsonde/internal/core/servers/mockhttp"
	"github.com/sorrelfox/postsonde/internal/core/servers/servertls"
	"github.com/sorrelfox/postsonde/internal/core/servers/sioserver"
	"github.com/sorrelfox/postsonde/internal/core/servers/sseserver"
	"github.com/sorrelfox/postsonde/internal/core/servers/tcpserver"
	"github.com/sorrelfox/postsonde/internal/core/servers/udpserver"
	"github.com/sorrelfox/postsonde/internal/core/servers/webhook"
	"github.com/sorrelfox/postsonde/internal/core/servers/wsserver"
	"github.com/sorrelfox/postsonde/internal/core/stream"
)

// shutdownTimeout bounds how long app shutdown waits for servers to stop.
const shutdownTimeout = 5 * time.Second

// trafficMaxItems caps the events in one servers:traffic event. Events
// carry up to core.DefaultContentLimit bytes each, so this keeps one
// payload within a few megabytes.
const trafficMaxItems = 64

// ServersService runs the local servers and streams their traffic.
type ServersService struct {
	manager *core.Manager
	emit    func(name string, data any)
	logger  *slog.Logger
	traffic *stream.Batcher[core.Event]
	clients *clientsNotifier
	conns   *clientsNotifier
	// captures publishes the kept requests of webhook receivers; replay
	// sends one (WithReplayer).
	captures *clientsNotifier
	// subscribers and peers publish the subscribers of SSE servers and the
	// recent peers of UDP servers.
	subscribers *clientsNotifier
	peers       *clientsNotifier
	// sockets publishes the sockets of Socket.IO servers.
	sockets *clientsNotifier
	replay  Replayer
	// pick shows the file dialog of PickOpenAPIFile (WithFilePicker).
	pick FilePicker
	// project holds the saved server configs (WithProject); projMu
	// serializes the changes of saved servers, so a sync never races an
	// edit.
	project Project
	projMu  sync.Mutex
	// ca signs generated certificates and secrets keeps certificate
	// passphrases (WithTLS); bundles are the trusted CA bundles
	// (WithTrustedBundles) and reveal shows a file (WithReveal).
	ca      *servertls.Authority
	secrets *appsecrets.Store
	bundles func() []string
	reveal  func(path string) error
	// projectRoot is the open project's root, for the relative .proto
	// paths of gRPC mock servers (WithProjectRoot).
	projectRoot func() string
	// allowedDirs are the directories besides the project's that the
	// files of server definitions may be read from (WithAllowedDirs,
	// PRV-10); nil for none.
	allowedDirs func() []string
	// knownSecrets masks the routes made from responses (WithSecrets).
	knownSecrets Secrets
}

// New returns a ServersService with the built-in server types. emit sends
// an event to the UI (nil drops events). A nil logger means slog.Default().
func New(emit func(name string, data any), logger *slog.Logger, options ...Option) *ServersService {
	return newService(emit, logger, core.Options{}, options...)
}

// newService lets tests set the manager options; Types, OnChange,
// OnTraffic and Logger are filled in when empty.
func newService(emit func(name string, data any), logger *slog.Logger, opts core.Options, options ...Option) *ServersService {
	if logger == nil {
		logger = slog.Default()
	}
	if emit == nil {
		emit = func(string, any) {}
	}
	s := &ServersService{emit: emit, logger: logger.With("service", Domain)}
	s.traffic = stream.New(context.Background(), stream.Options[core.Event]{MaxItems: trafficMaxItems}, s.flushTraffic)
	s.clients = newClientsNotifier(clientsDelay, s.publishClients)
	s.conns = newClientsNotifier(clientsDelay, s.publishConnections)
	s.captures = newClientsNotifier(clientsDelay, s.publishCaptures)
	s.subscribers = newClientsNotifier(clientsDelay, s.publishSubscribers)
	s.peers = newClientsNotifier(clientsDelay, s.publishPeers)
	s.sockets = newClientsNotifier(clientsDelay, s.publishSockets)
	for _, o := range options {
		o(s)
	}
	if len(opts.Types) == 0 {
		opts.Types = []core.Type{
			echo.New(), mockhttp.New(mockhttp.Options{}), wsserver.New(wsserver.Options{}), tcpserver.New(tcpserver.Options{}),
			webhook.New(webhook.Options{}), sseserver.New(sseserver.Options{}), udpserver.New(udpserver.Options{}),
			grpcmock.New(grpcmock.Options{Resolve: s.resolveProto, Access: s.protoAccess}), sioserver.New(sioserver.Options{}),
			graphqlmock.New(graphqlmock.Options{ReadFile: s.readSchemaFile}),
		}
	}
	if certs := s.initTLS(); opts.Certificates == nil {
		opts.Certificates = certs
	}
	opts.OnChange = s.publishChanged
	opts.OnTraffic = func(e core.Event) {
		_ = s.traffic.Add(e) // dropped only after shutdown
		if e.WS != nil {
			s.clients.mark(e.ServerID) // a WebSocket client connected, sent or left
		}
		if e.TCP != nil {
			s.conns.mark(e.ServerID) // a TCP connection opened, sent or closed
		}
		if e.Captured {
			s.captures.mark(e.ServerID) // a webhook receiver kept a request
		}
		if e.SSE != nil {
			s.subscribers.mark(e.ServerID) // an SSE subscriber came, got an event or left
		}
		if e.UDP != nil {
			s.peers.mark(e.ServerID) // a UDP datagram came in or went out
		}
		if e.SIO != nil {
			s.sockets.mark(e.ServerID) // a Socket.IO client came, sent, joined or left
		}
	}
	if opts.Logger == nil {
		opts.Logger = s.logger
	}
	s.manager = core.NewManager(opts)
	return s
}

// ServiceName names the service in Wails logs.
func (s *ServersService) ServiceName() string { return Domain }

// ServiceShutdown stops every server when the app quits, then sends the
// last traffic.
func (s *ServersService) ServiceShutdown() error {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	s.manager.StopAll(ctx)
	s.traffic.Close()
	s.clients.close()
	s.conns.close()
	s.captures.close()
	s.subscribers.close()
	s.peers.close()
	s.sockets.close()
	return nil
}

// Types returns the kinds of server that can be created.
func (s *ServersService) Types(ctx context.Context) ([]TypeDTO, error) {
	if err := interrupted(ctx, "Loading the server types"); err != nil {
		return nil, err
	}
	return toTypeDTOs(s.manager.Types()), nil
}

// List returns every server, in the order created.
func (s *ServersService) List(ctx context.Context) ([]ServerDTO, error) {
	if err := interrupted(ctx, "Loading the servers"); err != nil {
		return nil, err
	}
	return s.serverDTOs(s.manager.List()), nil
}

// Create defines a server, stopped. An unknown type, an invalid port or a
// host that is not an IP address or localhost is an invalid_argument
// error (servers.unknown_type, listen.invalid_port, listen.invalid_host).
// With TLS from files, the files are read now (tlsconf.*, pkcs12.*) and a
// passphrase given is kept in the local secret storage.
func (s *ServersService) Create(ctx context.Context, in CreateInputDTO) (ServerDTO, error) {
	if err := interrupted(ctx, "Creating the server"); err != nil {
		return ServerDTO{}, err
	}
	spec := in.toCore()
	var pass *string
	var err error
	if spec.TLS, pass, err = s.prepareTLS(in.TLS); err != nil {
		return ServerDTO{}, err
	}
	if _, err := s.manager.Check(spec); err != nil {
		return ServerDTO{}, err
	}
	if err := s.storePassphrase(spec.TLS, pass); err != nil {
		return ServerDTO{}, err
	}
	srv, err := s.manager.Add(spec)
	if err != nil {
		return ServerDTO{}, err
	}
	return s.serverDTO(srv), nil
}

// UpdateConfig replaces the configuration of the server id (for a mock
// server, config.mockHttp) and returns the server with the diagnostics of
// the new routes. A running server applies it at once; requests in
// progress finish with the old routes. A saved server (SRV-04) writes it
// to its folder's _folder.json too; when that fails nothing changes. Too many routes or a body over the
// limit is an invalid_argument error (mockhttp.too_many_routes,
// mockhttp.body_too_large); a problem with one route is a diagnostic and
// the route is not served. Servers without a configuration ignore it.
func (s *ServersService) UpdateConfig(ctx context.Context, id string, config ConfigDTO) (ServerDTO, error) {
	if err := interrupted(ctx, "Updating the server"); err != nil {
		return ServerDTO{}, err
	}
	s.projMu.Lock()
	defer s.projMu.Unlock()
	srv, err := s.manager.Get(id)
	if err != nil {
		return ServerDTO{}, err
	}
	if srv.Spec.Type == mockhttp.TypeName && config.MockHTTP == nil {
		return ServerDTO{}, errs.New(errs.InvalidArgument, "The mock server's routes are missing.").WithCode(mockhttp.CodeInvalidConfig)
	}
	if srv.Spec.Type == wsserver.TypeName && config.WebSocket == nil {
		return ServerDTO{}, errs.New(errs.InvalidArgument, "The WebSocket server's settings are missing.").WithCode(wsserver.CodeInvalidConfig)
	}
	if srv.Spec.Type == webhook.TypeName && config.Webhook == nil {
		return ServerDTO{}, errs.New(errs.InvalidArgument, "The webhook receiver's response is missing.").WithCode(webhook.CodeInvalidConfig)
	}
	if srv.Spec.Type == sseserver.TypeName && config.SSE == nil {
		return ServerDTO{}, errs.New(errs.InvalidArgument, "The SSE server's settings are missing.").WithCode(sseserver.CodeInvalidConfig)
	}
	if srv.Spec.Type == udpserver.TypeName && config.UDP == nil {
		return ServerDTO{}, errs.New(errs.InvalidArgument, "The UDP server's replies are missing.").WithCode(udpserver.CodeInvalidConfig)
	}
	if srv.Spec.Type == grpcmock.TypeName && config.GRPCMock == nil {
		return ServerDTO{}, errs.New(errs.InvalidArgument, "The gRPC mock server's settings are missing.").WithCode(grpcmock.CodeInvalidConfig)
	}
	if srv.Spec.Type == sioserver.TypeName && config.SocketIO == nil {
		return ServerDTO{}, errs.New(errs.InvalidArgument, "The Socket.IO server's settings are missing.").WithCode(sioserver.CodeInvalidConfig)
	}
	if srv.Spec.Type == graphqlmock.TypeName && config.GraphQLMock == nil {
		return ServerDTO{}, errs.New(errs.InvalidArgument, "The settings of the GraphQL mock server are missing.").WithCode(graphqlmock.CodeInvalidConfig)
	}
	old := srv.Spec.Config
	srv, err = s.manager.UpdateConfig(id, configToCore(srv.Spec.Type, config))
	if err != nil {
		return ServerDTO{}, err
	}
	if err := s.persist(ctx, srv.Spec); err != nil {
		_, _ = s.manager.UpdateConfig(id, old)
		return ServerDTO{}, err
	}
	return s.serverDTO(srv), nil
}

// Start starts the server id and returns it running. When it cannot
// listen (port in use: listen.address_in_use; LAN address without
// allowLan: listen.lan_not_allowed) the error is returned and the server
// is left "failed" with that error. A saved server of the open project
// first takes the definition its folder holds now (SRV-04).
func (s *ServersService) Start(ctx context.Context, id string) (ServerDTO, error) {
	if err := s.reloadSaved(ctx, id); err != nil {
		return ServerDTO{}, err
	}
	srv, err := s.manager.Start(ctx, id)
	if err != nil {
		return ServerDTO{}, err
	}
	return s.serverDTO(srv), nil
}

// Stop stops the server id: requests in progress get a few seconds, then
// connections are closed.
func (s *ServersService) Stop(ctx context.Context, id string) (ServerDTO, error) {
	srv, err := s.manager.Stop(ctx, id)
	if err != nil {
		return ServerDTO{}, err
	}
	return s.serverDTO(srv), nil
}

// Remove stops the server id if needed and deletes it with its traffic. A
// saved server of the open project is removed from its folder's
// _folder.json too (when that fails, nothing is removed); one of another
// project stays in its project's file.
func (s *ServersService) Remove(ctx context.Context, id string) error {
	s.projMu.Lock()
	defer s.projMu.Unlock()
	srv, err := s.manager.Get(id)
	if err != nil {
		return err
	}
	if err := s.forget(ctx, srv); err != nil {
		return err
	}
	return s.manager.Remove(ctx, id)
}

// Traffic returns the most recent traffic of the server id kept in Go, for
// a UI that was reloaded: up to 1,000 events, oldest first.
func (s *ServersService) Traffic(ctx context.Context, id string) ([]TrafficDTO, error) {
	if err := interrupted(ctx, "Loading the traffic"); err != nil {
		return nil, err
	}
	events, err := s.manager.Traffic(id)
	if err != nil {
		return nil, err
	}
	return toTrafficDTOs(events), nil
}

// ClearTraffic forgets the recent traffic of the server id kept in Go,
// and the requests a webhook receiver keeps.
func (s *ServersService) ClearTraffic(ctx context.Context, id string) error {
	if err := interrupted(ctx, "Clearing the traffic"); err != nil {
		return err
	}
	if err := s.manager.ClearTraffic(id); err != nil {
		return err
	}
	if srv, err := s.manager.Get(id); err == nil && srv.Spec.Type == webhook.TypeName {
		s.captures.mark(id)
	}
	return nil
}

// Clients returns the clients connected to the WebSocket server id, oldest
// first; [] when it is not running. A server of another type is
// servers.not_websocket.
func (s *ServersService) Clients(ctx context.Context, id string) ([]WSClientDTO, error) {
	if err := interrupted(ctx, "Loading the clients"); err != nil {
		return nil, err
	}
	if _, err := s.webSocketServer(id); err != nil {
		return nil, err
	}
	return s.clientsOf(id), nil
}

// SendToClient sends one text or binary message to a client of a running
// WebSocket server (wsserver.client_not_found when it left,
// servers.not_running, wsserver.send_failed).
func (s *ServersService) SendToClient(ctx context.Context, in WSSendInputDTO) error {
	if err := interrupted(ctx, "Sending the message"); err != nil {
		return err
	}
	hub, err := s.hub(in.ServerID)
	if err != nil {
		return err
	}
	data, err := in.Message.payload()
	if err != nil {
		return err
	}
	return hub.Send(in.ClientID, in.Message.Binary, data)
}

// Broadcast sends one message to every client of a running WebSocket
// server and counts the clients it reached.
func (s *ServersService) Broadcast(ctx context.Context, in WSBroadcastInputDTO) (WSBroadcastResultDTO, error) {
	if err := interrupted(ctx, "Sending the message"); err != nil {
		return WSBroadcastResultDTO{}, err
	}
	hub, err := s.hub(in.ServerID)
	if err != nil {
		return WSBroadcastResultDTO{}, err
	}
	data, err := in.Message.payload()
	if err != nil {
		return WSBroadcastResultDTO{}, err
	}
	if !in.Message.Binary && !utf8.Valid(data) {
		return WSBroadcastResultDTO{}, invalidMessage("A text message must be valid UTF-8.")
	}
	res := hub.Broadcast(in.Message.Binary, data)
	return WSBroadcastResultDTO{Sent: res.Sent, Failed: res.Failed}, nil
}

// DisconnectClient closes a client's connection with a close code (0 means
// 1000) and reason, waits a moment for the client's close frame and returns
// once it is gone. An invalid code or reason is wsframe.invalid_argument.
func (s *ServersService) DisconnectClient(ctx context.Context, in WSDisconnectInputDTO) error {
	hub, err := s.hub(in.ServerID)
	if err != nil {
		return err
	}
	code := in.Code
	if code == 0 {
		code = int(wsframe.CloseNormalClosure)
	}
	return hub.Disconnect(ctx, in.ClientID, code, in.Reason)
}

// webSocketServer returns the server id when it is a WebSocket server.
func (s *ServersService) webSocketServer(id string) (core.Server, error) {
	srv, err := s.manager.Get(id)
	if err != nil {
		return core.Server{}, err
	}
	if srv.Spec.Type != wsserver.TypeName {
		return core.Server{}, errs.New(errs.InvalidArgument, "This server is not a WebSocket server.").
			WithCode(CodeNotWebSocket).WithDetail("id", id)
	}
	return srv, nil
}

// hub returns the client operations of the running WebSocket server id.
func (s *ServersService) hub(id string) (wsserver.Hub, error) {
	if _, err := s.webSocketServer(id); err != nil {
		return nil, err
	}
	inst, err := s.manager.Running(id)
	if err != nil {
		return nil, err
	}
	hub, ok := inst.(wsserver.Hub)
	if !ok {
		return nil, errs.New(errs.Internal, "This server cannot list its clients.").WithCode(CodeNotWebSocket)
	}
	return hub, nil
}

// clientsOf lists the clients of the WebSocket server id, [] when it does
// not run.
func (s *ServersService) clientsOf(id string) []WSClientDTO {
	inst, err := s.manager.Running(id)
	if err != nil {
		return []WSClientDTO{}
	}
	hub, ok := inst.(wsserver.Hub)
	if !ok {
		return []WSClientDTO{}
	}
	return toWSClientDTOs(hub.Clients())
}

func (s *ServersService) publishClients(id string) {
	if _, err := s.manager.Get(id); err != nil {
		return // removed
	}
	s.emit(EventClients, ClientsEventDTO{ServerID: id, Clients: s.clientsOf(id)})
}

// publishChanged is the manager's OnChange: it runs in change order, so
// the last servers:changed event has the latest list.
func (s *ServersService) publishChanged(core.Server, bool) {
	s.emit(EventChanged, ChangedEventDTO{Servers: s.serverDTOs(s.manager.List())})
}

func (s *ServersService) flushTraffic(batch []core.Event) {
	s.emit(EventTraffic, TrafficEventDTO{Events: toTrafficDTOs(batch)})
}

func interrupted(ctx context.Context, what string) error {
	if err := ctx.Err(); err != nil {
		return errs.Wrap(err, errs.KindOf(err), what+" was interrupted.")
	}
	return nil
}
