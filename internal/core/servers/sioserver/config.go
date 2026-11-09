package sioserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/realtime/socketio"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

// Limits and defaults of a configuration.
const (
	// DefaultPath is the Engine.IO path of a new server, socket.io's.
	DefaultPath = "/socket.io/"
	// MaxPathBytes is the longest path.
	MaxPathBytes = 2048
	// MaxNamespaces is the most namespaces a server lists.
	MaxNamespaces = 100
	// MaxAuthBytes bounds the required auth members.
	MaxAuthBytes = 64 << 10
	// DefaultPingIntervalMs and DefaultPingTimeoutMs are the heartbeat of
	// a new server, socket.io v4's defaults.
	DefaultPingIntervalMs = 25_000
	DefaultPingTimeoutMs  = 20_000
	// MinHeartbeatMs and MaxHeartbeatMs bound both (100 ms to 1 hour).
	MinHeartbeatMs = 100
	MaxHeartbeatMs = 3_600_000
	// DefaultMaxPayload is the largest long-polling body and WebSocket
	// message accepted when Config.MaxPayload is 0: 1,000,000 bytes, as
	// socket.io's maxHttpBufferSize.
	DefaultMaxPayload = 1_000_000
	// MaxPayloadLimit is the largest Config.MaxPayload (256 MiB).
	MaxPayloadLimit = 256 << 20
	// MaxRules is the most auto-reply rules a server has.
	MaxRules = 100
	// MaxRulePatternBytes is the longest event name pattern.
	MaxRulePatternBytes = 1024
	// MaxRuleArgsBytes is the longest arguments text of a rule.
	MaxRuleArgsBytes = 1 << 20
	// MaxRuleDelayMs is the longest delay of a rule (one minute).
	MaxRuleDelayMs = 60_000
)

// Transports a server accepts (Config.Transports).
const (
	// TransportsAll ("") accepts long-polling and WebSocket, with the
	// upgrade from one to the other.
	TransportsAll = ""
	// TransportsWebSocket accepts WebSocket connections only.
	TransportsWebSocket = "websocket"
	// TransportsPolling accepts long-polling only (no upgrade offered).
	TransportsPolling = "polling"
)

// How an event that asks for an acknowledgement is answered when no rule
// answers it (Config.AckMode).
const (
	// AckEcho acknowledges with the event's own arguments (the default).
	AckEcho = "echo"
	// AckEmpty acknowledges with no arguments.
	AckEmpty = "empty"
	// AckNone never acknowledges, so the client's ack timeout can be
	// tested.
	AckNone = "none"
)

// How a rule matches an event's name (Rule.Match).
const (
	MatchExact    = "exact"
	MatchContains = "contains"
	MatchRegex    = "regex"
)

// OriginAny as Config.AllowOrigin allows every origin (CORS).
const OriginAny = "*"

// CodeInvalidConfig: a configuration NormalizeConfig refuses. Details:
// field, and for a rule ruleIndex (0-based) and ruleField.
const CodeInvalidConfig = "sioserver.invalid_config"

// Config is the configuration of a Socket.IO server (Spec.Config).
type Config struct {
	// Path is the Engine.IO path clients connect to ("/socket.io/"); a
	// trailing "/" is added. Requests to the path without it are served
	// too.
	Path string
	// Namespaces are the namespaces clients may connect to besides "/";
	// empty accepts every namespace. Another namespace is refused with
	// CONNECT_ERROR "Invalid namespace", as socket.io does.
	Namespaces []string
	// Auth is a JSON object, or "": a CONNECT is accepted only when its
	// auth payload has every member of it with the same value (other
	// members are allowed). Otherwise CONNECT_ERROR "Not authorized".
	Auth string
	// Transports is TransportsAll, TransportsWebSocket or
	// TransportsPolling.
	Transports string
	// AllowOrigin adds CORS headers to the long-polling responses so a
	// page on another origin can connect: "" none, OriginAny every origin,
	// or one origin.
	AllowOrigin string
	// PingIntervalMs and PingTimeoutMs are the heartbeat sent in the
	// handshake: the server pings every interval and closes a client that
	// does not answer within the timeout.
	PingIntervalMs int
	PingTimeoutMs  int
	// MaxPayload is the largest long-polling body and WebSocket message
	// accepted, in bytes (sent in the handshake); 0 means
	// DefaultMaxPayload.
	MaxPayload int
	// Echo emits an event that no rule answers back to the socket that
	// sent it, with the same name and arguments.
	Echo bool
	// AckMode answers the events that ask for an acknowledgement when no
	// rule answers them: AckEcho (the default for ""), AckEmpty or
	// AckNone.
	AckMode string
	// Rules are the auto-reply rules, in order: the first enabled rule
	// whose pattern matches an event's name answers it.
	Rules []Rule
}

// Rule is an auto-reply rule: an event whose name matches Pattern is
// acknowledged with Ack (when it asks for an acknowledgement) and answered
// with the event Emit, after DelayMs. Ack and EmitArgs are arguments as
// JSON text (an array holds one argument per element; {"$base64": "…"}
// is binary) and may hold dynamic values such as {{$timestamp}}.
type Rule struct {
	Enabled bool
	// Match is MatchExact (the default for ""), MatchContains or
	// MatchRegex (Go RE2, anywhere in the name).
	Match   string
	Pattern string
	// Ack are the acknowledgement's arguments; "" acknowledges with none.
	Ack string
	// Emit is the event sent back to the socket, "" for none, with
	// EmitArgs.
	Emit     string
	EmitArgs string
	// DelayMs holds the answer back (0 to MaxRuleDelayMs).
	DelayMs int
}

// DefaultConfig is the configuration of a new Socket.IO server.
func DefaultConfig() Config {
	return Config{
		Path: DefaultPath, Namespaces: []string{}, PingIntervalMs: DefaultPingIntervalMs, PingTimeoutMs: DefaultPingTimeoutMs,
		MaxPayload: DefaultMaxPayload, AckMode: AckEcho, Rules: []Rule{},
	}
}

// Normalize checks c and returns a copy that shares nothing with it:
// fields trimmed, defaults filled in, the auth compacted.
func Normalize(c Config) (Config, error) {
	out := Config{
		Path:           strings.TrimSpace(c.Path),
		Namespaces:     make([]string, 0, len(c.Namespaces)),
		Transports:     c.Transports,
		AllowOrigin:    strings.TrimSpace(c.AllowOrigin),
		PingIntervalMs: c.PingIntervalMs,
		PingTimeoutMs:  c.PingTimeoutMs,
		MaxPayload:     c.MaxPayload,
		Echo:           c.Echo,
		AckMode:        c.AckMode,
		Rules:          make([]Rule, 0, len(c.Rules)),
	}
	if out.Path == "" {
		out.Path = DefaultPath
	}
	if !strings.HasPrefix(out.Path, "/") {
		return Config{}, invalidConfig("path", "The path must start with /.")
	}
	if len(out.Path) > MaxPathBytes {
		return Config{}, invalidConfig("path", fmt.Sprintf("The path is longer than %d bytes.", MaxPathBytes))
	}
	for _, r := range out.Path {
		if r <= ' ' || r == 0x7f || r == '?' || r == '#' {
			return Config{}, invalidConfig("path", "The path cannot contain spaces, control characters, ? or #.")
		}
	}
	if !strings.HasSuffix(out.Path, "/") {
		out.Path += "/"
	}
	if len(c.Namespaces) > MaxNamespaces {
		return Config{}, invalidConfig("namespaces", fmt.Sprintf("A server lists at most %d namespaces.", MaxNamespaces))
	}
	for _, n := range c.Namespaces {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		if !strings.HasPrefix(n, "/") {
			n = "/" + n
		}
		if strings.ContainsAny(n, ", ?#") || strings.ContainsFunc(n, func(r rune) bool { return r < ' ' || r == 0x7f }) || !utf8.ValidString(n) {
			return Config{}, invalidConfig("namespaces", fmt.Sprintf("%q is not a namespace: namespaces look like /admin, without spaces, commas, ? or #.", n))
		}
		if n != socketio.MainNamespace && !slices.Contains(out.Namespaces, n) {
			out.Namespaces = append(out.Namespaces, n)
		}
	}
	auth, err := normalizeAuth(c.Auth)
	if err != nil {
		return Config{}, err
	}
	out.Auth = auth
	switch out.Transports {
	case TransportsAll, TransportsWebSocket, TransportsPolling:
	default:
		return Config{}, invalidConfig("transports", fmt.Sprintf("%q is not a transport choice: use websocket, polling or leave it empty for both.", out.Transports))
	}
	if o := out.AllowOrigin; o != "" && o != OriginAny {
		if strings.ContainsAny(o, " \t\r\n,") || !strings.Contains(o, "://") {
			return Config{}, invalidConfig("allowOrigin", "Enter one origin such as http://localhost:3000, or * for any origin.")
		}
		out.AllowOrigin = strings.TrimSuffix(o, "/")
	}
	if out.PingIntervalMs == 0 {
		out.PingIntervalMs = DefaultPingIntervalMs
	}
	if out.PingTimeoutMs == 0 {
		out.PingTimeoutMs = DefaultPingTimeoutMs
	}
	if out.PingIntervalMs < MinHeartbeatMs || out.PingIntervalMs > MaxHeartbeatMs {
		return Config{}, invalidConfig("pingIntervalMs", fmt.Sprintf("The ping interval must be from %d ms to 1 hour.", MinHeartbeatMs))
	}
	if out.PingTimeoutMs < MinHeartbeatMs || out.PingTimeoutMs > MaxHeartbeatMs {
		return Config{}, invalidConfig("pingTimeoutMs", fmt.Sprintf("The ping timeout must be from %d ms to 1 hour.", MinHeartbeatMs))
	}
	if out.MaxPayload == 0 {
		out.MaxPayload = DefaultMaxPayload
	}
	if out.MaxPayload < 0 || out.MaxPayload > MaxPayloadLimit {
		return Config{}, invalidConfig("maxPayload", fmt.Sprintf("The largest payload must be from 1 byte to %d MiB.", MaxPayloadLimit>>20))
	}
	switch out.AckMode {
	case "":
		out.AckMode = AckEcho
	case AckEcho, AckEmpty, AckNone:
	default:
		return Config{}, invalidConfig("ackMode", fmt.Sprintf("%q is not an acknowledgement mode: use echo, empty or none.", out.AckMode))
	}
	if len(c.Rules) > MaxRules {
		return Config{}, invalidConfig("rules", fmt.Sprintf("A server has at most %d auto-reply rules.", MaxRules))
	}
	for i, r := range c.Rules {
		r, err := normalizeRule(i, r)
		if err != nil {
			return Config{}, err
		}
		out.Rules = append(out.Rules, r)
	}
	return out, nil
}

// normalizeAuth checks the required auth members: "" or a JSON object,
// returned compacted.
func normalizeAuth(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if len(s) > MaxAuthBytes {
		return "", invalidConfig("auth", fmt.Sprintf("The required auth is larger than %d KiB.", MaxAuthBytes>>10))
	}
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return "", invalidConfig("auth", "The required auth is not valid JSON.").WithHint(`Enter a JSON object, like {"token": "abc"}.`)
	}
	if _, ok := v.(map[string]any); !ok {
		return "", invalidConfig("auth", "The required auth must be a JSON object.").WithHint(`Enter a JSON object, like {"token": "abc"}.`)
	}
	var buf bytes.Buffer
	_ = json.Compact(&buf, []byte(s))
	return buf.String(), nil
}

func normalizeRule(i int, r Rule) (Rule, error) {
	n := i + 1
	if r.Match == "" {
		r.Match = MatchExact
	}
	switch r.Match {
	case MatchExact, MatchContains, MatchRegex:
	default:
		return Rule{}, invalidRule(i, "match", fmt.Sprintf("Rule %d: %q is not a match mode: use exact, contains or regex.", n, r.Match))
	}
	if len(r.Pattern) > MaxRulePatternBytes || !utf8.ValidString(r.Pattern) {
		return Rule{}, invalidRule(i, "pattern", fmt.Sprintf("Rule %d: the event name must be UTF-8 text of at most %d bytes.", n, MaxRulePatternBytes))
	}
	if r.Match == MatchRegex {
		if _, err := regexp.Compile(r.Pattern); err != nil {
			return Rule{}, invalidRule(i, "pattern", fmt.Sprintf("Rule %d: the regular expression is not valid: %s.", n, strings.TrimPrefix(err.Error(), "error parsing regexp: ")))
		}
	}
	for _, f := range []struct{ field, text string }{{"ack", r.Ack}, {"emitArgs", r.EmitArgs}} {
		if len(f.text) > MaxRuleArgsBytes {
			return Rule{}, invalidRule(i, f.field, fmt.Sprintf("Rule %d: the arguments are larger than %d MiB.", n, MaxRuleArgsBytes>>20))
		}
		if _, err := ruleArgs(f.text, time.Now); err != nil {
			return Rule{}, invalidRule(i, f.field, fmt.Sprintf("Rule %d: %s", n, messageOf(err)))
		}
	}
	r.Emit = strings.TrimSpace(r.Emit)
	if err := checkEventName(r.Emit, true); err != nil {
		return Rule{}, invalidRule(i, "emit", fmt.Sprintf("Rule %d: %s", n, messageOf(err)))
	}
	if r.DelayMs < 0 || r.DelayMs > MaxRuleDelayMs {
		return Rule{}, invalidRule(i, "delayMs", fmt.Sprintf("Rule %d: the delay must be from 0 to %d ms.", n, MaxRuleDelayMs))
	}
	return r, nil
}

// ParseArgs renders the dynamic values ({{$timestamp}}) of arguments
// typed as JSON text and parses them (socketio.ParseArgs): an array holds
// one argument per element, {"$base64": "…"} is binary. now is the clock
// of the timestamps; nil means time.Now.
func ParseArgs(text string, now func() time.Time) ([]any, error) {
	if now == nil {
		now = time.Now
	}
	return ruleArgs(text, now)
}

// ruleArgs renders the dynamic values of a rule's arguments text and
// parses it (socketio.ParseArgs).
func ruleArgs(text string, now func() time.Time) ([]any, error) {
	if strings.Contains(text, "{{") {
		text = template.Resolver{Now: now}.Render(text).Text
	}
	return socketio.ParseArgs(text)
}

// checkEventName checks the name of an event the server emits.
func checkEventName(name string, optional bool) error {
	switch {
	case name == "" && optional:
		return nil
	case name == "":
		return errs.New(errs.InvalidArgument, "The event needs a name.").WithCode(CodeInvalidEvent)
	case !utf8.ValidString(name) || len(name) > MaxRulePatternBytes:
		return errs.New(errs.InvalidArgument, fmt.Sprintf("The event name must be UTF-8 text of at most %d bytes.", MaxRulePatternBytes)).WithCode(CodeInvalidEvent)
	case socketio.IsReservedEvent(name):
		return errs.New(errs.InvalidArgument, fmt.Sprintf("The event name %q is reserved by Socket.IO.", name)).WithCode(CodeInvalidEvent)
	}
	return nil
}

// ruleSet is the compiled enabled rules of a configuration.
type ruleSet struct {
	rules []compiledRule
}

type compiledRule struct {
	index int
	rule  Rule
	re    *regexp.Regexp
}

func compileRules(rules []Rule) *ruleSet {
	s := &ruleSet{}
	for i, r := range rules {
		if !r.Enabled {
			continue
		}
		c := compiledRule{index: i, rule: r}
		if r.Match == MatchRegex {
			re, err := regexp.Compile(r.Pattern)
			if err != nil {
				continue // Normalize refused it; never here
			}
			c.re = re
		}
		s.rules = append(s.rules, c)
	}
	return s
}

// match returns the first enabled rule whose pattern matches the event
// name, and its index in Config.Rules.
func (s *ruleSet) match(name string) (Rule, int, bool) {
	if s == nil {
		return Rule{}, 0, false
	}
	for _, c := range s.rules {
		var ok bool
		switch c.rule.Match {
		case MatchContains:
			ok = strings.Contains(name, c.rule.Pattern)
		case MatchRegex:
			ok = c.re.MatchString(name)
		default:
			ok = name == c.rule.Pattern
		}
		if ok {
			return c.rule, c.index, true
		}
	}
	return Rule{}, 0, false
}

func (c Config) acceptsNamespace(nsp string) bool {
	return nsp == socketio.MainNamespace || len(c.Namespaces) == 0 || slices.Contains(c.Namespaces, nsp)
}

// authorized reports whether auth (the CONNECT's payload, nil for none)
// has every member of the required auth with the same value.
func (c Config) authorized(auth json.RawMessage) bool {
	if c.Auth == "" {
		return true
	}
	var want, got map[string]any
	if json.Unmarshal([]byte(c.Auth), &want) != nil {
		return false
	}
	if len(auth) == 0 || json.Unmarshal(auth, &got) != nil {
		return len(want) == 0
	}
	for k, v := range want {
		g, ok := got[k]
		if !ok || !jsonEqual(v, g) {
			return false
		}
	}
	return true
}

func jsonEqual(a, b any) bool {
	x, err1 := json.Marshal(a)
	y, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && bytes.Equal(x, y)
}

func (c Config) allows(transport string) bool {
	return c.Transports == TransportsAll || c.Transports == transport
}

func (c Config) matchesPath(p string) bool {
	return p == c.Path || p == strings.TrimSuffix(c.Path, "/")
}

func invalidConfig(field, msg string) *errs.Error {
	return errs.New(errs.InvalidArgument, msg).WithCode(CodeInvalidConfig).WithDetail("field", field)
}

func invalidRule(i int, field, msg string) *errs.Error {
	return errs.New(errs.InvalidArgument, msg).WithCode(CodeInvalidConfig).
		WithDetail("field", "rules").WithDetail("ruleIndex", fmt.Sprint(i)).WithDetail("ruleField", field)
}
