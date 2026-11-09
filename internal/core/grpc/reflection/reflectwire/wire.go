// Package reflectwire encodes and decodes the messages of the gRPC server
// reflection protocol (grpc.reflection.v1 and v1alpha, which share their
// wire format) directly with protowire, without generated code or an
// embedded reflection.proto. The client in package reflection and the test
// server in grpctest both use it.
//
// See docs/grpc-reflection.md.
package reflectwire

import (
	"errors"
	"fmt"

	"github.com/sorrelfox/postsonde/internal/core/grpc/protowire"
)

// Full method names of the reflection stream.
const (
	MethodV1      = "/grpc.reflection.v1.ServerReflection/ServerReflectionInfo"
	MethodV1Alpha = "/grpc.reflection.v1alpha.ServerReflection/ServerReflectionInfo"
	// ServiceV1 and ServiceV1Alpha are the reflection services' names as
	// servers list them.
	ServiceV1      = "grpc.reflection.v1.ServerReflection"
	ServiceV1Alpha = "grpc.reflection.v1alpha.ServerReflection"
)

// RequestKind is the message_request member of a ServerReflectionRequest.
type RequestKind uint8

// Request kinds, named after the oneof members.
const (
	RequestNone                      RequestKind = 0
	RequestFileByFilename            RequestKind = 3
	RequestFileContainingSymbol      RequestKind = 4
	RequestFileContainingExtension   RequestKind = 5
	RequestAllExtensionNumbersOfType RequestKind = 6
	RequestListServices              RequestKind = 7
)

func (k RequestKind) String() string {
	switch k {
	case RequestFileByFilename:
		return "file_by_filename"
	case RequestFileContainingSymbol:
		return "file_containing_symbol"
	case RequestFileContainingExtension:
		return "file_containing_extension"
	case RequestAllExtensionNumbersOfType:
		return "all_extension_numbers_of_type"
	case RequestListServices:
		return "list_services"
	}
	return "none"
}

// Request is a ServerReflectionRequest.
type Request struct {
	Host string
	Kind RequestKind
	// Name is the file name, symbol, type name or list_services value;
	// for RequestFileContainingExtension it is the containing type.
	Name string
	// ExtensionNumber is set for RequestFileContainingExtension.
	ExtensionNumber int32
}

// ResponseKind is the message_response member of a
// ServerReflectionResponse.
type ResponseKind uint8

// Response kinds, named after the oneof members.
const (
	ResponseNone                ResponseKind = 0
	ResponseFileDescriptor      ResponseKind = 4
	ResponseAllExtensionNumbers ResponseKind = 5
	ResponseListServices        ResponseKind = 6
	ResponseError               ResponseKind = 7
)

func (k ResponseKind) String() string {
	switch k {
	case ResponseFileDescriptor:
		return "file_descriptor_response"
	case ResponseAllExtensionNumbers:
		return "all_extension_numbers_response"
	case ResponseListServices:
		return "list_services_response"
	case ResponseError:
		return "error_response"
	}
	return "none"
}

// Response is a ServerReflectionResponse.
type Response struct {
	ValidHost string
	// Original is the original_request, nil if absent.
	Original *Request
	Kind     ResponseKind
	// Files are serialized FileDescriptorProtos (ResponseFileDescriptor).
	Files [][]byte
	// BaseTypeName and ExtensionNumbers are set for
	// ResponseAllExtensionNumbers.
	BaseTypeName     string
	ExtensionNumbers []int32
	// Services are the listed service names (ResponseListServices).
	Services []string
	// ErrorCode (a gRPC status code) and ErrorMessage are set for
	// ResponseError.
	ErrorCode    int32
	ErrorMessage string
}

// ErrMalformed is returned for input that is not a valid message.
var ErrMalformed = errors.New("malformed reflection message")

// MarshalRequest encodes r.
func MarshalRequest(r Request) []byte {
	var b []byte
	if r.Host != "" {
		b = protowire.AppendTag(b, 1, protowire.BytesType)
		b = protowire.AppendString(b, r.Host)
	}
	switch r.Kind {
	case RequestFileByFilename, RequestFileContainingSymbol, RequestAllExtensionNumbersOfType, RequestListServices:
		b = protowire.AppendTag(b, protowire.Number(r.Kind), protowire.BytesType)
		b = protowire.AppendString(b, r.Name)
	case RequestFileContainingExtension:
		var ext []byte
		if r.Name != "" {
			ext = protowire.AppendTag(ext, 1, protowire.BytesType)
			ext = protowire.AppendString(ext, r.Name)
		}
		if r.ExtensionNumber != 0 {
			ext = protowire.AppendTag(ext, 2, protowire.VarintType)
			ext = protowire.AppendVarint(ext, uint64(int64(r.ExtensionNumber)))
		}
		b = protowire.AppendTag(b, 5, protowire.BytesType)
		b = protowire.AppendBytes(b, ext)
	}
	return b
}

// UnmarshalRequest decodes a ServerReflectionRequest. Unknown fields and
// fields with an unexpected wire type are skipped.
func UnmarshalRequest(b []byte) (Request, error) {
	var r Request
	err := fields(b, func(num protowire.Number, typ protowire.Type, v []byte, x uint64) error {
		if typ != protowire.BytesType {
			return nil
		}
		switch num {
		case 1:
			r.Host = string(v)
		case 3, 4, 6, 7:
			r.Kind, r.Name, r.ExtensionNumber = RequestKind(num), string(v), 0
		case 5:
			r.Kind, r.Name, r.ExtensionNumber = RequestFileContainingExtension, "", 0
			return fields(v, func(num protowire.Number, typ protowire.Type, v []byte, x uint64) error {
				switch {
				case num == 1 && typ == protowire.BytesType:
					r.Name = string(v)
				case num == 2 && typ == protowire.VarintType:
					r.ExtensionNumber = int32(x)
				}
				return nil
			})
		}
		return nil
	})
	return r, err
}

// MarshalResponse encodes r.
func MarshalResponse(r Response) []byte {
	var b []byte
	if r.ValidHost != "" {
		b = protowire.AppendTag(b, 1, protowire.BytesType)
		b = protowire.AppendString(b, r.ValidHost)
	}
	if r.Original != nil {
		b = protowire.AppendTag(b, 2, protowire.BytesType)
		b = protowire.AppendBytes(b, MarshalRequest(*r.Original))
	}
	var body []byte
	switch r.Kind {
	case ResponseFileDescriptor:
		for _, f := range r.Files {
			body = protowire.AppendTag(body, 1, protowire.BytesType)
			body = protowire.AppendBytes(body, f)
		}
	case ResponseAllExtensionNumbers:
		if r.BaseTypeName != "" {
			body = protowire.AppendTag(body, 1, protowire.BytesType)
			body = protowire.AppendString(body, r.BaseTypeName)
		}
		if len(r.ExtensionNumbers) > 0 {
			var packed []byte
			for _, n := range r.ExtensionNumbers {
				packed = protowire.AppendVarint(packed, uint64(int64(n)))
			}
			body = protowire.AppendTag(body, 2, protowire.BytesType)
			body = protowire.AppendBytes(body, packed)
		}
	case ResponseListServices:
		for _, s := range r.Services {
			var svc []byte
			if s != "" {
				svc = protowire.AppendTag(svc, 1, protowire.BytesType)
				svc = protowire.AppendString(svc, s)
			}
			body = protowire.AppendTag(body, 1, protowire.BytesType)
			body = protowire.AppendBytes(body, svc)
		}
	case ResponseError:
		if r.ErrorCode != 0 {
			body = protowire.AppendTag(body, 1, protowire.VarintType)
			body = protowire.AppendVarint(body, uint64(int64(r.ErrorCode)))
		}
		if r.ErrorMessage != "" {
			body = protowire.AppendTag(body, 2, protowire.BytesType)
			body = protowire.AppendString(body, r.ErrorMessage)
		}
	default:
		return b
	}
	b = protowire.AppendTag(b, protowire.Number(r.Kind), protowire.BytesType)
	return protowire.AppendBytes(b, body)
}

// UnmarshalResponse decodes a ServerReflectionResponse. Unknown fields and
// fields with an unexpected wire type are skipped.
func UnmarshalResponse(b []byte) (Response, error) {
	var r Response
	err := fields(b, func(num protowire.Number, typ protowire.Type, v []byte, x uint64) error {
		if typ != protowire.BytesType {
			return nil
		}
		switch num {
		case 1:
			r.ValidHost = string(v)
			return nil
		case 2:
			req, err := UnmarshalRequest(v)
			if err != nil {
				return err
			}
			r.Original = &req
			return nil
		case 4, 5, 6, 7:
		default:
			return nil
		}
		// A oneof member replaces the previous one.
		kind := ResponseKind(num)
		if r.Kind != kind {
			r = Response{ValidHost: r.ValidHost, Original: r.Original}
			r.Kind = kind
		}
		return fields(v, func(n protowire.Number, typ protowire.Type, v []byte, x uint64) error {
			switch {
			case kind == ResponseFileDescriptor && n == 1 && typ == protowire.BytesType:
				r.Files = append(r.Files, v)
			case kind == ResponseAllExtensionNumbers && n == 1 && typ == protowire.BytesType:
				r.BaseTypeName = string(v)
			case kind == ResponseAllExtensionNumbers && n == 2 && typ == protowire.VarintType:
				r.ExtensionNumbers = append(r.ExtensionNumbers, int32(x))
			case kind == ResponseAllExtensionNumbers && n == 2 && typ == protowire.BytesType:
				for len(v) > 0 {
					u, m, err := protowire.ConsumeVarint(v)
					if err != nil {
						return fmt.Errorf("%w: %v", ErrMalformed, err)
					}
					r.ExtensionNumbers = append(r.ExtensionNumbers, int32(u))
					v = v[m:]
				}
			case kind == ResponseListServices && n == 1 && typ == protowire.BytesType:
				name := ""
				err := fields(v, func(n protowire.Number, typ protowire.Type, v []byte, x uint64) error {
					if n == 1 && typ == protowire.BytesType {
						name = string(v)
					}
					return nil
				})
				if err != nil {
					return err
				}
				r.Services = append(r.Services, name)
			case kind == ResponseError && n == 1 && typ == protowire.VarintType:
				r.ErrorCode = int32(x)
			case kind == ResponseError && n == 2 && typ == protowire.BytesType:
				r.ErrorMessage = string(v)
			}
			return nil
		})
	})
	return r, err
}

// FileInfo reads the name (field 1) and dependencies (field 3) of a
// serialized FileDescriptorProto without decoding the rest.
func FileInfo(fdp []byte) (name string, deps []string, err error) {
	err = fields(fdp, func(num protowire.Number, typ protowire.Type, v []byte, x uint64) error {
		if typ != protowire.BytesType {
			return nil
		}
		switch num {
		case 1:
			name = string(v)
		case 3:
			deps = append(deps, string(v))
		}
		return nil
	})
	return name, deps, err
}

// fields calls fn for each field of a message: v is the payload of a
// length-delimited field (a sub-slice of b), x the value of a varint or
// fixed field. Groups are skipped.
func fields(b []byte, fn func(num protowire.Number, typ protowire.Type, v []byte, x uint64) error) error {
	for len(b) > 0 {
		num, typ, n, err := protowire.ConsumeTag(b)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrMalformed, err)
		}
		b = b[n:]
		var v []byte
		var x uint64
		switch typ {
		case protowire.VarintType:
			x, n, err = protowire.ConsumeVarint(b)
		case protowire.Fixed32Type:
			var u uint32
			u, n, err = protowire.ConsumeFixed32(b)
			x = uint64(u)
		case protowire.Fixed64Type:
			x, n, err = protowire.ConsumeFixed64(b)
		case protowire.BytesType:
			v, n, err = protowire.ConsumeBytes(b)
		default:
			n, err = protowire.ConsumeFieldValue(num, typ, b)
			if err == nil {
				b = b[n:]
				continue
			}
		}
		if err != nil {
			return fmt.Errorf("%w: %v", ErrMalformed, err)
		}
		b = b[n:]
		if err := fn(num, typ, v, x); err != nil {
			return err
		}
	}
	return nil
}
