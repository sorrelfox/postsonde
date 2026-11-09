// Package themes is the bound service for custom themes (UI-05): it lists
// the theme files in the data directory's themes folder, validated by
// internal/core/themes, and opens that folder in the file manager. The UI
// applies the chosen theme itself (ui.customTheme in the settings). See
// docs/themes.md.
package themes

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"

	core "github.com/sorrelfox/postsonde/internal/core/themes"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// Domain names the service.
const Domain = "themes"

// Error codes, in addition to the internal/core/themes codes.
const (
	// CodeUnavailable: there is no data directory or no way to open a
	// folder (tests, headless runs).
	CodeUnavailable = "themes.unavailable"
	// CodeOpenFailed: the themes folder could not be created or opened.
	// Details: path.
	CodeOpenFailed = "themes.open_failed"
)

// ThemesService lists the custom themes and opens their folder.
type ThemesService struct {
	dir    string
	open   func(path string) error
	logger *slog.Logger
}

// An Option configures New.
type Option func(*ThemesService)

// WithOpen sets how OpenFolder shows the folder (the app passes Wails'
// Browser.OpenFile, which opens a folder in the file manager).
func WithOpen(open func(path string) error) Option {
	return func(s *ThemesService) { s.open = open }
}

// New returns the service for the themes folder of dataDir
// (<data>/themes). An empty dataDir has no themes.
func New(dataDir string, logger *slog.Logger, opts ...Option) *ThemesService {
	if logger == nil {
		logger = slog.Default()
	}
	s := &ThemesService{logger: logger.With("service", Domain)}
	if dataDir != "" {
		s.dir = filepath.Join(dataDir, core.DirName)
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// ServiceName names the service in Wails logs.
func (s *ThemesService) ServiceName() string { return Domain }

// List reads every theme file in the themes folder, sorted by file name.
// Files with errors are listed with their problems and usable false. The
// folder is read again on every call (the UI's Reload).
func (s *ThemesService) List(ctx context.Context) (ThemesDTO, error) {
	if err := ctx.Err(); err != nil {
		return ThemesDTO{}, errs.Wrap(err, errs.KindOf(err), "Loading the themes was interrupted.")
	}
	out := ThemesDTO{Dir: s.dir, Themes: []ThemeDTO{}}
	if s.dir == "" {
		return out, nil
	}
	list, err := core.List(s.dir)
	if err != nil {
		return ThemesDTO{}, err
	}
	for _, t := range list {
		if !t.Usable() {
			s.logger.Warn("theme file has errors", "file", t.ID, "problems", len(t.Problems))
		}
		out.Themes = append(out.Themes, toThemeDTO(t))
	}
	return out, nil
}

// OpenFolder creates the themes folder if needed (only the user can read
// it, like the rest of the data directory) and opens it in the file
// manager.
func (s *ThemesService) OpenFolder(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return errs.Wrap(err, errs.KindOf(err), "Opening the themes folder was interrupted.")
	}
	if s.dir == "" || s.open == nil {
		return errs.New(errs.Unsupported, "Opening the themes folder is not available here.").WithCode(CodeUnavailable)
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return errs.Wrap(err, errs.KindOf(err), "The themes folder could not be created.").
			WithCode(CodeOpenFailed).
			WithHint("Check that the data directory is writable.").
			WithDetail("path", s.dir)
	}
	if err := s.open(s.dir); err != nil {
		return errs.Wrap(err, errs.KindOf(err), "The themes folder could not be opened.").
			WithCode(CodeOpenFailed).
			WithHint("Open it yourself: "+s.dir).
			WithDetail("path", s.dir)
	}
	return nil
}
