package servers

import (
	"context"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	core "github.com/sorrelfox/postsonde/internal/core/servers"
	"github.com/sorrelfox/postsonde/internal/core/servers/tcpserver"
)

// The TCP server's connection operations (SSRV-05).

// CodeNotTCP: a connection operation on a server that is not a TCP server.
const CodeNotTCP = "servers.not_tcp"

// Connections returns the open connections of the TCP server id, oldest
// first; [] when it is not running. A server of another type is
// servers.not_tcp.
func (s *ServersService) Connections(ctx context.Context, id string) ([]TCPConnectionDTO, error) {
	if err := interrupted(ctx, "Loading the connections"); err != nil {
		return nil, err
	}
	if _, err := s.tcpServer(id); err != nil {
		return nil, err
	}
	return s.connectionsOf(id), nil
}

// SendToConnection sends text (with its line ending) or hex bytes to a
// connection of a running TCP server and returns how many bytes were sent.
// Bad hex is a hexbytes.* error, an unknown mode or line ending
// tcpserver.invalid_message, nothing to send tcpserver.empty_message; a
// connection that closed is tcpserver.connection_not_found, a failed write
// tcpserver.send_failed (the connection is closed then).
func (s *ServersService) SendToConnection(ctx context.Context, in TCPSendInputDTO) (TCPSendResultDTO, error) {
	if err := interrupted(ctx, "Sending"); err != nil {
		return TCPSendResultDTO{}, err
	}
	hub, err := s.tcpHub(in.ServerID)
	if err != nil {
		return TCPSendResultDTO{}, err
	}
	data, err := in.payload()
	if err != nil {
		return TCPSendResultDTO{}, err
	}
	if err := hub.Send(in.ConnectionID, data); err != nil {
		return TCPSendResultDTO{}, err
	}
	return TCPSendResultDTO{Bytes: len(data)}, nil
}

// CloseConnection closes a connection of a running TCP server and resolves
// once it is closed and logged.
func (s *ServersService) CloseConnection(ctx context.Context, in TCPCloseInputDTO) error {
	hub, err := s.tcpHub(in.ServerID)
	if err != nil {
		return err
	}
	return hub.Close(in.ConnectionID)
}

// tcpServer returns the server id when it is a TCP server.
func (s *ServersService) tcpServer(id string) (core.Server, error) {
	srv, err := s.manager.Get(id)
	if err != nil {
		return core.Server{}, err
	}
	if srv.Spec.Type != tcpserver.TypeName {
		return core.Server{}, errs.New(errs.InvalidArgument, "This server is not a TCP server.").
			WithCode(CodeNotTCP).WithDetail("id", id)
	}
	return srv, nil
}

// tcpHub returns the connection operations of the running TCP server id.
func (s *ServersService) tcpHub(id string) (tcpserver.Hub, error) {
	if _, err := s.tcpServer(id); err != nil {
		return nil, err
	}
	inst, err := s.manager.Running(id)
	if err != nil {
		return nil, err
	}
	hub, ok := inst.(tcpserver.Hub)
	if !ok {
		return nil, errs.New(errs.Internal, "This server cannot list its connections.").WithCode(CodeNotTCP)
	}
	return hub, nil
}

// connectionsOf lists the connections of the TCP server id, [] when it
// does not run.
func (s *ServersService) connectionsOf(id string) []TCPConnectionDTO {
	inst, err := s.manager.Running(id)
	if err != nil {
		return []TCPConnectionDTO{}
	}
	hub, ok := inst.(tcpserver.Hub)
	if !ok {
		return []TCPConnectionDTO{}
	}
	return toTCPConnectionDTOs(hub.Connections())
}

func (s *ServersService) publishConnections(id string) {
	if _, err := s.manager.Get(id); err != nil {
		return // removed
	}
	s.emit(EventConnections, ConnectionsEventDTO{ServerID: id, Connections: s.connectionsOf(id)})
}
