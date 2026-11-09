package websocket

import (
	"context"

	"github.com/sorrelfox/postsonde/internal/core/template"
)

// Scopes returns the variable scopes of websocket request requestID (WS-11):
// global, the active environment and the folders above it, as for an HTTP
// send. The app builds it from collection.LoadVariablesSource and
// http.ScopeChain; it is called on every connect and send, so switching
// the environment changes the next one.
type Scopes func(ctx context.Context, requestID string) (template.Chain, error)

// WithScopes renders {{variables}} with the scopes scopes returns. Without
// it (and for a connection with no request ID) no variable is defined.
func WithScopes(scopes Scopes) Option {
	return func(s *WebSocketService) { s.scopes = scopes }
}

// rendering renders the {{variables}} of one connect or send. Undefined
// names do not stop it: they are left as written and collected, so one
// template.unresolved error lists those of every field (unresolvedErr), or,
// with allowUnresolved (the "Send anyway" answer of ENV-11), they are sent
// as written. Every other problem (a cycle, an empty name) stops it at
// once.
type rendering struct {
	resolver        template.Resolver
	allowUnresolved bool
	unresolved      []template.Problem
	// secrets are the values of the secret variables rendered, for the
	// Code tab's masking (Snippet).
	secrets []string
}

// collectSecrets appends the values of the secret references in refs, and
// of the secret references nested in them.
func collectSecrets(out *[]string, refs []template.Ref) {
	for _, r := range refs {
		if r.Secret && r.Resolved && r.Value != "" {
			*out = append(*out, r.Value)
		}
		collectSecrets(out, r.Nested)
	}
}

// rendering loads the scopes of requestID.
func (s *WebSocketService) rendering(ctx context.Context, requestID string, allowUnresolved bool) (*rendering, error) {
	var chain template.Chain
	if s.scopes != nil && requestID != "" {
		c, err := s.scopes(ctx, requestID)
		if err != nil {
			return nil, err
		}
		chain = c
	}
	return &rendering{resolver: template.Resolver{Scopes: chain}, allowUnresolved: allowUnresolved}, nil
}

func (r *rendering) render(text string) (string, error) {
	res := r.resolver.Render(text)
	collectSecrets(&r.secrets, res.Refs)
	var other []template.Problem
	for _, p := range res.Problems {
		if p.Kind == template.ProblemUnresolved {
			if !r.allowUnresolved {
				r.unresolved = append(r.unresolved, p)
			}
			continue
		}
		other = append(other, p)
	}
	if err := (template.Result{Problems: other}).Err(); err != nil {
		return "", err
	}
	return res.Text, nil
}

// unresolvedErr is the template.unresolved error listing every undefined
// name met so far (details.names), or nil.
func (r *rendering) unresolvedErr() error {
	if len(r.unresolved) == 0 {
		return nil
	}
	return template.Result{Problems: r.unresolved}.Err()
}

// renderConnect renders the URL, the enabled header rows and the
// subprotocols of in.
func (r *rendering) renderConnect(in ConnectInputDTO) (ConnectInputDTO, error) {
	out := in
	var err error
	if out.URL, err = r.render(in.URL); err != nil {
		return in, err
	}
	out.Headers = make([]HeaderDTO, len(in.Headers))
	for i, h := range in.Headers {
		if h.Enabled {
			// A disabled row is not sent, so its variables do not matter.
			if h.Name, err = r.render(h.Name); err != nil {
				return in, err
			}
			if h.Value, err = r.render(h.Value); err != nil {
				return in, err
			}
		}
		out.Headers[i] = h
	}
	out.Subprotocols = make([]string, len(in.Subprotocols))
	for i, p := range in.Subprotocols {
		if out.Subprotocols[i], err = r.render(p); err != nil {
			return in, err
		}
	}
	return out, nil
}
