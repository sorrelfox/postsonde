package graphql

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/fileaccess"
	"github.com/sorrelfox/postsonde/internal/core/graphql/schema"
	"github.com/sorrelfox/postsonde/internal/core/graphql/schemacache"
	"github.com/sorrelfox/postsonde/internal/core/request"
)

// A GraphQL request can take its schema from a local SDL file instead of
// introspection (GQL-11): its schemaFile holds the path as stored (relative
// to the project root with "/" when inside the project, else absolute).
// The path comes from the project, so the file is read only from the
// project folder or an allowed directory of the settings (PRV-10,
// fileaccess), with fileaccess.not_allowed for any other.
// The file is parsed with schema.ParseSDL and serves diagnostics,
// completion and the docs explorer like an introspected schema. It is
// never copied into the schema cache: the file is the source, re-read when
// its size or modification time changes and on ReloadSchemaFile.

// Error codes of schema files.
const (
	// CodeSchemaFileMissing: the schema file does not exist (kind not
	// found). Details: path.
	CodeSchemaFileMissing = "graphql.schema_file_missing"
	// CodeSchemaFileUnreadable: the schema file could not be read (kind
	// io). Details: path.
	CodeSchemaFileUnreadable = "graphql.schema_file_unreadable"
	// CodeSchemaFileTooLarge: the schema file is larger than
	// MaxSchemaFileSize (kind invalid argument). Details: path.
	CodeSchemaFileTooLarge = "graphql.schema_file_too_large"
	// CodeSchemaFileNoProject: the path is relative to the project, but
	// no project is open (kind invalid argument).
	CodeSchemaFileNoProject = "graphql.schema_file_no_project"
)

// MaxSchemaFileSize bounds the size of a schema file (GitHub's public
// schema is about 1.3 MB).
const MaxSchemaFileSize = 32 << 20

// MaxSchemaProblems bounds the problems SchemaInfoDTO lists.
const MaxSchemaProblems = 50

// Schema sources, SchemaInfoDTO.Source.
const (
	SourceIntrospection = "introspection"
	SourceFile          = "file"
)

// fileKeyPrefix starts the key of a file schema: a NUL cannot appear in
// an endpoint URL, so file keys never collide with endpoints.
const fileKeyPrefix = "file\x00"

// SchemaFileOf returns the schema file stored in GraphQL request id, ""
// when it uses introspection (collection.LoadHTTPSource).
type SchemaFileOf func(ctx context.Context, id string) (string, error)

// ProjectRoot returns the open project's root, "" when none is open.
type ProjectRoot func() string

// FileDialog describes the native dialog a FilePicker shows.
type FileDialog struct {
	Title, Message, ButtonText string
	// FilterName and FilterPattern ("*.graphql;*.graphqls") restrict the
	// files shown.
	FilterName, FilterPattern string
	// Directory is where the dialog starts; "" for the system default.
	Directory string
}

// FilePicker shows a native file dialog and returns the chosen absolute
// path, "" when the user cancels. The app layer supplies it.
type FilePicker func(ctx context.Context, d FileDialog) (string, error)

// WithSchemaFiles lets requests take their schema from SDL files:
// schemaFile reads a request's stored path, root resolves relative paths,
// pick shows the dialog of PickSchemaFile (nil makes it fail).
func WithSchemaFiles(schemaFile SchemaFileOf, root ProjectRoot, pick FilePicker) Option {
	return func(s *GraphQLService) {
		s.schemaFile, s.root, s.pick = schemaFile, root, pick
	}
}

// WithAllowedDirs gives the allowed directories of the settings
// (privacy.allowedDirs), read on every load: schema files are read from
// them and the open project's folder only (PRV-10). Without it only the
// project's folder is allowed.
func WithAllowedDirs(dirs func() []string) Option {
	return func(s *GraphQLService) { s.allowedDirs = dirs }
}

// filePolicy is what schema files are read through: the open project's
// folder and the allowed directories.
func (s *GraphQLService) filePolicy() fileaccess.Policy {
	p := fileaccess.Policy{Subject: "The schema file"}
	if s.root != nil {
		p.ProjectRoot = s.root()
	}
	if s.allowedDirs != nil {
		p.Allowed = s.allowedDirs()
	}
	return p
}

// fileSchema is a schema file as last read.
type fileSchema struct {
	path  string // absolute
	size  int64
	mod   time.Time
	entry *schemacache.Entry // nil when the file did not give a schema
	// problems are the syntax or type system problems, err the failure
	// that left entry nil (a *errs.Error).
	problems schema.Problems
	err      error
}

// fileKey is the SchemaInfoDTO.Key of the schema file at abs.
func fileKey(abs string) string { return fileKeyPrefix + abs }

// requestSchemaFile returns the stored schema file of request id, "" when
// the request uses introspection or files are not wired.
func (s *GraphQLService) requestSchemaFile(ctx context.Context, id string) (string, error) {
	if s.schemaFile == nil {
		return "", nil
	}
	stored, err := s.schemaFile(ctx, id)
	return strings.TrimSpace(stored), err
}

// resolveSchemaFile returns the absolute path of stored, which must be in
// the project folder or an allowed directory (fileaccess.not_allowed).
func (s *GraphQLService) resolveSchemaFile(stored string) (string, error) {
	p := s.filePolicy()
	abs, err := p.Abs(stored)
	if err != nil {
		return "", errs.New(errs.InvalidArgument, "The schema file's path is relative to the project, but no project is open.").
			WithCode(CodeSchemaFileNoProject)
	}
	abs = filepath.Clean(abs)
	if _, err := p.Resolve(stored); fileaccess.IsNotAllowed(err) {
		return "", err
	}
	return abs, nil
}

// loadFile returns the schema file at abs from memory while it is
// unchanged on disk, else reads and parses it again; reload forces the
// read.
func (s *GraphQLService) loadFile(abs string, reload bool) *fileSchema {
	info, statErr := os.Stat(abs)
	s.mu.Lock()
	cur := s.files[abs]
	s.mu.Unlock()
	// An unchanged file is not parsed again, also when it had errors.
	if cur != nil && !reload && statErr == nil && info.Size() == cur.size && info.ModTime().Equal(cur.mod) {
		return cur
	}
	f := readSchemaFile(s.filePolicy(), abs)
	s.mu.Lock()
	if len(s.files) >= maxMemory {
		clear(s.files)
	}
	s.files[abs] = f
	s.mu.Unlock()
	if f.err != nil {
		s.logger.Debug("schema file not loaded", "err", f.err)
	}
	return f
}

// readSchemaFile reads and parses the SDL file at abs through p.
func readSchemaFile(p fileaccess.Policy, abs string) *fileSchema {
	out := &fileSchema{path: abs, size: -1}
	fail := func(err error) *fileSchema {
		out.err = err
		return out
	}
	f, info, err := p.Open(abs)
	if err != nil {
		var e *errs.Error
		switch {
		case fileaccess.IsNotAllowed(err):
			return fail(err)
		case errors.As(err, &e) && e.Code == fileaccess.CodeUnreadable && e.Err == nil:
			err = errors.New("it is a folder")
		case errors.Is(err, fs.ErrNotExist):
			return fail(errs.New(errs.NotFound, "The schema file "+filepath.Base(abs)+" does not exist.").
				WithCode(CodeSchemaFileMissing).WithDetail("path", abs).
				WithHint("Choose the schema file again, or switch the request back to introspection."))
		}
		return fail(errs.Wrap(err, errs.IO, "The schema file "+filepath.Base(abs)+" could not be read.").
			WithCode(CodeSchemaFileUnreadable).WithDetail("path", abs))
	}
	defer f.Close()
	out.size, out.mod = info.Size(), info.ModTime()
	data, err := io.ReadAll(io.LimitReader(f, MaxSchemaFileSize+1))
	if err != nil {
		return fail(errs.Wrap(err, errs.IO, "The schema file "+filepath.Base(abs)+" could not be read.").
			WithCode(CodeSchemaFileUnreadable).WithDetail("path", abs))
	}
	if len(data) > MaxSchemaFileSize {
		return fail(errs.New(errs.InvalidArgument, "The schema file "+filepath.Base(abs)+" is larger than "+
			strconv.Itoa(MaxSchemaFileSize>>20)+" MB.").WithCode(CodeSchemaFileTooLarge).WithDetail("path", abs))
	}
	sch, err := schema.ParseSDL(string(data))
	errors.As(err, &out.problems)
	if sch == nil {
		return fail(err)
	}
	out.entry = &schemacache.Entry{Endpoint: abs, FetchedAt: out.mod, Schema: sch, Problems: len(out.problems)}
	return out
}

// cachedFile returns the schema of a file key, or nil; nil too for a
// file no longer allowed (PRV-10).
func (s *GraphQLService) cachedFile(key string) *schemacache.Entry {
	abs := strings.TrimPrefix(key, fileKeyPrefix)
	if _, err := s.filePolicy().Resolve(abs); fileaccess.IsNotAllowed(err) {
		return nil
	}
	return s.loadFile(abs, false).entry
}

// fileInfo describes the schema file stored as stored.
func (s *GraphQLService) fileInfo(endpoint, stored string, reload bool) SchemaInfoDTO {
	out := SchemaInfoDTO{Endpoint: endpoint, Source: SourceFile, File: stored, ProblemList: []SchemaProblemDTO{}}
	abs, err := s.resolveSchemaFile(stored)
	if err != nil {
		out.Error = errorDTO(err)
		return out
	}
	f := s.loadFile(abs, reload)
	if !f.mod.IsZero() {
		out.ModifiedAt = f.mod.UTC().Format(time.RFC3339)
	}
	for i, p := range f.problems {
		if i == MaxSchemaProblems {
			break
		}
		out.ProblemList = append(out.ProblemList, SchemaProblemDTO{Message: p.Message, Line: p.Line, Column: p.Column})
	}
	out.Problems = len(f.problems)
	if f.err != nil {
		out.Error = errorDTO(f.err)
		return out
	}
	out.Cached = true
	out.Key = fileKey(abs)
	out.FetchedAt = out.ModifiedAt
	out.TypeCount = schemacache.TypeCount(f.entry.Schema)
	return out
}

func errorDTO(err error) *SchemaErrorDTO {
	e := &SchemaErrorDTO{Message: err.Error()}
	var ee *errs.Error
	if errors.As(err, &ee) {
		e.Code, e.Message, e.Hint = ee.Code, ee.Message, ee.Hint
	}
	return e
}

// ReloadSchemaFile reads the schema file of GraphQL request in.RequestID
// again, even when it looks unchanged, and returns what SchemaInfo would
// (the Refresh button of a request with a schema file). A request without
// a schema file gets its introspection info.
func (s *GraphQLService) ReloadSchemaFile(ctx context.Context, in SchemaInputDTO) (SchemaInfoDTO, error) {
	if err := interrupted(ctx, "Reloading the schema file"); err != nil {
		return SchemaInfoDTO{}, err
	}
	return s.schemaInfo(ctx, in.RequestID, true)
}

// PickSchemaFile shows the native dialog for a .graphql or .graphqls SDL
// file and returns it as it is stored: relative to the open project's
// root with "/" when inside it, else absolute; "" when cancelled. The
// editor saves it as the request's schemaFile.
func (s *GraphQLService) PickSchemaFile(ctx context.Context) (string, error) {
	if err := interrupted(ctx, "Choosing the schema file"); err != nil {
		return "", err
	}
	if s.pick == nil {
		return "", errs.New(errs.Unsupported, "File dialogs are not available.")
	}
	root := ""
	if s.root != nil {
		root = s.root()
	}
	path, err := s.pick(ctx, FileDialog{
		Title:         "Load Schema from File",
		Message:       "Choose a GraphQL SDL file (.graphql or .graphqls) with the endpoint's schema.",
		ButtonText:    "Load",
		FilterName:    "GraphQL schema (*.graphql, *.graphqls, *.gql)",
		FilterPattern: "*.graphql;*.graphqls;*.gql",
		Directory:     root,
	})
	if err != nil {
		return "", errs.Wrap(err, errs.KindOf(err), "The dialog could not be shown.")
	}
	if path == "" {
		return "", nil
	}
	return request.StoredFilePath(root, path), nil
}
