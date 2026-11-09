package servers

import (
	"encoding/base64"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/apperr"
	core "github.com/sorrelfox/postsonde/internal/core/servers"
)

// DTOs are the only types ServersService methods take or return. See
// docs/bindings.md and, for the fields, docs/servers.md.

// TypeDTO is a kind of server the app can run.
type TypeDTO struct {
	// Name is the value of CreateInputDTO.Type ("echo_http").
	Name  string `json:"name"`
	Label string `json:"label"`
	// DefaultConfig is the configuration a new server of this type gets
	// when CreateInputDTO.Config has none (the mock's example route).
	DefaultConfig ConfigDTO `json:"defaultConfig"`
}

// CreateInputDTO defines a new server.
type CreateInputDTO struct {
	Type string `json:"type"`
	// Name is a label; "" uses the type's label.
	Name string `json:"name"`
	// Host is "" or "localhost" (loopback, IPv4 and IPv6) or an IP address.
	// Anything but loopback needs AllowLAN.
	Host string `json:"host"`
	// Port is 1–65535, or 0 for a free port picked at every start.
	Port     int  `json:"port"`
	AllowLAN bool `json:"allowLan"`
	// TLS serves TLS (SRV-05); null serves plain text.
	TLS *ServerTLSInputDTO `json:"tls"`
	// Config is the type's configuration (mockHttp for mock_http,
	// webSocket for websocket, sse for sse, udp for udp, grpcMock for
	// grpc_mock, socketIo for socketio); a null field gets the type's
	// default.
	Config ConfigDTO `json:"config"`
}

// WarningDTO says a running server can be reached from other devices.
type WarningDTO struct {
	// Code is listen.lan_exposed or listen.all_interfaces_exposed.
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ServerDTO is one server and its state.
type ServerDTO struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	TypeLabel string `json:"typeLabel"`
	Name      string `json:"name"`
	// Host, Port and AllowLAN are as defined.
	Host     string `json:"host"`
	Port     int    `json:"port"`
	AllowLAN bool   `json:"allowLan"`
	// TLS is how it serves TLS (SRV-05), null for plain text.
	TLS *ServerTLSDTO `json:"tls"`
	// Certificate is the certificate it presents while it runs with TLS,
	// else null.
	Certificate *ServerCertDTO `json:"certificate"`
	// State is "stopped", "starting", "running", "stopping" or "failed".
	State string `json:"state"`
	// Addresses are the bound addresses ("127.0.0.1:8080", "[::1]:8080")
	// while running, else []. BoundPort is their port, else 0.
	Addresses []string `json:"addresses"`
	BoundPort int      `json:"boundPort"`
	// Scope is "loopback", "lan" or "all_interfaces" while running, else "".
	Scope   string      `json:"scope"`
	Warning *WarningDTO `json:"warning"`
	// StartedAt is when it last started running, StoppedAt when it last
	// stopped or failed; RFC 3339 in UTC, "" when it never did.
	StartedAt string `json:"startedAt"`
	StoppedAt string `json:"stoppedAt"`
	// Error is why it failed, when State is "failed".
	Error *apperr.ErrorDTO `json:"error"`
	// Events is the number of traffic events so far (the last seq).
	Events int64 `json:"events"`
	// Config is the type's configuration, as normalized (route IDs filled
	// in); Diagnostics are its problems, [] when none.
	Config      ConfigDTO            `json:"config"`
	Diagnostics []RouteDiagnosticDTO `json:"diagnostics"`
	// Folder is the project folder whose _folder.json defines the server
	// (SRV-04), or null for a server made in the Servers view.
	Folder *FolderLinkDTO `json:"folder"`
}

// FolderLinkDTO names the project folder a saved server belongs to.
type FolderLinkDTO struct {
	WorkspaceID string `json:"workspaceId"`
	FolderID    string `json:"folderId"`
	// FolderName is the folder's name when the server was last loaded from
	// it, for a server whose project is no longer open.
	FolderName string `json:"folderName"`
}

// FolderServerInputDTO defines a server saved in a project folder.
type FolderServerInputDTO struct {
	// WorkspaceID is the open project's ID, FolderID the folder's.
	WorkspaceID string         `json:"workspaceId"`
	FolderID    string         `json:"folderId"`
	Server      CreateInputDTO `json:"server"`
}

// ServerSettingsDTO is what UpdateServer changes: the name, where the
// server listens and how it serves TLS.
type ServerSettingsDTO struct {
	Name     string `json:"name"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	AllowLAN bool   `json:"allowLan"`
	// TLS serves TLS (SRV-05); null serves plain text.
	TLS *ServerTLSInputDTO `json:"tls"`
}

// ProjectServersDTO is the result of SyncProject.
type ProjectServersDTO struct {
	// WorkspaceID is the open project's ID, "" when none is open.
	WorkspaceID string `json:"workspaceId"`
	// Problems are the saved servers that could not be defined.
	Problems []FolderServerProblemDTO `json:"problems"`
}

// FolderServerProblemDTO is a folder whose saved server could not be
// defined: an unknown type, a bad port or host, a bad configuration.
type FolderServerProblemDTO struct {
	FolderID   string          `json:"folderId"`
	FolderName string          `json:"folderName"`
	Error      apperr.ErrorDTO `json:"error"`
}

// HeaderDTO is one HTTP header line.
type HeaderDTO struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// HTTPDTO is the HTTP detail of a request or response event.
type HTTPDTO struct {
	Method string `json:"method"`
	Target string `json:"target"`
	Proto  string `json:"proto"`
	// Status is the response status, 0 on a request.
	Status  int         `json:"status"`
	Headers []HeaderDTO `json:"headers"`
	// Route is the mock route that answered (HTTP mock), else null.
	Route *RouteMatchDTO `json:"route"`
	// DelayMs is how long the mock held the response back.
	DelayMs int64 `json:"delayMs"`
}

// TrafficDTO is one entry of a server's traffic log.
type TrafficDTO struct {
	ServerID string `json:"serverId"`
	// Seq is 1, 2, 3, … per server.
	Seq  int64  `json:"seq"`
	Time string `json:"time"`
	// Peer is the remote address, "" for the server's own events.
	Peer   string `json:"peer"`
	ConnID int64  `json:"connId"`
	// Direction is "in", "out" or "system".
	Direction string `json:"direction"`
	// Kind is "request", "response", "message", "connection_open",
	// "connection_close", "started", "stopped" or "error".
	Kind    string `json:"kind"`
	Summary string `json:"summary"`
	// Text is the content when it is UTF-8; otherwise Binary is true and
	// DataBase64 holds it.
	Text       string `json:"text"`
	DataBase64 string `json:"dataBase64"`
	Binary     bool   `json:"binary"`
	// Size is the full content length; Truncated says the content was cut.
	Size      int      `json:"size"`
	Truncated bool     `json:"truncated"`
	HTTP      *HTTPDTO `json:"http"`
	// WS is the WebSocket detail (WebSocket server), else null.
	WS *WSDTO `json:"ws"`
	// TCP is the TCP server's detail, else null.
	TCP *TCPDTO `json:"tcp"`
	// SSE is the SSE server's detail, else left out.
	SSE *SSEDTO `json:"sse,omitempty"`
	// UDP is the UDP server's detail, else left out.
	UDP *UDPDTO `json:"udp,omitempty"`
	// GRPC is the gRPC mock server's detail, else left out.
	GRPC *GRPCDTO `json:"grpc,omitempty"`
	// SIO is the Socket.IO server's detail, else left out.
	SIO *SIODTO `json:"sio,omitempty"`
	// GraphQL is the detail of the GraphQL mock server, else left out.
	GraphQL *GraphQLDTO `json:"graphql,omitempty"`
	// Captured is set on a request the server keeps whole (webhook
	// receiver): ServersService.Capture and Replay find it by Seq.
	Captured bool `json:"captured"`
}

// ChangedEventDTO is the payload of servers:changed: every server.
type ChangedEventDTO struct {
	Servers []ServerDTO `json:"servers"`
}

// TrafficEventDTO is the payload of servers:traffic: a batch of events,
// possibly of several servers, in seq order per server.
type TrafficEventDTO struct {
	Events []TrafficDTO `json:"events"`
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func toTypeDTOs(types []core.Type) []TypeDTO {
	out := make([]TypeDTO, 0, len(types))
	for _, t := range types {
		out = append(out, TypeDTO{Name: t.Name(), Label: t.Label(), DefaultConfig: defaultConfigDTO(t)})
	}
	return out
}

// toCore returns the Spec of in; TLS is set by the caller (prepareTLS).
func (in CreateInputDTO) toCore() core.Spec {
	return core.Spec{Type: in.Type, Name: in.Name, Host: in.Host, Port: in.Port, AllowLAN: in.AllowLAN, Config: configToCore(in.Type, in.Config)}
}

func toServerDTO(s core.Server) ServerDTO {
	dto := ServerDTO{
		ID:          s.ID,
		Type:        s.Spec.Type,
		TypeLabel:   s.TypeLabel,
		Name:        s.Spec.Name,
		Host:        s.Spec.Host,
		Port:        s.Spec.Port,
		AllowLAN:    s.Spec.AllowLAN,
		TLS:         toTLSDTO(s.Spec.TLS),
		Certificate: toCertDTO(s.Cert),
		State:       string(s.State),
		Addresses:   make([]string, 0, len(s.Addrs)),
		BoundPort:   s.Port(),
		Scope:       string(s.Scope),
		StartedAt:   formatTime(s.StartedAt),
		StoppedAt:   formatTime(s.StoppedAt),
		Events:      int64(s.Events),
		Config:      toConfigDTO(s.Spec.Config),
		Diagnostics: toDiagnosticDTOs(s.Spec.Config),
	}
	for _, a := range s.Addrs {
		dto.Addresses = append(dto.Addresses, a.String())
	}
	if s.Warning != nil {
		dto.Warning = &WarningDTO{Code: s.Warning.Code, Message: s.Warning.Message}
	}
	if o := s.Spec.Owner; !o.IsZero() {
		dto.Folder = &FolderLinkDTO{WorkspaceID: o.Workspace, FolderID: o.Folder, FolderName: o.Label}
	}
	if s.Err != nil {
		e := apperr.ToDTO(s.Err)
		dto.Error = &e
	}
	return dto
}

func toServerDTOs(list []core.Server) []ServerDTO {
	out := make([]ServerDTO, 0, len(list))
	for _, s := range list {
		out = append(out, toServerDTO(s))
	}
	return out
}

func toTrafficDTO(e core.Event) TrafficDTO {
	dto := TrafficDTO{
		ServerID:  e.ServerID,
		Seq:       int64(e.Seq),
		Time:      formatTime(e.Time),
		Peer:      e.Peer,
		ConnID:    int64(e.ConnID),
		Direction: string(e.Direction),
		Kind:      string(e.Kind),
		Summary:   e.Summary,
		Binary:    e.Binary,
		Size:      e.Size,
		Truncated: e.Truncated,
		WS:        toWSDTO(e.WS),
		TCP:       toTCPDTO(e.TCP),
		SSE:       toSSEDTO(e.SSE),
		UDP:       toUDPDTO(e.UDP),
		GRPC:      toGRPCDTO(e.GRPC),
		SIO:       toSIODTO(e.SIO),
		GraphQL:   toGraphQLDTO(e.GraphQL),
		Captured:  e.Captured,
	}
	if e.Binary {
		dto.DataBase64 = base64.StdEncoding.EncodeToString(e.Content)
	} else {
		dto.Text = string(e.Content)
	}
	if e.HTTP != nil {
		h := &HTTPDTO{
			Method:  e.HTTP.Method,
			Target:  e.HTTP.Target,
			Proto:   e.HTTP.Proto,
			Status:  e.HTTP.Status,
			Headers: make([]HeaderDTO, 0, len(e.HTTP.Headers)),
			Route:   toRouteMatchDTO(e.HTTP.Route),
			DelayMs: e.HTTP.Delay.Milliseconds(),
		}
		for _, hd := range e.HTTP.Headers {
			h.Headers = append(h.Headers, HeaderDTO{Name: hd.Name, Value: hd.Value})
		}
		dto.HTTP = h
	}
	return dto
}

func toTrafficDTOs(events []core.Event) []TrafficDTO {
	out := make([]TrafficDTO, 0, len(events))
	for _, e := range events {
		out = append(out, toTrafficDTO(e))
	}
	return out
}
