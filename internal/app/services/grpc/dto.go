package grpc

import (
	"path/filepath"
	"slices"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/desc"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/linker"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/parser"
	"github.com/sorrelfox/postsonde/internal/core/request"
)

// Kinds of method, for MethodDTO.Kind.
const (
	MethodUnary        = "unary"
	MethodClientStream = "client_stream"
	MethodServerStream = "server_stream"
	MethodBidi         = "bidi"
)

// DescribeInputDTO is a proto setup to describe.
type DescribeInputDTO struct {
	// Files and ImportPaths are stored paths: relative to the open
	// project's root ("/" separators) or absolute. Empty entries are
	// skipped.
	Files       []string `json:"files"`
	ImportPaths []string `json:"importPaths"`
	// Reload links the files again even when none changed.
	Reload bool `json:"reload"`
}

// DescribeResultDTO lists what a proto setup defines.
type DescribeResultDTO struct {
	// Services are the services of every loaded file except the built-in
	// well-known types, sorted by full name.
	Services []ServiceDTO `json:"services"`
	// Diagnostics are the problems found, errors and warnings, by file and
	// position.
	Diagnostics []DiagnosticDTO `json:"diagnostics"`
	// Files is the number of .proto files loaded, imports included and
	// built-in files not.
	Files int `json:"files"`
	// Errors is the number of error diagnostics.
	Errors int `json:"errors"`
	// Cached is set when the linked files were reused.
	Cached bool `json:"cached"`
}

// ServiceDTO is a service and its methods in declaration order.
type ServiceDTO struct {
	// Name is the simple name, FullName the qualified one
	// ("shop.v1.Orders").
	Name     string `json:"name"`
	FullName string `json:"fullName"`
	// File is the import path of the file that defines it.
	File string `json:"file"`
	// Comment is the leading comment, or the trailing one when there is
	// none, trimmed.
	Comment    string      `json:"comment"`
	Deprecated bool        `json:"deprecated"`
	Methods    []MethodDTO `json:"methods"`
}

// MethodDTO is an rpc.
type MethodDTO struct {
	Name     string `json:"name"`
	FullName string `json:"fullName"`
	// InputType and OutputType are the full names of the message types,
	// or the names as written when they did not resolve.
	InputType       string `json:"inputType"`
	OutputType      string `json:"outputType"`
	ClientStreaming bool   `json:"clientStreaming"`
	ServerStreaming bool   `json:"serverStreaming"`
	// Kind is "unary", "client_stream", "server_stream" or "bidi".
	Kind       string `json:"kind"`
	Deprecated bool   `json:"deprecated"`
	Comment    string `json:"comment"`
}

// DiagnosticDTO is a problem with the proto setup.
type DiagnosticDTO struct {
	// Code is the linker's ("proto.import_not_found", docs/proto.md) or the
	// service's ("grpc.import_path_missing").
	Code string `json:"code"`
	// Severity is "error" or "warning".
	Severity string `json:"severity"`
	// File is the import path of the file, or the path as stored for a
	// file or import path that could not be used.
	File string `json:"file"`
	// Path is where the file is, as a stored path (relative to the project
	// when inside it); "" when unknown or built in.
	Path string `json:"path"`
	// Line and Column are 1-based; 0 for problems with the whole file.
	Line    int    `json:"line"`
	Column  int    `json:"column"`
	Message string `json:"message"`
}

func toDescribeResult(root string, res *linker.Result, extra []linker.Diagnostic, cached bool) DescribeResultDTO {
	out := DescribeResultDTO{Services: make([]ServiceDTO, 0), Diagnostics: make([]DiagnosticDTO, 0), Cached: cached}
	diags := extra
	if res != nil {
		diags = append(slices.Clone(extra), res.Diagnostics...)
		for _, f := range res.Pool.Files() {
			if f.Builtin {
				continue
			}
			out.Files++
			for _, svc := range f.Services {
				out.Services = append(out.Services, toServiceDTO(svc))
			}
		}
		slices.SortStableFunc(out.Services, func(a, b ServiceDTO) int { return strings.Compare(a.FullName, b.FullName) })
	}
	for _, d := range diags {
		if d.Severity == parser.SeverityError {
			out.Errors++
		}
		out.Diagnostics = append(out.Diagnostics, toDiagnosticDTO(root, d))
	}
	return out
}

func toServiceDTO(s *desc.Service) ServiceDTO {
	out := ServiceDTO{
		Name:       s.Name,
		FullName:   s.FullName,
		Comment:    comment(&s.Base),
		Deprecated: s.Deprecated,
		Methods:    make([]MethodDTO, 0, len(s.Methods)),
	}
	if s.File != nil {
		out.File = s.File.Path
	}
	for _, m := range s.Methods {
		in, outName := m.InputName, m.OutputName
		if m.Input != nil {
			in = m.Input.FullName
		}
		if m.Output != nil {
			outName = m.Output.FullName
		}
		out.Methods = append(out.Methods, MethodDTO{
			Name:            m.Name,
			FullName:        m.FullName,
			InputType:       strings.TrimPrefix(in, "."),
			OutputType:      strings.TrimPrefix(outName, "."),
			ClientStreaming: m.ClientStreaming,
			ServerStreaming: m.ServerStreaming,
			Kind:            methodKind(m),
			Deprecated:      m.Deprecated,
			Comment:         comment(&m.Base),
		})
	}
	return out
}

// comment returns the documentation of an element: its leading comment,
// else its trailing one, with each line trimmed.
func comment(b *desc.Base) string {
	text := b.Comments.Leading
	if strings.TrimSpace(text) == "" {
		text = b.Comments.Trailing
	}
	lines := strings.Split(strings.TrimSpace(text), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	return strings.Join(lines, "\n")
}

func toDiagnosticDTO(root string, d linker.Diagnostic) DiagnosticDTO {
	out := DiagnosticDTO{
		Code:     d.Code,
		Severity: d.Severity.String(),
		File:     d.File,
		Line:     d.Line,
		Column:   d.Column,
		Message:  d.Message,
	}
	if filepath.IsAbs(d.File) {
		out.File = request.StoredFilePath(root, d.File)
	}
	if d.DiskPath != "" && filepath.IsAbs(d.DiskPath) {
		out.Path = request.StoredFilePath(root, d.DiskPath)
	}
	return out
}
