// Package proxyscopes is the bound service behind the project and
// environment proxies (SOCKS-08) on the settings screen: it lists the
// scopes of the open project, the proxy in effect and which scope it
// comes from, and saves a change of one scope. Scope passwords go to the
// project's local secret side file and never come back to the UI
// (SOCKS-10). The logic is internal/core/proxyscope; see
// docs/network.md#proxy-scopes.
package proxyscopes

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/sorrelfox/postsonde/internal/app/apperr"
	coreenvironments "github.com/sorrelfox/postsonde/internal/core/environments"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/proxyscope"
	"github.com/sorrelfox/postsonde/internal/core/workspace"
)

// Domain names the service and its events.
const Domain = "proxyscopes"

// ProxyScopesService reads and changes the proxy scopes of the open
// project.
type ProxyScopesService struct {
	workspaces *workspace.Manager
	resolve    func(context.Context) (proxyscope.Effective, error)
	emit       func(name string, data any)
	logger     *slog.Logger

	// mu serialises changes: a proxyscope.Store is not safe for
	// concurrent writes.
	mu sync.Mutex

	// refresh and every run on a timer while the app runs (WithRefresh).
	refresh func(context.Context)
	every   time.Duration
	stop    chan struct{}
	done    chan struct{}
}

// Option configures a ProxyScopesService.
type Option func(*ProxyScopesService)

// WithRefresh runs refresh every interval between ServiceStartup and
// ServiceShutdown: the app reads the proxy in effect again, so a change
// nothing announces (the OS proxy settings) closes the idle pools too.
func WithRefresh(interval time.Duration, refresh func(context.Context)) Option {
	return func(s *ProxyScopesService) { s.every, s.refresh = interval, refresh }
}

// New returns the service. resolve returns the proxy in effect, as the
// app's dialer reads it (internal/app, proxyResolver); emit sends
// EventChanged after a change. A nil logger means slog.Default().
func New(workspaces *workspace.Manager, resolve func(context.Context) (proxyscope.Effective, error), emit func(name string, data any), logger *slog.Logger, opts ...Option) *ProxyScopesService {
	if logger == nil {
		logger = slog.Default()
	}
	s := &ProxyScopesService{workspaces: workspaces, resolve: resolve, emit: emit, logger: logger.With("service", Domain)}
	for _, o := range opts {
		o(s)
	}
	return s
}

// ServiceName names the service in Wails logs.
func (s *ProxyScopesService) ServiceName() string { return Domain }

// ServiceStartup starts the refresh timer (WithRefresh).
func (s *ProxyScopesService) ServiceStartup(context.Context, application.ServiceOptions) error {
	if s.refresh == nil || s.every <= 0 || s.stop != nil {
		return nil
	}
	s.stop, s.done = make(chan struct{}), make(chan struct{})
	go func(stop, done chan struct{}) {
		defer close(done)
		t := time.NewTicker(s.every)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				s.refresh(context.Background())
			}
		}
	}(s.stop, s.done)
	return nil
}

// ServiceShutdown stops the refresh timer.
func (s *ProxyScopesService) ServiceShutdown() error {
	if s.stop != nil {
		close(s.stop)
		<-s.done
		s.stop, s.done = nil, nil
	}
	return nil
}

// Scopes returns the scopes of the open project, its environments and
// the proxy in effect. Without an open project only the proxy in effect
// (the app's) is filled in.
func (s *ProxyScopesService) Scopes(ctx context.Context) (ScopesDTO, error) {
	if err := ctx.Err(); err != nil {
		return ScopesDTO{}, errs.Wrap(err, errs.KindOf(err), "Loading the proxy scopes was interrupted.")
	}
	return s.describe(ctx)
}

// SetScope changes the proxy of the project (level "project") or of one
// of its environments (level "environment") and returns Scopes. Mode
// "inherit" leaves the choice to the next level out, "manual" uses url
// (a password in it, or in password, goes to the local secret storage;
// null keeps the stored one, "" removes it), "off" is no proxy. Errors
// carry detail field: "level", "environmentId", "mode", "url", "password"
// or "bypass". Sends EventChanged.
func (s *ProxyScopesService) SetScope(ctx context.Context, in ScopeInputDTO) (ScopesDTO, error) {
	if err := ctx.Err(); err != nil {
		return ScopesDTO{}, errs.Wrap(err, errs.KindOf(err), "Saving the proxy scope was interrupted.")
	}
	w := s.workspaces.Current()
	if w == nil {
		_, err := s.workspaces.LocalDir()
		return ScopesDTO{}, err
	}
	s.mu.Lock()
	envs, err := coreenvironments.New(w.Root, w.LocalDir, true).List()
	if err != nil {
		s.mu.Unlock()
		return ScopesDTO{}, err
	}
	known := map[string]bool{}
	for _, e := range envs.Environments {
		known[e.ID] = true
	}
	if in.Level == proxyscope.LevelEnvironment && !known[in.EnvironmentID] {
		s.mu.Unlock()
		return ScopesDTO{}, errs.New(errs.NotFound, "The environment no longer exists.").
			WithCode(coreenvironments.CodeNotFound).WithDetail("field", "environmentId")
	}
	err = proxyscope.New(w.LocalDir).Set(proxyscope.Input{
		Level:         in.Level,
		EnvironmentID: in.EnvironmentID,
		Mode:          in.Mode,
		URL:           in.URL,
		Password:      in.Password,
		Bypass:        in.Bypass,
	}, func(id string) bool { return known[id] })
	s.mu.Unlock()
	if err != nil {
		return ScopesDTO{}, err
	}
	s.logger.Info("proxy scope changed", "level", in.Level, "environment", in.EnvironmentID, "mode", in.Mode)
	if s.emit != nil {
		s.emit(EventChanged, ChangedDTO{Level: in.Level, EnvironmentID: in.EnvironmentID})
	}
	return s.describe(ctx)
}

func (s *ProxyScopesService) describe(ctx context.Context) (ScopesDTO, error) {
	dto := ScopesDTO{Environments: []EnvironmentScopeDTO{}, Project: scopeDTO(proxyscope.Scope{}, false)}
	dto.Effective = s.effective(ctx)
	w := s.workspaces.Current()
	if w == nil {
		return dto, nil
	}
	dto.Open = true
	store := proxyscope.New(w.LocalDir)
	envStore := coreenvironments.New(w.Root, w.LocalDir, true)
	snap, err := envStore.List()
	if err != nil {
		return ScopesDTO{}, err
	}
	cfg, err := store.Read()
	if err != nil {
		e := apperr.ToDTO(err)
		dto.Error = &e
		return dto, nil
	}
	passwords, err := store.Passwords()
	if err != nil {
		e := apperr.ToDTO(err)
		dto.Error = &e
		passwords = nil
	}
	dto.Project = scopeDTO(cfg.Project, proxyscope.HasPassword(cfg.Project, passwords, proxyscope.LevelProject, ""))
	for _, env := range snap.Environments {
		sc := cfg.Environments[env.ID]
		dto.Environments = append(dto.Environments, EnvironmentScopeDTO{
			ID:     env.ID,
			Name:   env.Name,
			Active: env.ID == snap.ActiveID,
			Scope:  scopeDTO(sc, proxyscope.HasPassword(sc, passwords, proxyscope.LevelEnvironment, env.ID)),
		})
	}
	return dto, nil
}

func (s *ProxyScopesService) effective(ctx context.Context) EffectiveDTO {
	dto := EffectiveDTO{Bypass: []string{}}
	if s.resolve == nil {
		dto.Level = proxyscope.LevelApp
		return dto
	}
	eff, err := s.resolve(ctx)
	dto.Level = eff.Level
	if err != nil {
		e := apperr.ToDTO(err)
		dto.Error = &e
		if ae, ok := errors.AsType[*errs.Error](err); ok {
			if src := ae.Details["source"]; src == proxyscope.LevelProject || src == proxyscope.LevelEnvironment {
				dto.Level = src
			}
		}
		return dto
	}
	p, hp := eff.Config.Proxy, eff.Config.ForHTTP()
	for _, u := range []netx.ProxyURL{p, hp} {
		if err := u.CheckSupported(); !u.IsZero() && err != nil {
			e := apperr.ToDTO(err)
			dto.Error = &e
			return dto
		}
	}
	dto.Proxy = p.String()
	dto.RemoteDNS = !p.IsZero() && p.RemoteDNS()
	if eff.Config.SplitHTTP {
		dto.HTTPSplit = true
		if hp != p {
			dto.HTTPProxy = hp.String()
		}
		if p.IsZero() && !hp.IsZero() {
			dto.RemoteDNS = hp.RemoteDNS()
		}
	}
	dto.Source = eff.Source
	dto.NoProxyVar = eff.NoProxyVar
	if !eff.Config.IsZero() {
		if b := eff.Config.Bypass.Entries(); b != nil {
			dto.Bypass = b
		}
	}
	return dto
}

func scopeDTO(sc proxyscope.Scope, hasPassword bool) ScopeDTO {
	mode := sc.Mode
	if sc.Inherits() {
		mode = proxyscope.ModeInherit
	}
	bypass := sc.Bypass
	if bypass == nil {
		bypass = []string{}
	}
	return ScopeDTO{Mode: mode, URL: sc.URL, HasPassword: hasPassword, Bypass: bypass}
}
