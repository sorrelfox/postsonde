package servers

import (
	"path/filepath"
	"strconv"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/fileaccess"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/linker"
	"github.com/sorrelfox/postsonde/internal/core/request"
	core "github.com/sorrelfox/postsonde/internal/core/servers"
	"github.com/sorrelfox/postsonde/internal/core/servers/grpcmock"
)

// The DTOs of the gRPC mock server (SSRV-07): its .proto files and canned
// responses, and the gRPC detail of its traffic. The method list, message
// examples and checks of the editor come from the gRPC service
// (GRPCService.Describe, ExampleMessage and ValidateMessage with output
// set). See docs/servers.md.

// GRPCMockConfigDTO is the configuration of a grpc_mock server.
type GRPCMockConfigDTO struct {
	// Files and ImportPaths are stored paths, as a gRPC request's proto
	// setup: relative to the project root, or absolute.
	Files       []string `json:"files"`
	ImportPaths []string `json:"importPaths"`
	// Reflection serves grpc.reflection.v1 and v1alpha.
	Reflection bool                `json:"reflection"`
	Methods    []GRPCMockMethodDTO `json:"methods"`
}

// GRPCMockMethodDTO is the canned response of one method.
type GRPCMockMethodDTO struct {
	// Service is the fully-qualified service, Method the method's name.
	Service string `json:"service"`
	Method  string `json:"method"`
	// Messages are the response messages as JSON text.
	Messages []string `json:"messages"`
	// Status is the gRPC status code (0 is OK) and StatusMessage its
	// message.
	Status        int    `json:"status"`
	StatusMessage string `json:"statusMessage"`
	// Headers and Trailers are metadata rows; "-bin" values are Base64.
	Headers  []MockHeaderDTO `json:"headers"`
	Trailers []MockHeaderDTO `json:"trailers"`
	DelayMs  int             `json:"delayMs"`
	// IntervalMs spaces streamed messages.
	IntervalMs int `json:"intervalMs"`
	// Bidi is "each", "stream" or "echo".
	Bidi string `json:"bidi"`
}

// GRPCDTO is the gRPC mock server's detail of a traffic event.
type GRPCDTO struct {
	// Call numbers the calls of one run.
	Call int64 `json:"call"`
	// Method is "/pkg.Service/Method"; Kind is "unary", "server_stream",
	// "client_stream", "bidi" or "" for an unknown method.
	Method string `json:"method"`
	Kind   string `json:"kind"`
	Web    bool   `json:"web"`
	// Type and Index are a message's type and number (from 0).
	Type  string `json:"type"`
	Index int    `json:"index"`
	// Code, CodeName and Message are the status, on the response event.
	Code     int    `json:"code"`
	CodeName string `json:"codeName"`
	Message  string `json:"message"`
	// Trailers are the trailer metadata sent, on the response event.
	Trailers []HeaderDTO `json:"trailers"`
}

func (c GRPCMockConfigDTO) toCore() grpcmock.Config {
	out := grpcmock.Config{Files: c.Files, ImportPaths: c.ImportPaths, Reflection: c.Reflection, Methods: make([]grpcmock.Method, 0, len(c.Methods))}
	rows := func(in []MockHeaderDTO) []grpcmock.Metadata {
		md := make([]grpcmock.Metadata, 0, len(in))
		for _, r := range in {
			md = append(md, grpcmock.Metadata{Enabled: r.Enabled, Name: r.Name, Value: r.Value})
		}
		return md
	}
	for _, m := range c.Methods {
		out.Methods = append(out.Methods, grpcmock.Method{
			Service: m.Service, Method: m.Method, Messages: m.Messages,
			Status: m.Status, StatusMessage: m.StatusMessage,
			Headers: rows(m.Headers), Trailers: rows(m.Trailers),
			DelayMs: m.DelayMs, IntervalMs: m.IntervalMs, Bidi: m.Bidi,
		})
	}
	return out
}

func toGRPCMockConfigDTO(c grpcmock.Config) *GRPCMockConfigDTO {
	out := &GRPCMockConfigDTO{
		Files: nonNil(c.Files), ImportPaths: nonNil(c.ImportPaths), Reflection: c.Reflection,
		Methods: make([]GRPCMockMethodDTO, 0, len(c.Methods)),
	}
	rows := func(in []grpcmock.Metadata) []MockHeaderDTO {
		out := make([]MockHeaderDTO, 0, len(in))
		for _, r := range in {
			out = append(out, MockHeaderDTO{Enabled: r.Enabled, Name: r.Name, Value: r.Value})
		}
		return out
	}
	for _, m := range c.Methods {
		out.Methods = append(out.Methods, GRPCMockMethodDTO{
			Service: m.Service, Method: m.Method, Messages: nonNil(m.Messages),
			Status: m.Status, StatusMessage: m.StatusMessage,
			Headers: rows(m.Headers), Trailers: rows(m.Trailers),
			DelayMs: m.DelayMs, IntervalMs: m.IntervalMs, Bidi: m.Bidi,
		})
	}
	return out
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func toGRPCDTO(g *core.GRPCInfo) *GRPCDTO {
	if g == nil {
		return nil
	}
	out := &GRPCDTO{
		Call: int64(g.Call), Method: g.Method, Kind: g.Kind, Web: g.Web, Type: g.Type, Index: g.Index,
		Code: g.Code, CodeName: g.CodeName, Message: g.Message, Trailers: make([]HeaderDTO, 0, len(g.Trailers)),
	}
	for _, h := range g.Trailers {
		out.Trailers = append(out.Trailers, HeaderDTO{Name: h.Name, Value: h.Value})
	}
	return out
}

// CodeNoProjectForProto: a gRPC mock's .proto path is relative, and no
// project is open to resolve it against.
const CodeNoProjectForProto = grpcmock.CodeRelativePath

// WithProjectRoot gives the open project's root, against which the
// relative .proto paths of gRPC mock servers are resolved when they start
// (as a gRPC request resolves its own). Without it only absolute paths
// work.
func WithProjectRoot(root func() string) Option {
	return func(s *ServersService) { s.projectRoot = root }
}

// WithAllowedDirs gives the allowed directories of the settings
// (privacy.allowedDirs), read whenever files are read: the .proto files
// and import paths of gRPC mock servers and the certificate files of TLS
// servers are read from them and the open project's folder only (PRV-10).
// Without it only the project's folder is allowed.
func WithAllowedDirs(dirs func() []string) Option {
	return func(s *ServersService) { s.allowedDirs = dirs }
}

// filePolicy is what the files a server definition names are read
// through: the open project's folder and the allowed directories.
func (s *ServersService) filePolicy() fileaccess.Policy {
	var p fileaccess.Policy
	if s.projectRoot != nil {
		p.ProjectRoot = s.projectRoot()
	}
	if s.allowedDirs != nil {
		p.Allowed = s.allowedDirs()
	}
	return p
}

// protoAccess is what the .proto files of gRPC mock servers and their
// imports are read through.
func (s *ServersService) protoAccess() linker.FileAccess {
	p := s.filePolicy()
	p.Subject = "The .proto file"
	return p
}

// resolveProto makes a stored .proto path or import path absolute against
// the open project's root, refusing one outside the project folder and
// the allowed directories (fileaccess.not_allowed).
func (s *ServersService) resolveProto(stored string) (string, error) {
	p := s.filePolicy()
	abs := request.ResolveFilePath(p.ProjectRoot, stored)
	if !filepath.IsAbs(abs) {
		return "", errs.New(errs.InvalidArgument, "The .proto path "+strconv.Quote(stored)+" is relative to the project, but no project is open.").
			WithCode(CodeNoProjectForProto).WithDetail("path", stored).
			WithHint("Open the project the server belongs to, or use absolute paths.")
	}
	p.Subject = "The .proto path"
	if _, err := p.Resolve(stored); fileaccess.IsNotAllowed(err) {
		return "", err
	}
	return abs, nil
}
