package protojson

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/sorrelfox/postsonde/internal/core/grpc/dynamic"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/desc"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/linker"
)

// linkSource links one in-memory .proto file, ignoring diagnostics, so that
// tests can use broken or unusual schemas.
func linkSource(t *testing.T, src string, noWellKnown bool) *desc.Pool {
	t.Helper()
	res, err := linker.Load(context.Background(), linker.Options{
		Files:       []string{"t.proto"},
		FS:          fstest.MapFS{"t.proto": &fstest.MapFile{Data: []byte(src)}},
		NoWellKnown: noWellKnown,
	})
	if err != nil && res == nil {
		t.Fatal(err)
	}
	return res.Pool
}

func field(t *testing.T, md *desc.Message, name string) *desc.Field {
	t.Helper()
	fd := md.FieldByName(name)
	if fd == nil {
		t.Fatalf("%s has no field %s", md.FullName, name)
	}
	return fd
}

func TestErrorPosition(t *testing.T) {
	md := msgType(t, "jsontest.Scalars")
	for _, tc := range []struct {
		in                   string
		offset, line, column int
		msg                  string
	}{
		{"{\n  \"nope\": 1}", 4, 2, 3, `unknown field "nope"`},
		{`{"fInt32":"x"}`, 10, 1, 11, `invalid value for int32 field fInt32: "x"`},
		{"{\"fString\":\"éé\",\"fInt32\":true}", 27, 1, 26, "invalid value for int32 field fInt32: true"},
		{`{"fInt32":1,"fInt32":2}`, 12, 1, 13, `duplicate field "fInt32"`},
		{`{"fInt32":1`, 11, 1, 12, "unexpected end of input"},
		{`{} x`, 3, 1, 4, "invalid value x"},
	} {
		err := Unmarshal([]byte(tc.in), dynamic.New(md))
		var e *Error
		if !errors.As(err, &e) {
			t.Fatalf("%q: error %v is not *Error", tc.in, err)
		}
		if e.Offset != tc.offset || e.Line != tc.line || e.Column != tc.column || e.Message != tc.msg {
			t.Errorf("%q: got offset %d line %d column %d %q, want %d %d %d %q", tc.in, e.Offset, e.Line, e.Column, e.Message,
				tc.offset, tc.line, tc.column, tc.msg)
		}
	}
	err := Unmarshal([]byte("{\n\"nope\":1}"), dynamic.New(md))
	if got, want := err.Error(), `protojson: line 2, column 1 (offset 2): unknown field "nope"`; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	oerr := (&Error{Offset: -1, Message: "x"}).Error()
	if oerr != "protojson: x" {
		t.Errorf("output Error() = %q", oerr)
	}
}

func TestMarshalBasics(t *testing.T) {
	if b, err := Marshal(nil); err != nil || string(b) != "{}" {
		t.Errorf("Marshal(nil) = %s, %v", b, err)
	}
	m := dynamic.New(msgType(t, "jsontest.Scalars"))
	if _, err := (MarshalOptions{Indent: " x"}).Marshal(m); err == nil {
		t.Error("indent with letters accepted")
	}
	must(t, m.Set(field(t, m.Descriptor(), "f_int32"), int32(1)))
	sub := m.Mutable(field(t, m.Descriptor(), "f_message")).(*dynamic.Message)
	must(t, sub.Set(field(t, sub.Descriptor(), "s"), "x"))
	b, err := MarshalOptions{Indent: "\t"}.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if want := "{\n\t\"fInt32\": 1,\n\t\"fMessage\": {\n\t\t\"s\": \"x\"\n\t}\n}"; string(b) != want {
		t.Errorf("tab indent:\n%s\nwant\n%s", b, want)
	}
	// Empty objects and arrays stay on one line.
	e := dynamic.New(msgType(t, "jsontest.Optionals"))
	b, err = MarshalOptions{Indent: "  ", EmitDefaultValues: true}.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if want := "{\n  \"plain\": 0,\n  \"plainList\": [],\n  \"plainMap\": {}\n}"; string(b) != want {
		t.Errorf("empty composites:\n%s\nwant\n%s", b, want)
	}
}

func TestMarshalInvalidUTF8(t *testing.T) {
	md := msgType(t, "jsontest.Maps")
	m := dynamic.New(md)
	mp := m.Mutable(field(t, md, "string_string")).(*dynamic.Map)
	must(t, mp.Set("a\xff", "x"))
	if _, err := Marshal(m); err == nil || !strings.Contains(err.Error(), "invalid UTF-8") {
		t.Errorf("invalid UTF-8 map key: %v", err)
	}
	s := dynamic.New(msgType(t, "jsontest.Scalars"))
	must(t, s.Set(field(t, s.Descriptor(), "f_string"), "\xc3"))
	if _, err := Marshal(s); err == nil || !strings.Contains(err.Error(), "invalid UTF-8") {
		t.Errorf("invalid UTF-8 string: %v", err)
	}
}

// TestClosedEnumUnknownNumber: JSON accepts any number for a closed enum
// (as protobuf-go does); it is written back as a number.
func TestClosedEnumUnknownNumber(t *testing.T) {
	md := msgType(t, "jsontest2.P2")
	m := dynamic.New(md)
	if err := (UnmarshalOptions{Resolver: pool(t)}).Unmarshal([]byte(`{"optClosed":7}`), m); err != nil {
		t.Fatal(err)
	}
	if got := m.Get(field(t, md, "opt_closed")); got != dynamic.EnumNumber(7) {
		t.Fatalf("opt_closed = %v", got)
	}
	if b, err := Marshal(m); err != nil || string(b) != `{"optClosed":7}` {
		t.Errorf("Marshal = %s, %v", b, err)
	}
}

func TestRecursionLimit(t *testing.T) {
	md := msgType(t, "jsontest.Nested")
	nest := func(n int) []byte {
		return []byte(strings.Repeat(`{"child":`, n-1) + "{}" + strings.Repeat("}", n-1))
	}
	if err := Unmarshal(nest(DefaultRecursionLimit), dynamic.New(md)); err != nil {
		t.Errorf("%d levels: %v", DefaultRecursionLimit, err)
	}
	if err := Unmarshal(nest(DefaultRecursionLimit+1), dynamic.New(md)); err == nil || !strings.Contains(err.Error(), "recursion") {
		t.Errorf("%d levels: %v", DefaultRecursionLimit+1, err)
	}
	o := UnmarshalOptions{RecursionLimit: 3}
	if err := o.Unmarshal(nest(3), dynamic.New(md)); err != nil {
		t.Errorf("limit 3, 3 levels: %v", err)
	}
	if err := o.Unmarshal(nest(4), dynamic.New(md)); err == nil {
		t.Error("limit 3, 4 levels accepted")
	}
	// Skipped unknown values count too.
	o.DiscardUnknown = true
	if err := o.Unmarshal([]byte(`{"x":[[[[1]]]]}`), dynamic.New(md)); err == nil {
		t.Error("deeply nested unknown value accepted")
	}
}

func TestAnyWithoutResolver(t *testing.T) {
	md := msgType(t, "google.protobuf.Any")
	m := dynamic.New(md)
	if err := Unmarshal([]byte(`{}`), m); err != nil {
		t.Errorf("empty Any: %v", err)
	}
	if err := Unmarshal([]byte(`{"@type":"type.googleapis.com/jsontest.Nested"}`), m); err == nil {
		t.Error("Any resolved without a resolver")
	}
	must(t, m.Set(field(t, md, "type_url"), "type.googleapis.com/jsontest.Nested"))
	if _, err := Marshal(m); err == nil {
		t.Error("Any marshalled without a resolver")
	}
	// Extensions are unknown fields without a resolver.
	p2 := dynamic.New(msgType(t, "jsontest2.P2"))
	if err := Unmarshal([]byte(`{"[jsontest2.ext_int32]":1}`), p2); err == nil {
		t.Error("extension resolved without a resolver")
	}
}

func TestAnyBinaryErrorWraps(t *testing.T) {
	p := pool(t)
	md := msgType(t, "google.protobuf.Any")
	m := dynamic.New(md)
	must(t, m.Set(field(t, md, "type_url"), "type.googleapis.com/jsontest.Nested"))
	must(t, m.Set(field(t, md, "value"), []byte{0x0a, 0x05}))
	_, err := MarshalOptions{Resolver: p}.Marshal(m)
	var de *dynamic.Error
	if !errors.As(err, &de) {
		t.Errorf("error %v does not wrap the binary decoding error", err)
	}
}

func TestMessageSet(t *testing.T) {
	p := linkSource(t, `syntax = "proto2";
package ms;
message Set {
  option message_set_wire_format = true;
  extensions 4 to max;
}
message Holder { optional Set set = 1; }
`, false)
	md := p.FindMessage("ms.Holder")
	m := dynamic.New(md)
	if err := Unmarshal([]byte(`{"set":{}}`), m); err == nil || !strings.Contains(err.Error(), "message sets") {
		t.Errorf("unmarshal: %v", err)
	}
	m.Mutable(field(t, md, "set"))
	if _, err := Marshal(m); err == nil || !strings.Contains(err.Error(), "message sets") {
		t.Errorf("marshal: %v", err)
	}
}

// TestBrokenSchemas: unresolved field types and redefined well-known types
// are errors, never panics.
func TestBrokenSchemas(t *testing.T) {
	p := linkSource(t, `syntax = "proto3";
package broken;
message M {
  int32 ok = 1;
  Missing bad = 2;
}
`, false)
	md := p.FindMessage("broken.M")
	if err := Unmarshal([]byte(`{"bad":{}}`), dynamic.New(md)); err == nil {
		t.Error("field with unresolved type accepted")
	}
	if b, err := Marshal(dynamic.New(md)); err != nil || string(b) != "{}" {
		t.Errorf("unset unresolved field: %s, %v", b, err)
	}
	if _, err := (MarshalOptions{EmitUnpopulated: true}).Marshal(dynamic.New(md)); err == nil {
		t.Error("EmitUnpopulated with an unresolved field succeeded")
	}

	wk := linkSource(t, `syntax = "proto3";
package google.protobuf;
message Timestamp { string seconds = 1; }
message Holder { Timestamp ts = 1; }
`, true)
	hd := wk.FindMessage("google.protobuf.Holder")
	h := dynamic.New(hd)
	if err := Unmarshal([]byte(`{"ts":"2020-01-01T00:00:00Z"}`), h); err == nil || !strings.Contains(err.Error(), "well-known") {
		t.Errorf("unmarshal redefined Timestamp: %v", err)
	}
	h.Mutable(field(t, hd, "ts"))
	if _, err := Marshal(h); err == nil || !strings.Contains(err.Error(), "well-known") {
		t.Errorf("marshal redefined Timestamp: %v", err)
	}
}

// TestExtensionFromOtherPool: an extension must extend this descriptor, not
// a same-named message of another pool.
func TestExtensionFromOtherPool(t *testing.T) {
	other := linkSource(t, `syntax = "proto2";
package jsontest2;
message P2 { extensions 1 to 9; }
extend P2 { optional int32 ext_int32 = 1; }
`, false)
	m := dynamic.New(msgType(t, "jsontest2.P2"))
	err := (UnmarshalOptions{Resolver: other}).Unmarshal([]byte(`{"[jsontest2.ext_int32]":1}`), m)
	if err == nil || !strings.Contains(err.Error(), "cannot be extended") {
		t.Errorf("got %v", err)
	}
}

func TestUnmarshalResets(t *testing.T) {
	md := msgType(t, "jsontest.Scalars")
	m := dynamic.New(md)
	must(t, Unmarshal([]byte(`{"fInt32":5}`), m))
	must(t, Unmarshal([]byte(`{"fBool":true}`), m))
	if m.Has(field(t, md, "f_int32")) {
		t.Error("Unmarshal did not reset the message")
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
