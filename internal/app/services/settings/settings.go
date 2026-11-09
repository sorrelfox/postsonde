// Package settings is the bound service for the settings file
// (SET-01). The file itself (layout, validation, unknown keys, backups) is
// internal/core/settings; this service exposes it to the UI and tells the
// UI when it changes. See docs/settings.md.
package settings

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/sorrelfox/postsonde/internal/app/apperr"
	"github.com/sorrelfox/postsonde/internal/core/appsecrets"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	core "github.com/sorrelfox/postsonde/internal/core/settings"
	"github.com/sorrelfox/postsonde/internal/core/sysproxy"
)

// CodeOpenFailed: OpenFile or RevealFile could not hand the file to the
// OS. Details: path, when there is a file.
const CodeOpenFailed = "settings.open_failed"

// PollInterval is how often the file is checked for changes made outside
// the app. Reading a file this small every two seconds costs nothing, works
// on every OS and file system, and needs no file-watching dependency.
const PollInterval = 2 * time.Second

// SettingsService reads and updates the settings and emits settings:changed.
type SettingsService struct {
	store    *core.Store
	emit     func(name string, data any)
	logger   *slog.Logger
	interval time.Duration
	desktop  Desktop
	dataDir  string
	pick     FilePicker // AddCABundle's dialog (WithFilePicker); nil fails
	// projectRoot is the open project's folder, which CheckFileAccess
	// counts as allowed (WithProjectRoot); nil for none.
	projectRoot func() string

	secretsMu sync.Mutex
	secrets   *appsecrets.Store // certificate passphrases, the proxy password (WithSecrets)
	// getenv reads the environment for the ALL_PROXY proxy (WithProxyEnv);
	// nil reads nothing.
	getenv func(string) string
	// testProxy runs the proxy connection test (WithProxyTester); nil
	// fails TestProxy as unsupported.
	testProxy ProxyTester
	// systemProxy reads the OS proxy settings (WithSystemProxy); nil
	// reads none.
	systemProxy func() (sysproxy.Settings, error)

	emitMu sync.Mutex // one snapshot-and-emit at a time, so events stay in order

	// listMu serialises the changes of the list settings (allowed
	// directories, CA bundles, client certificates), each a read of the
	// list and a write of it changed: two windows adding at once both keep
	// their entry (APP-06).
	listMu sync.Mutex

	watchMu sync.Mutex
	stop    chan struct{} // closed to stop the watcher; nil when not running
	done    chan struct{} // closed when the watcher has stopped
}

// Desktop hands a file to the operating system. The app passes Wails'
// Browser.OpenFile and Env.OpenFileManager; a nil function makes the
// matching method fail with an unsupported error (tests, headless runs).
type Desktop struct {
	// Open opens path in its default application.
	Open func(path string) error
	// Reveal shows path, selected, in the file manager.
	Reveal func(path string) error
}

// An Option configures New.
type Option func(*SettingsService)

// WithDesktop sets how OpenFile and RevealFile reach the OS.
func WithDesktop(d Desktop) Option {
	return func(s *SettingsService) { s.desktop = d }
}

// WithDataDir sets the data directory Location reports.
func WithDataDir(dir string) Option {
	return func(s *SettingsService) { s.dataDir = dir }
}

// New returns a SettingsService over store. emit sends an event to the UI
// (the app passes its event emitter; nil drops events). A nil logger means
// slog.Default().
func New(store *core.Store, emit func(name string, data any), logger *slog.Logger, opts ...Option) *SettingsService {
	if logger == nil {
		logger = slog.Default()
	}
	if emit == nil {
		emit = func(string, any) {}
	}
	s := &SettingsService{
		store:    store,
		emit:     emit,
		logger:   logger.With("service", Domain),
		interval: PollInterval,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// ServiceName names the service in Wails logs.
func (s *SettingsService) ServiceName() string { return Domain }

// ServiceStartup starts watching the file for hand edits.
func (s *SettingsService) ServiceStartup(context.Context, application.ServiceOptions) error {
	s.startWatching(s.interval)
	return nil
}

// ServiceShutdown stops watching the file.
func (s *SettingsService) ServiceShutdown() error {
	s.stopWatching()
	return nil
}

// Get returns the settings in effect.
func (s *SettingsService) Get(ctx context.Context) (SettingsDTO, error) {
	if err := ctx.Err(); err != nil {
		return SettingsDTO{}, errs.Wrap(err, errs.KindOf(err), "Loading the settings was interrupted.")
	}
	return toSettingsDTO(s.store.Settings()), nil
}

// Update applies the non-null fields of patch, saves the file and returns
// the settings in effect. An invalid value is an invalid_argument error with
// code settings.invalid_value, and nothing is changed or written. A change
// is followed by a settings:changed event with source "app".
//
// When the file is from a newer PostSonde or cannot be read, the change is
// kept for this session only (Problems says so).
func (s *SettingsService) Update(ctx context.Context, patch SettingsPatchDTO) (SettingsDTO, error) {
	if err := ctx.Err(); err != nil {
		return SettingsDTO{}, errs.Wrap(err, errs.KindOf(err), "Saving the settings was interrupted.")
	}
	next, changed, err := s.store.Update(patch.toCore())
	if err != nil {
		return SettingsDTO{}, err
	}
	if changed {
		s.publish(SourceApp)
	}
	return toSettingsDTO(next), nil
}

// Problems returns what is wrong with the settings file: values replaced
// by their defaults, a file that could not be parsed or read, or one from a
// newer version. It is empty when the file is fine or missing.
func (s *SettingsService) Problems(ctx context.Context) ([]apperr.ErrorDTO, error) {
	if err := ctx.Err(); err != nil {
		return nil, errs.Wrap(err, errs.KindOf(err), "Loading the settings problems was interrupted.")
	}
	return toProblemDTOs(s.store.State().Problems), nil
}

// Location returns where the settings file and the data directory are.
func (s *SettingsService) Location(ctx context.Context) (LocationDTO, error) {
	if err := ctx.Err(); err != nil {
		return LocationDTO{}, errs.Wrap(err, errs.KindOf(err), "Finding the settings file was interrupted.")
	}
	return LocationDTO{Path: s.store.Path(), Exists: s.store.State().Exists, DataDir: s.dataDir}, nil
}

// OpenFile opens the settings file in the default application for JSON
// files, for editing by hand. A missing file is first written with the
// settings in effect. Errors: unsupported (code settings.open_failed) when
// the settings are kept in memory or the OS cannot be asked, and
// settings.open_failed with the cause's kind when the OS refuses.
func (s *SettingsService) OpenFile(ctx context.Context) error {
	return s.handOver(ctx, s.desktop.Open, "opened")
}

// RevealFile shows the settings file in the file manager (Finder,
// Explorer), writing it first if it does not exist yet. Errors as OpenFile.
func (s *SettingsService) RevealFile(ctx context.Context) error {
	return s.handOver(ctx, s.desktop.Reveal, "shown")
}

// handOver creates the file if needed and passes its path to do.
func (s *SettingsService) handOver(ctx context.Context, do func(string) error, verb string) error {
	if err := ctx.Err(); err != nil {
		return errs.Wrap(err, errs.KindOf(err), "Opening the settings file was interrupted.")
	}
	path := s.store.Path()
	if path == "" || do == nil {
		return errs.New(errs.Unsupported, "The settings file cannot be "+verb+" here.").
			WithCode(CodeOpenFailed).
			WithHint("This run of PostSonde keeps its settings in memory, so there is no file to open.")
	}
	if err := s.store.Create(); err != nil {
		return err
	}
	if err := do(path); err != nil {
		return errs.Wrap(err, errs.KindOf(err), "The settings file could not be "+verb+".").
			WithCode(CodeOpenFailed).
			WithHint("Open it yourself: "+path).
			WithDetail("path", path)
	}
	return nil
}

// publish emits settings:changed with the store's current state. Taking
// the snapshot under emitMu keeps the events in the order of the states
// they carry, so the last event always has the latest settings.
func (s *SettingsService) publish(source string) {
	s.emitMu.Lock()
	defer s.emitMu.Unlock()
	st := s.store.State()
	s.emit(EventChanged, ChangedEventDTO{
		Settings: toSettingsDTO(st.Settings),
		Problems: toProblemDTOs(st.Problems),
		Source:   source,
	})
}

// startWatching polls the file every interval and publishes a "file"
// event when Reload reports a change. The store recognises its own writes,
// so they are not reported. It does nothing for an in-memory store or when
// already watching.
func (s *SettingsService) startWatching(interval time.Duration) {
	s.watchMu.Lock()
	defer s.watchMu.Unlock()
	if s.stop != nil || s.store.Path() == "" {
		return
	}
	stop, done := make(chan struct{}), make(chan struct{})
	s.stop, s.done = stop, done
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if s.store.Reload() {
					s.logger.Info("settings file changed on disk", "path", s.store.Path())
					s.publish(SourceFile)
				}
			}
		}
	}()
}

// stopWatching stops the watcher and waits for it to finish.
func (s *SettingsService) stopWatching() {
	s.watchMu.Lock()
	defer s.watchMu.Unlock()
	if s.stop == nil {
		return
	}
	close(s.stop)
	<-s.done
	s.stop, s.done = nil, nil
}
