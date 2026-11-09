package projectfile

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/storage/atomicfile"
	"github.com/sorrelfox/postsonde/internal/core/storage/stablejson"
)

// WriteFile validates f and writes it to path atomically, in the format of
// Marshal. A new file gets FilePerm. The directory must exist.
func WriteFile(path string, f File) error {
	data, err := Marshal(f)
	if err != nil {
		return withPath(err, path)
	}
	if err := atomicfile.WriteFile(path, data, FilePerm); err != nil {
		return errs.Wrap(err, errs.KindOf(err), "A project file could not be saved.").
			WithCode(CodeSaveFailed).
			WithHint("Check that the project folder is writable and the disk is not full.").
			WithDetail("path", path)
	}
	return nil
}

// Marshal validates f and encodes it in the stablejson format: members in
// the order of the Go types, unknown members after the known ones sorted by
// key, schemaVersion set to CurrentVersion and kind to f's kind. f itself
// is not modified.
//
// It refuses (with an *errs.Error) a value decoded from a newer schema
// version (CodeNewerVersion), and an empty ID or HTTP method or a secret
// variable with a value, or a folder server without a type
// (CodeInvalidValue).
func Marshal(f File) ([]byte, error) {
	if f == nil || reflect.ValueOf(f).IsNil() {
		return nil, errs.New(errs.Internal, "A missing project file cannot be encoded.")
	}
	h := f.head()
	if h.Newer() {
		return nil, errs.New(errs.Unsupported,
			fmt.Sprintf("The file was written by a newer version of PostSonde (schema version %d; this version supports %d), so it is not overwritten.", h.SchemaVersion, CurrentVersion)).
			WithCode(CodeNewerVersion).
			WithHint("Update PostSonde to change this file.").
			WithDetail("version", strconv.Itoa(h.SchemaVersion)).
			WithDetail("supported", strconv.Itoa(CurrentVersion))
	}
	h = Head{SchemaVersion: CurrentVersion, Kind: f.FileKind()}

	var v any
	var err error
	switch f := f.(type) {
	case *Workspace:
		c := *f
		c.Head = h
		err = validate(c.ID, c.Variables)
		v = c
	case *Folder:
		c := *f
		c.Head = h
		err = validate(c.ID, c.Variables)
		if err == nil && c.Server != nil && strings.TrimSpace(c.Server.Type) == "" {
			err = invalid("server.type", "The folder's server has no type.").
				WithHint(`Set the kind of server, for example "mock_http".`)
		}
		v = c
	case *HTTPRequest:
		c := *f
		c.Head = h
		err = validate(c.ID, nil)
		if err == nil && strings.TrimSpace(c.Method) == "" {
			err = invalid("method", "The request has no HTTP method.").
				WithHint("Set the method, for example GET.")
		}
		v = c
	case *GraphQLRequest:
		c := *f
		c.Head = h
		err = validate(c.ID, nil)
		v = c
	case *TCPRequest:
		c := *f
		c.Head = h
		err = validate(c.ID, nil)
		v = c
	case *WebSocketRequest:
		c := *f
		c.Head = h
		err = validate(c.ID, nil)
		v = c
	case *GRPCRequest:
		c := *f
		c.Head = h
		err = validate(c.ID, nil)
		v = c
	case *SSERequest:
		c := *f
		c.Head = h
		err = validate(c.ID, nil)
		v = c
	case *SocketIORequest:
		c := *f
		c.Head = h
		err = validate(c.ID, nil)
		v = c
	case *UDPRequest:
		c := *f
		c.Head = h
		err = validate(c.ID, nil)
		v = c
	case *Environment:
		c := *f
		c.Head = h
		err = validate(c.ID, c.Variables)
		v = c
	default:
		return nil, errs.New(errs.Internal, fmt.Sprintf("%T is not a project file type.", f))
	}
	if err != nil {
		return nil, err
	}
	data, err := stablejson.Marshal(v)
	if err != nil {
		return nil, errs.Wrap(err, errs.Internal, "A project file could not be encoded.")
	}
	return data, nil
}

// validate checks what every file needs: an ID, and no secret values.
func validate(id string, vars []Variable) error {
	if strings.TrimSpace(id) == "" {
		return invalid("id", "The file has no id.").
			WithHint(`Give it a unique id, for example "req_" and 16 hex digits.`)
	}
	for i, v := range vars {
		if v.Secret && v.Value != "" {
			// Never put the value, or the variable's name, in the message.
			return invalid(fmt.Sprintf("variables[%d].value", i),
				"A secret variable's value cannot be written to a project file.").
				WithHint("Keep the value in PostSonde's local storage and leave it empty in the project.")
		}
	}
	return nil
}

func invalid(field, msg string) *errs.Error {
	return errs.New(errs.InvalidArgument, msg).
		WithCode(CodeInvalidValue).
		WithDetail("field", field)
}
