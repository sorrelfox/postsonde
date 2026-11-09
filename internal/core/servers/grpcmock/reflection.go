package grpcmock

import (
	"slices"
	"strconv"

	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/desc"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/descproto"
	"github.com/sorrelfox/postsonde/internal/core/grpc/reflection/reflectwire"
	"github.com/sorrelfox/postsonde/internal/core/grpc/transport"
)

// reflectionIndex answers server reflection requests from a schema: the
// linked files as FileDescriptorProtos (descproto.Marshal), by file name
// and by the symbols they define.
type reflectionIndex struct {
	byName   map[string][]byte
	deps     map[string][]string
	symbols  map[string]string // full name -> file name
	services []string
	pool     *desc.Pool
}

// reflection returns the schema's reflection index, built the first time
// it is asked for.
func (s *schema) reflection() *reflectionIndex {
	s.reflOnce.Do(func() { s.refl = newReflectionIndex(s) })
	return s.refl
}

// newReflectionIndex marshals the input files and everything they
// import. A file that cannot be marshalled is left out (clients then
// report it missing).
func newReflectionIndex(s *schema) *reflectionIndex {
	idx := &reflectionIndex{
		byName:   map[string][]byte{},
		deps:     map[string][]string{},
		symbols:  map[string]string{},
		services: slices.Clone(s.services),
		pool:     s.pool,
	}
	idx.services = append(idx.services, reflectwire.ServiceV1, reflectwire.ServiceV1Alpha)
	var add func(f *desc.File)
	add = func(f *desc.File) {
		if f == nil {
			return
		}
		if _, done := idx.byName[f.Path]; done {
			return
		}
		b, err := descproto.Marshal(f, s.pool)
		if err != nil {
			idx.byName[f.Path] = nil
			return
		}
		idx.byName[f.Path] = b
		for _, imp := range f.Imports {
			idx.deps[f.Path] = append(idx.deps[f.Path], imp.Path)
		}
		desc.Walk(f, func(d desc.Descriptor) bool {
			if name := d.Desc().FullName; name != "" {
				if _, dup := idx.symbols[name]; !dup {
					idx.symbols[name] = f.Path
				}
			}
			return true
		})
		for _, imp := range f.Imports {
			add(imp.File)
		}
	}
	for _, f := range s.inputs {
		add(f)
	}
	for name, b := range idx.byName {
		if b == nil {
			delete(idx.byName, name)
		}
	}
	return idx
}

// isReflection reports whether path is a reflection method.
func isReflection(path string) bool {
	return path == reflectwire.MethodV1 || path == reflectwire.MethodV1Alpha
}

// answer returns the response to one reflection request. sent tracks the
// files sent on the stream, so dependencies go once, as grpc-go does.
func (idx *reflectionIndex) answer(req reflectwire.Request, sent map[string]bool) (reflectwire.Response, bool) {
	resp := reflectwire.Response{ValidHost: req.Host, Original: &req}
	switch req.Kind {
	case reflectwire.RequestListServices:
		resp.Kind, resp.Services = reflectwire.ResponseListServices, idx.services
	case reflectwire.RequestFileByFilename:
		idx.files(&resp, req.Name, sent, req.Name)
	case reflectwire.RequestFileContainingSymbol:
		idx.files(&resp, idx.symbols[req.Name], sent, req.Name)
	case reflectwire.RequestFileContainingExtension:
		name := ""
		if m := idx.pool.FindMessage(req.Name); m != nil {
			if x := idx.pool.FindExtensionByNumber(m, req.ExtensionNumber); x != nil && x.File != nil {
				name = x.File.Path
			}
		}
		idx.files(&resp, name, sent, req.Name+" extension "+strconv.Itoa(int(req.ExtensionNumber)))
	case reflectwire.RequestAllExtensionNumbersOfType:
		m := idx.pool.FindMessage(req.Name)
		if m == nil {
			resp.Kind, resp.ErrorCode, resp.ErrorMessage = reflectwire.ResponseError, int32(transport.NotFound), "type not found: "+req.Name
			break
		}
		resp.Kind, resp.BaseTypeName = reflectwire.ResponseAllExtensionNumbers, req.Name
		resp.ExtensionNumbers = []int32{}
		for _, x := range idx.pool.Extensions(m) {
			resp.ExtensionNumbers = append(resp.ExtensionNumbers, x.Number)
		}
	default:
		return resp, false
	}
	return resp, true
}

// files fills resp with the file name and its dependencies not sent on
// the stream yet, or a NOT_FOUND error_response.
func (idx *reflectionIndex) files(resp *reflectwire.Response, name string, sent map[string]bool, asked string) {
	b, ok := idx.byName[name]
	if !ok {
		resp.Kind, resp.ErrorCode, resp.ErrorMessage = reflectwire.ResponseError, int32(transport.NotFound), "not found: "+asked
		return
	}
	resp.Kind = reflectwire.ResponseFileDescriptor
	// The requested file always comes first, even if it was sent before.
	resp.Files = [][]byte{b}
	sent[name] = true
	queue := slices.Clone(idx.deps[name])
	for len(queue) > 0 {
		d := queue[0]
		queue = queue[1:]
		fb, ok := idx.byName[d]
		if !ok || sent[d] {
			continue
		}
		sent[d] = true
		resp.Files = append(resp.Files, fb)
		queue = append(queue, idx.deps[d]...)
	}
}
