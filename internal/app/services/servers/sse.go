package servers

import (
	"context"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	core "github.com/sorrelfox/postsonde/internal/core/servers"
	"github.com/sorrelfox/postsonde/internal/core/servers/sseserver"
	"github.com/sorrelfox/postsonde/internal/core/servers/udpserver"
)

// The SSE server's subscriber operations (SSRV-04) and the UDP server's
// peer operations (SSRV-06).

// Error codes: an SSE or UDP operation on a server of another type.
const (
	CodeNotSSE = "servers.not_sse"
	CodeNotUDP = "servers.not_udp"
)

// Subscribers returns the subscribers of the SSE server id, oldest first;
// [] when it is not running. A server of another type is servers.not_sse.
func (s *ServersService) Subscribers(ctx context.Context, id string) ([]SSESubscriberDTO, error) {
	if err := interrupted(ctx, "Loading the subscribers"); err != nil {
		return nil, err
	}
	if _, err := s.typed(id, sseserver.TypeName, CodeNotSSE, "This server is not an SSE server."); err != nil {
		return nil, err
	}
	return s.subscribersOf(id), nil
}

// SendEvent sends an event to one subscriber of a running SSE server, or
// to every subscriber when subscriberId is "", after rendering its dynamic
// values ({{$timestamp}}). A bad type, ID or data is
// sseserver.invalid_message (detail field); a subscriber that left
// sseserver.subscriber_not_found; servers.not_running when stopped.
func (s *ServersService) SendEvent(ctx context.Context, in SSESendInputDTO) (SSESendResultDTO, error) {
	if err := interrupted(ctx, "Sending the event"); err != nil {
		return SSESendResultDTO{}, err
	}
	hub, err := s.sseHub(in.ServerID)
	if err != nil {
		return SSESendResultDTO{}, err
	}
	res, err := hub.Send(in.SubscriberID, sseserver.Message{Event: in.Event, Data: in.Data, ID: in.ID, RetryMs: in.RetryMs})
	if err != nil {
		return SSESendResultDTO{}, err
	}
	return SSESendResultDTO{Sent: res.Sent, Failed: res.Failed, ID: res.ID}, nil
}

// DisconnectSubscriber ends a subscriber's stream and resolves once it is
// gone. A browser's EventSource reconnects after its retry time.
func (s *ServersService) DisconnectSubscriber(ctx context.Context, in SSEDisconnectInputDTO) error {
	hub, err := s.sseHub(in.ServerID)
	if err != nil {
		return err
	}
	return hub.Disconnect(in.SubscriberID)
}

// Peers returns the peers the UDP server id heard from recently, most
// recent first; [] when it is not running. A server of another type is
// servers.not_udp.
func (s *ServersService) Peers(ctx context.Context, id string) ([]UDPPeerDTO, error) {
	if err := interrupted(ctx, "Loading the peers"); err != nil {
		return nil, err
	}
	if _, err := s.typed(id, udpserver.TypeName, CodeNotUDP, "This server is not a UDP server."); err != nil {
		return nil, err
	}
	return s.peersOf(id), nil
}

// SendDatagram sends text (with its line ending) or hex bytes as one
// datagram to a recent peer of a running UDP server and returns its size.
// Bad hex is a hexbytes.* error, an unknown mode or line ending
// tcpserver.invalid_message (the TCP server's composer rules), a peer not
// heard from udpserver.peer_not_found, more than 65,507 bytes
// udpserver.too_large.
func (s *ServersService) SendDatagram(ctx context.Context, in UDPSendInputDTO) (UDPSendResultDTO, error) {
	if err := interrupted(ctx, "Sending"); err != nil {
		return UDPSendResultDTO{}, err
	}
	hub, err := s.udpHub(in.ServerID)
	if err != nil {
		return UDPSendResultDTO{}, err
	}
	data, err := TCPSendInputDTO{Mode: in.Mode, Text: in.Text, LineEnding: in.LineEnding}.payload()
	if err != nil {
		return UDPSendResultDTO{}, err
	}
	if err := hub.Send(in.Peer, data); err != nil {
		return UDPSendResultDTO{}, err
	}
	return UDPSendResultDTO{Bytes: len(data)}, nil
}

// typed returns the server id when it is of type typ, else an
// invalid_argument error with code.
func (s *ServersService) typed(id, typ, code, msg string) (core.Server, error) {
	srv, err := s.manager.Get(id)
	if err != nil {
		return core.Server{}, err
	}
	if srv.Spec.Type != typ {
		return core.Server{}, errs.New(errs.InvalidArgument, msg).WithCode(code).WithDetail("id", id)
	}
	return srv, nil
}

func (s *ServersService) sseHub(id string) (sseserver.Hub, error) {
	if _, err := s.typed(id, sseserver.TypeName, CodeNotSSE, "This server is not an SSE server."); err != nil {
		return nil, err
	}
	inst, err := s.manager.Running(id)
	if err != nil {
		return nil, err
	}
	hub, ok := inst.(sseserver.Hub)
	if !ok {
		return nil, errs.New(errs.Internal, "This server cannot list its subscribers.").WithCode(CodeNotSSE)
	}
	return hub, nil
}

func (s *ServersService) udpHub(id string) (udpserver.Hub, error) {
	if _, err := s.typed(id, udpserver.TypeName, CodeNotUDP, "This server is not a UDP server."); err != nil {
		return nil, err
	}
	inst, err := s.manager.Running(id)
	if err != nil {
		return nil, err
	}
	hub, ok := inst.(udpserver.Hub)
	if !ok {
		return nil, errs.New(errs.Internal, "This server cannot list its peers.").WithCode(CodeNotUDP)
	}
	return hub, nil
}

// subscribersOf lists the subscribers of the SSE server id, [] when it
// does not run.
func (s *ServersService) subscribersOf(id string) []SSESubscriberDTO {
	inst, err := s.manager.Running(id)
	if err != nil {
		return []SSESubscriberDTO{}
	}
	hub, ok := inst.(sseserver.Hub)
	if !ok {
		return []SSESubscriberDTO{}
	}
	return toSSESubscriberDTOs(hub.Subscribers())
}

// peersOf lists the recent peers of the UDP server id, [] when it does not
// run.
func (s *ServersService) peersOf(id string) []UDPPeerDTO {
	inst, err := s.manager.Running(id)
	if err != nil {
		return []UDPPeerDTO{}
	}
	hub, ok := inst.(udpserver.Hub)
	if !ok {
		return []UDPPeerDTO{}
	}
	return toUDPPeerDTOs(hub.Peers())
}

func (s *ServersService) publishSubscribers(id string) {
	if _, err := s.manager.Get(id); err != nil {
		return // removed
	}
	s.emit(EventSubscribers, SubscribersEventDTO{ServerID: id, Subscribers: s.subscribersOf(id)})
}

func (s *ServersService) publishPeers(id string) {
	if _, err := s.manager.Get(id); err != nil {
		return // removed
	}
	s.emit(EventPeers, PeersEventDTO{ServerID: id, Peers: s.peersOf(id)})
}
