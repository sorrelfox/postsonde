package websocket

import (
	"context"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/snippet"
)

// The Code tab of the WebSocket editor (GEN-11, GEN-12, GEN-13): a
// handshake as a websocat or wscat command, resolved or templated, secrets
// masked unless the user reveals them. See
// docs/snippets.md#websocat-and-wscat.

// Snippet modes, for SnippetInputDTO.Mode, as the HTTP snippet panel's.
const (
	// SnippetResolved renders the {{variables}} as Connect renders them
	// now (also "").
	SnippetResolved = "resolved"
	// SnippetTemplated keeps every {{reference}} as written.
	SnippetTemplated = "templated"
)

// CodeSnippetMode: SnippetInputDTO.Mode is not a mode (kind
// invalid_argument). Details: mode.
const CodeSnippetMode = "ws.snippet_mode"

// SnippetLanguageDTO is a language of the WebSocket Code tab.
type SnippetLanguageDTO struct {
	// ID is what SnippetInputDTO.Language takes ("websocat").
	ID string `json:"id"`
	// Name is shown when the UI has no text of its own for ID.
	Name string `json:"name"`
	// Syntax is how the code is highlighted: "shell" or "text".
	Syntax string `json:"syntax"`
}

// SnippetInputDTO is what Snippet takes.
type SnippetInputDTO struct {
	// Connect is the handshake as Connect would take it.
	Connect ConnectInputDTO `json:"connect"`
	// Language is a SnippetLanguageDTO.ID; "" is websocat.
	Language string `json:"language"`
	// Mode is SnippetResolved (also "") or SnippetTemplated.
	Mode string `json:"mode"`
	// RevealSecrets writes the real values of the secrets in resolved
	// mode (PRV-09); it changes nothing in templated mode.
	RevealSecrets bool `json:"revealSecrets"`
}

// SnippetDTO is a handshake as code, shaped as the HTTP snippet panel's.
type SnippetDTO struct {
	Language string `json:"language"`
	Mode     string `json:"mode"`
	Code     string `json:"code"`
	// Masked is set when secrets were replaced by ••••••.
	Masked bool `json:"masked"`
	// Revealed is set when the code holds the real values of secrets
	// that would otherwise be masked.
	Revealed bool `json:"revealed"`
	// Unresolved are the names of the undefined {{variables}} left as
	// written in resolved mode, sorted; empty in templated mode.
	Unresolved []string `json:"unresolved"`
}

// SnippetLanguages returns the languages of the WebSocket Code tab, in the
// order it lists them.
func (s *WebSocketService) SnippetLanguages() []SnippetLanguageDTO {
	langs := snippet.WebSocketLanguages()
	out := make([]SnippetLanguageDTO, len(langs))
	for i, l := range langs {
		out[i] = SnippetLanguageDTO{ID: l.ID, Name: l.Name, Syntax: l.Syntax}
	}
	return out
}

// Snippet returns the handshake of in.Connect as code in in.Language
// (GEN-11). Nothing is dialed or asked.
//
//   - Resolved: the URL, header rows and subprotocols rendered with the
//     scopes of the request as Connect renders them, undefined variables
//     left as written and listed, the jar's cookies added unless the
//     request leaves them out; the values of the secret variables used and
//     every Authorization credential masked unless in.RevealSecrets.
//   - Templated: as written, without jar cookies; a typed Authorization
//     credential is masked, one made of references kept.
//
// It fails with CodeSnippetMode, snippet.CodeUnknownLanguage, a template
// problem other than an undefined name, and snippet.CodeNulByte.
func (s *WebSocketService) Snippet(ctx context.Context, in SnippetInputDTO) (SnippetDTO, error) {
	if err := ctx.Err(); err != nil {
		return SnippetDTO{}, errs.Wrap(err, errs.KindOf(err), "Writing the connection as code was interrupted.")
	}
	lang, err := snippet.WebSocketLanguageByID(in.Language)
	if err != nil {
		return SnippetDTO{}, err
	}
	out := SnippetDTO{Language: lang.ID, Mode: in.Mode, Unresolved: []string{}}
	c := in.Connect
	var r snippet.WebSocketRequest
	switch in.Mode {
	case "", SnippetResolved:
		out.Mode = SnippetResolved
		rd, err := s.rendering(ctx, c.RequestID, false)
		if err != nil {
			return SnippetDTO{}, err
		}
		if c, err = rd.renderConnect(c); err != nil {
			return SnippetDTO{}, err
		}
		for _, p := range rd.unresolved {
			out.Unresolved = append(out.Unresolved, p.Name)
		}
		slices.Sort(out.Unresolved)
		out.Unresolved = slices.Compact(out.Unresolved)
		r = snippetRequest(c)
		if c.SendCookies == nil || *c.SendCookies {
			r.Headers = snippet.WithCookies(r.Headers, s.jarCookies(r))
		}
		masked, changed := r.Masked(rd.secrets, false)
		if in.RevealSecrets {
			out.Revealed = changed
		} else {
			r, out.Masked = masked, changed
		}
	case SnippetTemplated:
		r, out.Masked = snippetRequest(c).Masked(nil, true)
	default:
		return SnippetDTO{}, errs.New(errs.InvalidArgument, "The snippet mode is not known.").
			WithCode(CodeSnippetMode).
			WithHint("Use resolved or templated.").
			WithDetail("mode", in.Mode)
	}
	if out.Code, err = lang.Generate(r); err != nil {
		return SnippetDTO{}, err
	}
	return out, nil
}

// snippetRequest is the model of in as the handshake sends it: the URL's
// userinfo as an Authorization row first, then the enabled rows with
// their values trimmed, and the keepalive of the connection.
func snippetRequest(in ConnectInputDTO) snippet.WebSocketRequest {
	u, auth := snippet.WebSocketURL(strings.TrimSpace(in.URL))
	r := snippet.WebSocketRequest{URL: u, Insecure: in.VerifyTLS != nil && !*in.VerifyTLS}
	if auth != "" {
		r.Headers = append(r.Headers, snippet.Header{Name: "Authorization", Value: auth})
	}
	for _, h := range in.Headers {
		if h.Enabled && strings.TrimSpace(h.Name) != "" {
			r.Headers = append(r.Headers, snippet.Header{Name: strings.TrimSpace(h.Name), Value: strings.TrimSpace(h.Value)})
		}
	}
	for _, p := range in.Subprotocols {
		if p = strings.TrimSpace(p); p != "" {
			r.Subprotocols = append(r.Subprotocols, p)
		}
	}
	if in.PingIntervalMs != nil && *in.PingIntervalMs > 0 {
		r.PingInterval = max(time.Duration(*in.PingIntervalMs)*time.Millisecond, minInterval)
		r.PongTimeout = DefaultPongTimeout
		if in.PongTimeoutMs != nil {
			r.PongTimeout = max(0, time.Duration(*in.PongTimeoutMs)*time.Millisecond)
		}
	}
	return r
}

// jarCookies are the jar's cookies for r's URL that no Cookie row names,
// as a Cookie value; "" for none or without a jar.
func (s *WebSocketService) jarCookies(r snippet.WebSocketRequest) string {
	if s.jars == nil {
		return ""
	}
	u, err := url.Parse(r.URL)
	if err != nil || u.Host == "" {
		return ""
	}
	jar, err := s.jars.Jar()
	if err != nil || jar == nil {
		return ""
	}
	named := map[string]bool{}
	for _, h := range r.Headers {
		if !strings.EqualFold(h.Name, "Cookie") {
			continue
		}
		for pair := range strings.SplitSeq(h.Value, ";") {
			if n, _, _ := strings.Cut(pair, "="); strings.TrimSpace(n) != "" {
				named[strings.TrimSpace(n)] = true
			}
		}
	}
	var parts []string
	for _, c := range jar.Cookies(&url.URL{Scheme: strings.ToLower(u.Scheme), Host: u.Host, Path: u.Path}) {
		switch {
		case named[c.Name]:
		case c.Name == "":
			parts = append(parts, c.Value)
		default:
			parts = append(parts, c.Name+"="+c.Value)
		}
	}
	return strings.Join(parts, "; ")
}
