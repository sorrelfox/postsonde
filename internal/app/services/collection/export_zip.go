package collection

import (
	"context"
	"strings"
	"time"

	core "github.com/sorrelfox/postsonde/internal/core/collection"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/importer"
	"github.com/sorrelfox/postsonde/internal/core/importer/projectzip"
	"github.com/sorrelfox/postsonde/internal/core/storage/atomicfile"
)

// Folder zips (EXP-02, docs/import.md#folder-zips): a folder, with its
// subfolders and requests, saved as a zip of native project files the
// user picks in the native save dialog. Secrets are never in it: the
// files are read as the project has them (pending edits written first),
// with their secrets in the local side files the export never reads, and
// a secret typed into a file by hand is left out and counted. The zip is
// imported back with the "postsonde-folder" import format.

// ExportZipInputDTO is the input of ExportFolderZip.
type ExportZipInputDTO struct {
	// ID is the folder to export.
	ID string `json:"id"`
}

// ExportZipResultDTO is what ExportFolderZip returns.
type ExportZipResultDTO struct {
	// Saved is false when the user cancelled the dialog.
	Saved bool   `json:"saved"`
	Path  string `json:"path"`
	// Name is the folder's name.
	Name string `json:"name"`
	// Folders counts the folders in the zip, the exported one included;
	// Requests the requests; Skipped the items left out.
	Folders  int `json:"folders"`
	Requests int `json:"requests"`
	Skipped  int `json:"skipped"`
	// SecretsRemoved counts the secrets typed into project files by hand
	// that were left out of the zip. Secrets kept on this computer are
	// never read by the export.
	SecretsRemoved int `json:"secretsRemoved"`
	// Warnings counts the warning notes.
	Warnings int             `json:"warnings"`
	Notes    []ExportNoteDTO `json:"notes"`
}

// ExportFolderZip writes folder in.ID, with everything inside, as a zip of
// project files the user picks in the native save dialog (EXP-02).
// Pending edits are written first. Requests of unknown kinds, files of a
// newer PostSonde and files that did not load are left out and listed in
// the notes. Nothing is written when the user cancels (saved false, the
// notes still filled).
//
// Errors: workspace.none_open without a project, collection.not_found for
// an ID that is not a folder, projectzip.too_large for a folder too large
// to import again, collection.export_failed when the file cannot be
// written, collection.export_unavailable without a save dialog.
func (s *CollectionService) ExportFolderZip(ctx context.Context, in ExportZipInputDTO) (ExportZipResultDTO, error) {
	if s.pickSave == nil {
		return ExportZipResultDTO{}, errs.New(errs.Unsupported, "Exporting is not available.").WithCode(CodeExportUnavailable)
	}
	if err := interrupted(ctx, "Exporting"); err != nil {
		return ExportZipResultDTO{}, err
	}
	res, err := s.buildZip(in.ID)
	if err != nil {
		return ExportZipResultDTO{}, err
	}
	out := toExportZipDTO(res)
	path, err := s.pickSave(ctx, SaveDialog{
		Title:      "Export as zip",
		Message:    "Export the folder as a zip of PostSonde project files. Secrets are not exported.",
		ButtonText: "Export",
		Filename:   projectzip.FileName(res.Name),
	})
	if err != nil {
		return ExportZipResultDTO{}, errs.Wrap(err, errs.Internal, "The save dialog could not be shown.")
	}
	if path == "" {
		return out, nil
	}
	if err := atomicfile.WriteFile(path, res.Data, 0o644); err != nil {
		kind := errs.KindOf(err)
		if kind == errs.Internal {
			kind = errs.IO
		}
		return ExportZipResultDTO{}, errs.Wrap(err, kind, "The zip could not be saved to "+path+".").WithCode(CodeExportFailed).
			WithDetail("path", path).WithHint("Check that the folder exists and can be written to, and that the disk is not full.")
	}
	s.logger.Info("exported as zip", "folders", res.Folders, "requests", res.Requests, "skipped", res.Skipped, "secretsRemoved", res.SecretsRemoved)
	out.Saved, out.Path = true, path
	return out, nil
}

// buildZip makes the zip of folder id.
func (s *CollectionService) buildZip(id string) (*projectzip.ExportResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ws, tree, err := s.currentLocked(false)
	if err != nil {
		return nil, err
	}
	// Pending edits go to disk first: the export reads the files as the
	// project has them, with their secrets in the local side files.
	s.flushDirLocked(ws.Root)
	n := tree.Node(id)
	if id == "" || n == nil || n.Kind != core.KindFolder {
		return nil, errs.New(errs.NotFound, "The folder is no longer in the project.").
			WithCode(core.CodeNotFound).
			WithHint("It may have been moved or deleted; reload the project.").
			WithDetail("id", id)
	}
	return projectzip.Export(n, projectzip.ExportOptions{Modified: time.Now()})
}

func toExportZipDTO(r *projectzip.ExportResult) ExportZipResultDTO {
	out := ExportZipResultDTO{Name: r.Name, Folders: r.Folders, Requests: r.Requests, Skipped: r.Skipped,
		SecretsRemoved: r.SecretsRemoved, Notes: make([]ExportNoteDTO, 0, len(r.Notes))}
	for _, n := range r.Notes {
		if n.Severity == importer.SeverityWarning {
			out.Warnings++
		}
		out.Notes = append(out.Notes, ExportNoteDTO{
			Path: strings.Join(n.Path, " / "), Kind: n.Kind, Code: n.Code, Severity: string(n.Severity), Message: n.Message, Skipped: n.Skipped,
		})
	}
	return out
}
