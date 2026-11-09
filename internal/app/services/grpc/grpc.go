// Package grpc is the bound service for gRPC requests (GRPC-01, GRPC-02,
// GRPC-10): it loads and links a request's .proto files through their
// import paths and describes the services and methods they define, for the
// service and method picker, and shows the native dialogs that choose
// .proto files and import roots. Invoke makes calls of every kind (GRPC-11
// to GRPC-14, GRPC-18, GRPC-21) and streams what happens as an operation
// (docs/events.md), over gRPC or gRPC-Web and optionally gzip-compressed
// (GRPC-24, GRPC-25); SendMessage and CloseSend drive the request stream of
// client-streaming and bidi calls (GRPC-22). ExampleMessage and ValidateMessage serve the
// request message editor (GRPC-15, GRPC-16). Parsing and linking are
// internal/core/grpc/proto, the wire is internal/core/grpc/transport. See
// docs/grpc.md.
package grpc

import (
	"context"
	"crypto/x509"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/app/streamcheck"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/fileaccess"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/desc"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/linker"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/parser"
	"github.com/sorrelfox/postsonde/internal/core/grpc/transport"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/request"
	coreworkspace "github.com/sorrelfox/postsonde/internal/core/workspace"
)

// Domain names the gRPC service.
const Domain = "grpc"

// Diagnostic codes the service adds to the linker's (docs/proto.md).
const (
	// CodeImportPathMissing is a warning: an import path that is not a
	// directory.
	CodeImportPathMissing = "grpc.import_path_missing"
	// CodeNoProject is an error: a relative path while no project is open.
	CodeNoProject = "grpc.no_project"
	// A .proto file or import path outside the project folder and the
	// allowed directories is an error with fileaccess.CodeNotAllowed
	// (PRV-10).
)

// cacheSize is how many linked setups are kept.
const cacheSize = 8

// Dialog describes the native dialog a PathPicker shows.
type Dialog struct {
	Title      string
	Message    string
	ButtonText string
	// Folder asks for a folder instead of files.
	Folder bool
	// Multiple allows choosing several files.
	Multiple bool
	// FilterName and FilterPattern ("*.proto") restrict the files shown.
	FilterName    string
	FilterPattern string
	// Directory is where the dialog starts; "" for the system default.
	Directory string
}

// PathPicker shows a native file or folder dialog and returns the chosen
// absolute paths, none when the user cancels. The app layer supplies it;
// tests fake it.
type PathPicker func(ctx context.Context, dialog Dialog) ([]string, error)

// GRPCService describes .proto setups for the gRPC editor and makes calls.
type GRPCService struct {
	manager *coreworkspace.Manager
	ops     *operation.Registry
	dialer  netx.Dialer
	pick    PathPicker
	logger  *slog.Logger

	mu    sync.Mutex
	cache []*cacheEntry // most recently used last

	// roots gives the CAs trusted for TLS calls (WithTrust); nil uses the
	// system's.
	roots RootsFunc
	// clientCerts picks the client certificate of TLS calls
	// (WithClientCerts); nil sends none.
	clientCerts httpclient.ClientCertFunc
	// requireTLS refuses TLS calls that turn certificate verification off
	// (WithRequireTLSVerification).
	requireTLS bool
	// allowedDirs are the directories .proto files and import paths may
	// be in besides the project's (WithAllowedDirs, PRV-10); nil for none.
	allowedDirs func() []string
	// auth builds the auth sent in the metadata and scopes renders its
	// variables (WithAuth); nil sends none.
	auth   request.HandshakeAuthFunc
	scopes Scopes
	// proxies gives the request's own proxy (WithProxy); nil inherits.
	proxies ProxySource

	clientsMu sync.Mutex
	clients   map[clientKey]*transport.Client

	// calls are the running calls by operation ID, for SendMessage and
	// CloseSend.
	callsMu sync.Mutex
	calls   map[string]*call

	// checks loads the assertions of a request (WithChecks); ended keeps
	// the last calls that ended, for CheckAssertions.
	checks streamcheck.Loader
	ended  streamcheck.Recent[endedCall]
}

// RootsFunc returns the certificate authorities trusted for TLS calls:
// nil for the system's, or the system's plus the custom CA bundles
// (NET-04, tlsconf.Trust.Roots). An error means a configured bundle cannot
// be used.
type RootsFunc func() (*x509.CertPool, error)

// An Option configures New.
type Option func(*GRPCService)

// WithTrust gives the CA pool verified TLS calls use, read on every call.
// Without it, the system roots only.
func WithTrust(roots RootsFunc) Option {
	return func(s *GRPCService) { s.roots = roots }
}

// WithClientCerts gives the per-host client certificates (AUTH-30): a TLS
// call sends the certificate pick returns for its address (port 443 when
// it has none). An error from pick fails the call before it connects.
func WithClientCerts(pick httpclient.ClientCertFunc) Option {
	return func(s *GRPCService) { s.clientCerts = pick }
}

// WithRequireTLSVerification refuses a TLS call that turns certificate
// verification off (settings.tls_verification_required): the admin
// policy's requireTlsVerification (SET-02). Plaintext (h2c) calls are not
// affected.
func WithRequireTLSVerification(require bool) Option {
	return func(s *GRPCService) { s.requireTLS = require }
}

// WithAllowedDirs gives the allowed directories of the settings
// (privacy.allowedDirs), read on every load: .proto files, their imports
// and import paths are read from them and the open project's folder only
// (PRV-10). Without it only the project's folder is allowed.
func WithAllowedDirs(dirs func() []string) Option {
	return func(s *GRPCService) { s.allowedDirs = dirs }
}

// filePolicy is what .proto files are read through: the open project's
// folder and the allowed directories.
func (s *GRPCService) filePolicy(root string) fileaccess.Policy {
	p := fileaccess.Policy{ProjectRoot: root}
	if s.allowedDirs != nil {
		p.Allowed = s.allowedDirs()
	}
	return p
}

// New returns a GRPCService that resolves stored paths against the root of
// manager's open project, starts calls as operations in ops and connects
// through dialer (a nil ops or dialer makes Invoke fail). pick shows the
// dialogs (nil makes the Pick methods fail). A nil logger means
// slog.Default().
func New(manager *coreworkspace.Manager, ops *operation.Registry, dialer netx.Dialer, pick PathPicker, logger *slog.Logger, opts ...Option) *GRPCService {
	if logger == nil {
		logger = slog.Default()
	}
	s := &GRPCService{
		manager: manager,
		ops:     ops,
		dialer:  dialer,
		pick:    pick,
		logger:  logger.With("service", Domain),
		clients: map[clientKey]*transport.Client{},
		calls:   map[string]*call{},
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// ServiceName names the service in Wails logs.
func (s *GRPCService) ServiceName() string { return Domain }

// ServiceShutdown closes the idle connections of every client. Running
// calls are operations, which the shared registry cancels at shutdown.
func (s *GRPCService) ServiceShutdown() error {
	s.clientsMu.Lock()
	defer s.clientsMu.Unlock()
	for _, c := range s.clients {
		c.CloseIdleConnections()
	}
	return nil
}

// Describe loads and links in.Files through in.ImportPaths and returns the
// services they and their imports define, with every diagnostic. Paths are
// stored paths: relative to the open project's root, or absolute. Problems
// with the files are diagnostics, not errors; no files gives an empty
// result. A linked setup is reused while none of its files changed on disk
// (size and modification time), unless in.Reload is set.
func (s *GRPCService) Describe(ctx context.Context, in DescribeInputDTO) (DescribeResultDTO, error) {
	if err := ctx.Err(); err != nil {
		return DescribeResultDTO{}, errs.Wrap(err, errs.KindOf(err), "Loading the .proto files was interrupted.")
	}
	res, cached, extra, err := s.link(ctx, in.Files, in.ImportPaths, in.Reload)
	if err != nil {
		return DescribeResultDTO{}, err
	}
	return toDescribeResult(s.root(), res, extra, cached), nil
}

// PickProtoFiles shows the native dialog for one or more .proto files and
// returns them as they are stored: relative to the open project's root
// when inside it, else absolute. Cancelling returns an empty list.
func (s *GRPCService) PickProtoFiles(ctx context.Context) ([]string, error) {
	paths, err := s.show(ctx, Dialog{
		Title:         "Add .proto Files",
		Message:       "Choose the .proto files that define the services to call.",
		ButtonText:    "Add",
		Multiple:      true,
		FilterName:    "Protocol Buffers (*.proto)",
		FilterPattern: "*.proto",
	})
	if err != nil {
		return nil, err
	}
	root := s.root()
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		out = append(out, request.StoredFilePath(root, p))
	}
	return out, nil
}

// PickImportPath shows the native folder dialog for an import root and
// returns it as it is stored, or "" when cancelled.
func (s *GRPCService) PickImportPath(ctx context.Context) (string, error) {
	paths, err := s.show(ctx, Dialog{
		Title:      "Add Import Path",
		Message:    "Choose a folder that imports are resolved from, like protoc's --proto_path.",
		ButtonText: "Add",
		Folder:     true,
	})
	if err != nil || len(paths) == 0 {
		return "", err
	}
	return request.StoredFilePath(s.root(), paths[0]), nil
}

func (s *GRPCService) show(ctx context.Context, d Dialog) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, errs.Wrap(err, errs.KindOf(err), "Choosing was interrupted.")
	}
	if s.pick == nil {
		return nil, errs.New(errs.Unsupported, "File dialogs are not available.")
	}
	d.Directory = s.root()
	paths, err := s.pick(ctx, d)
	if err != nil {
		return nil, errs.Wrap(err, errs.KindOf(err), "The dialog could not be shown.")
	}
	return paths, nil
}

// root is the open project's root, or "".
func (s *GRPCService) root() string {
	if s.manager == nil {
		return ""
	}
	if w := s.manager.Current(); w != nil {
		return w.Root
	}
	return ""
}

// link returns the linked result of a setup given as stored paths, from
// the cache when it is still valid, with the service's own diagnostics
// (missing import paths, relative paths without a project). A nil result
// means there were no files to load.
func (s *GRPCService) link(ctx context.Context, files, importPaths []string, reload bool) (*linker.Result, bool, []linker.Diagnostic, error) {
	root := s.root()
	policy := s.filePolicy(root)
	var extra []linker.Diagnostic
	resolve := func(stored, subject string) (string, bool) {
		stored = strings.TrimSpace(stored)
		abs := request.ResolveFilePath(root, stored)
		if !filepath.IsAbs(abs) {
			extra = append(extra, linker.Diagnostic{Code: CodeNoProject, Severity: parser.SeverityError, File: stored,
				Message: "the path is relative to the project, but no project is open"})
			return "", false
		}
		p := policy
		p.Subject = subject
		if _, err := p.Resolve(stored); fileaccess.IsNotAllowed(err) {
			extra = append(extra, linker.Diagnostic{Code: fileaccess.CodeNotAllowed, Severity: parser.SeverityError, File: stored,
				DiskPath: abs, Message: errMessage(err)})
			return "", false
		}
		return abs, true
	}
	var absFiles, absRoots []string
	for _, f := range files {
		if strings.TrimSpace(f) == "" {
			continue
		}
		if abs, ok := resolve(f, "The .proto file"); ok {
			absFiles = append(absFiles, abs)
		}
	}
	for _, p := range importPaths {
		if strings.TrimSpace(p) == "" {
			continue
		}
		abs, ok := resolve(p, "The import path")
		if !ok {
			continue
		}
		if info, err := os.Stat(abs); err != nil || !info.IsDir() {
			extra = append(extra, linker.Diagnostic{Code: CodeImportPathMissing, Severity: parser.SeverityWarning, File: p,
				DiskPath: abs, Message: "the import path is not a folder that exists"})
		}
		absRoots = append(absRoots, abs)
	}
	if len(absFiles) == 0 {
		return nil, false, extra, nil
	}

	// The allowlist is part of the key: a result read while a folder was
	// allowed is not reused once it is not.
	key := strings.Join(absFiles, "\x00") + "\x01" + strings.Join(absRoots, "\x00") + "\x02" + strings.Join(policy.Allowed, "\x00")
	if !reload {
		if res := s.cached(key); res != nil {
			return res, true, extra, nil
		}
	}
	res, err := linker.Load(ctx, linker.Options{Files: absFiles, ImportPaths: absRoots, Access: policy})
	if err != nil {
		return nil, false, nil, err
	}
	if cacheable(res) {
		s.store(&cacheEntry{key: key, res: res, stamps: stampsOf(res)})
	} else {
		s.drop(key)
	}
	return res, false, extra, nil
}

// cacheEntry is a linked setup and the state of its files when linked.
type cacheEntry struct {
	key    string
	res    *linker.Result
	stamps []stamp
}

type stamp struct {
	path string
	size int64
	mod  time.Time
}

// stampsOf records every file the result was read from.
func stampsOf(res *linker.Result) []stamp {
	var out []stamp
	for _, f := range res.Pool.Files() {
		if f.Builtin || f.DiskPath == "" {
			continue
		}
		st := stamp{path: f.DiskPath, size: -1}
		if info, err := os.Stat(f.DiskPath); err == nil {
			st.size, st.mod = info.Size(), info.ModTime()
		}
		out = append(out, st)
	}
	return out
}

func (e *cacheEntry) fresh() bool {
	for _, st := range e.stamps {
		info, err := os.Stat(st.path)
		if err != nil || info.Size() != st.size || !info.ModTime().Equal(st.mod) {
			return false
		}
	}
	return true
}

// cacheable reports whether a result can be reused while its files are
// unchanged: not when a file or import was missing, since creating it
// changes the result without touching any file that was read.
func cacheable(res *linker.Result) bool {
	for _, d := range res.Diagnostics {
		switch d.Code {
		case linker.CodeFileNotFound, linker.CodeReadFailed, linker.CodeImportNotFound, linker.CodeWeakImportNotFound:
			return false
		}
	}
	return true
}

func (s *GRPCService) cached(key string) *linker.Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.cache, func(e *cacheEntry) bool { return e.key == key })
	if i < 0 {
		return nil
	}
	e := s.cache[i]
	s.cache = slices.Delete(s.cache, i, i+1)
	if !e.fresh() {
		return nil
	}
	s.cache = append(s.cache, e)
	return e.res
}

func (s *GRPCService) store(e *cacheEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cache = slices.DeleteFunc(s.cache, func(old *cacheEntry) bool { return old.key == e.key })
	s.cache = append(s.cache, e)
	if len(s.cache) > cacheSize {
		s.cache = slices.Delete(s.cache, 0, len(s.cache)-cacheSize)
	}
}

func (s *GRPCService) drop(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cache = slices.DeleteFunc(s.cache, func(e *cacheEntry) bool { return e.key == key })
}

// methodKind classifies a method for the picker's badge.
func methodKind(m *desc.Method) string {
	switch {
	case m.ClientStreaming && m.ServerStreaming:
		return MethodBidi
	case m.ClientStreaming:
		return MethodClientStream
	case m.ServerStreaming:
		return MethodServerStream
	}
	return MethodUnary
}

// errMessage is the message of an *errs.Error, else err's text.
func errMessage(err error) string {
	if e, ok := errors.AsType[*errs.Error](err); ok {
		return e.Message
	}
	return err.Error()
}
