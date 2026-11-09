package grpctest

import (
	"io"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/sorrelfox/postsonde/internal/core/grpc/protowire"
	"github.com/sorrelfox/postsonde/internal/core/grpc/reflection/reflectwire"
	"github.com/sorrelfox/postsonde/internal/core/grpc/transport"
)

// ReflectionOptions configure the server reflection service.
type ReflectionOptions struct {
	// Files are serialized google.protobuf.FileDescriptorProtos.
	Files [][]byte
	// Services are listed by list_services. Nil lists every service
	// declared in Files, then the enabled reflection services, as grpc-go
	// does.
	Services []string
	// NoV1 and NoV1Alpha turn off one protocol version; its method then
	// answers UNIMPLEMENTED like any unknown method.
	NoV1, NoV1Alpha bool
	// WithDependencies answers file_containing_symbol, file_by_filename
	// and file_containing_extension with the file and its transitive
	// dependencies that were not sent on the stream yet, as grpc-go does.
	// Otherwise only the file itself is sent.
	WithDependencies bool
	// Errors makes requests answer with an error_response of the given
	// code instead. Keys are "list_services" or "<request kind>:<name>",
	// such as "file_by_filename:a/b.proto" or
	// "file_containing_symbol:pkg.Service".
	Errors map[string]transport.Code
}

// reflectionIndex maps names to files.
type reflectionIndex struct {
	opts     ReflectionOptions
	byName   map[string][]byte
	deps     map[string][]string
	symbols  map[string]string // full name -> file name
	exts     map[string]string // "extendee:number" -> file name
	extNums  map[string][]int32
	services []string

	mu       sync.Mutex
	requests []reflectwire.Request
}

// EnableReflection registers the v1 and v1alpha reflection services
// serving o.Files. Calling it again replaces the previous configuration.
func (s *Server) EnableReflection(o ReflectionOptions) {
	idx := &reflectionIndex{
		opts:    o,
		byName:  map[string][]byte{},
		deps:    map[string][]string{},
		symbols: map[string]string{},
		exts:    map[string]string{},
		extNums: map[string][]int32{},
	}
	for _, b := range o.Files {
		idx.addFile(b)
	}
	if o.Services != nil {
		idx.services = o.Services
	} else {
		if !o.NoV1 {
			idx.services = append(idx.services, reflectwire.ServiceV1)
		}
		if !o.NoV1Alpha {
			idx.services = append(idx.services, reflectwire.ServiceV1Alpha)
		}
	}
	s.mu.Lock()
	s.reflection = idx
	delete(s.handlers, reflectwire.MethodV1)
	delete(s.handlers, reflectwire.MethodV1Alpha)
	if !o.NoV1 {
		s.handlers[reflectwire.MethodV1] = idx.serve
	}
	if !o.NoV1Alpha {
		s.handlers[reflectwire.MethodV1Alpha] = idx.serve
	}
	s.mu.Unlock()
}

// ReflectionRequests returns the reflection requests received so far, in
// order, over both versions.
func (s *Server) ReflectionRequests() []reflectwire.Request {
	s.mu.Lock()
	idx := s.reflection
	s.mu.Unlock()
	if idx == nil {
		return nil
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	return slices.Clone(idx.requests)
}

// eachField calls fn for every length-delimited field of b and stops at
// malformed input.
func eachField(b []byte, fn func(num protowire.Number, v []byte)) {
	for len(b) > 0 {
		num, typ, n, err := protowire.ConsumeTag(b)
		if err != nil {
			return
		}
		m, err := protowire.ConsumeFieldValue(num, typ, b[n:])
		if err != nil {
			return
		}
		if typ == protowire.BytesType {
			v, _, _ := protowire.ConsumeBytes(b[n:])
			fn(num, v)
		}
		b = b[n+m:]
	}
}

func varintField(b []byte, want protowire.Number) int32 {
	var out int32
	for len(b) > 0 {
		num, typ, n, err := protowire.ConsumeTag(b)
		if err != nil {
			return out
		}
		if num == want && typ == protowire.VarintType {
			v, _, _ := protowire.ConsumeVarint(b[n:])
			out = int32(v)
		}
		m, err := protowire.ConsumeFieldValue(num, typ, b[n:])
		if err != nil {
			return out
		}
		b = b[n+m:]
	}
	return out
}

func stringField(b []byte, want protowire.Number) string {
	var out string
	eachField(b, func(num protowire.Number, v []byte) {
		if num == want {
			out = string(v)
		}
	})
	return out
}

// addFile indexes the symbols of a FileDescriptorProto.
func (idx *reflectionIndex) addFile(b []byte) {
	name, pkg := stringField(b, 1), stringField(b, 2)
	if _, dup := idx.byName[name]; dup {
		return
	}
	idx.byName[name] = b
	join := func(scope, n string) string {
		if scope == "" {
			return n
		}
		return scope + "." + n
	}
	ext := func(scope string, v []byte) {
		idx.symbols[join(scope, stringField(v, 1))] = name
		extendee := strings.TrimPrefix(stringField(v, 2), ".")
		num := varintField(v, 3)
		idx.exts[extendee+":"+strconv.Itoa(int(num))] = name
		idx.extNums[extendee] = append(idx.extNums[extendee], num)
	}
	var message func(scope string, v []byte)
	message = func(scope string, v []byte) {
		full := join(scope, stringField(v, 1))
		idx.symbols[full] = name
		eachField(v, func(num protowire.Number, x []byte) {
			switch num {
			case 3:
				message(full, x)
			case 4:
				idx.symbols[join(full, stringField(x, 1))] = name
			case 6:
				ext(full, x)
			}
		})
	}
	eachField(b, func(num protowire.Number, v []byte) {
		switch num {
		case 3:
			idx.deps[name] = append(idx.deps[name], string(v))
		case 4:
			message(pkg, v)
		case 5:
			idx.symbols[join(pkg, stringField(v, 1))] = name
		case 6:
			svc := join(pkg, stringField(v, 1))
			idx.symbols[svc] = name
			if idx.opts.Services == nil {
				idx.services = append(idx.services, svc)
			}
			eachField(v, func(num protowire.Number, m []byte) {
				if num == 2 {
					idx.symbols[svc+"."+stringField(m, 1)] = name
				}
			})
		case 7:
			ext(pkg, v)
		}
	})
}

// serve answers reflection requests until the client closes the stream.
func (idx *reflectionIndex) serve(ss *ServerStream) transport.Status {
	sent := map[string]bool{}
	for {
		msg, err := ss.Recv()
		if err == io.EOF {
			return transport.Status{}
		}
		if err != nil {
			return errStatus(err)
		}
		req, err := reflectwire.UnmarshalRequest(msg)
		if err != nil {
			return transport.Status{Code: transport.InvalidArgument, Message: err.Error()}
		}
		idx.mu.Lock()
		idx.requests = append(idx.requests, req)
		idx.mu.Unlock()

		resp := reflectwire.Response{ValidHost: req.Host, Original: &req}
		key := req.Kind.String() + ":" + req.Name
		if req.Kind == reflectwire.RequestListServices {
			key = "list_services"
		}
		if code, ok := idx.opts.Errors[key]; ok {
			resp.Kind, resp.ErrorCode, resp.ErrorMessage = reflectwire.ResponseError, int32(code), "injected error for "+key
		} else {
			switch req.Kind {
			case reflectwire.RequestListServices:
				resp.Kind, resp.Services = reflectwire.ResponseListServices, idx.services
			case reflectwire.RequestFileByFilename:
				idx.files(&resp, req.Name, sent, req.Name)
			case reflectwire.RequestFileContainingSymbol:
				idx.files(&resp, idx.symbols[req.Name], sent, req.Name)
			case reflectwire.RequestFileContainingExtension:
				idx.files(&resp, idx.exts[req.Name+":"+strconv.Itoa(int(req.ExtensionNumber))], sent, req.Name)
			case reflectwire.RequestAllExtensionNumbersOfType:
				if _, ok := idx.symbols[req.Name]; !ok {
					resp.Kind, resp.ErrorCode, resp.ErrorMessage = reflectwire.ResponseError, int32(transport.NotFound), "type not found"
				} else {
					resp.Kind, resp.BaseTypeName, resp.ExtensionNumbers = reflectwire.ResponseAllExtensionNumbers, req.Name, idx.extNums[req.Name]
				}
			default:
				return transport.Status{Code: transport.InvalidArgument, Message: "invalid MessageRequest"}
			}
		}
		if err := ss.Send(reflectwire.MarshalResponse(resp)); err != nil {
			return errStatus(err)
		}
	}
}

// files fills resp with file name (and its unsent dependencies), or a
// NOT_FOUND error_response.
func (idx *reflectionIndex) files(resp *reflectwire.Response, name string, sent map[string]bool, asked string) {
	if _, ok := idx.byName[name]; !ok {
		resp.Kind, resp.ErrorCode, resp.ErrorMessage = reflectwire.ResponseError, int32(transport.NotFound), "not found: "+asked
		return
	}
	resp.Kind = reflectwire.ResponseFileDescriptor
	if !idx.opts.WithDependencies {
		resp.Files = [][]byte{idx.byName[name]}
		return
	}
	// The requested file always comes first, even if it was sent before.
	resp.Files = [][]byte{idx.byName[name]}
	sent[name] = true
	queue := slices.Clone(idx.deps[name])
	for len(queue) > 0 {
		d := queue[0]
		queue = queue[1:]
		b, ok := idx.byName[d]
		if !ok || sent[d] {
			continue
		}
		sent[d] = true
		resp.Files = append(resp.Files, b)
		queue = append(queue, idx.deps[d]...)
	}
}
