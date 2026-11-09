package http

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

// CodeNoJWT: the request's auth does not make a JWT (another type, a
// disabled auth, or no key yet), so there is nothing to preview (kind
// invalid_argument).
const CodeNoJWT = "http.no_jwt"

// JWTPreviewInputDTO is what JWTPreview takes.
type JWTPreviewInputDTO struct {
	RequestID string `json:"requestId"`
}

// JWTPreviewDTO is a JWT as a send of the request would make it now
// (AUTH-16).
type JWTPreviewDTO struct {
	// Token is the signed token, header.payload.signature. It is a
	// credential: the Auth tab shows it only on request.
	Token string `json:"token"`
	// Header and Payload are the token's two JSON parts, indented.
	Header  string `json:"header"`
	Payload string `json:"payload"`
	// Algorithm is the "alg" it was signed with.
	Algorithm string `json:"algorithm"`
	// FolderName names the folder whose auth made it when the request
	// inherits it; "" for the request's own.
	FolderName string `json:"folderName"`
	// Unresolved are the undefined {{variables}}, left as written.
	Unresolved []string `json:"unresolved"`
}

// JWTPreview builds the JWT request in.RequestID (HTTP, GraphQL or SSE)
// would send now, with its own or inherited jwt auth, its variables
// (dynamic values included), the claims added with this moment's time and
// the key typed or read from its file, and returns it with its decoded
// header and payload. Nothing is sent, and only the auth is built, so a
// problem elsewhere in the request does not stop it.
//
// Undefined variables do not fail it: they stay as written and are listed
// in Unresolved. It fails with CodeNoJWT when the auth makes no token,
// and as Send does for the auth's own errors.
func (s *HTTPService) JWTPreview(ctx context.Context, in JWTPreviewInputDTO) (JWTPreviewDTO, error) {
	if err := ctx.Err(); err != nil {
		return JWTPreviewDTO{}, errs.Wrap(err, errs.KindOf(err), "Building the JWT was interrupted.")
	}
	if !s.canLoadAuth() {
		return JWTPreviewDTO{}, errs.New(errs.Unsupported, "Sending HTTP requests is not available.").WithCode(CodeUnavailable)
	}
	src, err := s.loadAuth(ctx, in.RequestID)
	if err != nil {
		return JWTPreviewDTO{}, err
	}
	if src.Auth.Type != projectfile.AuthJWT || src.Auth.Disabled {
		return JWTPreviewDTO{}, noJWT()
	}
	chain, err := s.chainOf(src)
	if err != nil {
		return JWTPreviewDTO{}, err
	}
	x := &expansion{resolver: template.Resolver{Scopes: chain}, allowUnresolved: true}
	root := ""
	if src.Workspace != nil {
		root = src.Workspace.Root
	}
	a, err := request.AuthWith(src.Auth, nil, request.AuthOptions{Expand: x.expand, Root: root, AllowedDirs: s.allowed()})
	if err != nil {
		return JWTPreviewDTO{}, err
	}
	if a.JWT == nil {
		return JWTPreviewDTO{}, noJWT()
	}
	var h map[string]any
	_ = json.Unmarshal([]byte(a.JWT.Header), &h)
	alg, _ := h["alg"].(string)
	return JWTPreviewDTO{
		Token:      a.JWT.Compact,
		Header:     indent(a.JWT.Header),
		Payload:    indent(a.JWT.Payload),
		Algorithm:  alg,
		FolderName: src.AuthFolderName,
		Unresolved: x.skippedNames(),
	}, nil
}

func noJWT() error {
	return errs.New(errs.InvalidArgument, "The request's auth makes no JWT.").
		WithCode(CodeNoJWT).
		WithHint("Choose the JWT auth type and enter a secret or key, and make sure “Send this auth” is on.")
}

// indent returns the JSON text indented by two spaces, or as it is when
// it does not parse.
func indent(s string) string {
	var b bytes.Buffer
	if err := json.Indent(&b, []byte(s), "", "  "); err != nil {
		return s
	}
	return b.String()
}
