// Package proxyscope keeps the proxies of a project and of its
// environments (SOCKS-08) and decides which proxy is in effect: the most
// specific scope that sets one wins.
//
//	environment (the active one)  >  project  >  app (settings.json)
//
// Each scope either inherits (the default: the next level out decides),
// sets a proxy of its own ("manual", with its own bypass list) or says "no
// proxy" ("off"). Proxies are a property of the computer and its network,
// not of the project, so the scopes live in the workspace's local
// side-file directory, never in a project file:
//
//	<local>/proxy-scopes.json                  {"version": 1, "project": {…}, "environments": {<environment id>: {…}}}
//	<local>/secrets/requests/proxy_scopes.json the scopes' proxy passwords (requestsecrets, SOCKS-10)
//
// A scope's URL is kept without its password: a password typed into it,
// or into the password field, goes to the requestsecrets side file under
// SecretsID, keyed "project.password" or "environment.<id>.password", like
// the typed auth secrets of requests. So scope credentials are never in a
// project file, an export or settings.json, and the log masker (DBG-07)
// knows them. Files are 0600 in 0700 directories, written atomically.
//
// A Store is not safe for concurrent writes; callers serialise Set.
// Reading while another goroutine writes is safe: files are replaced
// atomically. Standard library only. See docs/network.md#proxy-scopes.
package proxyscope

import (
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/requestsecrets"
	"github.com/sorrelfox/postsonde/internal/core/settings"
	"github.com/sorrelfox/postsonde/internal/core/storage/atomicfile"
)

// FileName is the scopes file in the workspace's local directory.
const FileName = "proxy-scopes.json"

// SecretsID is the requestsecrets item ID whose side file keeps the
// scopes' passwords (<local>/secrets/requests/proxy_scopes.json). No
// project item has an ID without a generated suffix like it.
const SecretsID = "proxy_scopes"

// Levels of a proxy scope, from the outermost.
const (
	// LevelApp: the app's settings (network.proxyMode, settings.json).
	LevelApp = "app"
	// LevelProject: the open project, on this computer.
	LevelProject = "project"
	// LevelEnvironment: the active environment of the open project, on
	// this computer.
	LevelEnvironment = "environment"
)

// Modes of a scope.
const (
	// ModeInherit: the next level out decides. The default.
	ModeInherit = "inherit"
	// ModeManual: the scope's URL, with its bypass list.
	ModeManual = "manual"
	// ModeOff: no proxy, whatever the levels out say.
	ModeOff = "off"
)

// Error codes.
const (
	// CodeLocalDataFailed: the scopes file could not be read, parsed or
	// written. The kind comes from the cause (parse for a broken file).
	// Details: path.
	CodeLocalDataFailed = "proxyscope.local_data_failed"
	// CodeInvalid: a scope that cannot be saved (kind invalid_argument):
	// an unknown level or mode, a manual scope without a URL. Details:
	// field.
	CodeInvalid = "proxyscope.invalid"
)

const (
	fileVersion             = 1
	dirPerm     fs.FileMode = 0o700
	filePerm    fs.FileMode = 0o600
)

// Scope is the proxy setting of one scope, as the file keeps it.
type Scope struct {
	// Mode is ModeManual or ModeOff; "" or ModeInherit inherits.
	Mode string `json:"mode"`
	// URL is the scope's proxy without its password. Kept while Mode is
	// not manual, so switching back restores it.
	URL string `json:"url,omitempty"`
	// Bypass lists the targets that connect directly while the scope's
	// proxy is in effect, as network.proxyBypass (netx.ParseBypass). The
	// app's list applies too.
	Bypass []string `json:"bypass,omitempty"`
}

// Inherits reports whether the scope leaves the choice to the next level
// out.
func (s Scope) Inherits() bool { return s.Mode == "" || s.Mode == ModeInherit }

func (s Scope) isEmpty() bool { return s.Inherits() && s.URL == "" && len(s.Bypass) == 0 }

// Config is every scope of a workspace.
type Config struct {
	Project Scope
	// Environments are the environments' scopes by environment ID.
	Environments map[string]Scope
}

type fileData struct {
	Version      int              `json:"version"`
	Project      *Scope           `json:"project,omitempty"`
	Environments map[string]Scope `json:"environments,omitempty"`
}

// Store reads and writes the scopes of one workspace.
type Store struct {
	local   string
	secrets *requestsecrets.Store
}

// New returns a Store for the workspace local directory localDir.
func New(localDir string) *Store {
	return &Store{local: localDir, secrets: requestsecrets.New(localDir)}
}

// Path returns the scopes file.
func (s *Store) Path() string { return filepath.Join(s.local, FileName) }

// Read returns the scopes; none when there is no file. A file that cannot
// be parsed is an error, so connections fail rather than skip a proxy
// the user set, and nothing overwrites it.
func (s *Store) Read() (Config, error) {
	path := s.Path()
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, failed(err, "PostSonde could not read the project's proxy settings.", path)
	}
	var f fileData
	if err := json.Unmarshal(data, &f); err != nil {
		return Config{}, errs.Wrap(err, errs.Parse, "The project's proxy settings on this computer could not be read.").
			WithCode(CodeLocalDataFailed).
			WithHint("Fix or delete the file; the project's and its environments' proxies then have to be set again under Settings, Network, Proxy.").
			WithDetail("path", path)
	}
	c := Config{Environments: f.Environments}
	if f.Project != nil {
		c.Project = *f.Project
	}
	return c, nil
}

func (s *Store) write(c Config) error {
	path := s.Path()
	f := fileData{Version: fileVersion}
	if !c.Project.isEmpty() {
		p := c.Project
		f.Project = &p
	}
	for id, sc := range c.Environments {
		if sc.isEmpty() {
			continue
		}
		if f.Environments == nil {
			f.Environments = map[string]Scope{}
		}
		f.Environments[id] = sc
	}
	if f.Project == nil && f.Environments == nil {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return failed(err, "PostSonde could not save the project's proxy settings.", path)
		}
		return nil
	}
	if err := os.MkdirAll(s.local, dirPerm); err != nil {
		return failed(err, "PostSonde could not save the project's proxy settings.", s.local)
	}
	if err := atomicfile.WriteJSON(path, f, filePerm); err != nil {
		return failed(err, "PostSonde could not save the project's proxy settings.", path)
	}
	return nil
}

// PasswordKey returns the key of a scope's password in the side file:
// "project.password" or "environment.<id>.password".
func PasswordKey(level, environmentID string) string {
	if level == LevelEnvironment {
		return "environment." + environmentID + ".password"
	}
	return "project.password"
}

// Passwords returns the stored scope passwords by PasswordKey.
func (s *Store) Passwords() (map[string]string, error) {
	return s.secrets.Read(SecretsID)
}

// Active is what the scopes say for the open project with environment
// active: the project's scope and the active environment's, with their
// stored passwords.
type Active struct {
	Project         Scope
	ProjectPassword string
	// EnvironmentID is the active environment, "" for none.
	EnvironmentID       string
	Environment         Scope
	EnvironmentPassword string
}

// Decides reports whether a scope sets the proxy (or "no proxy"), so the
// app's setting does not matter.
func (a Active) Decides() bool {
	return !a.Project.Inherits() || (a.EnvironmentID != "" && !a.Environment.Inherits())
}

// Active reads the scopes that apply with the environment environmentID
// active ("" for none). The passwords are read only when a manual scope
// needs one.
func (s *Store) Active(environmentID string) (Active, error) {
	c, err := s.Read()
	if err != nil {
		return Active{}, err
	}
	a := Active{Project: c.Project, EnvironmentID: environmentID}
	if environmentID != "" {
		a.Environment = c.Environments[environmentID]
	}
	if needsPassword(a.Project) || needsPassword(a.Environment) {
		pw, err := s.Passwords()
		if err != nil {
			return Active{}, err
		}
		a.ProjectPassword = pw[PasswordKey(LevelProject, "")]
		a.EnvironmentPassword = pw[PasswordKey(LevelEnvironment, environmentID)]
	}
	return a, nil
}

func needsPassword(sc Scope) bool {
	return sc.Mode == ModeManual && strings.Contains(sc.URL, "@")
}

// Input is a change of one scope.
type Input struct {
	// Level is LevelProject or LevelEnvironment.
	Level string
	// EnvironmentID names the environment of LevelEnvironment.
	EnvironmentID string
	// Mode is ModeInherit, ModeManual or ModeOff.
	Mode string
	// URL is the scope's proxy; required for ModeManual. A password in
	// it is moved to the side file.
	URL string
	// Password of the URL's user when the URL has none: nil keeps the
	// stored one, "" removes it.
	Password *string
	// Bypass replaces the scope's bypass list.
	Bypass []string
}

// Set checks and saves a change of one scope: the URL without its
// password in the scopes file, the password in the side file. keep
// reports whether an environment still exists; scopes of the others are
// dropped with their passwords (nil keeps them all). Errors: CodeInvalid
// and netx.CodeProxyInvalidURL (details field "mode", "url" or
// "password"), netx.CodeProxyInvalidBypass (field "bypass"), and
// CodeLocalDataFailed or requestsecrets.CodeLocalDataFailed.
func (s *Store) Set(in Input, keep func(environmentID string) bool) error {
	switch in.Level {
	case LevelProject:
		in.EnvironmentID = ""
	case LevelEnvironment:
		if in.EnvironmentID == "" {
			return invalid("Choose the environment whose proxy to set.", "environmentId")
		}
	default:
		return invalid("The proxy scope must be project or environment.", "level")
	}
	mode := in.Mode
	switch mode {
	case "", ModeInherit:
		mode = ModeInherit
	case ModeManual, ModeOff:
	default:
		return invalid("The proxy mode of a scope must be inherit, manual or off.", "mode")
	}
	raw := strings.TrimSpace(in.URL)
	if raw == "" && mode == ModeManual {
		return errs.New(errs.InvalidArgument, "Enter the proxy URL.").
			WithCode(netx.CodeProxyInvalidURL).WithHint(netx.HintProxyInvalidURL).WithDetail("field", "url")
	}
	var u netx.ProxyURL
	if raw != "" {
		var err error
		if u, err = netx.ParseProxyURL(raw); err != nil {
			if e, ok := errors.AsType[*errs.Error](err); ok {
				return e.WithDetail("field", "url")
			}
			return err
		}
	}
	bypass := slices.DeleteFunc(slices.Clone(in.Bypass), func(e string) bool { return strings.TrimSpace(e) == "" })
	for i, e := range bypass {
		bypass[i] = strings.TrimSpace(e)
	}
	if _, err := netx.ParseBypass(bypass); err != nil {
		if e, ok := errors.AsType[*errs.Error](err); ok {
			return e.WithDetail("field", "bypass")
		}
		return err
	}

	c, err := s.Read()
	if err != nil {
		return err
	}
	passwords, err := s.Passwords()
	if err != nil {
		return err
	}
	key := PasswordKey(in.Level, in.EnvironmentID)
	stored := passwords[key]
	old := c.Project
	if in.Level == LevelEnvironment {
		old = c.Environments[in.EnvironmentID]
	}
	password := stored
	if prev, err := netx.ParseProxyURL(old.URL); err == nil && prev.Username != u.Username {
		// Another user: the stored password was not theirs.
		password = ""
	}
	switch {
	case u.Password != "":
		password = u.Password
	case in.Password != nil:
		password = *in.Password
	}
	if u.Username == "" || u.IsSOCKS4() {
		if in.Password != nil && *in.Password != "" {
			msg := "A proxy password needs a user name; write it into the URL as socks5h://user@host:port."
			if u.IsSOCKS4() {
				msg = "A SOCKS4 proxy takes a user ID but no password."
			}
			return errs.New(errs.InvalidArgument, msg).WithCode(netx.CodeProxyInvalidURL).WithDetail("field", "password")
		}
		password = ""
	}
	if strings.HasPrefix(u.Scheme, "socks") && len(password) > netx.MaxProxyCredentialLen {
		return errs.New(errs.InvalidArgument, "The proxy password is longer than 255 bytes.").
			WithCode(netx.CodeProxyInvalidURL).WithDetail("field", "password")
	}

	next := Scope{Mode: mode, URL: u.String(), Bypass: bypass}
	if mode == ModeInherit {
		next.Mode = ""
	}
	if next.Mode == "" && next.URL == "" && len(next.Bypass) == 0 {
		next = Scope{}
	}
	envs := maps.Clone(c.Environments)
	if keep != nil {
		for id := range envs {
			if id != in.EnvironmentID && !keep(id) {
				delete(envs, id)
				delete(passwords, PasswordKey(LevelEnvironment, id))
			}
		}
	}
	if in.Level == LevelEnvironment {
		if envs == nil {
			envs = map[string]Scope{}
		}
		envs[in.EnvironmentID] = next
	} else {
		c.Project = next
	}
	c.Environments = envs

	// The password first: a scope never names a user whose password
	// could not be stored.
	if password == "" {
		delete(passwords, key)
	} else {
		passwords[key] = password
	}
	if err := s.secrets.Write(SecretsID, passwords); err != nil {
		return err
	}
	return s.write(c)
}

// HasPassword reports whether a scope has a stored password for the user
// of its URL.
func HasPassword(sc Scope, passwords map[string]string, level, environmentID string) bool {
	u, err := netx.ParseProxyURL(sc.URL)
	if err != nil || u.Username == "" || u.IsSOCKS4() {
		return false
	}
	return passwords[PasswordKey(level, environmentID)] != ""
}

// Effective is the proxy in effect and where it comes from.
type Effective struct {
	// Config is the proxy, with its password, and its bypass list.
	Config netx.ProxyConfig
	// Level is the scope that decided: LevelApp, LevelProject or
	// LevelEnvironment.
	Level string
	// Source is, for LevelApp, where the app's proxy comes from
	// (settings.ProxySourceSettings, "ALL_PROXY", ...), "" for none; the
	// level for the others.
	Source string
	// NoProxyVar is the environment variable whose bypass list was added
	// (the ALL_PROXY proxy's NO_PROXY), "" for none.
	NoProxyVar string
}

// Resolve returns the proxy in effect (SOCKS-08): that of the active
// environment's scope when it sets one or says "no proxy", else that of
// the project's scope, else the app's (n.EffectiveProxy with appPassword,
// getenv and system). A scope's proxy has the app's bypass list and its own. A
// scope whose URL cannot be used is an error with netx.CodeProxyInvalidURL
// and detail source (the level): connections then fail, never going
// direct.
func Resolve(n settings.Network, appPassword string, getenv func(string) string, system settings.SystemProxy, a Active) (Effective, error) {
	for _, s := range []struct {
		level    string
		scope    Scope
		password string
	}{
		{LevelEnvironment, a.Environment, a.EnvironmentPassword},
		{LevelProject, a.Project, a.ProjectPassword},
	} {
		if s.scope.Inherits() || (s.level == LevelEnvironment && a.EnvironmentID == "") {
			continue
		}
		cfg, err := s.scope.config(s.level, s.password, n)
		if err != nil {
			return Effective{}, err
		}
		return Effective{Config: cfg, Level: s.level, Source: s.level}, nil
	}
	p, err := n.EffectiveProxy(appPassword, getenv, system)
	if err != nil {
		return Effective{Level: LevelApp}, err
	}
	return Effective{Config: p.Config(), Level: LevelApp, Source: p.Source, NoProxyVar: p.NoProxyVar}, nil
}

// config is the scope's proxy with password and the bypass lists.
func (s Scope) config(level, password string, n settings.Network) (netx.ProxyConfig, error) {
	if s.Mode == ModeOff {
		return netx.ProxyConfig{}, nil
	}
	what := "The proxy of the project"
	if level == LevelEnvironment {
		what = "The proxy of the active environment"
	}
	hint := "Fix it under Settings, Network, Proxy, or choose Use app setting or No proxy for it."
	if s.Mode != ModeManual {
		return netx.ProxyConfig{}, errs.New(errs.InvalidArgument, what+" has an unknown mode.").
			WithCode(netx.CodeProxyInvalidURL).WithHint(hint).WithDetail("source", level)
	}
	if s.URL == "" {
		return netx.ProxyConfig{}, errs.New(errs.InvalidArgument, what+" is set to Manual, but no proxy URL is set.").
			WithCode(netx.CodeProxyInvalidURL).WithHint(hint).WithDetail("source", level)
	}
	u, err := netx.ParseProxyURL(s.URL)
	if err != nil {
		reason := ""
		if e, ok := errors.AsType[*errs.Error](err); ok {
			reason = " " + e.Message
		}
		return netx.ProxyConfig{}, errs.New(errs.InvalidArgument, what+" cannot be used."+reason).
			WithCode(netx.CodeProxyInvalidURL).WithHint(hint+" "+netx.HintProxyInvalidURL).WithDetail("source", level)
	}
	if u.Username != "" && u.Password == "" && !u.IsSOCKS4() {
		u.Password = password
	}
	own, err := netx.ParseBypass(s.Bypass)
	if err != nil {
		if e, ok := errors.AsType[*errs.Error](err); ok {
			return netx.ProxyConfig{}, e.WithDetail("source", level)
		}
		return netx.ProxyConfig{}, err
	}
	app, err := n.Bypass()
	if err != nil {
		return netx.ProxyConfig{}, err
	}
	return netx.ProxyConfig{Proxy: u, Bypass: netx.JoinBypass(app, own)}, nil
}

func invalid(msg, field string) *errs.Error {
	return errs.New(errs.InvalidArgument, msg).WithCode(CodeInvalid).WithDetail("field", field)
}

func failed(err error, msg, path string) error {
	return errs.Wrap(err, errs.KindOf(err), msg).
		WithCode(CodeLocalDataFailed).
		WithHint("Check that the PostSonde data directory is writable and the disk is not full.").
		WithDetail("path", path)
}
