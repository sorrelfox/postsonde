package wsserver

import (
	"fmt"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// Limits and defaults of a configuration.
const (
	// AnyPath as Config.Path accepts connections on every path.
	AnyPath = "*"
	// DefaultPath is the path of a new server.
	DefaultPath = "/"
	// MaxPathBytes is the longest path.
	MaxPathBytes = 2048
	// MaxSubprotocols is the most subprotocols a server accepts.
	MaxSubprotocols = 32
	// DefaultMaxMessageSize limits a received message when
	// Config.MaxMessageSize is 0 (16 MiB).
	DefaultMaxMessageSize = 16 << 20
	// MaxMessageSizeLimit is the largest Config.MaxMessageSize (256 MiB).
	MaxMessageSizeLimit = 256 << 20
)

// Error codes of NormalizeConfig.
const (
	CodeInvalidConfig = "wsserver.invalid_config"
)

// Config is the configuration of a WebSocket server (Spec.Config).
type Config struct {
	// Path is where clients connect ("/", "/socket"), matched exactly
	// against the request path (the query is ignored); AnyPath accepts
	// every path.
	Path string
	// Subprotocols are the subprotocols the server accepts. The first one
	// the client offers that is in this list is selected; none is
	// selected when the list is empty or nothing matches.
	Subprotocols []string
	// Echo sends a message received back to its client when no rule
	// answers it (every binary message, and a text message no enabled rule
	// matches).
	Echo bool
	// Rules are the auto-reply rules (SSRV-02), in order: the first
	// enabled rule that matches a text message sends its reply.
	Rules []Rule
	// MaxMessageSize limits a received message (and frame) in bytes; 0
	// means DefaultMaxMessageSize. A larger message closes the connection
	// with 1009.
	MaxMessageSize int64
}

// DefaultConfig is the configuration of a new WebSocket server.
func DefaultConfig() Config {
	return Config{Path: DefaultPath, Subprotocols: []string{}, Rules: []Rule{}}
}

// Normalize checks c and returns a copy that shares nothing with it: the
// path and subprotocols trimmed, an empty path set to DefaultPath, a
// rule's empty match mode set to MatchExact.
func Normalize(c Config) (Config, error) {
	out := Config{
		Path:           strings.TrimSpace(c.Path),
		Echo:           c.Echo,
		MaxMessageSize: c.MaxMessageSize,
		Subprotocols:   make([]string, 0, len(c.Subprotocols)),
		Rules:          make([]Rule, 0, len(c.Rules)),
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
	if len(c.Subprotocols) > MaxSubprotocols {
		return Config{}, invalidConfig("subprotocols", fmt.Sprintf("A WebSocket server accepts at most %d subprotocols.", MaxSubprotocols))
	}
	for _, p := range c.Subprotocols {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !isToken(p) {
			return Config{}, invalidConfig("subprotocols", fmt.Sprintf("%q is not a valid subprotocol: use letters, digits and !#$%%&'*+-.^_`|~ only.", p))
		}
		for _, q := range out.Subprotocols {
			if q == p {
				return Config{}, invalidConfig("subprotocols", fmt.Sprintf("The subprotocol %q is listed twice.", p))
			}
		}
		out.Subprotocols = append(out.Subprotocols, p)
	}
	if c.MaxMessageSize < 0 || c.MaxMessageSize > MaxMessageSizeLimit {
		return Config{}, invalidConfig("maxMessageSize", fmt.Sprintf("The largest message must be from 1 byte to %d MiB (0 for %d MiB).", MaxMessageSizeLimit>>20, DefaultMaxMessageSize>>20))
	}
	if _, err := CompileRules(c.Rules); err != nil {
		return Config{}, err
	}
	for i, r := range c.Rules {
		r, _ = normalizeRule(i, r)
		out.Rules = append(out.Rules, r)
	}
	return out, nil
}

func (c Config) maxMessageSize() int64 {
	if c.MaxMessageSize <= 0 {
		return DefaultMaxMessageSize
	}
	return c.MaxMessageSize
}

func (c Config) matchesPath(path string) bool {
	return c.Path == AnyPath || c.Path == path
}

// selectSubprotocol returns the first of offered that c accepts, or "".
func (c Config) selectSubprotocol(offered []string) string {
	for _, o := range offered {
		for _, a := range c.Subprotocols {
			if o == a {
				return o
			}
		}
	}
	return ""
}

func invalidConfig(field, msg string) *errs.Error {
	return errs.New(errs.InvalidArgument, msg).WithCode(CodeInvalidConfig).WithDetail("field", field)
}

// isToken reports whether s is an HTTP token (RFC 9110 §5.6.2).
func isToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isTokenChar(s[i]) {
			return false
		}
	}
	return true
}

func isTokenChar(b byte) bool {
	switch {
	case b >= '0' && b <= '9', b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z':
		return true
	}
	return strings.IndexByte("!#$%&'*+-.^_`|~", b) >= 0
}
