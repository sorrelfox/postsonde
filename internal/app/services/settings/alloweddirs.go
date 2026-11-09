package settings

import (
	"context"
	"os"
	"path/filepath"
	"slices"

	"github.com/sorrelfox/postsonde/internal/app/apperr"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/fileaccess"
	core "github.com/sorrelfox/postsonde/internal/core/settings"
)

// Allowed directories for file access (PRV-10): privacy.allowedDirs, the
// folders whose files the features driven by project data ({{$file PATH}},
// JWT key and netrc files, schema files, .proto files, server
// certificates) may read besides the open project's. The settings
// screen's Privacy page lists, adds and removes them with the methods
// below, and the editors' file pickers ask CheckFileAccess about a chosen
// file and offer AllowDirectory; the services read the list whenever they
// read a file. See docs/privacy.md#local-file-access-prv-10.

// CodeAllowedDirMissing: an allowed directory does not exist any more or
// is not a directory (kind not_found on AllowedDirDTO.Error), or the path
// chosen to add is not one (kind invalid_argument).
const CodeAllowedDirMissing = "settings.allowed_dir_missing"

// AllowedDirDTO is one allowed directory, as checked now.
type AllowedDirDTO struct {
	// Path is the directory's absolute path, as stored in the settings.
	Path string `json:"path"`
	// Error says why it allows nothing (moved, deleted, not a
	// directory); null when it is fine.
	Error *apperr.ErrorDTO `json:"error"`
}

// AllowedDirs returns the allowed directories, in the settings' order.
func (s *SettingsService) AllowedDirs(ctx context.Context) ([]AllowedDirDTO, error) {
	if err := ctx.Err(); err != nil {
		return nil, errs.Wrap(err, errs.KindOf(err), "Loading the allowed directories was interrupted.")
	}
	return describeAllowedDirs(s.store.Settings().Privacy.AllowedDirs), nil
}

// AddAllowedDir shows the native folder dialog and adds the chosen
// directory to the allowed directories, then returns them as AllowedDirs
// does. Nothing changes when the user cancels or picks a directory already
// in the list.
func (s *SettingsService) AddAllowedDir(ctx context.Context) ([]AllowedDirDTO, error) {
	if err := ctx.Err(); err != nil {
		return nil, errs.Wrap(err, errs.KindOf(err), "Adding an allowed directory was interrupted.")
	}
	if err := s.store.Policy().CheckLocked(allowedDirsKey); err != nil {
		return nil, err
	}
	if s.pick == nil {
		return nil, errs.New(errs.Unsupported, "Choosing a folder is not available here.").WithCode(CodePickUnavailable)
	}
	path, err := s.pick(ctx, FileDialog{
		Title:      "Allow a directory",
		Message:    "Choose a folder whose files projects may read: {{$file}}, key, schema, .proto and certificate files.",
		ButtonText: "Allow",
		Folder:     true,
	})
	if err != nil {
		return nil, errs.Wrap(err, errs.KindOf(err), "The folder dialog could not be shown.").WithCode(CodePickUnavailable)
	}
	if path == "" {
		return s.AllowedDirs(ctx)
	}
	if err := s.addAllowedDir(path); err != nil {
		return nil, err
	}
	return s.AllowedDirs(ctx)
}

// allowedDirsKey is the settings key of the allowed directories.
const allowedDirsKey = "privacy.allowedDirs"

// addAllowedDir adds the folder at path to the list, once.
func (s *SettingsService) addAllowedDir(path string) error {
	path = filepath.Clean(path)
	if info, err := os.Stat(path); err != nil || !info.IsDir() || !filepath.IsAbs(path) {
		return errs.New(errs.InvalidArgument, "Only a folder can be allowed.").
			WithCode(CodeAllowedDirMissing).
			WithHint("Choose a folder, not a file.").
			WithDetail("path", path)
	}
	// Read and changed under the list lock, so two windows adding at once
	// both keep their folder.
	return s.editList(func() error {
		list := s.store.Settings().Privacy.AllowedDirs
		if slices.Contains(list, path) {
			return nil
		}
		if err := s.updateAllowedDirs(append(slices.Clone(list), path)); err != nil {
			return err
		}
		s.logger.Info("allowed directory added", "path", path)
		return nil
	})
}

// WithProjectRoot gives the open project's folder ("" when none is open),
// which CheckFileAccess counts as allowed, and against which it resolves
// a relative path.
func WithProjectRoot(root func() string) Option {
	return func(s *SettingsService) { s.projectRoot = root }
}

// FileAccessDTO says whether the features limited to the allowed
// directories may read a file (PRV-10): what a file picker of the editors
// (a JWT key, a schema, .proto files, a server certificate) shows after
// the user chose one.
type FileAccessDTO struct {
	// Allowed is true when the file is in the open project's folder or in
	// an allowed directory, links followed.
	Allowed bool `json:"allowed"`
	// Folder is the folder to allow for it, "" when Allowed: the folder
	// the file really is in (links followed), or the path itself for a
	// folder (an import path).
	Folder string `json:"folder"`
	// Locked is true when the admin policy locks privacy.allowedDirs
	// (SET-02): the folder cannot be added from the app.
	Locked bool `json:"locked"`
}

// CheckFileAccess reports whether path, as stored in a project (absolute,
// "~/…", or relative to the open project's folder), is a file or folder
// the features driven by project data may read, and which folder would
// allow it. Nothing is read. An empty path, or a relative one without a
// project, is fileaccess.invalid_path.
func (s *SettingsService) CheckFileAccess(ctx context.Context, path string) (FileAccessDTO, error) {
	if err := ctx.Err(); err != nil {
		return FileAccessDTO{}, errs.Wrap(err, errs.KindOf(err), "Checking the file was interrupted.")
	}
	p := fileaccess.Policy{Allowed: s.store.Settings().Privacy.AllowedDirs}
	if s.projectRoot != nil {
		p.ProjectRoot = s.projectRoot()
	}
	out := FileAccessDTO{Locked: s.store.Policy().IsLocked(allowedDirsKey)}
	abs, err := p.Abs(path)
	if err != nil {
		return FileAccessDTO{}, err
	}
	if _, err := p.Resolve(path); !fileaccess.IsNotAllowed(err) {
		out.Allowed = true
		return out, nil
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		out.Folder = filepath.Dir(filepath.Clean(abs))
		return out, nil
	}
	if info, err := os.Stat(real); err == nil && info.IsDir() {
		out.Folder = real
	} else {
		out.Folder = filepath.Dir(real)
	}
	return out, nil
}

// AllowDirectory adds the folder at path to the allowed directories, as
// the offer of a file picker does after CheckFileAccess, and returns them
// as AllowedDirs does. A folder already in the list changes nothing. It
// fails with settings.locked when the admin policy locks the list, and
// with CodeAllowedDirMissing for a path that is not a folder.
func (s *SettingsService) AllowDirectory(ctx context.Context, path string) ([]AllowedDirDTO, error) {
	if err := ctx.Err(); err != nil {
		return nil, errs.Wrap(err, errs.KindOf(err), "Adding an allowed directory was interrupted.")
	}
	if err := s.store.Policy().CheckLocked(allowedDirsKey); err != nil {
		return nil, err
	}
	if err := s.addAllowedDir(path); err != nil {
		return nil, err
	}
	return s.AllowedDirs(ctx)
}

// RemoveAllowedDir removes path from the allowed directories and returns
// them as AllowedDirs does. A path not in the list changes nothing.
func (s *SettingsService) RemoveAllowedDir(ctx context.Context, path string) ([]AllowedDirDTO, error) {
	if err := ctx.Err(); err != nil {
		return nil, errs.Wrap(err, errs.KindOf(err), "Removing an allowed directory was interrupted.")
	}
	err := s.editList(func() error {
		list := s.store.Settings().Privacy.AllowedDirs
		i := slices.Index(list, path)
		if i < 0 {
			return nil
		}
		if err := s.updateAllowedDirs(slices.Delete(slices.Clone(list), i, i+1)); err != nil {
			return err
		}
		s.logger.Info("allowed directory removed", "path", path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.AllowedDirs(ctx)
}

// editList runs a read and change of a list setting, one at a time
// (listMu).
func (s *SettingsService) editList(change func() error) error {
	s.listMu.Lock()
	defer s.listMu.Unlock()
	return change()
}

// updateAllowedDirs saves the list and publishes the change.
func (s *SettingsService) updateAllowedDirs(list []string) error {
	_, changed, err := s.store.Update(core.Patch{Privacy: core.PrivacyPatch{AllowedDirs: &list}})
	if err != nil {
		return err
	}
	if changed {
		s.publish(SourceApp)
	}
	return nil
}

// describeAllowedDirs checks that each path is a directory.
func describeAllowedDirs(paths []string) []AllowedDirDTO {
	out := make([]AllowedDirDTO, 0, len(paths))
	for _, p := range paths {
		dto := AllowedDirDTO{Path: p}
		if info, err := os.Stat(p); err != nil || !info.IsDir() {
			e := apperr.ToDTO(errs.New(errs.NotFound, "This folder does not exist any more, so it allows nothing.").
				WithCode(CodeAllowedDirMissing).
				WithHint("Restore it, or remove it from the list.").
				WithDetail("path", p))
			dto.Error = &e
		}
		out = append(out, dto)
	}
	return out
}
