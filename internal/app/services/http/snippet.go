package http

import (
	"context"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/snippet"
)

// The snippet panel (GEN-12, GEN-13): a request as code in one of
// the snippet package's languages, resolved or templated, secrets masked
// unless the user reveals them. See docs/snippets.md.

// Snippet modes, for SnippetInputDTO.Mode.
const (
	// SnippetResolved writes the request as a send would build it now,
	// variables filled in (also "").
	SnippetResolved = "resolved"
	// SnippetTemplated keeps every {{reference}} as written.
	SnippetTemplated = "templated"
)

// CodeSnippetMode: SnippetInputDTO.Mode is not a mode (kind
// invalid_argument). Details: mode.
const CodeSnippetMode = "http.snippet_mode"

// SnippetLanguageDTO is a language of the snippet panel.
type SnippetLanguageDTO struct {
	// ID is what SnippetInputDTO.Language takes ("curl").
	ID string `json:"id"`
	// Name is shown when the UI has no text of its own for ID.
	Name string `json:"name"`
	// Syntax is how the code is highlighted: "shell" or "text".
	Syntax string `json:"syntax"`
}

// SnippetInputDTO is what Snippet takes.
type SnippetInputDTO struct {
	RequestID string `json:"requestId"`
	// Language is a SnippetLanguageDTO.ID; "" is curl.
	Language string `json:"language"`
	// Mode is SnippetResolved (also "") or SnippetTemplated.
	Mode string `json:"mode"`
	// RevealSecrets writes the real values of the secrets in resolved
	// mode (PRV-09): the user asked for them in this view. Templated code
	// never holds a secret value, so it changes nothing there.
	RevealSecrets bool `json:"revealSecrets"`
}

// SnippetDTO is a request as code.
type SnippetDTO struct {
	Language string `json:"language"`
	Mode     string `json:"mode"`
	Code     string `json:"code"`
	// Masked is set when secrets were replaced by ••••••.
	Masked bool `json:"masked"`
	// Revealed is set when the code holds the real values of secrets
	// that would otherwise be masked (RevealSecrets in resolved mode).
	Revealed bool `json:"revealed"`
	// Unresolved are the names of the undefined {{variables}} left as
	// written in resolved mode, sorted; empty in templated mode.
	Unresolved []string `json:"unresolved"`
}

// SnippetLanguages returns the languages of the snippet panel, in the
// order it lists them.
func (s *HTTPService) SnippetLanguages() []SnippetLanguageDTO {
	langs := snippet.Languages()
	out := make([]SnippetLanguageDTO, len(langs))
	for i, l := range langs {
		out[i] = SnippetLanguageDTO{ID: l.ID, Name: l.Name, Syntax: l.Syntax}
	}
	return out
}

// snippetResolvedBuild is how the panel builds a resolved snippet: as the
// header preview does (headerPreviewBuild), since the panel follows every
// edit. Nothing is fetched or asked: no token request (a stored token or
// the "Bearer …" placeholder), no {{$file}} read and no netrc file read
// (both left as placeholders), no {{$prompt}} dialog (left as written).
// Everything else is resolved as a send resolves it.
var snippetResolvedBuild = headerPreviewBuild

// snippetTemplatedBuild keeps every reference as written, with the auth
// of snippet.TemplateAuth.
var snippetTemplatedBuild = buildOptions{allowUnresolved: true, preview: true, deferNetrc: true, deferFiles: true, templated: true}

// Snippet returns request in.RequestID (HTTP, GraphQL or SSE) as code in
// in.Language (GEN-12, GEN-13). Nothing is sent, fetched or asked.
//
//   - Resolved: built as a send builds it (snippetResolvedBuild), the
//     same variables, auth, folder headers, settings and jar cookies,
//     then written like Copy as cURL. Secrets are masked as Copy as cURL
//     masks them unless in.RevealSecrets, which shows the values of this
//     very build. Undefined variables stay as written and are listed.
//   - Templated: built with every {{reference}} kept as written
//     (snippet.KeepReferences) and snippet.TemplateAuth; no jar cookie.
//     Values typed in the auth's secret members and what an auth makes
//     for each send are ••••••; a credential made of references is kept
//     (snippet.Request.MaskedTemplate). in.RevealSecrets changes nothing.
//
// Nothing is kept: every call builds anew, so a reveal lasts as long as
// the caller asks for it. It fails as CurlCommand does, with
// CodeSnippetMode for an unknown mode and snippet.CodeUnknownLanguage for
// an unknown language.
func (s *HTTPService) Snippet(ctx context.Context, in SnippetInputDTO) (SnippetDTO, error) {
	if err := ctx.Err(); err != nil {
		return SnippetDTO{}, errs.Wrap(err, errs.KindOf(err), "Writing the request as code was interrupted.")
	}
	lang, err := snippet.LanguageByID(in.Language)
	if err != nil {
		return SnippetDTO{}, err
	}
	out := SnippetDTO{Language: lang.ID, Mode: in.Mode, Unresolved: []string{}}
	var r snippet.Request
	switch in.Mode {
	case "", SnippetResolved:
		out.Mode = SnippetResolved
		_, x, b, err := s.build(ctx, in.RequestID, snippetResolvedBuild)
		if err != nil {
			return SnippetDTO{}, err
		}
		if r, err = snippet.FromBuild(b, s.jarCookies(b)); err != nil {
			return SnippetDTO{}, err
		}
		out.Unresolved = x.skippedNames()
		masked, changed := r.Masked(secretsOf(x, b))
		if in.RevealSecrets {
			out.Revealed = changed
		} else {
			r, out.Masked = masked, changed
		}
	case SnippetTemplated:
		src, _, b, err := s.build(ctx, in.RequestID, snippetTemplatedBuild)
		if err != nil {
			return SnippetDTO{}, err
		}
		if r, err = snippet.FromTemplateBuild(b); err != nil {
			return SnippetDTO{}, err
		}
		// What the templated auth added is a reference or Mask already
		// (b.AuthSecrets would mask the references): the values typed in
		// it are masked wherever else they appear.
		var masked bool
		r, masked = r.MaskedTemplate(snippet.TypedSecrets(src.Auth))
		_, standIn := snippet.TemplateAuth(src.Auth)
		out.Masked = masked || standIn
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
