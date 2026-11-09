// Package rpcstatus decodes gRPC's rich error model (GRPC-20): the
// google.rpc.Status a server sends in the grpc-status-details-bin trailer,
// with its google.protobuf.Any details decoded by type URL. The standard
// detail types of google/rpc/error_details.proto (ErrorInfo, RetryInfo,
// BadRequest, ...) are built in; other types are found in the caller's
// descriptors, and details of unknown types are kept as bytes. See
// docs/grpc.md (Status and error details).
package rpcstatus

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/sorrelfox/postsonde/internal/core/grpc/dynamic"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/desc"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/linker"
	"github.com/sorrelfox/postsonde/internal/core/grpc/protojson"
)

// Full names of the messages this package reads.
const (
	StatusName = "google.rpc.Status"
	anyName    = "google.protobuf.Any"
)

// builtinFiles are linked into the built-in pool.
var builtinFiles = []string{"google/rpc/status.proto", "google/rpc/error_details.proto"}

var (
	builtinOnce sync.Once
	builtin     *desc.Pool
	builtinErr  error
)

// Builtin returns the pool with google.rpc.Status, the error detail
// messages and the well-known types they use, linked once from the
// embedded sources.
func Builtin() (*desc.Pool, error) {
	builtinOnce.Do(func() {
		res, err := linker.Load(context.Background(), linker.Options{Files: builtinFiles})
		if err == nil {
			err = res.Err()
		}
		if err != nil {
			builtinErr = err
			return
		}
		builtin = res.Pool
	})
	return builtin, builtinErr
}

// Resolver finds message types by full name, and extensions for
// protojson; *desc.Pool implements it.
type Resolver = protojson.Resolver

// chain looks names up in each resolver in turn.
type chain []Resolver

func (c chain) FindMessage(name string) *desc.Message {
	for _, r := range c {
		if m := r.FindMessage(name); m != nil {
			return m
		}
	}
	return nil
}

func (c chain) FindExtension(name string) *desc.Field {
	for _, r := range c {
		if f := r.FindExtension(name); f != nil {
			return f
		}
	}
	return nil
}

func (c chain) FindExtensionByNumber(m *desc.Message, n int32) *desc.Field {
	for _, r := range c {
		if f := r.FindExtensionByNumber(m, n); f != nil {
			return f
		}
	}
	return nil
}

// Status is a decoded google.rpc.Status.
type Status struct {
	// Code and Message are the status's own fields, normally the same as
	// grpc-status and grpc-message.
	Code    int32
	Message string
	Details []Detail
	// Resolver finds the types of the details: the caller's first, then
	// the built-in ones. Use it to write them as JSON.
	Resolver Resolver
}

// Detail is one entry of Status.Details, a google.protobuf.Any.
type Detail struct {
	// TypeURL is the Any's type URL, Type the full name after its last "/".
	TypeURL string
	Type    string
	// Value is the payload as sent.
	Value []byte
	// Message is the payload decoded, nil when the type is unknown or the
	// bytes do not decode (Err).
	Message *dynamic.Message
	Err     error
}

// Known reports whether the detail's type was found.
func (d Detail) Known() bool { return d.Message != nil || d.Err != nil }

// ErrNotStatus is wrapped by Decode's errors: the bytes are not a
// google.rpc.Status.
var ErrNotStatus = errors.New("not a google.rpc.Status")

// Decode parses b, the bytes of grpc-status-details-bin, as a
// google.rpc.Status and decodes each detail whose type types (may be nil)
// or the built-in pool knows. types come first, so a project's own copy
// of a type is used. A detail that does not decode is kept with Err; only
// bytes that are not a Status at all fail.
func Decode(b []byte, types Resolver) (*Status, error) {
	pool, err := Builtin()
	if err != nil {
		return nil, err
	}
	res := chain{pool}
	if types != nil {
		res = chain{types, pool}
	}
	md := pool.FindMessage(StatusName)
	msg := dynamic.New(md)
	if err := (dynamic.UnmarshalOptions{Resolver: pool}).Unmarshal(b, msg); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNotStatus, err)
	}
	st := &Status{Resolver: res}
	if v, ok := msg.Get(md.FieldByName("code")).(int32); ok {
		st.Code = v
	}
	if v, ok := msg.Get(md.FieldByName("message")).(string); ok {
		st.Message = v
	}
	list, _ := msg.Get(md.FieldByName("details")).(*dynamic.List)
	for i := range list.Len() {
		a, ok := list.Get(i).(*dynamic.Message)
		if !ok || a.Descriptor().FullName != anyName {
			continue
		}
		st.Details = append(st.Details, decodeAny(a, res))
	}
	return st, nil
}

// decodeAny decodes an Any's payload with res.
func decodeAny(a *dynamic.Message, res chain) Detail {
	amd := a.Descriptor()
	var d Detail
	d.TypeURL, _ = a.Get(amd.FieldByName("type_url")).(string)
	d.Value, _ = a.Get(amd.FieldByName("value")).([]byte)
	d.Type = d.TypeURL[strings.LastIndexByte(d.TypeURL, '/')+1:]
	md := res.FindMessage(d.Type)
	if md == nil {
		return d
	}
	m := dynamic.New(md)
	if err := (dynamic.UnmarshalOptions{Resolver: res}).Unmarshal(d.Value, m); err != nil {
		d.Err = err
		return d
	}
	d.Message = m
	return d
}

// TypeURLPrefix is the prefix Marshal gives type URLs.
const TypeURLPrefix = "type.googleapis.com/"

// Marshal encodes st as a google.rpc.Status, as a server puts it in
// grpc-status-details-bin. A detail with a Message is encoded from it,
// with TypeURLPrefix and its full name when TypeURL is empty; otherwise
// TypeURL and Value are sent as they are.
func Marshal(st *Status) ([]byte, error) {
	pool, err := Builtin()
	if err != nil {
		return nil, err
	}
	md := pool.FindMessage(StatusName)
	msg := dynamic.New(md)
	if err := msg.Set(md.FieldByName("code"), st.Code); err != nil {
		return nil, err
	}
	if err := msg.Set(md.FieldByName("message"), st.Message); err != nil {
		return nil, err
	}
	list := msg.Mutable(md.FieldByName("details")).(*dynamic.List)
	for _, d := range st.Details {
		url, value := d.TypeURL, d.Value
		if d.Message != nil {
			if url == "" {
				url = TypeURLPrefix + d.Message.Descriptor().FullName
			}
			if value, err = (dynamic.MarshalOptions{Deterministic: true}).Marshal(d.Message); err != nil {
				return nil, err
			}
		}
		a := list.AppendMessage()
		amd := a.Descriptor()
		if err := a.Set(amd.FieldByName("type_url"), url); err != nil {
			return nil, err
		}
		if err := a.Set(amd.FieldByName("value"), value); err != nil {
			return nil, err
		}
	}
	return dynamic.MarshalOptions{Deterministic: true}.Marshal(msg)
}

// JSON writes a decoded detail's payload as indented protojson ("" for a
// detail that did not decode).
func (s *Status) JSON(d Detail) (string, error) {
	if d.Message == nil {
		return "", nil
	}
	b, err := protojson.MarshalOptions{Indent: "  ", Resolver: s.Resolver, AllowPartial: true}.Marshal(d.Message)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
