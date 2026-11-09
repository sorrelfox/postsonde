package grpc

// auth.go sends a grpc request's auth (its own or its folder's) in the
// call's metadata (AUTH-31). See docs/grpc.md#auth.

import (
	"context"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/grpc/transport"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

// Scopes returns the variable scopes of grpc request requestID, as for an
// HTTP send: the auth's {{variables}} are rendered with them.
type Scopes func(ctx context.Context, requestID string) (template.Chain, error)

// WithAuth sends the auth of the call's request in its metadata
// (AUTH-31), built by auth on every call with its fields rendered with
// the scopes of the request (scopes; nil defines no variable). Its
// headers become metadata keys in lower case after the metadata rows; a
// row of the same key wins. Without it no auth is sent.
func WithAuth(auth request.HandshakeAuthFunc, scopes Scopes) Option {
	return func(s *GRPCService) { s.auth, s.scopes = auth, scopes }
}

// callAuth returns md with the auth of in's request added, and the
// masker of its secrets (nil without any). Undefined variables in the
// auth fields refuse the call (template.unresolved): there is no "call
// anyway" for gRPC.
func (s *GRPCService) callAuth(ctx context.Context, in InvokeInputDTO, md transport.Metadata) (transport.Metadata, *httpclient.SecretMasker, error) {
	if s.auth == nil || in.RequestID == "" {
		return md, nil, nil
	}
	var chain template.Chain
	if s.scopes != nil {
		c, err := s.scopes(ctx, in.RequestID)
		if err != nil {
			return nil, nil, err
		}
		chain = c
	}
	resolver := template.Resolver{Scopes: chain}
	expand := func(text string) (string, error) {
		res := resolver.Render(text)
		if err := res.Err(); err != nil {
			return "", err
		}
		return res.Text, nil
	}
	scheme := "http://"
	if in.TLS {
		scheme = "https://"
	}
	h, err := s.auth(ctx, request.HandshakeInput{
		RequestID: in.RequestID, Protocol: request.HandshakeGRPC, URL: scheme + strings.TrimSpace(in.Address), Expand: expand,
	})
	if err != nil {
		return nil, nil, err
	}
	rows := make([]httpclient.Header, len(md))
	for i, e := range md {
		rows[i] = httpclient.Header{Name: e.Key, Value: e.Value, Enabled: true}
	}
	for _, a := range request.HandshakeHeaders(rows, h.Headers, true) {
		md = append(md, transport.MDEntry{Key: a.Name, Value: a.Value})
	}
	var m *httpclient.SecretMasker
	if len(h.Secrets) > 0 {
		m = httpclient.NewSecretMasker(h.Secrets)
	}
	return md, m, nil
}

// maskMetadata hides the auth's secrets in the metadata shown.
func maskMetadata(md []MetadataDTO, m *httpclient.SecretMasker) []MetadataDTO {
	if m == nil {
		return md
	}
	for i, e := range md {
		if masked := m.MaskText(e.Value); masked != e.Value {
			md[i].Value = m.MaskHeader(e.Key, e.Value)
		}
	}
	return md
}
