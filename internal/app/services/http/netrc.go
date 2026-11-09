package http

import (
	"context"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netrc"
	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

// CodeNoNetrc: the request's auth is not an enabled netrc auth, so there is
// no entry to check (kind invalid_argument).
const CodeNoNetrc = "http.no_netrc"

// WithNetrc sets where a netrc auth (AUTH-19) looks for its file and how
// it reads it; without it, the real environment and file system. Tests
// give a netrc.Env of their own, so the user's ~/.netrc is never read.
func WithNetrc(env netrc.Env) Option {
	return func(s *HTTPService) { s.netrc = env }
}

// netrcOptions are the request.NetrcOptions of a build; with deferRead
// the file is not read (the header preview, computed after every edit).
func (s *HTTPService) netrcOptions(deferRead bool) request.NetrcOptions {
	return request.NetrcOptions{Env: s.netrc, Defer: deferRead}
}

// NetrcFileInputDTO is what NetrcFile takes.
type NetrcFileInputDTO struct {
	// RequestID is the request whose variables and project folder the
	// path is resolved with; "" for none (a folder's auth).
	RequestID string `json:"requestId"`
	// File is the netrc file field as typed; "" for the default file.
	File string `json:"file"`
}

// NetrcFileDTO is the file a netrc auth reads.
type NetrcFileDTO struct {
	// Path is the file read first.
	Path string `json:"path"`
	// Fallback is read when Path does not exist (~/.netrc after ~/_netrc
	// on Windows); "" for none.
	Fallback string `json:"fallback"`
	// Source is "request" (the auth's file field), "env" (the NETRC
	// environment variable) or "home".
	Source string `json:"source"`
}

// NetrcFile returns the file a netrc auth with file field in.File would
// read (AUTH-19), without reading it or checking that it exists: the
// field rendered with the request's variables, "~/" the home directory, a
// relative path from the project folder; without one, NETRC or the home
// directory's .netrc (_netrc on Windows).
func (s *HTTPService) NetrcFile(ctx context.Context, in NetrcFileInputDTO) (NetrcFileDTO, error) {
	if err := ctx.Err(); err != nil {
		return NetrcFileDTO{}, errs.Wrap(err, errs.KindOf(err), "Finding the .netrc file was interrupted.")
	}
	file, root := strings.TrimSpace(in.File), ""
	if in.RequestID != "" && s.canLoadAuth() {
		src, err := s.loadAuth(ctx, in.RequestID)
		if err != nil {
			return NetrcFileDTO{}, err
		}
		chain, err := s.chainOf(src)
		if err != nil {
			return NetrcFileDTO{}, err
		}
		x := &expansion{resolver: template.Resolver{Scopes: chain}, allowUnresolved: true}
		if file, err = x.expand(file); err != nil {
			return NetrcFileDTO{}, err
		}
		root = workspaceRoot(src)
	}
	loc, err := request.NetrcFile(file, root, s.netrc)
	if err != nil {
		return NetrcFileDTO{}, err
	}
	return NetrcFileDTO{Path: loc.Path, Fallback: loc.Fallback, Source: loc.Source}, nil
}

// NetrcCheckInputDTO is what NetrcCheck takes.
type NetrcCheckInputDTO struct {
	RequestID string `json:"requestId"`
}

// NetrcCheckDTO is the netrc entry a send of the request would use now,
// without its password.
type NetrcCheckDTO struct {
	// Path is the file read, Source where its path came from ("request",
	// "env" or "home"); "" when the URL's credentials win.
	Path   string `json:"path"`
	Source string `json:"source"`
	// Host is the URL's host, as looked up.
	Host string `json:"host"`
	// Entry is the machine name matched, or "default"; Line its line.
	Entry string `json:"entry"`
	Line  int    `json:"line"`
	// Login is the entry's login; HasPassword whether it has a password.
	Login       string `json:"login"`
	HasPassword bool   `json:"hasPassword"`
	// Insecure is set when other users can read the file (Unix).
	Insecure bool `json:"insecure"`
	// URLCredentials is set when the URL has a user and password, which
	// are sent instead: the file is not read.
	URLCredentials bool `json:"urlCredentials"`
	// FolderName names the folder the auth is inherited from; "" for the
	// request's own.
	FolderName string `json:"folderName"`
	// Unresolved are the undefined {{variables}} of the URL and the file
	// field, left as written.
	Unresolved []string `json:"unresolved"`
}

// NetrcCheck reads the netrc file of request in.RequestID's netrc auth
// (its own or inherited) and returns the entry that matches the host of
// its URL, as a send now would (AUTH-19): the machine entry, else the
// default one. The password is never returned. Only the URL and the auth
// are built, so a problem elsewhere in the request does not stop it.
//
// It fails with CodeNoNetrc when the auth is not an enabled netrc auth,
// and as Send does for the auth's errors: request.auth_netrc_failed (no
// file, unreadable, a syntax error) and request.auth_netrc_no_entry.
func (s *HTTPService) NetrcCheck(ctx context.Context, in NetrcCheckInputDTO) (NetrcCheckDTO, error) {
	if err := ctx.Err(); err != nil {
		return NetrcCheckDTO{}, errs.Wrap(err, errs.KindOf(err), "Checking the .netrc file was interrupted.")
	}
	if !s.canLoadAuth() {
		return NetrcCheckDTO{}, errs.New(errs.Unsupported, "Sending HTTP requests is not available.").WithCode(CodeUnavailable)
	}
	src, err := s.loadAuth(ctx, in.RequestID)
	if err != nil {
		return NetrcCheckDTO{}, err
	}
	if src.Auth.Type != projectfile.AuthNetrc || src.Auth.Disabled {
		return NetrcCheckDTO{}, errs.New(errs.InvalidArgument, "The request's auth does not read a .netrc file.").
			WithCode(CodeNoNetrc).
			WithHint("Choose the Netrc auth type, and make sure “Send this auth” is on.")
	}
	chain, err := s.chainOf(src)
	if err != nil {
		return NetrcCheckDTO{}, err
	}
	x := &expansion{resolver: template.Resolver{Scopes: chain}, allowUnresolved: true}
	raw, params := src.Request.URL, src.Request.PathParams
	if src.GraphQL != nil {
		raw, params = src.GraphQL.URL, nil
	}
	url, err := request.SubstitutePath(raw, params, x.expand)
	if err != nil {
		return NetrcCheckDTO{}, err
	}
	if url, err = request.ExpandURL(url, x.expand); err != nil {
		return NetrcCheckDTO{}, err
	}
	if src.Handshake == projectfile.KindGRPC {
		// An address: the host is looked up as for the call (AUTH-31).
		url = GRPCAuthURL(url, false)
	}
	a, err := request.AuthWith(src.Auth, nil, request.AuthOptions{
		Expand: x.expand, Root: workspaceRoot(src), AllowedDirs: s.allowed(), URL: strings.TrimSpace(url), Netrc: s.netrcOptions(false),
	})
	if err != nil {
		return NetrcCheckDTO{}, err
	}
	m := a.Netrc
	if m == nil {
		return NetrcCheckDTO{}, errs.New(errs.Internal, "The .netrc file was not checked.").WithHint("This is a bug in PostSonde.")
	}
	return NetrcCheckDTO{
		Path: m.Path, Source: m.Source, Host: m.Host, Entry: m.Entry, Line: m.Line,
		Login: m.Login, HasPassword: m.HasPassword, Insecure: m.Insecure, URLCredentials: m.URLCredentials,
		FolderName: src.AuthFolderName, Unresolved: x.skippedNames(),
	}, nil
}
