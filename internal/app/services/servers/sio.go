package servers

import (
	"context"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/servers/sioserver"
)

// The Socket.IO server's socket operations (SSRV-03).

// CodeNotSocketIO: a socket operation on a server that is not a
// Socket.IO server.
const CodeNotSocketIO = "servers.not_socketio"

// Sockets returns the sockets connected to the Socket.IO server id, by
// client and namespace; [] when it is not running. A server of another
// type is servers.not_socketio.
func (s *ServersService) Sockets(ctx context.Context, id string) ([]SIOSocketDTO, error) {
	if err := interrupted(ctx, "Loading the sockets"); err != nil {
		return nil, err
	}
	if _, err := s.typed(id, sioserver.TypeName, CodeNotSocketIO, "This server is not a Socket.IO server."); err != nil {
		return nil, err
	}
	return s.socketsOf(id), nil
}

// EmitToSockets emits an event to one socket of a running Socket.IO
// server, to the sockets in a room, or to every socket. The arguments are
// JSON text (socketio.invalid_args with the offset when they are not);
// dynamic values such as {{$timestamp}} are rendered. A reserved or empty
// event name is sioserver.invalid_event, a socket that left
// sioserver.socket_not_found, a stopped server servers.not_running.
func (s *ServersService) EmitToSockets(ctx context.Context, in SIOEmitInputDTO) (SIOEmitResultDTO, error) {
	if err := interrupted(ctx, "Emitting the event"); err != nil {
		return SIOEmitResultDTO{}, err
	}
	hub, err := s.sioHub(in.ServerID)
	if err != nil {
		return SIOEmitResultDTO{}, err
	}
	args, err := sioserver.ParseArgs(in.Args, nil)
	if err != nil {
		return SIOEmitResultDTO{}, err
	}
	res, err := hub.Emit(sioserver.Target{SocketID: in.SocketID, Room: in.Room}, in.Event, args, in.Ack)
	if err != nil {
		return SIOEmitResultDTO{}, err
	}
	return SIOEmitResultDTO{Sent: res.Sent, Failed: res.Failed}, nil
}

// DisconnectSocket sends DISCONNECT to a socket, or closes its client's
// whole connection with closeClient.
func (s *ServersService) DisconnectSocket(ctx context.Context, in SIODisconnectInputDTO) error {
	if err := interrupted(ctx, "Disconnecting the socket"); err != nil {
		return err
	}
	hub, err := s.sioHub(in.ServerID)
	if err != nil {
		return err
	}
	return hub.Disconnect(in.SocketID, in.CloseClient)
}

// SetSocketRooms puts a socket in exactly the rooms given
// (sioserver.invalid_room for a name with a comma or line break).
func (s *ServersService) SetSocketRooms(ctx context.Context, in SIORoomsInputDTO) error {
	if err := interrupted(ctx, "Changing the rooms"); err != nil {
		return err
	}
	hub, err := s.sioHub(in.ServerID)
	if err != nil {
		return err
	}
	return hub.SetRooms(in.SocketID, in.Rooms)
}

func (s *ServersService) sioHub(id string) (sioserver.Hub, error) {
	if _, err := s.typed(id, sioserver.TypeName, CodeNotSocketIO, "This server is not a Socket.IO server."); err != nil {
		return nil, err
	}
	inst, err := s.manager.Running(id)
	if err != nil {
		return nil, err
	}
	hub, ok := inst.(sioserver.Hub)
	if !ok {
		return nil, errs.New(errs.Internal, "This server cannot list its sockets.").WithCode(CodeNotSocketIO)
	}
	return hub, nil
}

// socketsOf lists the sockets of the Socket.IO server id, [] when it does
// not run.
func (s *ServersService) socketsOf(id string) []SIOSocketDTO {
	inst, err := s.manager.Running(id)
	if err != nil {
		return []SIOSocketDTO{}
	}
	hub, ok := inst.(sioserver.Hub)
	if !ok {
		return []SIOSocketDTO{}
	}
	return toSIOSocketDTOs(hub.Sockets())
}

func (s *ServersService) publishSockets(id string) {
	if _, err := s.manager.Get(id); err != nil {
		return // removed
	}
	s.emit(EventSockets, SocketsEventDTO{ServerID: id, Sockets: s.socketsOf(id)})
}
