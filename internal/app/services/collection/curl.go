package collection

import (
	"context"
	"unicode/utf8"

	core "github.com/sorrelfox/postsonde/internal/core/collection"
	"github.com/sorrelfox/postsonde/internal/core/curl"
	"github.com/sorrelfox/postsonde/internal/core/importer"
	"github.com/sorrelfox/postsonde/internal/core/importer/curlimport"
)

// cURL import (IMP-01): a pasted curl command becomes an HTTP request.
// internal/core/curl reads the command and maps it; ParseCurl shows the
// result (the URL bar's paste and the Import cURL dialog's live
// diagnostics), ImportCurl writes it as a new request. See docs/curl.md.

// MaxCurlLength is the longest command ParseCurl and ImportCurl read, in
// bytes; a longer text is cut there.
const MaxCurlLength = 1 << 20

// CurlDiagnosticDTO is a problem found in a pasted curl command.
type CurlDiagnosticDTO struct {
	// Code is stable (docs/curl.md): "curl.missing_url", "curl.not_imported", …
	Code string `json:"code"`
	// Severity is "error", "warning" or "info".
	Severity string `json:"severity"`
	// From and To are the range in the text, in UTF-16 code units as
	// JavaScript strings count them.
	From int `json:"from"`
	To   int `json:"to"`
	// Message never contains header, data or credential values.
	Message string `json:"message"`
}

// CurlImportDTO is what ParseCurl makes of a curl command.
type CurlImportDTO struct {
	// OK is false when a diagnostic is an error: the command cannot be
	// imported.
	OK bool `json:"ok"`
	// Request is the http request it maps to, without ID and name. It is
	// only a best effort when OK is false.
	Request RequestDTO `json:"request"`
	// Name is a suggested name: the URL's last path segment, or its host.
	Name string `json:"name"`
	// Diagnostics are sorted by From.
	Diagnostics []CurlDiagnosticDTO `json:"diagnostics"`
}

// ImportCurlInputDTO is the input of ImportCurl.
type ImportCurlInputDTO struct {
	// ParentID is the folder to create the request in; "" for the top
	// level.
	ParentID string `json:"parentId"`
	// Name is the request's name; blank uses the suggested one.
	Name string `json:"name"`
	// Text is the curl command.
	Text string `json:"text"`
	// Index is its position among the folder's items; -1 puts it last.
	Index int `json:"index"`
}

// ParseCurl reads a curl command and returns the request it maps to with
// the diagnostics. It reads no file and needs no open project.
func (s *CollectionService) ParseCurl(ctx context.Context, text string) (CurlImportDTO, error) {
	if err := interrupted(ctx, "Reading the cURL command"); err != nil {
		return CurlImportDTO{}, err
	}
	text = limitCurl(text)
	im := curl.Import(text)
	req := im.Request
	return CurlImportDTO{
		OK:          !curl.HasErrors(im.Diagnostics),
		Request:     toRequestDTO(&req),
		Name:        im.Name,
		Diagnostics: toCurlDiagnosticDTOs(text, im.Diagnostics),
	}, nil
}

// ImportCurl creates an http request from a curl command in the folder
// in.ParentID and returns the tree with its ID. A command with an error
// diagnostic fails with errs.Parse, code curl.invalid_command. A typed
// password or token is kept in the local side files, as UpdateRequest
// keeps it. It goes through the import framework (docs/import.md) with
// the items written straight into the folder.
func (s *CollectionService) ImportCurl(ctx context.Context, in ImportCurlInputDTO) (NodeResultDTO, error) {
	m, err := curlimport.Command(limitCurl(in.Text), in.Name)
	if err != nil {
		return NodeResultDTO{}, err
	}
	return s.nodeOp(ctx, "create", "Importing the cURL command", func(_ *core.Trash, t *core.Tree) (string, string, error) {
		// nodeOp holds s.mu and has just set s.ws to the open workspace.
		target := s.importTarget(t)
		target.Environments = nil
		rep, err := importer.Write(target, m, importer.Options{ParentID: in.ParentID, Index: in.Index, IntoParent: true})
		if err != nil {
			return "", "", err
		}
		// Items[0] is the folder, Items[1] the request.
		it := rep.Items[1]
		return it.NodeID, "", it.Err
	})
}

// limitCurl cuts text to MaxCurlLength bytes, at a character boundary.
func limitCurl(text string) string {
	if len(text) <= MaxCurlLength {
		return text
	}
	cut := MaxCurlLength
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}

func toCurlDiagnosticDTOs(text string, ds []curl.Diagnostic) []CurlDiagnosticDTO {
	out := make([]CurlDiagnosticDTO, 0, len(ds))
	idx := newUTF16Index(text)
	for _, d := range ds {
		out = append(out, CurlDiagnosticDTO{
			Code:     d.Code,
			Severity: string(d.Severity),
			From:     idx.at(d.Start),
			To:       idx.at(d.End),
			Message:  d.Message,
		})
	}
	return out
}

// utf16Index converts byte offsets of a text to UTF-16 offsets.
type utf16Index struct {
	text string
	// units[i] is the UTF-16 offset of byte i, for i up to len(text).
	units []int
}

func newUTF16Index(text string) utf16Index {
	units := make([]int, len(text)+1)
	n := 0
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		for j := range size {
			units[i+j] = n
		}
		n += utf16Len(r)
		i += size
	}
	units[len(text)] = n
	return utf16Index{text: text, units: units}
}

func utf16Len(r rune) int {
	if r >= 0x10000 {
		return 2
	}
	return 1
}

func (x utf16Index) at(b int) int {
	b = max(0, min(b, len(x.text)))
	return x.units[b]
}
