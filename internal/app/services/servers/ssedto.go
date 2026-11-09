package servers

import (
	core "github.com/sorrelfox/postsonde/internal/core/servers"
	"github.com/sorrelfox/postsonde/internal/core/servers/sseserver"
)

// The DTOs of the SSE server (SSRV-04): its configuration, its subscribers
// and the event operations. See docs/servers.md.

// SSEConfigDTO is the configuration of an sse server.
type SSEConfigDTO struct {
	// Path is where subscribers connect ("/events"), or "*" for any path.
	Path string `json:"path"`
	// AllowOrigin is the origin allowed to subscribe from a browser page
	// (CORS): "" for none, "*" for any, or one origin.
	AllowOrigin string `json:"allowOrigin"`
	// KeepAliveSec is how often a keep-alive comment is sent; 0 for never.
	KeepAliveSec int `json:"keepAliveSec"`
	// RetryMs is sent to a new subscriber as its reconnection time; 0 for
	// none.
	RetryMs int `json:"retryMs"`
	// AutoID numbers the events sent without an ID.
	AutoID bool `json:"autoId"`
	// Timers send an event to every subscriber at an interval; null is
	// none.
	Timers []SSETimerDTO `json:"timers"`
}

// SSETimerDTO sends an event to every subscriber every intervalMs; event,
// data and id may hold dynamic values ({{$timestamp}}).
type SSETimerDTO struct {
	Enabled    bool   `json:"enabled"`
	Event      string `json:"event"`
	Data       string `json:"data"`
	ID         string `json:"id"`
	IntervalMs int    `json:"intervalMs"`
}

// SSESubscriberDTO is a subscriber connected to an SSE server.
type SSESubscriberDTO struct {
	// ID identifies the subscriber within one run of the server ("3"), as
	// in the traffic.
	ID     string `json:"id"`
	ConnID int64  `json:"connId"`
	Peer   string `json:"peer"`
	// Target is the path and query it subscribed to.
	Target string `json:"target"`
	// ConnectedAt is RFC 3339 in UTC.
	ConnectedAt string `json:"connectedAt"`
	// LastEventID is the Last-Event-ID it sent (a reconnecting
	// EventSource), "" when none.
	LastEventID string `json:"lastEventId"`
	Origin      string `json:"origin"`
	UserAgent   string `json:"userAgent"`
	EventsSent  int64  `json:"eventsSent"`
	BytesSent   int64  `json:"bytesSent"`
}

// SubscribersEventDTO is the payload of servers:subscribers: the
// subscribers of one SSE server now ([] once it stopped).
type SubscribersEventDTO struct {
	ServerID    string             `json:"serverId"`
	Subscribers []SSESubscriberDTO `json:"subscribers"`
}

// SSESendInputDTO sends an event to one subscriber, or to all when
// subscriberId is "".
type SSESendInputDTO struct {
	ServerID     string `json:"serverId"`
	SubscriberID string `json:"subscriberId"`
	// Event is the event type ("" for message), Data its data (several
	// lines allowed), ID its ID ("" for none, or the next number with
	// autoId); all three may hold dynamic values.
	Event string `json:"event"`
	Data  string `json:"data"`
	ID    string `json:"id"`
	// RetryMs is sent as the retry field when positive.
	RetryMs int `json:"retryMs"`
}

// SSESendResultDTO counts the subscribers an event reached and gives the
// ID it was sent with.
type SSESendResultDTO struct {
	Sent   int    `json:"sent"`
	Failed int    `json:"failed"`
	ID     string `json:"id"`
}

// SSEDisconnectInputDTO ends one subscriber's stream.
type SSEDisconnectInputDTO struct {
	ServerID     string `json:"serverId"`
	SubscriberID string `json:"subscriberId"`
}

// SSEDTO is the SSE server's detail of a traffic event.
type SSEDTO struct {
	// SubscriberID is the subscriber of a request, response, close or
	// error, or the one an event went to ("" for all).
	SubscriberID string `json:"subscriberId"`
	// Event, ID and RetryMs are the fields of an event sent.
	Event   string `json:"event"`
	ID      string `json:"id"`
	RetryMs int    `json:"retryMs"`
	// Recipients is how many subscribers an event was written to.
	Recipients int `json:"recipients"`
	// Source is "" (sent from the app) or "timer 2".
	Source string `json:"source"`
	// LastEventID is the Last-Event-ID a subscriber connected with.
	LastEventID string `json:"lastEventId"`
	// InitiatedBy is set on connection_close: "client" or "server".
	InitiatedBy string `json:"initiatedBy"`
}

func (c SSEConfigDTO) toCore() sseserver.Config {
	out := sseserver.Config{
		Path: c.Path, AllowOrigin: c.AllowOrigin, KeepAliveSec: c.KeepAliveSec, RetryMs: c.RetryMs, AutoID: c.AutoID,
		Timers: make([]sseserver.Timer, 0, len(c.Timers)),
	}
	for _, t := range c.Timers {
		out.Timers = append(out.Timers, sseserver.Timer{Enabled: t.Enabled, Event: t.Event, Data: t.Data, ID: t.ID, IntervalMs: t.IntervalMs})
	}
	return out
}

func toSSEConfigDTO(c sseserver.Config) *SSEConfigDTO {
	out := &SSEConfigDTO{
		Path: c.Path, AllowOrigin: c.AllowOrigin, KeepAliveSec: c.KeepAliveSec, RetryMs: c.RetryMs, AutoID: c.AutoID,
		Timers: make([]SSETimerDTO, 0, len(c.Timers)),
	}
	for _, t := range c.Timers {
		out.Timers = append(out.Timers, SSETimerDTO{Enabled: t.Enabled, Event: t.Event, Data: t.Data, ID: t.ID, IntervalMs: t.IntervalMs})
	}
	return out
}

func toSSESubscriberDTOs(list []sseserver.Subscriber) []SSESubscriberDTO {
	out := make([]SSESubscriberDTO, 0, len(list))
	for _, s := range list {
		out = append(out, SSESubscriberDTO{
			ID: s.ID, ConnID: int64(s.ConnID), Peer: s.Peer, Target: s.Target, ConnectedAt: formatTime(s.ConnectedAt),
			LastEventID: s.LastEventID, Origin: s.Origin, UserAgent: s.UserAgent,
			EventsSent: int64(s.EventsSent), BytesSent: int64(s.BytesSent),
		})
	}
	return out
}

func toSSEDTO(s *core.SSEInfo) *SSEDTO {
	if s == nil {
		return nil
	}
	return &SSEDTO{
		SubscriberID: s.SubscriberID, Event: s.Event, ID: s.ID, RetryMs: s.Retry, Recipients: s.Recipients,
		Source: s.Source, LastEventID: s.LastEventID, InitiatedBy: s.InitiatedBy,
	}
}
