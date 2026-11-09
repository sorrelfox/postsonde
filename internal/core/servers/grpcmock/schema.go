package grpcmock

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/grpc/dynamic"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/desc"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/linker"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/parser"
	"github.com/sorrelfox/postsonde/internal/core/grpc/protojson"
	"github.com/sorrelfox/postsonde/internal/core/grpc/transport"
)

// Error codes of loading the .proto files.
const (
	// CodeProtoFailed: the .proto files have errors (a missing file, a
	// syntax error, a type that does not resolve). Details: errors (the
	// count).
	CodeProtoFailed = "grpcmock.proto_failed"
	// CodeRelativePath: a relative path with no project open to resolve it
	// against. Details: path.
	CodeRelativePath = "grpcmock.relative_path"
)

// linkTimeout bounds loading the .proto files.
const linkTimeout = 30 * time.Second

// schema is the linked .proto files of one configuration.
type schema struct {
	pool *desc.Pool
	// inputs are the files the configuration names, linked.
	inputs []*desc.File
	// methods are every method of the input files' services (their
	// imports' too) by path.
	methods map[string]*desc.Method
	// services are the full names of those services, in file order.
	services []string

	reflOnce sync.Once
	refl     *reflectionIndex
}

// resolvePaths makes the stored paths of c absolute.
func resolvePaths(c Config, resolve func(string) (string, error)) (files, roots []string, err error) {
	abs := func(p string) (string, error) {
		if resolve != nil {
			return resolve(p)
		}
		if !filepath.IsAbs(p) {
			return "", errs.New(errs.InvalidArgument, "The path "+strconv.Quote(p)+" is relative to the project, but no project is open.").
				WithCode(CodeRelativePath).WithDetail("path", p)
		}
		return p, nil
	}
	for _, f := range c.Files {
		a, err := abs(f)
		if err != nil {
			return nil, nil, err
		}
		files = append(files, a)
	}
	for _, r := range c.ImportPaths {
		a, err := abs(r)
		if err != nil {
			return nil, nil, err
		}
		roots = append(roots, a)
	}
	return files, roots, nil
}

// link loads and links the .proto files of c, resolved and read as opts
// says. No files is an empty schema. Errors in the files fail it with
// CodeProtoFailed.
func link(c Config, opts Options) (*schema, error) {
	files, roots, err := resolvePaths(c, opts.Resolve)
	if err != nil {
		return nil, err
	}
	s := &schema{methods: map[string]*desc.Method{}}
	if len(files) == 0 {
		s.pool = desc.NewPool(nil)
		return s, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), linkTimeout)
	defer cancel()
	lo := linker.Options{Files: files, ImportPaths: roots}
	if opts.Access != nil {
		lo.Access = opts.Access()
	}
	res, err := linker.Load(ctx, lo)
	if err != nil {
		return nil, errs.Wrap(err, errs.KindOf(err), "The .proto files could not be loaded: "+err.Error()).WithCode(CodeProtoFailed)
	}
	if lerr := res.Err(); lerr != nil {
		var first string
		n := 0
		for _, d := range res.Diagnostics {
			if d.Severity == parser.SeverityError {
				if first == "" {
					first = d.Error()
				}
				n++
			}
		}
		if first == "" {
			first = lerr.Error()
		}
		return nil, errs.Wrap(lerr, errs.InvalidArgument, "The .proto files have errors: "+first).
			WithCode(CodeProtoFailed).WithDetail("errors", strconv.Itoa(n)).
			WithHint("Fix the files, or open them in a gRPC request to see every problem.")
	}
	s.pool, s.inputs = res.Pool, res.Files
	seen := map[*desc.File]bool{}
	var walk func(f *desc.File)
	walk = func(f *desc.File) {
		if f == nil || seen[f] {
			return
		}
		seen[f] = true
		if !f.Builtin {
			for _, svc := range f.Services {
				s.services = append(s.services, svc.FullName)
				for _, m := range svc.Methods {
					s.methods["/"+svc.FullName+"/"+m.Name] = m
				}
			}
		}
		for _, imp := range f.Imports {
			walk(imp.File)
		}
	}
	for _, f := range res.Files {
		walk(f)
	}
	return s, nil
}

// methodKind names a method's call type as the traffic does.
func methodKind(m *desc.Method) string {
	switch {
	case m.ClientStreaming && m.ServerStreaming:
		return "bidi"
	case m.ClientStreaming:
		return "client_stream"
	case m.ServerStreaming:
		return "server_stream"
	}
	return "unary"
}

// answer is a Method compiled against the schema.
type answer struct {
	cfg    Method
	method *desc.Method
	// messages are the canned messages, encoded.
	messages [][]byte
	// problem is why the answer cannot be sent (a message that does not
	// fit the output type, echo between different types); the call then
	// ends INTERNAL with it.
	problem  string
	status   transport.Status
	headers  transport.Metadata
	trailers transport.Metadata
}

// state is what the server serves: a configuration and its schema.
type state struct {
	cfg     Config
	schema  *schema
	answers map[string]*answer
	// problems are the configuration's methods that cannot be served, one
	// line each, for the traffic log.
	problems []string
}

// compile builds the answers of cfg against sc.
func compile(cfg Config, sc *schema) *state {
	st := &state{cfg: cfg, schema: sc, answers: map[string]*answer{}}
	for i, m := range cfg.Methods {
		md := sc.methods[m.Path()]
		if md == nil {
			st.problems = append(st.problems, fmt.Sprintf("Method %d: %s is not in the .proto files; it is not served.", i+1, m.Path()))
			continue
		}
		a := &answer{
			cfg:      m,
			method:   md,
			status:   transport.Status{Code: transport.Code(m.Status), Message: m.StatusMessage},
			headers:  metadata(m.Headers),
			trailers: metadata(m.Trailers),
		}
		switch {
		case md.Output == nil:
			a.problem = "the output type " + md.OutputName + " did not resolve"
		case md.ClientStreaming && md.ServerStreaming && m.Bidi == BidiEcho && md.Input != md.Output:
			a.problem = "echo needs the same input and output type, but " + m.Path() + " takes " + md.Input.FullName + " and returns " + md.Output.FullName
		default:
			for j, text := range m.Messages {
				b, err := encodeJSON(sc.pool, md.Output, text)
				if err != nil {
					a.problem = fmt.Sprintf("message %d is not a valid %s: %s", j+1, md.Output.FullName, jsonMessage(err))
					a.messages = nil
					break
				}
				a.messages = append(a.messages, b)
			}
		}
		if a.problem != "" {
			st.problems = append(st.problems, fmt.Sprintf("Method %d (%s): %s; calls end with INTERNAL.", i+1, m.Path(), a.problem))
		}
		st.answers[m.Path()] = a
	}
	return st
}

// encodeJSON parses text (blank is an empty message) as md and encodes it.
func encodeJSON(pool *desc.Pool, md *desc.Message, text string) ([]byte, error) {
	in := []byte(text)
	if strings.TrimSpace(text) == "" {
		in = []byte("{}")
	}
	msg := dynamic.New(md)
	if err := (protojson.UnmarshalOptions{Resolver: pool}).Unmarshal(in, msg); err != nil {
		return nil, err
	}
	return dynamic.Marshal(msg)
}

// decodeJSON decodes b as md and writes it as indented JSON.
func decodeJSON(pool *desc.Pool, md *desc.Message, b []byte) ([]byte, error) {
	msg := dynamic.New(md)
	if err := dynamic.Unmarshal(b, msg); err != nil {
		return nil, err
	}
	return (protojson.MarshalOptions{Indent: "  ", AllowPartial: true, Resolver: pool}).Marshal(msg)
}

// jsonMessage is a protojson error without its prefix, with the position.
func jsonMessage(err error) string {
	if je, ok := errors.AsType[*protojson.Error](err); ok {
		if je.Offset >= 0 {
			return je.Message + " (line " + strconv.Itoa(je.Line) + ", column " + strconv.Itoa(je.Column) + ")"
		}
		return je.Message
	}
	return err.Error()
}
