package socketio

import (
	"context"

	"github.com/sorrelfox/postsonde/internal/core/template"
)

// Scopes returns the variable scopes of socketio request requestID:
// global, the active environment and the folders above it, as for an HTTP
// send. It is called on every connect and emit, so switching the
// environment changes the next one.
type Scopes func(ctx context.Context, requestID string) (template.Chain, error)

// WithScopes renders {{variables}} with the scopes scopes returns. Without
// it (and for a connection with no request ID) no variable is defined.
func WithScopes(scopes Scopes) Option {
	return func(s *SocketIOService) { s.scopes = scopes }
}

// rendering renders the {{variables}} of one connect or emit. Undefined
// names do not stop it: they are left as written and collected, so one
// template.unresolved error lists those of every field (unresolvedErr), or,
// with allowUnresolved, they are sent as written. Every other problem
// stops it at once.
type rendering struct {
	resolver        template.Resolver
	allowUnresolved bool
	unresolved      []template.Problem
}

func (s *SocketIOService) rendering(ctx context.Context, requestID string, allowUnresolved bool) (*rendering, error) {
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

// renderConnect renders the URL, the enabled header rows, the path, the
// namespace and the auth payload of in.
func (r *rendering) renderConnect(in ConnectInputDTO) (ConnectInputDTO, error) {
	out := in
	var err error
	for _, f := range []*string{&out.URL, &out.Path, &out.Namespace, &out.AuthPayload} {
		if *f, err = r.render(*f); err != nil {
			return in, err
		}
	}
	out.Headers = make([]HeaderDTO, len(in.Headers))
	for i, h := range in.Headers {
		if h.Enabled {
			if h.Name, err = r.render(h.Name); err != nil {
				return in, err
			}
			if h.Value, err = r.render(h.Value); err != nil {
				return in, err
			}
		}
		out.Headers[i] = h
	}
	return out, nil
}
