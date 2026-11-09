package sseserver

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// Limits and defaults of a configuration.
const (
	// AnyPath as Config.Path accepts subscribers on every path.
	AnyPath = "*"
	// DefaultPath is the path of a new server.
	DefaultPath = "/events"
	// MaxPathBytes is the longest path.
	MaxPathBytes = 2048
	// DefaultKeepAliveSec is the keep-alive interval of a new server.
	DefaultKeepAliveSec = 15
	// MaxKeepAliveSec is the longest keep-alive interval (1 hour).
	MaxKeepAliveSec = 3600
	// MaxRetryMs is the largest retry (1 hour).
	MaxRetryMs = 3_600_000
	// MaxTimers is the most timers a server has.
	MaxTimers = 50
	// MinIntervalMs and MaxIntervalMs bound a timer's interval (100 ms to
	// 24 hours).
	MinIntervalMs = 100
	MaxIntervalMs = 86_400_000
	// MaxDataBytes bounds an event's data (1 MiB).
	MaxDataBytes = 1 << 20
	// MaxFieldBytes bounds an event's type and ID.
	MaxFieldBytes = 1024
)

// CORS modes of Config.AllowOrigin.
const (
	// OriginAny answers every origin: the request's Origin is sent back
	// with Access-Control-Allow-Credentials, so EventSource with
	// withCredentials works too.
	OriginAny = "*"
)

// Error codes of NormalizeConfig and of a message.
const (
	CodeInvalidConfig  = "sseserver.invalid_config"
	CodeInvalidMessage = "sseserver.invalid_message"
)

// Config is the configuration of an SSE server (Spec.Config).
type Config struct {
	// Path is where subscribers connect ("/events"), matched exactly
	// against the request path (the query is ignored); AnyPath accepts
	// every path.
	Path string
	// AllowOrigin adds CORS headers so a page on another origin can
	// subscribe: "" sends none, OriginAny allows every origin, and an
	// origin ("http://localhost:3000") allows that one.
	AllowOrigin string
	// KeepAliveSec sends a comment line to every subscriber that often, so
	// proxies and clients keep the stream open; 0 sends none.
	KeepAliveSec int
	// RetryMs is sent as "retry:" when a subscriber connects (how long an
	// EventSource waits before it reconnects); 0 sends none.
	RetryMs int
	// AutoID gives every event sent without an ID the next number
	// (1, 2, … per run), so clients can resume with Last-Event-ID.
	AutoID bool
	// Timers send an event to every subscriber at an interval.
	Timers []Timer
}

// Timer sends one event to every subscriber every IntervalMs. The event's
// type, data and ID may hold dynamic values ({{$timestamp}}), rendered at
// every tick.
type Timer struct {
	Enabled    bool
	Event      string
	Data       string
	ID         string
	IntervalMs int
}

// DefaultConfig is the configuration of a new SSE server.
func DefaultConfig() Config {
	return Config{Path: DefaultPath, KeepAliveSec: DefaultKeepAliveSec, Timers: []Timer{}}
}

// Normalize checks c and returns a copy that shares nothing with it: the
// path and origin trimmed, an empty path set to DefaultPath.
func Normalize(c Config) (Config, error) {
	out := Config{
		Path:         strings.TrimSpace(c.Path),
		AllowOrigin:  strings.TrimSpace(c.AllowOrigin),
		KeepAliveSec: c.KeepAliveSec,
		RetryMs:      c.RetryMs,
		AutoID:       c.AutoID,
		Timers:       make([]Timer, 0, len(c.Timers)),
	}
	if out.Path == "" {
		out.Path = DefaultPath
	}
	if out.Path != AnyPath {
		if !strings.HasPrefix(out.Path, "/") {
			return Config{}, invalidConfig("path", "The path must start with / (or be * for any path).")
		}
		if len(out.Path) > MaxPathBytes {
			return Config{}, invalidConfig("path", fmt.Sprintf("The path is longer than %d bytes.", MaxPathBytes))
		}
		for _, r := range out.Path {
			if r <= ' ' || r == 0x7f || r == '?' || r == '#' {
				return Config{}, invalidConfig("path", "The path cannot contain spaces, control characters, ? or #.")
			}
		}
	}
	if o := out.AllowOrigin; o != "" && o != OriginAny {
		if strings.ContainsAny(o, " \t\r\n,") || !strings.Contains(o, "://") {
			return Config{}, invalidConfig("allowOrigin", "Enter one origin such as http://localhost:3000, or * for any origin.")
		}
		out.AllowOrigin = strings.TrimSuffix(o, "/")
	}
	if c.KeepAliveSec < 0 || c.KeepAliveSec > MaxKeepAliveSec {
		return Config{}, invalidConfig("keepAliveSec", fmt.Sprintf("The keep-alive interval must be from 0 (none) to %d seconds.", MaxKeepAliveSec))
	}
	if c.RetryMs < 0 || c.RetryMs > MaxRetryMs {
		return Config{}, invalidConfig("retryMs", fmt.Sprintf("The retry must be from 0 (none) to %d ms.", MaxRetryMs))
	}
	if len(c.Timers) > MaxTimers {
		return Config{}, invalidConfig("timers", fmt.Sprintf("An SSE server has at most %d timers.", MaxTimers))
	}
	for i, t := range c.Timers {
		if t.IntervalMs < MinIntervalMs || t.IntervalMs > MaxIntervalMs {
			return Config{}, invalidTimer(i, "intervalMs", fmt.Sprintf("Timer %d: the interval must be from %d ms to 24 hours.", i+1, MinIntervalMs))
		}
		if e := checkMessage(Message{Event: t.Event, Data: t.Data, ID: t.ID}); e != nil {
			return Config{}, invalidTimer(i, e.Details["field"], fmt.Sprintf("Timer %d: %s", i+1, e.Message))
		}
		out.Timers = append(out.Timers, t)
	}
	return out, nil
}

// CheckMessage checks the fields of an event as written, before its
// dynamic values are rendered: the type and ID are one line each (an ID
// without U+0000, which clients ignore), the data at most MaxDataBytes, all
// UTF-8. Errors are sseserver.invalid_message with the detail field.
func CheckMessage(m Message) error {
	if e := checkMessage(m); e != nil {
		return e
	}
	return nil
}

func checkMessage(m Message) *errs.Error {
	for _, f := range []struct{ name, value string }{{"event", m.Event}, {"id", m.ID}, {"data", m.Data}} {
		if !utf8.ValidString(f.value) {
			return invalidMessage(f.name, "The "+fieldLabel(f.name)+" must be valid UTF-8 text.")
		}
	}
	if strings.ContainsAny(m.Event, "\r\n") || len(m.Event) > MaxFieldBytes {
		return invalidMessage("event", fmt.Sprintf("The event type must be one line of at most %d bytes.", MaxFieldBytes))
	}
	if strings.ContainsAny(m.ID, "\r\n\x00") || len(m.ID) > MaxFieldBytes {
		return invalidMessage("id", fmt.Sprintf("The ID must be one line of at most %d bytes, without NUL.", MaxFieldBytes))
	}
	if len(m.Data) > MaxDataBytes {
		return invalidMessage("data", fmt.Sprintf("The data is larger than %d MiB.", MaxDataBytes>>20))
	}
	if m.RetryMs < 0 || m.RetryMs > MaxRetryMs {
		return invalidMessage("retryMs", fmt.Sprintf("The retry must be from 0 (none) to %d ms.", MaxRetryMs))
	}
	return nil
}

func fieldLabel(name string) string {
	if name == "event" {
		return "event type"
	}
	if name == "id" {
		return "ID"
	}
	return name
}

func invalidConfig(field, msg string) *errs.Error {
	return errs.New(errs.InvalidArgument, msg).WithCode(CodeInvalidConfig).WithDetail("field", field)
}

func invalidTimer(i int, field, msg string) *errs.Error {
	return errs.New(errs.InvalidArgument, msg).WithCode(CodeInvalidConfig).
		WithDetail("field", "timers").WithDetail("timerIndex", fmt.Sprint(i)).WithDetail("timerField", field)
}

func invalidMessage(field, msg string) *errs.Error {
	return errs.New(errs.InvalidArgument, msg).WithCode(CodeInvalidMessage).WithDetail("field", field)
}
