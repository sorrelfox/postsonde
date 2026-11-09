package environments

import (
	"context"
	"time"

	core "github.com/sorrelfox/postsonde/internal/core/environments"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/importer/postman"
	"github.com/sorrelfox/postsonde/internal/core/storage/atomicfile"
)

// Environment export (EXP-05, docs/environments.md#export): an
// environment as a file the user picks, without secret or local values.

// Export formats.
const (
	// FormatPostSonde is a PostSonde environment file, the format of the
	// project's environments/ files.
	FormatPostSonde = "postsonde"
	// FormatPostman is a Postman environment export.
	FormatPostman = "postman"
)

// Error codes of Export.
const (
	// CodeInvalidFormat: the format is not one of the Format constants
	// (kind invalid_argument). Details: format.
	CodeInvalidFormat = "environments.invalid_format"
	// CodeExportFailed: the file could not be written (kind io, not_found
	// or permission_denied). Details: path.
	CodeExportFailed = "environments.export_failed"
	// CodeExportUnavailable: there is no save dialog (kind unsupported).
	CodeExportUnavailable = "environments.export_unavailable"
)

// SaveDialog describes the native save dialog Export shows.
type SaveDialog struct {
	Title      string
	Message    string
	ButtonText string
	// Filename is the suggested file name.
	Filename string
}

// SavePicker shows the native save dialog and returns the chosen path, or
// "" when the user cancels. The app layer supplies it; tests fake it.
type SavePicker func(ctx context.Context, d SaveDialog) (string, error)

// WithExport lets the user export environments (EXP-05) with the save
// dialog pick; version is the app's version, written in Postman exports.
func WithExport(pick SavePicker, version string) Option {
	return func(s *EnvironmentsService) { s.pickSave, s.version = pick, version }
}

// ExportInputDTO is what Export takes.
type ExportInputDTO struct {
	// ID is the environment to export.
	ID string `json:"id"`
	// Format is "postsonde" or "postman".
	Format string `json:"format"`
}

// ExportResultDTO is what Export returns.
type ExportResultDTO struct {
	// Saved is false when the user cancelled the dialog.
	Saved bool   `json:"saved"`
	Path  string `json:"path"`
	// Variables counts the variables written; Secrets those of them that
	// are secret, written without their value.
	Variables int `json:"variables"`
	Secrets   int `json:"secrets"`
}

// Export writes environment in.ID to a file the user picks in the native
// save dialog (EXP-05): a PostSonde environment file ("postsonde"), which
// the PostSonde environment importer reads back, or a Postman environment
// export ("postman"). It is made from the environment's file in the
// project only, never from the local side files: every secret variable is
// written without a value (as a secret, so it can be filled in after an
// import) and local values (SEC-02) are left out. Nothing is written when
// the user cancels. It works in a read-only workspace too.
//
// Errors: workspace.none_open, environments.not_found,
// environments.invalid_format, environments.export_failed,
// environments.export_unavailable without a save dialog.
func (s *EnvironmentsService) Export(ctx context.Context, in ExportInputDTO) (ExportResultDTO, error) {
	if s.pickSave == nil {
		return ExportResultDTO{}, errs.New(errs.Unsupported, "Exporting environments is not available.").WithCode(CodeExportUnavailable).
			WithHint("Environments can be exported in the PostSonde app only.")
	}
	if in.Format != FormatPostSonde && in.Format != FormatPostman {
		return ExportResultDTO{}, errs.New(errs.InvalidArgument, "The export format is not known.").WithCode(CodeInvalidFormat).
			WithHint(`Use "postsonde" or "postman".`).WithDetail("format", in.Format)
	}
	if err := interrupted(ctx, "Exporting the environment"); err != nil {
		return ExportResultDTO{}, err
	}
	env, data, err := s.exportData(in)
	if err != nil {
		return ExportResultDTO{}, err
	}
	out := ExportResultDTO{Variables: len(env.Variables)}
	for _, v := range env.Variables {
		if v.Secret {
			out.Secrets++
		}
	}
	d := SaveDialog{
		Title:      "Export environment",
		Message:    "Export the environment as a PostSonde environment file. Secret and local values are not exported.",
		ButtonText: "Export",
		Filename:   core.ExportFileName(env.Name, ".json"),
	}
	if in.Format == FormatPostman {
		d.Message = "Export the environment for Postman. Secret and local values are not exported."
		d.Filename = core.ExportFileName(env.Name, postman.EnvironmentExtension)
	}
	path, err := s.pickSave(ctx, d)
	if err != nil {
		return ExportResultDTO{}, errs.Wrap(err, errs.Internal, "The save dialog could not be shown.")
	}
	if path == "" {
		return out, nil
	}
	if err := atomicfile.WriteFile(path, data, 0o644); err != nil {
		kind := errs.KindOf(err)
		if kind == errs.Internal {
			kind = errs.IO
		}
		return ExportResultDTO{}, errs.Wrap(err, kind, "The environment could not be saved to "+path+".").WithCode(CodeExportFailed).
			WithDetail("path", path).WithHint("Check that the folder exists and can be written to, and that the disk is not full.")
	}
	s.logger.Info("environment exported", "id", env.ID, "format", in.Format, "variables", out.Variables)
	out.Saved, out.Path = true, path
	return out, nil
}

// exportData makes the file of an export.
func (s *EnvironmentsService) exportData(in ExportInputDTO) (core.Environment, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.store()
	if err != nil {
		return core.Environment{}, nil, err
	}
	env, leftOut, err := st.Shared(in.ID)
	if err != nil {
		return core.Environment{}, nil, err
	}
	if leftOut > 0 {
		s.logger.Info("export left out secret values typed into the environment file", "id", env.ID, "count", leftOut)
	}
	var data []byte
	if in.Format == FormatPostman {
		data, err = postman.ExportEnvironment(env, s.version, time.Now())
	} else {
		data, err = core.MarshalExport(env)
	}
	if err != nil {
		return core.Environment{}, nil, errs.Wrap(err, errs.Internal, "The environment could not be exported.")
	}
	return env, data, nil
}
