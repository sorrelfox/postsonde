package protojson

import (
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/grpc/dynamic"
)

//go:embed testdata/golden.json
var goldenJSON []byte

type goldenOutput struct {
	JSON  string `json:"json"`
	Error bool   `json:"error"`
}

// goldenCase is one reference case written by protobuf-go (see
// testdata/README.md).
type goldenCase struct {
	Name           string                  `json:"name"`
	Type           string                  `json:"type"`
	Input          *string                 `json:"input"`
	InputHex       string                  `json:"inputHex"`
	DiscardUnknown bool                    `json:"discardUnknown"`
	AllowPartial   bool                    `json:"allowPartial"`
	Error          bool                    `json:"error"`
	Binary         *string                 `json:"binary"`
	Outputs        map[string]goldenOutput `json:"outputs"`
}

func (g goldenCase) input(tb testing.TB) ([]byte, bool) {
	tb.Helper()
	switch {
	case g.Input != nil:
		return []byte(*g.Input), true
	case g.InputHex != "":
		b, err := hex.DecodeString(g.InputHex)
		if err != nil {
			tb.Fatal(err)
		}
		return b, true
	}
	return nil, false
}

// goldenOptions are the marshal options each output was written with.
var goldenOptions = []struct {
	name string
	o    MarshalOptions
}{
	{"default", MarshalOptions{}},
	{"protoNames", MarshalOptions{UseProtoNames: true}},
	{"enumNumbers", MarshalOptions{UseEnumNumbers: true}},
	{"emitUnpopulated", MarshalOptions{EmitUnpopulated: true}},
	{"emitDefaultValues", MarshalOptions{EmitDefaultValues: true}},
	{"indent", MarshalOptions{Indent: "  "}},
}

func loadGolden(tb testing.TB) []goldenCase {
	tb.Helper()
	var cases []goldenCase
	if err := json.Unmarshal(goldenJSON, &cases); err != nil {
		tb.Fatal(err)
	}
	return cases
}

// lossyJSON lists cases whose JSON does not survive a round trip: a
// NullValue field holding a number other than 0 is written as null, which
// parses as 0 and is then not written at all.
var lossyJSON = map[string]bool{"wkt/null_value_unknown_number": true}

// TestGolden checks every reference case written by protobuf-go's
// encoding/protojson:
//
//   - JSON input: accepted or rejected alike; when accepted, the parsed
//     message encodes (deterministically) to protobuf-go's binary bytes.
//   - Binary to JSON: the reference binary, decoded, converts to exactly
//     protobuf-go's JSON for each option set (protobuf-go's random extra
//     spaces were removed when the goldens were written), or fails alike.
//   - JSON -> binary -> JSON: our output parses, encodes and converts back
//     to the same JSON.
func TestGolden(t *testing.T) {
	cases := loadGolden(t)
	if len(cases) < 400 {
		t.Fatalf("only %d golden cases", len(cases))
	}
	p := pool(t)
	for _, g := range cases {
		t.Run(g.Name, func(t *testing.T) {
			md := msgType(t, g.Type)
			uo := UnmarshalOptions{Resolver: p, DiscardUnknown: g.DiscardUnknown, AllowPartial: g.AllowPartial}
			if in, ok := g.input(t); ok {
				m := dynamic.New(md)
				err := uo.Unmarshal(in, m)
				if g.Error {
					if err == nil {
						b, _ := MarshalOptions{Resolver: p, AllowPartial: true}.Marshal(m)
						t.Fatalf("input accepted, protobuf-go rejects it; parsed as %s", b)
					}
					if _, ok := err.(*Error); !ok {
						t.Errorf("error %T is not *Error: %v", err, err)
					}
					return
				}
				if err != nil {
					t.Fatalf("input rejected, protobuf-go accepts it: %v", err)
				}
				b, err := dynamic.MarshalOptions{Deterministic: true}.Marshal(m)
				if err != nil {
					t.Fatal(err)
				}
				if got := hex.EncodeToString(b); got != *g.Binary {
					t.Errorf("binary differs\ngot  %s\nwant %s", got, *g.Binary)
				}
			}

			bin, err := hex.DecodeString(*g.Binary)
			if err != nil {
				t.Fatal(err)
			}
			m := dynamic.New(md)
			if err := (dynamic.UnmarshalOptions{Resolver: p}).Unmarshal(bin, m); err != nil {
				t.Fatal(err)
			}
			def := g.Outputs["default"]
			for _, oo := range goldenOptions {
				want, ok := g.Outputs[oo.name]
				if !ok {
					want = def
				}
				o := oo.o
				o.Resolver = p
				o.AllowPartial = g.AllowPartial
				out, err := o.Marshal(m)
				if want.Error {
					if err == nil {
						t.Errorf("%s: marshal succeeded, protobuf-go fails: %s", oo.name, out)
					}
					continue
				}
				if err != nil {
					t.Errorf("%s: marshal failed, protobuf-go succeeds: %v", oo.name, err)
					continue
				}
				if string(out) != want.JSON {
					t.Errorf("%s: JSON differs\ngot  %s\nwant %s", oo.name, out, want.JSON)
					continue
				}
				// JSON -> binary -> JSON is stable. (The message itself may
				// change: unknown fields in Any values and unknown NullValue
				// numbers are not written, and EmitUnpopulated writes null
				// for unset Value fields, which parses as null_value.)
				back := dynamic.New(md)
				if err := (UnmarshalOptions{Resolver: p, AllowPartial: g.AllowPartial}).Unmarshal(out, back); err != nil {
					t.Errorf("%s: output does not parse: %v", oo.name, err)
					continue
				}
				bb, err := dynamic.MarshalOptions{Deterministic: true}.Marshal(back)
				if err != nil {
					t.Fatal(err)
				}
				again := dynamic.New(md)
				if err := (dynamic.UnmarshalOptions{Resolver: p}).Unmarshal(bb, again); err != nil {
					t.Fatal(err)
				}
				if lossyJSON[g.Name] {
					continue
				}
				if out2, err := o.Marshal(again); err != nil || string(out2) != string(out) {
					t.Errorf("%s: JSON -> binary -> JSON differs (%v)\ngot  %s\nwant %s", oo.name, err, out2, out)
				}
			}
		})
	}
}
