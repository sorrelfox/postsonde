package collection

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/app/apperr"
	envservice "github.com/sorrelfox/postsonde/internal/app/services/environments"
	core "github.com/sorrelfox/postsonde/internal/core/collection"
	"github.com/sorrelfox/postsonde/internal/core/environments"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/importer"
	"github.com/sorrelfox/postsonde/internal/core/importer/altair"
	"github.com/sorrelfox/postsonde/internal/core/importer/curlimport"
	"github.com/sorrelfox/postsonde/internal/core/importer/harimport"
	"github.com/sorrelfox/postsonde/internal/core/importer/insomnia"
	"github.com/sorrelfox/postsonde/internal/core/importer/openapiimport"
	"github.com/sorrelfox/postsonde/internal/core/importer/postman"
	"github.com/sorrelfox/postsonde/internal/core/importer/postsondeenv"
	"github.com/sorrelfox/postsonde/internal/core/importer/projectzip"
	"github.com/sorrelfox/postsonde/internal/core/requestsecrets"
	coreworkspace "github.com/sorrelfox/postsonde/internal/core/workspace"
)

// Imports (IMP-10, docs/import.md): a file is read by the importer of its
// format (internal/core/importer), previewed, then written into the open
// project with a report. PickImportFile asks for the file, PreviewImport
// reads it and says what would be created, Import writes it.

// FileDialog describes the native open dialog a FilePicker shows.
type FileDialog struct {
	Title      string
	Message    string
	ButtonText string
}

// FilePicker shows a native dialog to choose one file and returns its
// path, or "" when the user cancels. The app layer supplies it; tests fake
// it.
type FilePicker func(ctx context.Context, dialog FileDialog) (string, error)

// WithFilePicker sets the dialog PickImportFile shows.
func WithFilePicker(pick FilePicker) Option {
	return func(o *options) { o.pickFile = pick }
}

// WithImporters replaces the formats Import reads (by default every format
// PostSonde knows), for tests.
func WithImporters(r *importer.Registry) Option {
	return func(o *options) { o.importers = r }
}

// defaultImporters are the formats PostSonde imports; new formats are
// added here.
func defaultImporters() *importer.Registry {
	return importer.NewRegistry(curlimport.Importer{}, insomnia.Importer{}, postman.Importer{}, openapiimport.Importer{},
		harimport.Importer{}, altair.Importer{}, postsondeenv.Importer{}, projectzip.Importer{})
}

// ImportFormatDTO is a file format that can be imported.
type ImportFormatDTO struct {
	// ID is stable ("curl"); PreviewImport and Import take it.
	ID   string `json:"id"`
	Name string `json:"name"`
	// Extensions are the usual extensions, with the dot.
	Extensions []string `json:"extensions"`
}

// ImportLocationDTO says where something is in the imported file; every
// member is optional (0 or "").
type ImportLocationDTO struct {
	// Line and Column are 1-based, the column in characters.
	Line   int `json:"line"`
	Column int `json:"column"`
	// Path is the item's place in the format's structure: "command 2",
	// "item[0].item[3]".
	Path string `json:"path"`
}

// ImportDiagnosticDTO is a problem or a note about an item or the file.
type ImportDiagnosticDTO struct {
	Code string `json:"code"`
	// Severity is "error", "warning" or "info".
	Severity string            `json:"severity"`
	Message  string            `json:"message"`
	Source   ImportLocationDTO `json:"source"`
}

// ImportItemDTO is a line of a preview or report.
type ImportItemDTO struct {
	// Kind is "folder", "request", "environment" or "skipped".
	Kind string `json:"kind"`
	Name string `json:"name"`
	// Parent is the index of the folder the item is in; -1 for the first
	// item (the import's own folder) and the environments.
	Parent int `json:"parent"`
	// FileKind is a request's kind ("http"…); SkippedKind what a skipped
	// item was in the source ("curl command", "script"…).
	FileKind    string `json:"fileKind"`
	SkippedKind string `json:"skippedKind"`
	// Method is an http request's method; Detail its URL or address.
	Method string `json:"method"`
	Detail string `json:"detail"`
	// Status is "new" (preview), "imported", "skipped" or "failed".
	Status string `json:"status"`
	// NodeID is the created tree node's or environment's ID.
	NodeID      string                `json:"nodeId"`
	Source      ImportLocationDTO     `json:"source"`
	Diagnostics []ImportDiagnosticDTO `json:"diagnostics"`
}

// ImportCountsDTO sums up a preview or report.
type ImportCountsDTO struct {
	Folders      int `json:"folders"`
	Requests     int `json:"requests"`
	Environments int `json:"environments"`
	Skipped      int `json:"skipped"`
	Failed       int `json:"failed"`
	// Lossy counts the imported items with a warning.
	Lossy    int `json:"lossy"`
	Errors   int `json:"errors"`
	Warnings int `json:"warnings"`
	Infos    int `json:"infos"`
}

// ImportReportDTO is what PreviewImport would create, or what Import did.
type ImportReportDTO struct {
	Format ImportFormatDTO `json:"format"`
	// Path is the imported file; FileName its base name.
	Path     string `json:"path"`
	FileName string `json:"fileName"`
	// Digest identifies the file's content; Import checks it.
	Digest string `json:"digest"`
	// IntoParent is set when the items went into an existing folder.
	IntoParent bool `json:"intoParent"`
	// Items: the import's folder first, then its content depth first,
	// then the environments.
	Items       []ImportItemDTO       `json:"items"`
	Diagnostics []ImportDiagnosticDTO `json:"diagnostics"`
	Counts      ImportCountsDTO       `json:"counts"`
}

// PreviewImportInputDTO is the input of PreviewImport.
type PreviewImportInputDTO struct {
	Path string `json:"path"`
	// Format is a format ID; "" detects it from the file.
	Format string `json:"format"`
}

// ImportInputDTO is the input of Import.
type ImportInputDTO struct {
	Path   string `json:"path"`
	Format string `json:"format"`
	// Digest is the preview's; "" skips the check.
	Digest string `json:"digest"`
	// ParentID is the folder to import into; "" for the top level.
	ParentID string `json:"parentId"`
	// FolderName names the new folder; blank uses the suggested name.
	FolderName string `json:"folderName"`
	// IntoParent puts the items straight into ParentID, without a new
	// folder.
	IntoParent bool `json:"intoParent"`
	// SkipEnvironments leaves the file's environments out.
	SkipEnvironments bool `json:"skipEnvironments"`
	// Index is the new folder's position; -1 puts it last.
	Index int `json:"index"`
}

// ImportResultDTO is what Import returns.
type ImportResultDTO struct {
	Tree TreeDTO `json:"tree"`
	// NodeID is the new folder, or the folder the items went into ("" for
	// the top level).
	NodeID string          `json:"nodeId"`
	Report ImportReportDTO `json:"report"`
}

// ImportFormats lists the formats PostSonde imports.
func (s *CollectionService) ImportFormats(_ context.Context) []ImportFormatDTO {
	fs := s.importers.Formats()
	out := make([]ImportFormatDTO, 0, len(fs))
	for _, f := range fs {
		out = append(out, toImportFormatDTO(f))
	}
	return out
}

// PickImportFile shows the native dialog to choose a file to import and
// returns its path, or "" when the user cancels.
func (s *CollectionService) PickImportFile(ctx context.Context) (string, error) {
	if s.pickFile == nil {
		return "", errs.New(errs.Unsupported, "Files cannot be chosen here.").
			WithHint("This is a bug in PostSonde.")
	}
	return s.pickFile(ctx, FileDialog{
		Title:      "Import",
		Message:    "Choose a file to import into the project.",
		ButtonText: "Choose",
	})
}

// PreviewImport reads the file at in.Path with the importer of in.Format
// (detected when "") and returns what Import would create, with every
// diagnostic. It writes nothing and needs no open project. It fails with
// import.read_failed or import.too_large when the file cannot be read,
// import.unknown_format when no format fits, and import.invalid_file (or
// the format's own code) when the file is not valid in its format.
func (s *CollectionService) PreviewImport(ctx context.Context, in PreviewImportInputDTO) (ImportReportDTO, error) {
	m, im, src, digest, err := s.parseImport(ctx, in.Path, in.Format)
	if err != nil {
		return ImportReportDTO{}, err
	}
	return toImportReportDTO(importer.Preview(m), im, in.Path, src.Name, digest), nil
}

// Import reads the file again, checks that it is the previewed one
// (import.source_changed otherwise), and writes it into the open project:
// a new folder in in.ParentID (or its items straight into it), the
// environments next to the project's. Nothing is overwritten. It fails,
// writing nothing, when the parent is gone or read-only or the new folder
// cannot be created; items that fail after that are "failed" in the
// report. Emits collection:changed (op "import"), and
// environments:changed when environments were created.
func (s *CollectionService) Import(ctx context.Context, in ImportInputDTO) (ImportResultDTO, error) {
	m, im, src, digest, err := s.parseImport(ctx, in.Path, in.Format)
	if err != nil {
		return ImportResultDTO{}, err
	}
	if in.Digest != "" && in.Digest != digest {
		return ImportResultDTO{}, importer.SourceChanged(in.Path)
	}
	var rep *importer.Report
	var envs *environments.Store
	res, err := s.nodeOp(ctx, "import", "Importing", func(_ *core.Trash, t *core.Tree) (string, string, error) {
		target := s.importTarget(t)
		envs = target.Environments
		var err error
		rep, err = importer.Write(target, m, importer.Options{
			ParentID:         in.ParentID,
			Index:            in.Index,
			FolderName:       in.FolderName,
			IntoParent:       in.IntoParent,
			SkipEnvironments: in.SkipEnvironments,
		})
		if err != nil {
			return "", "", err
		}
		s.logger.Info("import written", "format", im.Format().ID, "folders", rep.Counts.Folders,
			"requests", rep.Counts.Requests, "environments", rep.Counts.Environments,
			"skipped", rep.Counts.Skipped, "failed", rep.Counts.Failed)
		return rep.Items[0].NodeID, "", nil
	})
	if err != nil {
		return ImportResultDTO{}, err
	}
	if rep.Counts.Environments > 0 && envs != nil {
		active := ""
		if env, ok, err := envs.Active(); err == nil && ok {
			active = env.ID
		}
		s.emit(envservice.EventChanged, envservice.ChangedDTO{ActiveID: active})
	}
	return ImportResultDTO{
		Tree: res.Tree, NodeID: res.NodeID,
		Report: toImportReportDTO(rep, im, in.Path, src.Name, digest),
	}, nil
}

// MaxDetectFiles is the most files DetectImport looks at in one call; a
// drop of more is refused as a whole (IMP-11).
const MaxDetectFiles = 20

// CodeTooManyFiles: DetectImport was given more than MaxDetectFiles files
// (kind invalid_argument). Details: count, limit.
const CodeTooManyFiles = "import.too_many_files"

// DetectImportInputDTO is the input of DetectImport.
type DetectImportInputDTO struct {
	Paths []string `json:"paths"`
}

// DetectedImportDTO is what DetectImport found out about one file.
type DetectedImportDTO struct {
	Path     string `json:"path"`
	FileName string `json:"fileName"`
	// Format is the format PreviewImport would detect; its ID is "" when
	// the file cannot be imported (Error says why).
	Format ImportFormatDTO `json:"format"`
	// Confidence is the importer's (importer.Maybe … importer.Sure); 0
	// when none.
	Confidence int `json:"confidence"`
	// Error is why the file cannot be imported: import.unknown_format,
	// import.read_failed or import.too_large. Nil when Format is set.
	Error *apperr.ErrorDTO `json:"error"`
}

// DetectImport says, for each file of in.Paths in their order, which
// format an import reads it in (IMP-11: files dropped on the window). It
// reads each file as PreviewImport does and asks the same detection
// (importer.Registry.Detect), so a file it recognises previews in that
// format. A file that cannot be read or is in no known format is an entry
// with Error, not a failure. It fails only with no paths, a relative path,
// or more than MaxDetectFiles files (import.too_many_files). It writes
// nothing, fetches nothing and needs no open project.
func (s *CollectionService) DetectImport(ctx context.Context, in DetectImportInputDTO) ([]DetectedImportDTO, error) {
	if len(in.Paths) == 0 {
		return nil, errs.New(errs.InvalidArgument, "Choose a file to import.").
			WithCode(importer.CodeReadFailed).
			WithHint("This is a bug in PostSonde.")
	}
	if len(in.Paths) > MaxDetectFiles {
		return nil, errs.New(errs.InvalidArgument, "Too many files to import at once.").
			WithCode(CodeTooManyFiles).
			WithHint(fmt.Sprintf("Drop at most %d files at a time.", MaxDetectFiles)).
			WithDetail("count", strconv.Itoa(len(in.Paths))).
			WithDetail("limit", strconv.Itoa(MaxDetectFiles))
	}
	out := make([]DetectedImportDTO, 0, len(in.Paths))
	for _, path := range in.Paths {
		if err := interrupted(ctx, "Reading the files"); err != nil {
			return nil, err
		}
		if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
			return nil, errs.New(errs.InvalidArgument, "Choose a file to import.").
				WithCode(importer.CodeReadFailed).
				WithHint("This is a bug in PostSonde.").
				WithDetail("path", path)
		}
		d := DetectedImportDTO{Path: path, FileName: filepath.Base(path), Format: ImportFormatDTO{Extensions: []string{}}}
		src, _, err := importer.ReadFile(path)
		var im importer.Importer
		if err == nil {
			im, err = s.importers.Detect(src)
		}
		if err != nil {
			e := apperr.ToDTO(err)
			d.Error = &e
		} else {
			d.Format = toImportFormatDTO(im.Format())
			d.Confidence = im.Detect(src)
		}
		out = append(out, d)
	}
	return out, nil
}

// parseImport reads and parses the file at path.
func (s *CollectionService) parseImport(ctx context.Context, path, format string) (*importer.Model, importer.Importer, importer.Source, string, error) {
	if err := interrupted(ctx, "Reading the file"); err != nil {
		return nil, nil, importer.Source{}, "", err
	}
	if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
		return nil, nil, importer.Source{}, "", errs.New(errs.InvalidArgument, "Choose a file to import.").
			WithCode(importer.CodeReadFailed).
			WithHint("This is a bug in PostSonde.").
			WithDetail("path", path)
	}
	src, digest, err := importer.ReadFile(path)
	if err != nil {
		return nil, nil, src, "", err
	}
	m, im, err := s.importers.Parse(ctx, src, format)
	if err != nil {
		return nil, nil, src, "", err
	}
	return m, im, src, digest, nil
}

// importTarget is where an import writes: the tree, the workspace's local
// side files and its environments. s.mu is held (nodeOp has set s.ws).
func (s *CollectionService) importTarget(t *core.Tree) importer.Target {
	ws := s.ws
	return importer.Target{
		Tree:         t,
		Secrets:      requestsecrets.New(ws.LocalDir),
		Environments: environmentsOf(ws),
	}
}

func environmentsOf(ws *coreworkspace.Workspace) *environments.Store {
	return environments.New(ws.Root, ws.LocalDir, ws.Newer())
}

func toImportFormatDTO(f importer.Format) ImportFormatDTO {
	ext := f.Extensions
	if ext == nil {
		ext = []string{}
	}
	return ImportFormatDTO{ID: f.ID, Name: f.Name, Extensions: ext}
}

func toImportLocationDTO(l importer.Location) ImportLocationDTO {
	return ImportLocationDTO{Line: l.Line, Column: l.Column, Path: l.Path}
}

func toImportDiagnosticDTOs(ds []importer.Diagnostic) []ImportDiagnosticDTO {
	out := make([]ImportDiagnosticDTO, 0, len(ds))
	for _, d := range ds {
		out = append(out, ImportDiagnosticDTO{
			Code: d.Code, Severity: string(d.Severity), Message: d.Message, Source: toImportLocationDTO(d.Source),
		})
	}
	return out
}

func toImportReportDTO(r *importer.Report, im importer.Importer, path, name, digest string) ImportReportDTO {
	items := make([]ImportItemDTO, 0, len(r.Items))
	for _, it := range r.Items {
		items = append(items, ImportItemDTO{
			Kind: string(it.Kind), Name: it.Name, Parent: it.Parent,
			FileKind: string(it.FileKind), SkippedKind: it.SkippedKind,
			Method: it.Method, Detail: it.Detail, Status: string(it.Status), NodeID: it.NodeID,
			Source: toImportLocationDTO(it.Source), Diagnostics: toImportDiagnosticDTOs(it.Diagnostics),
		})
	}
	c := r.Counts
	return ImportReportDTO{
		Format: toImportFormatDTO(im.Format()), Path: path, FileName: name, Digest: digest,
		IntoParent: r.IntoParent, Items: items, Diagnostics: toImportDiagnosticDTOs(r.Diagnostics),
		Counts: ImportCountsDTO{
			Folders: c.Folders, Requests: c.Requests, Environments: c.Environments,
			Skipped: c.Skipped, Failed: c.Failed, Lossy: c.Lossy,
			Errors: c.Errors, Warnings: c.Warnings, Infos: c.Infos,
		},
	}
}
