package descproto

import (
	"bytes"
	"context"
	"strconv"
	"testing/fstest"

	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/desc"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/linker"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/parser"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/wellknown"
)

// Diagnostic codes added by Link, besides the linker's own.
const (
	// CodeInvalidDescriptor: bytes that are not a FileDescriptorProto, or
	// one that cannot be expressed as .proto source (invalid names, an
	// unsupported syntax or edition). The file is left out.
	CodeInvalidDescriptor = "proto.invalid_descriptor"
	// CodeDuplicateDescriptor: two different descriptors with the same file
	// name; the first is used (warning).
	CodeDuplicateDescriptor = "proto.duplicate_descriptor"
	// CodeUnknownOptions: option fields that no loaded file defines were
	// left out of the model (warning).
	CodeUnknownOptions = "proto.unknown_options"
)

// Source renders one serialized FileDescriptorProto as .proto source and
// returns its file name. Custom options (extensions) are left out; Link
// includes those it can resolve.
func Source(fdp []byte) (name, src string, err error) {
	f, err := decodeFile(fdp)
	if err != nil {
		return "", "", err
	}
	src, _, err = render(f, nil)
	return f.name, src, err
}

// Link decodes serialized FileDescriptorProtos, renders each as .proto
// source and links them with package linker. The files may come in any
// order; imports between them resolve by file name, and imports of the
// well-known types (google/protobuf/*.proto) use the built-in copies, which
// also replace descriptors of those files in files.
//
// Problems with individual descriptors are diagnostics in the result, as
// are the linker's; the error is only for a cancelled context. The linked
// files' AST.Source is the rendered source, which is what diagnostic
// positions refer to.
func Link(ctx context.Context, files [][]byte) (*linker.Result, error) {
	var diags []linker.Diagnostic
	report := func(file, code string, sev parser.Severity, msg string) {
		diags = append(diags, linker.Diagnostic{Code: code, Severity: sev, File: file, Message: msg})
	}

	type input struct {
		raw []byte
		f   *fileProto
	}
	var inputs []input
	byName := map[string][]byte{}
	for i, b := range files {
		f, err := decodeFile(b)
		if err != nil {
			report("descriptor #"+strconv.Itoa(i+1), CodeInvalidDescriptor, parser.SeverityError,
				"not a valid FileDescriptorProto: "+err.Error())
			continue
		}
		if !linkerPath(f.name) {
			report("descriptor #"+strconv.Itoa(i+1), CodeInvalidDescriptor, parser.SeverityError,
				"invalid file name "+strconv.Quote(f.name))
			continue
		}
		if prev, dup := byName[f.name]; dup {
			if !bytes.Equal(prev, b) {
				report(f.name, CodeDuplicateDescriptor, parser.SeverityWarning,
					"two different descriptors for "+strconv.Quote(f.name)+"; the first one is used")
			}
			continue
		}
		byName[f.name] = b
		if _, builtin := wellknown.Source(f.name); builtin {
			continue
		}
		inputs = append(inputs, input{b, f})
	}

	renderAll := func(pool *desc.Pool) (fstest.MapFS, []string, int) {
		fsys := fstest.MapFS{}
		var names []string
		dropped := 0
		for _, in := range inputs {
			src, n, err := render(in.f, pool)
			if err != nil {
				if pool == nil {
					report(in.f.name, CodeInvalidDescriptor, parser.SeverityError,
						"cannot be read as a .proto file: "+err.Error())
				}
				continue
			}
			dropped += n
			fsys[in.f.name] = &fstest.MapFile{Data: []byte(src)}
			names = append(names, in.f.name)
		}
		return fsys, names, dropped
	}

	fsys, names, dropped := renderAll(nil)
	if len(names) == 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return &linker.Result{Pool: desc.NewPool(nil), Diagnostics: diags}, nil
	}
	res, err := linker.Load(ctx, linker.Options{Files: names, ImportPaths: []string{"."}, FS: fsys})
	if err != nil {
		return nil, err
	}
	if dropped > 0 {
		// Custom options: render again with the extensions the first pass
		// linked, so that they decode as known fields.
		fsys2, names2, dropped2 := renderAll(res.Pool)
		res2, err := linker.Load(ctx, linker.Options{Files: names2, ImportPaths: []string{"."}, FS: fsys2})
		if err != nil {
			return nil, err
		}
		res, dropped = res2, dropped2
		if dropped > 0 {
			report("", CodeUnknownOptions, parser.SeverityWarning,
				strconv.Itoa(dropped)+" option value(s) use extensions that none of the files define; they were left out")
		}
	}
	res.Diagnostics = append(diags, res.Diagnostics...)
	return res, nil
}

// linkerPath reports whether name is usable as an import path.
func linkerPath(name string) bool {
	if name == "" || name[0] == '/' || name[len(name)-1] == '/' {
		return false
	}
	for i := 0; i < len(name); i++ {
		if name[i] == '\\' || name[i] == 0 {
			return false
		}
	}
	for _, part := range bytes.Split([]byte(name), []byte("/")) {
		if len(part) == 0 || string(part) == "." || string(part) == ".." {
			return false
		}
	}
	return true
}
