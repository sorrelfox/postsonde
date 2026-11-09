package protojson

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/sorrelfox/postsonde/internal/core/grpc/dynamic"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/desc"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/linker"
)

// exampleJSON writes Example(md) as the editor gets it and checks that it
// parses back into the same type.
func exampleJSON(t *testing.T, p *desc.Pool, md *desc.Message, opts ExampleOptions) string {
	t.Helper()
	m := Example(md, opts)
	b, err := MarshalOptions{Indent: " ", EmitDefaultValues: true, AllowPartial: true, Resolver: p}.Marshal(m)
	if err != nil {
		t.Fatalf("%s: Marshal: %v", md.FullName, err)
	}
	back := dynamic.New(md)
	if err := (UnmarshalOptions{Resolver: p, AllowPartial: true}).Unmarshal(b, back); err != nil {
		t.Fatalf("%s: example does not parse: %v\n%s", md.FullName, err, b)
	}
	return string(b)
}

func TestExampleShapes(t *testing.T) {
	p := pool(t)
	cases := []struct {
		name string
		want string
	}{
		{"jsontest.Scalars", `{
 "fDouble": 0,
 "fFloat": 0,
 "fInt32": 0,
 "fInt64": "0",
 "fUint32": 0,
 "fUint64": "0",
 "fSint32": 0,
 "fSint64": "0",
 "fFixed32": 0,
 "fFixed64": "0",
 "fSfixed32": 0,
 "fSfixed64": "0",
 "fBool": false,
 "fString": "",
 "fBytes": "",
 "fEnum": "COLOR_UNSPECIFIED",
 "fMessage": {
  "a": 0,
  "s": "",
  "child": {
   "a": 0,
   "s": ""
  }
 }
}`},
		{"jsontest.Oneofs", ""},
		{"jsontest.Optionals", ""},
		{"jsontest.WellKnown", ""},
		{"jsontest.Maps", ""},
		{"jsontest.Repeateds", ""},
		{"jsontest2.P2", ""},
		{"jsontest2.Holder", ""},
	}
	for _, c := range cases {
		got := exampleJSON(t, p, msgType(t, c.name), ExampleOptions{})
		if c.want != "" && got != c.want {
			t.Errorf("%s:\n%s\nwant\n%s", c.name, got, c.want)
		}
	}

	// Spot checks of the rules.
	oneofs := exampleJSON(t, p, msgType(t, "jsontest.Oneofs"), ExampleOptions{})
	if strings.Count(oneofs, "\n \"") != 2 { // before + the first member only
		t.Errorf("Oneofs: want before and one member:\n%s", oneofs)
	}
	opt := exampleJSON(t, p, msgType(t, "jsontest.Optionals"), ExampleOptions{})
	for _, want := range []string{`"oInt32": 0`, `"oString": ""`, `"oMessage": {`, `"plainList": [`, `"plainMap": {`} {
		if !strings.Contains(opt, want) {
			t.Errorf("Optionals: missing %s:\n%s", want, opt)
		}
	}
	wk := exampleJSON(t, p, msgType(t, "jsontest.WellKnown"), ExampleOptions{})
	for _, want := range []string{
		`"any": {}`, `"timestamp": "1970-01-01T00:00:00Z"`, `"duration": "0s"`, `"struct": {}`,
		`"value": null`, `"listValue": []`, `"fieldMask": ""`, `"empty": {}`, `"int64Value": "0"`,
		`"status": {`, `"details": [`, `"mValue": {`,
	} {
		if !strings.Contains(wk, want) {
			t.Errorf("WellKnown: missing %s:\n%s", want, wk)
		}
	}
	maps := exampleJSON(t, p, msgType(t, "jsontest.Maps"), ExampleOptions{})
	for _, want := range []string{`"stringString": {`, `"": ""`, `"0": 0`, `"false": false`, `"stringMessage": {`} {
		if !strings.Contains(maps, want) {
			t.Errorf("Maps: missing %s:\n%s", want, maps)
		}
	}
	p2 := exampleJSON(t, p, msgType(t, "jsontest2.P2"), ExampleOptions{})
	for _, want := range []string{`"optString": "hello"`, `"optClosed": "ONE"`, `"optBytes": "AQ=="`, `"optionalgroup": {`, `"req": 0`} {
		if !strings.Contains(p2, want) {
			t.Errorf("P2: missing %s:\n%s", want, p2)
		}
	}
	if strings.Contains(p2, "[jsontest2.") {
		t.Errorf("P2: extensions set:\n%s", p2)
	}
}

func TestExampleRecursion(t *testing.T) {
	res, err := linker.Load(context.Background(), linker.Options{
		Files: []string{"tree.proto"},
		FS: fstest.MapFS{"tree.proto": {Data: []byte(`syntax = "proto3";
package tree;
message Node {
  string name = 1;
  repeated Node children = 2;
  Node parent = 3;
  map<string, Node> index = 4;
  Leaf leaf = 5;
}
message Leaf { Node back = 1; int32 n = 2; }
message Chain1 { Chain2 next = 1; }
message Chain2 { Chain3 next = 1; }
message Chain3 { int32 end = 1; }
`)}},
	})
	if err != nil || res.HasErrors() {
		t.Fatalf("Load: %v %v", err, res.Diagnostics)
	}
	p := res.Pool
	node := p.FindMessage("tree.Node")

	// Default: Node is expanded once more inside itself.
	got := exampleJSON(t, p, node, ExampleOptions{})
	if n := strings.Count(got, `"name"`); n != 5 {
		// top, children[0], parent, index[""], leaf.back
		t.Errorf("default recursion: %d names, want 5:\n%s", n, got)
	}
	if !strings.Contains(got, `"children": []`) || !strings.Contains(got, `"index": {}`) {
		t.Errorf("default recursion: the innermost lists and maps should be empty:\n%s", got)
	}

	// Never again: only the top level.
	got = exampleJSON(t, p, node, ExampleOptions{Recursion: -1})
	if n := strings.Count(got, `"name"`); n != 1 {
		t.Errorf("no recursion: %d names, want 1:\n%s", n, got)
	}
	if strings.Contains(got, `"parent"`) || !strings.Contains(got, `"leaf": {`) || strings.Contains(got, `"back"`) {
		t.Errorf("no recursion:\n%s", got)
	}

	// Deeper recursion grows the tree.
	got = exampleJSON(t, p, node, ExampleOptions{Recursion: 2})
	if n := strings.Count(got, `"name"`); n <= 5 {
		t.Errorf("recursion 2: %d names:\n%s", n, got)
	}

	// MaxDepth cuts non-recursive chains too.
	chain := p.FindMessage("tree.Chain1")
	if got := exampleJSON(t, p, chain, ExampleOptions{}); !strings.Contains(got, `"end": 0`) {
		t.Errorf("chain: %s", got)
	}
	if got := exampleJSON(t, p, chain, ExampleOptions{MaxDepth: 2}); strings.Contains(got, `"end"`) || !strings.Contains(got, `"next": {}`) {
		t.Errorf("chain depth 2: %s", got)
	}
}
