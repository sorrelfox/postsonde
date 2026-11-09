package request

// authnetrc.go maps a netrc auth (AUTH-19): the login and password of the
// request host's entry in a .netrc file, read when the request is built,
// sent as Basic auth. See docs/auth.md#netrc-auth-19.

import (
	"encoding/base64"
	"errors"
	"io"
	"io/fs"
	"net/url"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/fileaccess"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netrc"
)

// Error and warning codes of a netrc auth.
const (
	// CodeAuthNetrcFailed: the netrc file could not be found, read or
	// parsed (kind not_found, permission_denied, io or parse). Details:
	// type, field "netrcFile", path, and line for a syntax error.
	CodeAuthNetrcFailed = "request.auth_netrc_failed"
	// CodeAuthNetrcNoEntry: the netrc file has no machine entry for the
	// request's host and no default entry (kind not_found). Details:
	// type, field "netrcFile", path, host.
	CodeAuthNetrcNoEntry = "request.auth_netrc_no_entry"
	// WarningNetrcInsecure: the netrc file may be read or written by the
	// group or others (Unix). Sent anyway, as curl does.
	WarningNetrcInsecure = "request.netrc_insecure"
)

// Details of netrc errors.
const (
	DetailHost = "host"
	DetailLine = "line"
)

// NetrcPlaceholder is the Authorization value a netrc auth shows when the
// file is not read (NetrcOptions.Defer).
const NetrcPlaceholder = "Basic …"

// NetrcOptions say how a netrc auth finds and reads its file.
type NetrcOptions struct {
	// Env is where the file is looked for and how it is read; the zero
	// value is the real environment and file system.
	Env netrc.Env
	// Defer leaves the file unread: the auth adds Authorization:
	// NetrcPlaceholder. For what is computed without the user asking,
	// like the header preview.
	Defer bool
}

// NetrcMatch is what a netrc auth took from the file, without the
// password.
type NetrcMatch struct {
	// Path is the file read, Source where its path came from
	// (netrc.SourceRequest, SourceEnv or SourceHome).
	Path   string
	Source string
	// Host is the request's host, as looked up.
	Host string
	// Entry is the machine name, or "default"; Line its line.
	Entry string
	Line  int
	// Login is the entry's login; HasPassword says whether it has a
	// password.
	Login       string
	HasPassword bool
	// Insecure is set when the group or others may read the file.
	Insecure bool
	// URLCredentials is set when the URL's userinfo has a password: it
	// wins, and nothing is taken from the file.
	URLCredentials bool
}

// NetrcFile returns the file a netrc auth with the file field stored (its
// variables rendered) reads, without reading it: for the Auth tab.
func NetrcFile(stored, root string, env netrc.Env) (netrc.Location, error) {
	loc, err := env.Locate(stored, root)
	if err != nil {
		return netrc.Location{}, netrcLocateError(err, stored)
	}
	return loc, nil
}

func netrcLocateError(err error, stored string) error {
	e := errs.Wrap(err, errs.NotFound, "The .netrc file was not found: there is no home directory to look in.").
		WithHint("Set the NETRC environment variable, or name the file in the Auth tab.")
	if stored != "" {
		e = errs.Wrap(err, errs.NotFound, "The .netrc file was not found: "+stored+".").
			WithHint("Use an absolute path, a path starting with ~/, or a path relative to the project folder.")
	}
	return e.WithCode(CodeAuthNetrcFailed).WithDetail(DetailAuthType, "netrc").WithDetail(DetailAuthField, "netrcFile").WithDetail(DetailPath, stored)
}

func (m authMapper) netrc() (AuthResult, error) {
	stored, err := m.field("netrcFile", m.auth.NetrcFile)
	if err != nil {
		return AuthResult{}, err
	}
	if m.opts.Netrc.Defer {
		return AuthResult{Headers: []httpclient.Header{{Name: AuthorizationHeader, Value: NetrcPlaceholder, Enabled: true}}}, nil
	}
	host, login, urlPassword, err := netrcHost(m.opts.URL)
	if err != nil {
		return AuthResult{}, m.invalid("url", "The URL has no host to look up in the .netrc file.",
			"Enter a URL with a host, e.g. https://example.com/path.")
	}
	match := &NetrcMatch{Host: host}
	if urlPassword {
		// URL userinfo with a password wins, as in curl: the file is not
		// read, and httpclient sends the URL's credentials.
		match.URLCredentials = true
		return AuthResult{Netrc: match}, nil
	}
	loc, err := m.opts.Netrc.Env.Locate(stored, m.opts.Root)
	if err != nil {
		return AuthResult{}, netrcLocateError(err, stored)
	}
	env := m.opts.Netrc.Env
	if loc.Source == netrc.SourceRequest {
		if env, err = m.limitNetrc(env, stored); err != nil {
			return AuthResult{}, err
		}
	}
	loaded, err := env.Load(loc)
	if err != nil {
		return AuthResult{}, m.netrcLoadError(err, loc)
	}
	match.Path, match.Source, match.Insecure = loaded.Path, loc.Source, loaded.Insecure
	e, ok := loaded.File.Lookup(host, login)
	if !ok {
		msg := "The .netrc file has no entry for " + host + "."
		if login != "" {
			msg = "The .netrc file has no entry for " + host + " with the login " + strconv.Quote(login) + " of the URL."
		}
		return AuthResult{}, m.detail(errs.New(errs.NotFound, msg).
			WithCode(CodeAuthNetrcNoEntry).
			WithHint("Add a “machine "+host+"” entry (or a default one) to "+loaded.Path+", or choose another auth type.").
			WithDetail(DetailPath, loaded.Path).
			WithDetail(DetailHost, host), "netrcFile")
	}
	match.Entry, match.Line, match.Login, match.HasPassword = e.Name(), e.Line, e.Login, e.HasPassword
	out := AuthResult{Netrc: match}
	if e.Password != "" {
		out.Secrets = []string{e.Password}
	}
	if loaded.Insecure {
		out.Warnings = append(out.Warnings, Warning{Code: WarningNetrcInsecure,
			Message: "The .netrc file " + loaded.Path + " can be read by other users. Restrict it with chmod 600."})
	}
	if e.Login == "" && e.Password == "" {
		return AuthResult{}, m.invalid("netrcFile", "The .netrc entry for "+e.Name()+" (line "+strconv.Itoa(e.Line)+") has no login or password.",
			"Add login and password to the entry in "+loaded.Path+".")
	}
	if strings.Contains(e.Login, ":") {
		return AuthResult{}, m.invalid("netrcFile", "The login of the .netrc entry for "+e.Name()+" contains a colon.",
			"Basic auth cannot send a username with \":\" (RFC 7617).")
	}
	if hasControl(e.Login) || hasControl(e.Password) {
		return AuthResult{}, m.invalid("netrcFile", "The .netrc entry for "+e.Name()+" has a line break or another control character in its login or password.",
			"Remove control characters from the entry in "+loaded.Path+".")
	}
	token := base64.StdEncoding.EncodeToString([]byte(e.Login + ":" + e.Password))
	out.Headers = []httpclient.Header{{Name: AuthorizationHeader, Value: "Basic " + token, Enabled: true}}
	out.Credentials = &Credentials{Username: e.Login, Password: e.Password}
	return out, nil
}

// netrcHost returns the host of the rendered URL (no port, no brackets),
// the user name of its userinfo, and whether the userinfo has a password.
func netrcHost(raw string) (host, login string, password bool, err error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", false, err
	}
	host = u.Hostname()
	if host == "" {
		return "", "", false, errors.New("no host")
	}
	if u.User != nil {
		login = u.User.Username()
		_, password = u.User.Password()
	}
	return host, login, password, nil
}

// limitNetrc limits the file the auth names, stored, to the project
// folder and the allowed directories (PRV-10): it fails with
// fileaccess.not_allowed for one outside them, and returns env reading
// through the allowlist (os.Root) unless env reads files its own way
// (tests). The file of NETRC or the home directory is not limited: the
// user chose it, not the project.
func (m authMapper) limitNetrc(env netrc.Env, stored string) (netrc.Env, error) {
	p := m.filePolicy("The .netrc file")
	p.HomeDir, p.MaxSize = env.Home, netrc.MaxSize
	if _, err := p.Resolve(stored); fileaccess.IsNotAllowed(err) {
		var e *errs.Error
		errors.As(err, &e)
		return env, m.detail(e.WithDetail(DetailPath, stored), "netrcFile")
	}
	if env.ReadFile == nil {
		env.ReadFile = func(path string) ([]byte, fs.FileMode, error) {
			f, info, err := p.Open(path)
			if err != nil {
				var e *errs.Error
				if errors.As(err, &e) && e.Code == fileaccess.CodeUnreadable && !errors.Is(err, fs.ErrPermission) {
					return nil, 0, netrc.ErrNotFile
				}
				return nil, 0, err
			}
			defer f.Close()
			data, err := io.ReadAll(io.LimitReader(f, netrc.MaxSize+1))
			if err != nil {
				return nil, 0, err
			}
			if len(data) > netrc.MaxSize {
				return nil, 0, netrc.ErrTooLarge
			}
			return data, info.Mode(), nil
		}
	}
	return env, nil
}

func (m authMapper) netrcLoadError(err error, loc netrc.Location) error {
	if fileaccess.IsNotAllowed(err) {
		// A link changed between the check and the read.
		var e *errs.Error
		errors.As(err, &e)
		return m.detail(e, "netrcFile")
	}
	path := loc.Path
	var pe *netrc.PathError
	if errors.As(err, &pe) {
		path = pe.Path
	}
	fail := func(kind errs.Kind, msg, hint string) *errs.Error {
		return m.detail(errs.Wrap(err, kind, msg).WithCode(CodeAuthNetrcFailed).WithHint(hint).WithDetail(DetailPath, path), "netrcFile")
	}
	var se *netrc.SyntaxError
	switch {
	case errors.As(err, &se):
		return fail(errs.Parse, "The .netrc file "+path+" cannot be read at line "+strconv.Itoa(se.Line)+": "+se.Msg+".",
			"Fix the file: entries are “machine <host> login <name> password <secret>”.").WithDetail(DetailLine, strconv.Itoa(se.Line))
	case errors.Is(err, fs.ErrNotExist):
		hint := "Create it with a “machine <host> login <name> password <secret>” entry, or name another file in the Auth tab."
		if loc.Source == netrc.SourceEnv {
			hint = "The NETRC environment variable names it. Create the file, or change the variable."
		}
		return fail(errs.NotFound, "The .netrc file was not found: "+path+".", hint)
	case errors.Is(err, fs.ErrPermission):
		return fail(errs.PermissionDenied, "The .netrc file could not be read: "+path+".",
			"Check that your user can read the file.")
	case errors.Is(err, netrc.ErrTooLarge):
		return fail(errs.InvalidArgument, "The .netrc file is larger than 1 MiB: "+path+".",
			"Check that the path names a .netrc file.")
	case errors.Is(err, netrc.ErrNotFile):
		return fail(errs.InvalidArgument, "The .netrc path is not a file: "+path+".",
			"Name the file itself, not its folder.")
	}
	return fail(errs.IO, "The .netrc file could not be read: "+path+".", "Check the file and try again.")
}
