// Package cookies is the bound service for the cookie manager (CK-02):
// listing, adding, editing and deleting the cookies of the open project's
// current jar. It also owns the jars for the rest of the app: Jars gives
// protocol services (HTTP sends, WebSocket handshakes) the same jar the
// manager edits, one per project and, when the project keeps separate
// cookies per environment (CK-03), one per environment. The logic is
// internal/core/cookies; see docs/cookies.md.
package cookies

import (
	"context"
	"log/slog"
	"net/url"
	"sync"

	core "github.com/sorrelfox/postsonde/internal/core/cookies"
	"github.com/sorrelfox/postsonde/internal/core/environments"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/workspace"
)

// Domain names the cookies service.
const Domain = "cookies"

// Workspaces finds the open project: *workspace.Manager.
type Workspaces interface {
	// LocalDir fails with workspace.none_open when no project is open.
	LocalDir() (string, error)
	// Current is the open project, or nil.
	Current() *workspace.Workspace
}

// Jars gives access to the cookie jar sends use now: the open project's
// shared jar or, when the project keeps a jar per environment (CK-03),
// the active environment's jar. Stores are opened on first use from the
// registry and saved in the project's local directory (docs/cookies.md).
// Every user of cookies in the app shares one Jars, so a cookie edited in
// the manager is the one the next request sends. It is safe for
// concurrent use.
type Jars struct {
	registry   *core.Registry
	workspaces Workspaces

	mu sync.Mutex
	// settings caches core.ReadSettings by local directory; CloseAll
	// forgets it.
	settings map[string]core.Settings
}

// NewJars returns the Jars over registry for the open project of
// workspaces.
func NewJars(registry *core.Registry, workspaces Workspaces) *Jars {
	return &Jars{registry: registry, workspaces: workspaces, settings: map[string]core.Settings{}}
}

// Location is the jar sends use now.
type Location struct {
	// LocalDir is the open project's local directory; Workspace its name.
	LocalDir  string
	Workspace string
	// PerEnvironment is the project's "separate cookies per environment"
	// setting.
	PerEnvironment bool
	// EnvironmentID and Environment (its name) are the active environment
	// whose jar this is; both "" for the shared jar (the setting is off,
	// or no environment is active).
	EnvironmentID string
	Environment   string
	// Jar is the jar's name in the registry: "" for the shared jar.
	Jar string
}

// Locate returns the jar sends use now. It fails with workspace.none_open
// when no project is open, and when the setting is on and the active
// environment cannot be read.
func (j *Jars) Locate() (Location, error) {
	w := j.workspaces.Current()
	if w == nil {
		_, err := j.workspaces.LocalDir()
		if err == nil {
			err = errs.New(errs.NotFound, "No project is open.").WithCode(workspace.CodeNoWorkspace)
		}
		return Location{}, err
	}
	loc := Location{LocalDir: w.LocalDir, Workspace: w.Name(), PerEnvironment: j.Settings(w.LocalDir).PerEnvironment}
	if !loc.PerEnvironment {
		return loc, nil
	}
	env, ok, err := environments.New(w.Root, w.LocalDir, w.Newer()).Active()
	if err != nil {
		return Location{}, err
	}
	if ok {
		loc.EnvironmentID, loc.Environment, loc.Jar = env.ID, env.Name, environments.SecretsKey(env.ID)
	}
	return loc, nil
}

// Settings returns the cookie settings of the project whose local
// directory is localDir.
func (j *Jars) Settings(localDir string) core.Settings {
	j.mu.Lock()
	defer j.mu.Unlock()
	st, ok := j.settings[localDir]
	if !ok {
		st = core.ReadSettings(localDir)
		j.settings[localDir] = st
	}
	return st
}

// SetPerEnvironment turns the open project's separate jars per environment
// on or off. No cookie moves: the shared jar keeps its cookies, and each
// environment's jar keeps its own for when the setting is on again.
func (j *Jars) SetPerEnvironment(on bool) error {
	dir, err := j.workspaces.LocalDir()
	if err != nil {
		return err
	}
	st := core.Settings{PerEnvironment: on}
	if err := core.WriteSettings(dir, st); err != nil {
		return err
	}
	j.mu.Lock()
	j.settings[dir] = st
	j.mu.Unlock()
	return nil
}

// Store returns the Store sends use now, opening it on first use.
func (j *Jars) Store() (*core.Store, error) {
	loc, err := j.Locate()
	if err != nil {
		return nil, err
	}
	return j.registry.OpenJar(loc.LocalDir, loc.Jar)
}

// Jar returns the jar sends use now. Pass it to
// httpclient.Client.WithCookieJar: the client then sends the jar's
// cookies and stores Set-Cookie on every hop, redirects included. A
// long-lived connection keeps the jar it got: switching the environment
// or the setting affects connections opened afterwards.
func (j *Jars) Jar() (*core.Jar, error) {
	s, err := j.Store()
	if err != nil {
		return nil, err
	}
	return s.Jar(), nil
}

// Header returns the Cookie header value the current jar sends to u (""
// when no cookie matches), for a send path that builds the header itself.
func (j *Jars) Header(u *url.URL) (string, error) {
	jar, err := j.Jar()
	if err != nil {
		return "", err
	}
	return jar.Header(u), nil
}

// SetCookieLines stores the raw Set-Cookie values of a response to u in
// the current jar. It returns the names stored and, for cookies ignored,
// "name: reason" (never values), as core.Jar.SetCookieLines.
func (j *Jars) SetCookieLines(u *url.URL, lines []string) (stored, rejected []string, err error) {
	jar, err := j.Jar()
	if err != nil {
		return nil, nil, err
	}
	stored, rejected = jar.SetCookieLines(u, lines)
	return stored, rejected, nil
}

// CloseAll saves and forgets every open jar and the cached settings: when
// the open project changes and on shutdown. The next call opens the new
// project's jar.
func (j *Jars) CloseAll() error {
	j.mu.Lock()
	j.settings = map[string]core.Settings{}
	j.mu.Unlock()
	return j.registry.CloseAll()
}

// CookiesService is the cookie manager's service. It has no events: the
// UI reloads the list when it opens the manager, when workspace:changed
// arrives and after each call.
type CookiesService struct {
	jars   *Jars
	logger *slog.Logger
}

// New returns a CookiesService over jars. A nil logger means
// slog.Default().
func New(jars *Jars, logger *slog.Logger) *CookiesService {
	if logger == nil {
		logger = slog.Default()
	}
	return &CookiesService{jars: jars, logger: logger.With("service", Domain)}
}

// ServiceName names the service in Wails logs.
func (s *CookiesService) ServiceName() string { return Domain }

// ServiceShutdown saves the jars when the app quits.
func (s *CookiesService) ServiceShutdown() error {
	if err := s.jars.CloseAll(); err != nil {
		s.logger.Warn("saving the cookie jars failed", "err", err)
	}
	return nil
}

func interrupted(ctx context.Context, what string) error {
	if err := ctx.Err(); err != nil {
		return errs.Wrap(err, errs.KindOf(err), what+" was interrupted.")
	}
	return nil
}

// List returns every unexpired cookie of the current jar (the open
// project's, or its active environment's), sorted by domain, path and
// name, which jar that is, and why its cookie file could not be loaded, if
// it could not.
func (s *CookiesService) List(ctx context.Context) (CookiesDTO, error) {
	if err := interrupted(ctx, "Loading the cookies"); err != nil {
		return CookiesDTO{}, err
	}
	return s.list()
}

func (s *CookiesService) list() (CookiesDTO, error) {
	loc, err := s.jars.Locate()
	if err != nil {
		return CookiesDTO{}, err
	}
	store, err := s.jars.registry.OpenJar(loc.LocalDir, loc.Jar)
	if err != nil {
		return CookiesDTO{}, err
	}
	out := toCookiesDTO(store.Jar().All(), store.Problem())
	out.Jar = JarDTO{
		Workspace:      loc.Workspace,
		PerEnvironment: loc.PerEnvironment,
		EnvironmentID:  loc.EnvironmentID,
		Environment:    loc.Environment,
	}
	return out, nil
}

// SetPerEnvironment turns the open project's "separate cookies per
// environment" setting on or off and returns the jar sends use from now
// on, as List. No cookie is moved or deleted.
func (s *CookiesService) SetPerEnvironment(ctx context.Context, on bool) (CookiesDTO, error) {
	if err := interrupted(ctx, "Changing the cookie setting"); err != nil {
		return CookiesDTO{}, err
	}
	if err := s.jars.SetPerEnvironment(on); err != nil {
		return CookiesDTO{}, err
	}
	s.logger.Info("separate cookie jars per environment changed", "on", on)
	return s.list()
}

// Set adds a cookie (in.Key "") or replaces the cookie with key in.Key,
// and returns it as stored. Invalid fields are invalid_argument with code
// cookies.invalid_cookie and a "field" detail; an identity another cookie
// has is cookies.exists; a replaced cookie that is gone is
// cookies.not_found.
func (s *CookiesService) Set(ctx context.Context, in SetInputDTO) (CookieDTO, error) {
	if err := interrupted(ctx, "Saving the cookie"); err != nil {
		return CookieDTO{}, err
	}
	c, err := fromCookieDTO(in.Cookie)
	if err != nil {
		return CookieDTO{}, err
	}
	var old *core.Cookie
	if in.Key != "" {
		k, err := decodeKey(in.Key)
		if err != nil {
			return CookieDTO{}, err
		}
		old = &k
	}
	jar, err := s.jars.Jar()
	if err != nil {
		return CookieDTO{}, err
	}
	stored, err := jar.Set(old, c)
	if err != nil {
		return CookieDTO{}, err
	}
	s.logger.Info("cookie saved", "name", stored.Name, "domain", stored.Domain, "replaced", old != nil)
	return toCookieDTO(stored), nil
}

// Delete removes the cookie with key. A cookie that is already gone is
// not an error.
func (s *CookiesService) Delete(ctx context.Context, key string) error {
	if err := interrupted(ctx, "Deleting the cookie"); err != nil {
		return err
	}
	k, err := decodeKey(key)
	if err != nil {
		return err
	}
	jar, err := s.jars.Jar()
	if err != nil {
		return err
	}
	if jar.Delete(k) {
		s.logger.Info("cookie deleted", "name", k.Name, "domain", k.Domain)
	}
	return nil
}

// DeleteDomain removes every cookie of a domain group (host-only and
// domain cookies with that Domain) and returns how many there were.
func (s *CookiesService) DeleteDomain(ctx context.Context, domain string) (int, error) {
	if err := interrupted(ctx, "Clearing the domain's cookies"); err != nil {
		return 0, err
	}
	jar, err := s.jars.Jar()
	if err != nil {
		return 0, err
	}
	n := jar.DeleteDomain(domain)
	s.logger.Info("cookies of a domain deleted", "domain", domain, "count", n)
	return n, nil
}

// Clear removes every cookie of the current jar (the open project's, or
// its active environment's); other jars keep theirs.
func (s *CookiesService) Clear(ctx context.Context) error {
	if err := interrupted(ctx, "Clearing the cookies"); err != nil {
		return err
	}
	jar, err := s.jars.Jar()
	if err != nil {
		return err
	}
	jar.Clear()
	s.logger.Info("cookie jar cleared")
	return nil
}
