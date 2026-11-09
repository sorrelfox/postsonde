package graphql

import (
	"context"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/graphql/schema"
	"github.com/sorrelfox/postsonde/internal/core/storage/atomicfile"
)

// Exporting a schema as SDL (GQL-20): SaveSchemaSDL prints the schema of
// a key with schema.SDL and writes it where the user picks in the native
// save dialog. Nothing is sent; the schema comes from the cache or the
// schema file, as for the docs explorer.

// Error codes of the SDL export.
const (
	// CodeNoSchema: the key has no schema to export (kind not found).
	CodeNoSchema = "graphql.no_schema"
	// CodeExportFailed: the SDL file could not be written (kind io).
	// Details: path.
	CodeExportFailed = "graphql.export_failed"
)

// SaveDialog describes the native save dialog SaveSchemaSDL shows.
type SaveDialog struct {
	Title      string
	Message    string
	ButtonText string
	// Filename is the suggested file name.
	Filename string
}

// SavePicker shows the native save dialog and returns the path the user
// chose, or "" when they cancelled. The OS asks before replacing a file.
type SavePicker func(ctx context.Context, d SaveDialog) (string, error)

// WithSaveDialog sets the dialog SaveSchemaSDL asks for the file with;
// without it SaveSchemaSDL fails with CodeUnavailable.
func WithSaveDialog(pick SavePicker) Option {
	return func(s *GraphQLService) { s.pickSave = pick }
}

// SaveSchemaSDL prints the schema of in.Key (SchemaInfoDTO.Key) as SDL,
// asks where to save it with the native save dialog (suggesting
// SchemaFileName(in.Name, key)) and writes it there. A cancelled dialog
// writes nothing and returns an empty Path. The file loads back, as a
// request's schema file (GQL-11), as the same schema.
//
// Errors: CodeNoSchema when the key has no schema, CodeExportFailed when
// the file cannot be written, CodeUnavailable without a save dialog.
func (s *GraphQLService) SaveSchemaSDL(ctx context.Context, in SaveSchemaInputDTO) (SaveSchemaResultDTO, error) {
	if err := interrupted(ctx, "Saving the schema"); err != nil {
		return SaveSchemaResultDTO{}, err
	}
	if s.pickSave == nil {
		return SaveSchemaResultDTO{}, unavailable()
	}
	e := s.cached(in.Key)
	if e == nil || e.Schema == nil {
		return SaveSchemaResultDTO{}, errs.New(errs.NotFound, "There is no schema to save.").
			WithCode(CodeNoSchema).
			WithHint("Fetch the schema first.")
	}
	text := schema.SDL(e.Schema)
	path, err := s.pickSave(ctx, SaveDialog{
		Title:      "Save Schema as SDL",
		Message:    "Save the schema as a GraphQL SDL file.",
		ButtonText: "Save",
		Filename:   SchemaFileName(in.Name, in.Key),
	})
	if err != nil {
		return SaveSchemaResultDTO{}, errs.Wrap(err, errs.Internal, "The save dialog could not be shown.")
	}
	if path == "" {
		return SaveSchemaResultDTO{}, nil
	}
	if err := atomicfile.WriteFile(path, []byte(text), 0o644); err != nil {
		s.logger.Warn("saving the schema failed", "err", err)
		kind := errs.KindOf(err)
		if kind == errs.Internal {
			kind = errs.IO // a full disk, a read-only volume, …
		}
		return SaveSchemaResultDTO{}, errs.Wrap(err, kind, "The schema could not be saved to "+path+".").
			WithCode(CodeExportFailed).WithDetail("path", path).
			WithHint("Check that the folder exists and can be written to, and that the disk is not full.")
	}
	types := 0
	for _, t := range e.Schema.Types {
		if !t.IsBuiltin() {
			types++
		}
	}
	s.logger.Info("schema saved as SDL", "bytes", len(text), "types", types)
	return SaveSchemaResultDTO{Path: path, TypeCount: types}, nil
}

// SchemaFileName suggests a file name for the schema of key: name (the
// request's) when it has usable characters, else the endpoint's host,
// else "schema", with the characters file systems refuse replaced and
// ".graphql" added. A schema file's key gives its own base name.
func SchemaFileName(name, key string) string {
	base := strings.TrimSpace(name)
	if base == "" {
		if abs, ok := strings.CutPrefix(key, fileKeyPrefix); ok {
			base = strings.TrimSuffix(filepath.Base(abs), filepath.Ext(abs))
		} else if u, err := url.Parse(key); err == nil && u.Hostname() != "" {
			base = u.Hostname()
		}
	}
	var b strings.Builder
	for _, r := range base {
		switch {
		case r < 0x20, strings.ContainsRune(`/\:*?"<>|`, r):
			b.WriteByte('-')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.Trim(b.String(), " .-")
	if out == "" {
		out = "schema"
	}
	if r := []rune(out); len(r) > 100 {
		out = string(r[:100])
	}
	return out + ".graphql"
}
