// Package reflection discovers a gRPC server's services through the server
// reflection API (GRPC-03) and keeps what it found per project for offline
// use (GRPC-05).
//
//   - Client.Fetch asks a server for its services and the
//     FileDescriptorProtos that define them, with their dependencies,
//     over grpc.reflection.v1 or, when the server does not implement it,
//     grpc.reflection.v1alpha.
//   - Link turns a Snapshot into linked descriptors (package desc) with
//     descproto.Link, the same model .proto files produce.
//   - Cache stores snapshots as JSON files in the project's local directory.
//   - Resolver combines them: Refresh, LoadCached and Resolve are what a
//     bound service calls.
//
// See docs/grpc-reflection.md.
package reflection

import (
	"context"
	"io"
	"strconv"
	"sync"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/wellknown"
	"github.com/sorrelfox/postsonde/internal/core/grpc/reflection/reflectwire"
	"github.com/sorrelfox/postsonde/internal/core/grpc/transport"
)

// Version is a reflection API version.
type Version string

// Versions.
const (
	V1      Version = "v1"
	V1Alpha Version = "v1alpha"
)

func (v Version) method() string {
	if v == V1Alpha {
		return reflectwire.MethodV1Alpha
	}
	return reflectwire.MethodV1
}

// Error codes. Transport failures keep their grpctransport.* and netx.*
// codes.
const (
	// CodeUnsupported: the server implements neither reflection version.
	CodeUnsupported = "grpcreflection.unsupported"
	// CodeInvalidVersion: FetchOptions.Version is not "", "v1" or "v1alpha".
	CodeInvalidVersion = "grpcreflection.invalid_version"
	// CodeServerError: the server answered list_services with an
	// error_response.
	CodeServerError = "grpcreflection.server_error"
	// CodeProtocol: a response that does not fit the request, a malformed
	// message, or a stream that ended early.
	CodeProtocol = "grpcreflection.protocol"
	// CodeTooManyFiles: the server sent more than MaxFiles descriptors.
	CodeTooManyFiles = "grpcreflection.too_many_files"

	// Problem codes: the fetch succeeded without these parts.
	CodeSymbolNotFound    = "grpcreflection.symbol_not_found"
	CodeFileNotFound      = "grpcreflection.file_not_found"
	CodeInvalidDescriptor = "grpcreflection.invalid_descriptor"
)

// MaxFiles bounds the descriptors one fetch accepts.
const MaxFiles = 10000

// Invoker starts gRPC calls; *transport.Client implements it.
type Invoker interface {
	Invoke(ctx context.Context, o transport.CallOptions) (*transport.Stream, error)
}

// Client fetches schemas by server reflection. It remembers, per address,
// which API version worked, and tries that one first next time. It is safe
// for concurrent use.
type Client struct {
	inv Invoker
	// Now returns the time recorded as Snapshot.FetchedAt (time.Now if
	// nil).
	Now func() time.Time

	mu       sync.Mutex
	versions map[string]Version
}

// NewClient returns a Client that makes calls through inv.
func NewClient(inv Invoker) *Client {
	return &Client{inv: inv, versions: map[string]Version{}}
}

// FetchOptions describe one fetch.
type FetchOptions struct {
	// Call holds the connection settings: Address, TLS, Authority,
	// Metadata, Timeout (for the whole fetch) and so on. Method is ignored.
	Call transport.CallOptions
	// Version forces an API version; "" tries the remembered one (v1 by
	// default) and falls back to the other on UNIMPLEMENTED.
	Version Version
	// IncludeReflectionServices keeps grpc.reflection.* in the service
	// list; by default they are left out.
	IncludeReflectionServices bool
}

// Snapshot is what one fetch found: the raw material to link and cache.
type Snapshot struct {
	// Address is the normalized address (NormalizeAddress).
	Address string
	// Version is the API version that answered.
	Version   Version
	FetchedAt time.Time
	// Services are the listed service names, in the server's order.
	Services []string
	// Files are the FileDescriptorProtos received, first occurrence of
	// each name, in the order they arrived.
	Files []File
	// Problems are the parts that could not be fetched.
	Problems []Problem
}

// File is one serialized FileDescriptorProto.
type File struct {
	Name       string
	Descriptor []byte
}

// Problem is a non-fatal issue: a service or dependency that could not be
// fetched, a descriptor that could not be read, or a cache that could not
// be used.
type Problem struct {
	Code    string
	Subject string // the service, file or cache path concerned
	Message string
}

// Descriptors returns the serialized descriptors of s's files.
func (s *Snapshot) Descriptors() [][]byte {
	out := make([][]byte, len(s.Files))
	for i, f := range s.Files {
		out[i] = f.Descriptor
	}
	return out
}

// RememberVersion records that address answered on v, as a successful
// fetch or a loaded cache does.
func (c *Client) RememberVersion(address string, v Version) {
	if v != V1 && v != V1Alpha {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.versions[NormalizeAddress(address)] = v
}

// RememberedVersion returns the version last seen working for address, or
// "".
func (c *Client) RememberedVersion(address string) Version {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.versions[NormalizeAddress(address)]
}

// Fetch lists the server's services and fetches the files defining them,
// with every dependency. It fails when the server cannot be reached, does
// not implement reflection, or rejects list_services; services and files
// that cannot be fetched are Problems in the snapshot.
func (c *Client) Fetch(ctx context.Context, o FetchOptions) (*Snapshot, error) {
	var order []Version
	switch o.Version {
	case V1, V1Alpha:
		order = []Version{o.Version}
	case "":
		order = []Version{V1, V1Alpha}
		if c.RememberedVersion(o.Call.Address) == V1Alpha {
			order = []Version{V1Alpha, V1}
		}
	default:
		return nil, errs.New(errs.InvalidArgument, "Unknown reflection API version "+strconv.Quote(string(o.Version))+".").
			WithCode(CodeInvalidVersion).WithHint(`Use "v1", "v1alpha" or leave it empty.`)
	}
	var unimplemented error
	for _, v := range order {
		snap, err := c.fetch(ctx, o, v)
		if err == nil {
			c.RememberVersion(o.Call.Address, v)
			return snap, nil
		}
		if transport.StatusFromError(err).Code != transport.Unimplemented {
			return nil, err
		}
		unimplemented = err
	}
	what := "server reflection"
	if len(order) == 1 {
		what = "server reflection " + string(order[0])
	}
	return nil, errs.Wrap(unimplemented, errs.Unsupported, "The server at "+o.Call.Address+" does not support "+what+".").
		WithCode(CodeUnsupported).
		WithHint("Enable the reflection service on the server, or load its .proto files instead.")
}

// fetcher runs one reflection stream.
type fetcher struct {
	o       FetchOptions
	stream  *transport.Stream
	snap    *Snapshot
	have    map[string]bool
	asked   map[string]bool
	pending []string
}

func (c *Client) fetch(ctx context.Context, o FetchOptions, v Version) (*Snapshot, error) {
	call := o.Call
	call.Method = v.method()
	stream, err := c.inv.Invoke(ctx, call)
	if err != nil {
		return nil, err
	}
	defer stream.Close()

	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	f := &fetcher{
		o:      o,
		stream: stream,
		snap:   &Snapshot{Address: NormalizeAddress(o.Call.Address), Version: v},
		have:   map[string]bool{},
		asked:  map[string]bool{},
	}

	resp, err := f.ask(reflectwire.Request{Kind: reflectwire.RequestListServices, Name: "*"}, reflectwire.ResponseListServices)
	if err != nil {
		return nil, err
	}
	if resp.Kind == reflectwire.ResponseError {
		return nil, errs.New(errs.Protocol, "The server at "+o.Call.Address+" refused to list its services: "+errorText(resp)).
			WithCode(CodeServerError).
			WithDetail("grpc_status", transport.Code(resp.ErrorCode).String())
	}
	seen := map[string]bool{}
	for _, s := range resp.Services {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		if !o.IncludeReflectionServices && (s == reflectwire.ServiceV1 || s == reflectwire.ServiceV1Alpha) {
			continue
		}
		f.snap.Services = append(f.snap.Services, s)
	}

	for _, s := range f.snap.Services {
		resp, err := f.ask(reflectwire.Request{Kind: reflectwire.RequestFileContainingSymbol, Name: s}, reflectwire.ResponseFileDescriptor)
		if err != nil {
			return nil, err
		}
		if resp.Kind == reflectwire.ResponseError {
			f.problem(CodeSymbolNotFound, s, "The server did not send the definition of service "+s+": "+errorText(resp))
			continue
		}
		if err := f.addFiles(resp.Files, s); err != nil {
			return nil, err
		}
		if err := f.fetchPending(); err != nil {
			return nil, err
		}
	}

	// End the stream cleanly; the result does not depend on how it ends.
	if f.stream.CloseSend() == nil {
		for {
			if _, err := f.stream.Recv(); err != nil {
				break
			}
		}
	}
	f.snap.FetchedAt = now().UTC()
	return f.snap, nil
}

// ask sends one request and returns its response, which must be of kind
// want or an error_response.
func (f *fetcher) ask(req reflectwire.Request, want reflectwire.ResponseKind) (reflectwire.Response, error) {
	// A Send that fails with io.EOF means the call ended; Recv says why.
	if err := f.stream.Send(reflectwire.MarshalRequest(req)); err != nil && err != io.EOF {
		return reflectwire.Response{}, err
	}
	msg, err := f.stream.Recv()
	if err == io.EOF {
		return reflectwire.Response{}, f.protocolErr("the server ended the reflection stream before answering " + req.Kind.String())
	}
	if err != nil {
		return reflectwire.Response{}, err
	}
	resp, err := reflectwire.UnmarshalResponse(msg)
	if err != nil {
		return reflectwire.Response{}, errs.Wrap(err, errs.Protocol, "The reflection response from "+f.o.Call.Address+" is not valid.").
			WithCode(CodeProtocol)
	}
	if resp.Kind != want && resp.Kind != reflectwire.ResponseError {
		return reflectwire.Response{}, f.protocolErr("the server answered " + req.Kind.String() + " with " + resp.Kind.String())
	}
	return resp, nil
}

func (f *fetcher) protocolErr(msg string) error {
	return errs.New(errs.Protocol, "Server reflection with "+f.o.Call.Address+" failed: "+msg+".").WithCode(CodeProtocol)
}

func (f *fetcher) problem(code, subject, msg string) {
	f.snap.Problems = append(f.snap.Problems, Problem{Code: code, Subject: subject, Message: msg})
}

func errorText(resp reflectwire.Response) string {
	s := transport.Code(resp.ErrorCode).String()
	if resp.ErrorMessage != "" {
		s += " (" + resp.ErrorMessage + ")"
	}
	return s
}

// addFiles records the files of a file_descriptor_response and queues
// their missing dependencies.
func (f *fetcher) addFiles(files [][]byte, subject string) error {
	for _, b := range files {
		name, deps, err := reflectwire.FileInfo(b)
		if err != nil || name == "" {
			f.problem(CodeInvalidDescriptor, subject, "The server sent a file descriptor that cannot be read.")
			continue
		}
		f.asked[name] = true
		if f.have[name] {
			continue
		}
		if len(f.snap.Files) >= MaxFiles {
			return errs.New(errs.Protocol, "The server at "+f.o.Call.Address+" sent more than "+strconv.Itoa(MaxFiles)+" file descriptors.").
				WithCode(CodeTooManyFiles)
		}
		f.have[name] = true
		f.snap.Files = append(f.snap.Files, File{Name: name, Descriptor: b})
		for _, d := range deps {
			if !f.have[d] && !f.asked[d] {
				f.pending = append(f.pending, d)
			}
		}
	}
	return nil
}

// fetchPending requests queued dependencies by file name until none is
// missing. Each name is requested at most once, so cycles end.
func (f *fetcher) fetchPending() error {
	for len(f.pending) > 0 {
		name := f.pending[0]
		f.pending = f.pending[1:]
		if f.have[name] || f.asked[name] {
			continue
		}
		f.asked[name] = true
		resp, err := f.ask(reflectwire.Request{Kind: reflectwire.RequestFileByFilename, Name: name}, reflectwire.ResponseFileDescriptor)
		if err != nil {
			return err
		}
		if resp.Kind == reflectwire.ResponseError {
			if _, builtin := wellknown.Source(name); !builtin {
				f.problem(CodeFileNotFound, name, "The server did not send "+name+": "+errorText(resp))
			}
			continue
		}
		if err := f.addFiles(resp.Files, name); err != nil {
			return err
		}
	}
	return nil
}
