package protojson

import (
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/grpc/dynamic"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/desc"
)

// fuzzTypes are the message types FuzzUnmarshal parses into, chosen by the
// first input byte.
var fuzzTypes = []string{
	"jsontest.Scalars", "jsontest.Names", "jsontest.Repeateds", "jsontest.Maps", "jsontest.Oneofs",
	"jsontest.Optionals", "jsontest.WellKnown", "jsontest.Nested", "jsontest2.P2", "jsontest2.Holder",
	"jsonedition.Editions", "google.protobuf.Value", "google.protobuf.Any", "google.protobuf.Timestamp",
	"google.protobuf.Duration", "google.protobuf.FieldMask", "google.protobuf.Struct", "google.rpc.Status",
}

// FuzzUnmarshal parses arbitrary input into rich message types (with and
// without DiscardUnknown and AllowPartial, chosen by the first byte) and
// checks that parsing never panics and, when the input is accepted, that
// its JSON output parses again and converts to the same JSON after one
// normalization (a NullValue number other than 0 is written as null and then
// disappears), for compact and indented output with every option.
//
//	go test ./internal/core/grpc/protojson -run '^$' -fuzz FuzzUnmarshal -fuzztime 60s
func FuzzUnmarshal(f *testing.F) {
	for _, g := range loadGolden(f) {
		in, ok := g.input(f)
		if !ok {
			continue
		}
		for i, tn := range fuzzTypes {
			if tn == g.Type {
				sel := byte(i)
				if g.DiscardUnknown {
					sel |= 0x40
				}
				if g.AllowPartial {
					sel |= 0x80
				}
				f.Add(append([]byte{sel}, in...))
			}
		}
	}
	p := pool(f)
	f.Fuzz(func(t *testing.T, in []byte) {
		if len(in) == 0 {
			return
		}
		sel := in[0]
		md := p.FindMessage(fuzzTypes[int(sel&0x3f)%len(fuzzTypes)])
		partial := sel&0x80 != 0
		uo := UnmarshalOptions{Resolver: p, DiscardUnknown: sel&0x40 != 0, AllowPartial: partial}
		m := dynamic.New(md)
		if err := uo.Unmarshal(in[1:], m); err != nil {
			if _, ok := err.(*Error); !ok {
				t.Fatalf("error %T is not *Error: %v", err, err)
			}
			return
		}
		strict := UnmarshalOptions{Resolver: p, AllowPartial: partial}
		for _, o := range []MarshalOptions{
			{},
			{Indent: "\t", UseProtoNames: true, UseEnumNumbers: true},
			{EmitUnpopulated: true},
			{EmitDefaultValues: true, Indent: " "},
		} {
			o.Resolver, o.AllowPartial = p, partial
			out1, err := o.Marshal(m)
			if err != nil {
				t.Fatalf("marshal of accepted input: %v", err)
			}
			out2 := reparse(t, strict, o, md, out1)
			out3 := reparse(t, strict, o, md, out2)
			if string(out3) != string(out2) {
				t.Fatalf("JSON not stable\nfirst  %s\nsecond %s\nthird  %s", out1, out2, out3)
			}
		}
	})
}

// reparse parses JSON, encodes and decodes the binary form, and converts it
// back to JSON.
func reparse(t *testing.T, uo UnmarshalOptions, o MarshalOptions, md *desc.Message, js []byte) []byte {
	t.Helper()
	m := dynamic.New(md)
	if err := uo.Unmarshal(js, m); err != nil {
		t.Fatalf("output does not parse: %v\n%s", err, js)
	}
	b, err := dynamic.MarshalOptions{Deterministic: true}.Marshal(m)
	if err != nil {
		t.Fatalf("binary encoding: %v", err)
	}
	m2 := dynamic.New(m.Descriptor())
	if err := (dynamic.UnmarshalOptions{Resolver: uo.Resolver}).Unmarshal(b, m2); err != nil {
		t.Fatalf("binary decoding: %v", err)
	}
	out, err := o.Marshal(m2)
	if err != nil {
		t.Fatalf("marshal: %v\n%s", err, js)
	}
	return out
}
