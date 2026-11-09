package grpc

import (
	"context"
	"encoding/base64"
	"strings"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/grpc/transport"
	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/snippet"
)

// The Code tab of the gRPC editor (GEN-10, GEN-13): a call as a
// grpcurl command, secrets masked unless the user reveals them. See
// docs/snippets.md#grpcurl.

// SnippetLanguageDTO is a language of the gRPC Code tab.
type SnippetLanguageDTO struct {
	// ID is what SnippetInputDTO.Language takes ("grpcurl").
	ID string `json:"id"`
	// Name is shown when the UI has no text of its own for ID.
	Name string `json:"name"`
	// Syntax is how the code is highlighted: "shell" or "text".
	Syntax string `json:"syntax"`
}

// SnippetInputDTO is what Snippet takes.
type SnippetInputDTO struct {
	// Call is the call as Invoke would take it.
	Call InvokeInputDTO `json:"call"`
	// Language is a SnippetLanguageDTO.ID; "" is grpcurl.
	Language string `json:"language"`
	// RevealSecrets writes the credentials of the authorization metadata
	// as typed (PRV-09): the user asked for them in this view.
	RevealSecrets bool `json:"revealSecrets"`
}

// SnippetDTO is a call as code, shaped as the HTTP snippet panel's.
type SnippetDTO struct {
	Language string `json:"language"`
	// Mode is always "resolved": a call has no {{variables}} to keep.
	Mode string `json:"mode"`
	Code string `json:"code"`
	// Masked is set when secrets were replaced by ••••••.
	Masked bool `json:"masked"`
	// Revealed is set when the code holds values that would otherwise be
	// masked.
	Revealed bool `json:"revealed"`
	// Unresolved is always empty.
	Unresolved []string `json:"unresolved"`
}

// SnippetLanguages returns the languages of the gRPC Code tab, in the
// order it lists them.
func (s *GRPCService) SnippetLanguages() []SnippetLanguageDTO {
	langs := snippet.GRPCLanguages()
	out := make([]SnippetLanguageDTO, len(langs))
	for i, l := range langs {
		out[i] = SnippetLanguageDTO{ID: l.ID, Name: l.Name, Syntax: l.Syntax}
	}
	return out
}

// Snippet returns in.Call as code in in.Language (GEN-10): what Invoke
// would send, written as typed. Nothing is dialed, read or linked: the
// .proto files are named by path (stored paths made absolute against the
// project), and with none the command asks the server's reflection
// service. The credentials of authorization and proxy-authorization
// metadata are masked unless in.RevealSecrets. It fails with
// snippet.CodeUnknownLanguage, CodeInvalidSettings for a transport or
// compression that is not known, and snippet.CodeNulByte.
func (s *GRPCService) Snippet(ctx context.Context, in SnippetInputDTO) (SnippetDTO, error) {
	if err := ctx.Err(); err != nil {
		return SnippetDTO{}, errs.Wrap(err, errs.KindOf(err), "Writing the call as code was interrupted.")
	}
	lang, err := snippet.GRPCLanguageByID(in.Language)
	if err != nil {
		return SnippetDTO{}, err
	}
	r, err := s.snippetRequest(in.Call)
	if err != nil {
		return SnippetDTO{}, err
	}
	out := SnippetDTO{Language: lang.ID, Mode: "resolved", Unresolved: []string{}}
	masked, changed := r.Masked(nil)
	if in.RevealSecrets {
		out.Revealed = changed
	} else {
		r, out.Masked = masked, changed
	}
	if out.Code, err = lang.Generate(r); err != nil {
		return SnippetDTO{}, err
	}
	return out, nil
}

// snippetRequest is the model of in: the defaults filled in as Invoke
// fills them, metadata as toMetadata keeps it ("-bin" values as standard
// Base64), the setup's paths absolute.
func (s *GRPCService) snippetRequest(in InvokeInputDTO) (snippet.GRPCRequest, error) {
	protocol, compression, err := callProtocol(in)
	if err != nil {
		return snippet.GRPCRequest{}, err
	}
	r := snippet.GRPCRequest{
		Address:     strings.TrimSpace(in.Address),
		TLS:         in.TLS,
		Insecure:    in.VerifyTLS != nil && !*in.VerifyTLS,
		Transport:   string(protocol),
		Compression: compression,
		Service:     strings.TrimSpace(in.Service),
		Method:      strings.TrimSpace(in.Method),
		Message:     in.Message,
	}
	if in.ConnectTimeoutMs != nil && *in.ConnectTimeoutMs > 0 {
		r.ConnectTimeout = time.Duration(*in.ConnectTimeoutMs) * time.Millisecond
	}
	if in.DeadlineMs != nil && *in.DeadlineMs > 0 {
		r.Deadline = time.Duration(*in.DeadlineMs) * time.Millisecond
	}
	root := s.root()
	for _, f := range in.Files {
		if f = strings.TrimSpace(f); f != "" {
			r.ProtoFiles = append(r.ProtoFiles, request.ResolveFilePath(root, f))
		}
	}
	for _, p := range in.ImportPaths {
		if p = strings.TrimSpace(p); p != "" {
			r.ImportPaths = append(r.ImportPaths, request.ResolveFilePath(root, p))
		}
	}
	for _, row := range in.Metadata {
		key := strings.ToLower(strings.TrimSpace(row.Name))
		if !row.Enabled || key == "" {
			continue
		}
		value := row.Value
		if transport.IsBinaryKey(key) {
			// grpcurl decodes standard or URL-safe Base64 without
			// whitespace; Invoke takes it with whitespace too.
			if b, ok := decodeBase64(value); ok {
				value = base64.StdEncoding.EncodeToString(b)
			}
		}
		r.Metadata = append(r.Metadata, snippet.Header{Name: key, Value: value})
	}
	return r, nil
}
