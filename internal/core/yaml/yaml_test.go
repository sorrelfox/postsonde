package yaml

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

func TestToJSON(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		// Block collections.
		{"mapping", "a: 1\nb: two\n", `{"a":1,"b":"two"}`},
		{"key order kept", "z: 1\na: 2\nm: 3\n", `{"z":1,"a":2,"m":3}`},
		{"nested mapping", "a:\n  b:\n    c: 1\n  d: 2\ne: 3\n", `{"a":{"b":{"c":1},"d":2},"e":3}`},
		{"sequence", "- a\n- b\n", `["a","b"]`},
		{"sequence at key indentation", "key:\n- a\n- b\nnext: 3\n", `{"key":["a","b"],"next":3}`},
		{"compact mapping in sequence", "- a: 1\n  b:\n    - x\n  c: 2\n", `[{"a":1,"b":["x"],"c":2}]`},
		{"compact nested sequence", "- - a\n  - b\n- - c\n", `[["a","b"],["c"]]`},
		{"compact with sequence at key indentation", "- key: value\n  list:\n  - one\n  - two\n  other: x\n", `[{"key":"value","list":["one","two"],"other":"x"}]`},
		{"empty values", "empty:\nalso: \nlist:\n- \n- x\n", `{"empty":null,"also":null,"list":[null,"x"]}`},
		{"value on next line", "key:    \n  value on next line\n", `{"key":"value on next line"}`},
		{"blank lines between keys", "top: 1\n\n\n\nbottom: 2\n", `{"top":1,"bottom":2}`},
		{"quoted keys", "\"quoted key\": 1\n'single': 2\n'200':\n  description: OK\n", `{"quoted key":1,"single":2,"200":{"description":"OK"}}`},
		{"keys with spaces", "key with spaces: value with spaces\n", `{"key with spaces":"value with spaces"}`},
		{"scalar keys become strings", "200: ok\ntrue: t\n~: n\n1.50: f\n0x10: h\n", `{"200":"ok","true":"t","null":"n","1.50":"f","16":"h"}`},

		// Flow collections.
		{"flow", "{a: 1, b: [2, 3], \"c\": {d: e}}\n", `{"a":1,"b":[2,3],"c":{"d":"e"}}`},
		{"json", `{"a":1,"b":[true,false,null],"c":"x"}`, `{"a":1,"b":[true,false,null],"c":"x"}`},
		{"flow pairs in sequence", "[a, b, c: d, {e: f}]\n", `["a","b",{"c":"d"},{"e":"f"}]`},
		{"flow over lines", "- [a,\n   b,\n   c]\n- {x: 1,\n   y: 2}\n", `[["a","b","c"],{"x":1,"y":2}]`},
		{"flow trailing comma", "k: [a, 'b', \"c\", ]\n", `{"k":["a","b","c"]}`},
		{"flow empty", "a: [ ]\nb: { }\nc: []\n", `{"a":[],"b":{},"c":[]}`},
		{"flow comment", "{a: 1, # c\n b: 2}\n", `{"a":1,"b":2}`},
		{"flow key without value", "{a, b: 1}", `{"a":null,"b":1}`},
		{"flow empty value", "a: {b: }\nc: [d: , e]\n", `{"a":{"b":null},"c":[{"d":null},"e"]}`},
		{"flow multi-line plain", "[a b\n c, d]\n", `["a b c","d"]`},
		{"flow colon in plain", "[http://x:80/a, a:b]", `["http://x:80/a","a:b"]`},

		// Plain scalars.
		{"core schema", "n: ~\nm: null\nt: true\nf: FALSE\ni: 0x1F\no: 0o17\nfl: 1.5e3\nneg: -12\nstr: 1.2.3\nlead: 010\n",
			`{"n":null,"m":null,"t":true,"f":false,"i":31,"o":15,"fl":1.5e3,"neg":-12,"str":"1.2.3","lead":10}`},
		{"floats", "x: -1.5\ny: .5\nz: +3\nw: 1.\nv: 1E+3\nu: 12345678901234567890123\n", `{"x":-1.5,"y":0.5,"z":3,"w":1,"v":1E+3,"u":12345678901234567890123}`},
		{"yaml 1.1 words are strings", "a: yes\nb: off\nc: 1_000\nd: 12:30\n", `{"a":"yes","b":"off","c":"1_000","d":"12:30"}`},
		{"colons and hashes", "url: http://example.com:8080/a#frag\nk: x #not: key\ns: a -- b\n", `{"url":"http://example.com:8080/a#frag","k":"x","s":"a -- b"}`},
		{"indicator-like starts", "x: :a\ny: -a\nz: ?a\n", `{"x":":a","y":"-a","z":"?a"}`},
		{"multi-line plain", "s: this is\n  a long\n\n  plain scalar\nt: x\n", `{"s":"this is a long\nplain scalar","t":"x"}`},
		{"multi-line plain in sequence", "- a\n  b\n- c\n", `["a b","c"]`},
		{"comments", "k: v # comment\n# full line\n  # indented\nk2: 'v2' # c\n", `{"k":"v","k2":"v2"}`},
		{"top-level scalar", "just text\n", `"just text"`},
		{"empty stream", "# only comments\n", `null`},

		// Quoted scalars.
		{"single quoted", "q: 'it''s\n  folded\n\n  here'\n", `{"q":"it's folded\nhere"}`},
		{"single quoted colon", "k: 'a: b'\n", `{"k":"a: b"}`},
		{"double escapes", `d: "esc \t \u00e9 \x41 \\ \" \/ \N\_ end"`, `{"d":"esc \t é A \\ \" / ` + "\u0085\u00a0" + ` end"}`},
		{"double astral", `k: "\U0001F600"`, `{"k":"😀"}`},
		{"double folding", "d: \"line one\n  line two \\\n  joined\n\n  para\"\n", `{"d":"line one line two joined\npara"}`},
		{"double escaped break with empty line", "- \"a\\\n\n  b\"\n", `["a\nb"]`},
		{"double trailing spaces dropped", "a: \"multi   \n\n\n  breaks\"\n", `{"a":"multi\n\nbreaks"}`},
		{"quoted multi-line value", "k:\n  'multi\n  line'\n", `{"k":"multi line"}`},

		// Block scalars.
		{"literal", "lit: |\n  line1\n    indented\n  line3\n\nnext: 1\n", `{"lit":"line1\n  indented\nline3\n","next":1}`},
		{"literal strip", "lit: |-\n  a\n  b\n\n\n", `{"lit":"a\nb"}`},
		{"literal keep", "lit: |+\n  a\n  b\n\n\nx: 1\n", `{"lit":"a\nb\n\n\n","x":1}`},
		{"literal keep at eof without break", "k: |+\n  a", `{"k":"a"}`},
		{"literal leading empty line", "a: |\n\n  x\n\n\n", `{"a":"\nx\n"}`},
		{"literal json body", "text: |-\n  {\n  \t\"json\": true\n  }\n", `{"text":"{\n\t\"json\": true\n}"}`},
		{"literal trailing spaces kept", "k: |\n  trailing   \n  x\n", `{"k":"trailing   \nx\n"}`},
		{"folded", "fold: >\n  a\n  b\n\n  c\n    more\n  d\n", `{"fold":"a b\nc\n  more\nd\n"}`},
		{"folded strip", "a: >-\n  long text\n  folded here\nb: x\n", `{"a":"long text folded here","b":"x"}`},
		{"folded spec 8.10", "k: >\n\n  folded\n  line\n\n  next\n  line\n    * bullet\n\n    * list\n    * lines\n\n  last\n  line\n\n# Comment\n",
			`{"k":"\nfolded line\nnext line\n  * bullet\n\n  * list\n  * lines\n\nlast line\n"}`},
		{"indentation indicator", "- |2\n   x\n- >1\n  y\n", `[" x\n"," y\n"]`},
		{"block scalar in sequence", "- |\n  text\n- next\n", `["text\n","next"]`},
		{"top-level block scalar", "--- |\n  x\n", `"x\n"`},

		// Anchors, aliases, merge keys, tags.
		{"alias", "a: &x [1, 2]\nb: *x\n", `{"a":[1,2],"b":[1,2]}`},
		{"anchor on own line", "k: &a\n  x: 1\nl: *a\n", `{"k":{"x":1},"l":{"x":1}}`},
		{"anchor on sequence entry", "- &a\n  x: 1\n- *a\n", `[{"x":1},{"x":1}]`},
		{"anchor on first key", "- &a x: 1\n  y: *a\n", `[{"x":1,"y":"x"}]`},
		{"anchor and tag", "k: &a !!str 1\nl: *a\nm: !!str &b 2\nn: *b\n", `{"k":"1","l":"1","m":"2","n":"2"}`},
		{"anchor redefined", "a: &x 1\nb: &x 2\nc: *x\n", `{"a":1,"b":2,"c":2}`},
		{"alias key", "base: &k name\n*k : v\n", `{"base":"name","name":"v"}`},
		{"merge", "base: &b\n  x: 1\n  y: 2\nderived:\n  <<: *b\n  y: 3\n", `{"base":{"x":1,"y":2},"derived":{"x":1,"y":3}}`},
		{"merge list", "a: &a {x: 1, y: 1}\nb: &b {y: 2, z: 2}\nc:\n  <<: [*a, *b]\n  w: 0\n", `{"a":{"x":1,"y":1},"b":{"y":2,"z":2},"c":{"x":1,"y":1,"z":2,"w":0}}`},
		{"quoted << is a key", "'<<': 1\n", `{"<<":1}`},
		{"tags", "k: !!str 123\nj: !!int '42'\nf: !!float 1\nb: !!bool 'true'\nn: !!null ''\ns: ! 12\nv: !<tag:yaml.org,2002:str> 1\n",
			`{"k":"123","j":42,"f":1,"b":true,"n":null,"s":"12","v":"1"}`},
		{"collection tags", "a: !!map\n  b: 1\nc: !!seq [1]\n", `{"a":{"b":1},"c":[1]}`},

		// Documents.
		{"explicit document", "---\na: 1\n...\n", `{"a":1}`},
		{"directive", "%YAML 1.2\n---\nx: 1\n", `{"x":1}`},
		{"document end marker", "k: v\n...\n", `{"k":"v"}`},
		{"crlf", "a: 1\r\nb: |\r\n  x\r\n  y\r\n", `{"a":1,"b":"x\ny\n"}`},
		{"bom", "\xEF\xBB\xBFa: 1\n", `{"a":1}`},
		{"html characters not escaped", "k: <a & b>\n", `{"k":"<a & b>"}`},
		{"control characters escaped", `k: "\x01\u2028"`, `{"k":"\u0001\u2028"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ToJSON([]byte(tt.in))
			if err != nil {
				t.Fatalf("ToJSON: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("got  %s\nwant %s", got, tt.want)
			}
			if !json.Valid(got) {
				t.Errorf("invalid JSON %s", got)
			}
		})
	}
}

func TestErrors(t *testing.T) {
	tests := []struct {
		name, in   string
		code       string
		line, col  int
		msgContain string
	}{
		{"sequence after key", "k: - a\n", CodeInvalidYAML, 1, 4, "block sequence cannot start"},
		{"over-indented key", "a: 1\n  b: 2\n", CodeInvalidYAML, 2, 4, "mapping values are not allowed here"},
		{"mapping on key line", "a: b: c\n", CodeInvalidYAML, 1, 4, "mapping values are not allowed here"},
		{"unexpected indentation", "a:\n    b: 1\n  c: 2\n", CodeInvalidYAML, 3, 3, "unexpected indentation"},
		{"sequence in mapping", "a: b\n- c\n", CodeInvalidYAML, 2, 1, "sequence entry is not allowed here"},
		{"mapping after sequence", "- a\nb: c\n", CodeInvalidYAML, 2, 1, "check the indentation"},
		{"unterminated flow", "a: [1, 2\n", CodeInvalidYAML, 1, 4, "unterminated flow sequence"},
		{"unterminated flow mapping", "a: {b: 1\n", CodeInvalidYAML, 1, 4, "unterminated flow mapping"},
		{"flow separator", "{a: b c: d}", CodeInvalidYAML, 1, 8, "expected ',' or '}'"},
		{"flow sequence separator", "[a, 'b' c]", CodeInvalidYAML, 1, 9, "expected ',' or ']'"},
		{"flow leading comma", "[, a]", CodeInvalidYAML, 1, 2, "unexpected ','"},
		{"extra bracket", "k: [a, b]]\n", CodeInvalidYAML, 1, 10, "unexpected content after the value"},
		{"tab indentation", "a:\n\tb: 1\n", CodeInvalidYAML, 2, 1, "tab characters"},
		{"unterminated single", "a: 'x\n", CodeInvalidYAML, 1, 4, "unterminated single-quoted"},
		{"unterminated double", "a: \"x\n\nb: 1", CodeInvalidYAML, 1, 4, "unterminated double-quoted"},
		{"quoted string cut by document marker", "a: 'x\n---\n'", CodeInvalidYAML, 1, 4, "unterminated single-quoted"},
		{"bad escape", `k: "a\zb"`, CodeInvalidYAML, 1, 6, "invalid escape"},
		{"bad unicode escape", `k: "\uD800"`, CodeInvalidYAML, 1, 5, "invalid escape"},
		{"unknown alias", "a: *nope\n", CodeInvalidYAML, 1, 4, `unknown anchor "nope"`},
		{"recursive alias", "a: &x [*x]\n", CodeInvalidYAML, 1, 8, `unknown anchor "x"`},
		{"alias without name", "a: *\n", CodeInvalidYAML, 1, 4, "needs a name"},
		{"two anchors", "&a &b x\n", CodeInvalidYAML, 1, 4, "only one anchor"},
		{"two tags", "!!str !!int x\n", CodeInvalidYAML, 1, 7, "only one tag"},
		{"alias with anchor", "a: &x 1\nb: &y *x\n", CodeInvalidYAML, 2, 4, "an alias cannot have"},
		{"duplicate key", "a: 1\nb: 2\na: 3\n", CodeInvalidYAML, 3, 1, `duplicate mapping key "a"`},
		{"duplicate key across styles", "'200': a\n200: b\n", CodeInvalidYAML, 2, 1, `duplicate mapping key "200"`},
		{"bad merge", "a: 1\nb:\n  <<: *x\n", CodeInvalidYAML, 3, 7, "unknown anchor"},
		{"merge of scalar", "a: &x 1\nb:\n  <<: *x\n", CodeInvalidYAML, 3, 7, "merge key"},
		{"bad int tag", "k: !!int abc\n", CodeInvalidYAML, 1, 4, `"abc" is not a valid !!int`},
		{"yaml 1.1 bool tag", "k: !!bool yes\n", CodeInvalidYAML, 1, 4, "not a valid !!bool"},
		{"map tag on scalar", "k: !!map x\n", CodeInvalidYAML, 1, 4, "cannot have the tag !!map"},
		{"reserved indicator", "k: @x\n", CodeInvalidYAML, 1, 4, `cannot start with '@'`},
		{"comment without space", "k: \"x\"# c\n", CodeInvalidYAML, 1, 7, "unexpected content"},
		{"leading empty lines", "k: |\n   \n  x\n", CodeInvalidYAML, 3, 1, "leading empty lines"},
		{"bad block header", "k: |x\n  a\n", CodeInvalidYAML, 1, 5, "invalid block scalar header"},
		{"zero indentation indicator", "k: |0\n  a\n", CodeInvalidYAML, 1, 5, "1 to 9"},
		{"directive without document", "%YAML 1.2\na: 1\n", CodeInvalidYAML, 2, 1, "expected '---'"},
		{"control character", "a: 1\nb: x\x01\n", CodeInvalidYAML, 2, 5, "control character U+0001"},
		{"invalid utf-8", "a: 1\nb: \xff\n", CodeInvalidYAML, 2, 4, "not valid UTF-8"},
		{"multi-line key", "'k\n  x': 1\n", CodeInvalidYAML, 2, 5, "mapping values are not allowed here"},

		{"explicit key", "? complex\n: v\n", CodeUnsupported, 1, 1, "explicit mapping keys"},
		{"explicit key in flow", "{? a: b}", CodeUnsupported, 1, 2, "explicit mapping keys"},
		{"flow collection as key", "[a, b]: c\n", CodeUnsupported, 1, 1, "collections as mapping keys"},
		{"flow collection as flow key", "{[a]: b}", CodeUnsupported, 1, 2, "collections as mapping keys"},
		{"empty key", ": v\n", CodeUnsupported, 1, 1, "empty mapping keys"},
		{"empty flow key", "{: v}", CodeUnsupported, 1, 2, "empty mapping keys"},
		{"custom tag", "k: !Ref x\n", CodeUnsupported, 1, 4, "custom tags such as !Ref"},
		{"unknown core tag", "k: !!set {a}\n", CodeUnsupported, 1, 4, "the tag !!set"},
		{"verbatim tag", "k: !<tag:example.com,2000:x> 1\n", CodeUnsupported, 1, 4, "!<tag:example.com,2000:x>"},
		{"tag directive", "%TAG ! tag:example.com,2000:\n---\na: 1\n", CodeUnsupported, 1, 1, "%TAG"},
		{"yaml 2", "%YAML 2.0\n---\na: 1\n", CodeUnsupported, 1, 1, "YAML 1.x"},
		{"several documents", "a: 1\n---\nb: 2\n", CodeUnsupported, 3, 1, "2 YAML documents"},
		{"infinity", "k: .inf\n", CodeUnsupported, 1, 4, ".inf has no JSON equivalent"},
		{"nan", "k: [.NaN]\n", CodeUnsupported, 1, 5, ".NaN has no JSON equivalent"},
		{"collection key via alias", "a: &m {x: 1}\n*m : 2\n", CodeUnsupported, 2, 1, "a mapping cannot be a key"},
		{"utf-16", "\xFF\xFEa\x00:\x00", CodeUnsupported, 0, 0, "not UTF-8"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ToJSON([]byte(tt.in))
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("got %v (%T), want *Error", err, err)
			}
			if e.Code != tt.code || e.Line != tt.line || e.Column != tt.col || !strings.Contains(e.Message, tt.msgContain) {
				t.Errorf("got %s %d:%d %q, want %s %d:%d containing %q", e.Code, e.Line, e.Column, e.Message, tt.code, tt.line, tt.col, tt.msgContain)
			}
		})
	}
}

func TestErrAndAsErr(t *testing.T) {
	_, err := Parse([]byte("a: b: c"))
	var ee *errs.Error
	if !errors.As(AsErr(err), &ee) {
		t.Fatalf("AsErr: %T", AsErr(err))
	}
	if ee.Kind != errs.Parse || ee.Code != CodeInvalidYAML || ee.Details["line"] != "1" || ee.Details["column"] != "4" {
		t.Errorf("got %+v", ee)
	}
	if !strings.Contains(ee.Message, "(line 1, column 4)") {
		t.Errorf("message %q", ee.Message)
	}
	if err.Error() != "yaml: line 1, column 4: mapping values are not allowed here" {
		t.Errorf("Error() = %q", err.Error())
	}

	_, err = Parse([]byte("k: !Ref x"))
	if errors.As(AsErr(err), &ee); ee.Kind != errs.Unsupported || ee.Code != CodeUnsupported || ee.Hint == "" {
		t.Errorf("unsupported: %+v", ee)
	}
	_, err = ParseAllLimits([]byte("aaaa"), Limits{MaxBytes: 3})
	if errors.As(AsErr(err), &ee); ee.Kind != errs.Unsupported || ee.Code != CodeTooLarge {
		t.Errorf("too large: %+v", ee)
	}
	if other := errors.New("x"); AsErr(other) != other {
		t.Error("AsErr changed a foreign error")
	}
}

func TestLimits(t *testing.T) {
	// Billion laughs: 9 levels of 9 aliases each.
	var b strings.Builder
	b.WriteString("a: &a [x, x, x, x, x, x, x, x, x]\n")
	prev := "a"
	for _, name := range []string{"b", "c", "d", "e", "f", "g", "h", "i"} {
		b.WriteString(name + ": &" + name + " [")
		for i := range 9 {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString("*" + prev)
		}
		b.WriteString("]\n")
		prev = name
	}
	n, err := Parse([]byte(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	_, err = n.JSONLimits(Limits{MaxNodes: 100_000})
	var e *Error
	if !errors.As(err, &e) || e.Code != CodeTooLarge {
		t.Fatalf("billion laughs: %v", err)
	}
	// Merge keys count too.
	_, err = ToJSON([]byte("a: &a {x: 1}\nb: &b {<<: [*a, *a]}\nc: {<<: [*b, *b]}\n"))
	if err != nil {
		t.Fatalf("small merge: %v", err)
	}

	deep := strings.Repeat("[", 50) + strings.Repeat("]", 50)
	if _, err := ParseAllLimits([]byte(deep), Limits{MaxDepth: 20}); !errors.As(err, &e) || e.Code != CodeTooLarge {
		t.Errorf("deep flow: %v", err)
	}
	var blk strings.Builder
	for i := range 30 {
		blk.WriteString(strings.Repeat("  ", i) + "k:\n")
	}
	if _, err := ParseAllLimits([]byte(blk.String()), Limits{MaxDepth: 20}); !errors.As(err, &e) || e.Code != CodeTooLarge {
		t.Errorf("deep block: %v", err)
	}
	if _, err := ParseAllLimits([]byte("[1, 2, 3, 4, 5]"), Limits{MaxNodes: 4}); !errors.As(err, &e) || e.Code != CodeTooLarge {
		t.Errorf("nodes: %v", err)
	}
	if _, err := ParseAllLimits([]byte(deep), Limits{}); err != nil {
		t.Errorf("default limits: %v", err)
	}
}

func TestTreeAndPositions(t *testing.T) {
	src := "# c\nname: Rex # dog\ntags: [a, 'b']\nbody: |\n  text\nref: &r {x: 1}\nuse: *r\n"
	n, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if n.Kind != MappingNode || n.Line != 2 || n.Column != 1 || len(n.Pairs) != 5 {
		t.Fatalf("root %+v", n)
	}
	check := func(what string, got *Node, kind Kind, style Style, tag, value string, line, col int) {
		t.Helper()
		if got == nil || got.Kind != kind || got.Style != style || got.Tag != tag || got.Value != value || got.Line != line || got.Column != col {
			t.Errorf("%s: got %+v", what, got)
		}
	}
	check("name key", n.Pairs[0].Key, ScalarNode, Plain, TagStr, "name", 2, 1)
	check("name", n.Get("name"), ScalarNode, Plain, TagStr, "Rex", 2, 7)
	check("tags", n.Get("tags"), SequenceNode, Flow, TagSeq, "", 3, 7)
	check("tags[1]", n.Get("tags").Items[1], ScalarNode, SingleQuoted, TagStr, "b", 3, 11)
	check("body", n.Get("body"), ScalarNode, Literal, TagStr, "text\n", 4, 7)
	check("ref", n.Get("ref"), MappingNode, Flow, TagMap, "", 6, 9)
	if n.Get("ref").Anchor != "r" {
		t.Error("anchor not set")
	}
	use := n.Get("use")
	check("use", use, AliasNode, Plain, "", "r", 7, 6)
	if use.Resolve() != n.Get("ref") || use.Get("x") == nil {
		t.Error("alias does not resolve to the anchored node")
	}
	if n.Get("missing") != nil || n.Get("name").Get("x") != nil {
		t.Error("Get on a missing key or a scalar")
	}
	for k, want := range map[Kind]string{ScalarNode: "scalar", MappingNode: "mapping", SequenceNode: "sequence", AliasNode: "alias", 0: "unknown"} {
		if k.String() != want {
			t.Errorf("Kind(%d) = %q", k, k.String())
		}
	}

	// Columns count characters.
	_, err = Parse([]byte("é: [1,"))
	var e *Error
	if !errors.As(err, &e) || e.Line != 1 || e.Column != 4 {
		t.Errorf("column with a multi-byte key: %v", err)
	}
}

func TestParseAll(t *testing.T) {
	docs, err := ParseAll([]byte("a: 1\n---\n- b\n--- text\n...\n---\n"))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, d := range docs {
		js, err := d.JSON()
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, string(js))
	}
	if want := []string{`{"a":1}`, `["b"]`, `"text"`, `null`}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	// Anchors do not cross documents.
	if _, err := ParseAll([]byte("a: &x 1\n---\nb: *x\n")); err == nil {
		t.Error("alias to an anchor of an earlier document")
	}
	if docs, err := ParseAll(nil); err != nil || len(docs) != 0 {
		t.Errorf("empty: %v %v", docs, err)
	}
	n, err := Parse(nil)
	if err != nil || n.Kind != ScalarNode || n.Tag != TagNull {
		t.Errorf("Parse(nil) = %+v, %v", n, err)
	}
}

func TestDecodeAndUnmarshal(t *testing.T) {
	n, err := Parse([]byte("a: 1\nb: [x, true, ~, 2.5]\n"))
	if err != nil {
		t.Fatal(err)
	}
	v, err := n.Decode()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"a": json.Number("1"), "b": []any{"x", true, nil, json.Number("2.5")}}
	if !reflect.DeepEqual(v, want) {
		t.Errorf("Decode = %#v", v)
	}
	if _, err := (&Node{Kind: ScalarNode, Tag: TagFloat, Value: ".inf"}).Decode(); err == nil {
		t.Error("Decode of .inf")
	}

	var doc struct {
		Name string `json:"name"`
		Meta struct {
			Created int64 `json:"created"`
		} `json:"meta"`
	}
	if err := Unmarshal([]byte("name: X\nmeta:\n  created: 1726412345678\n"), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Name != "X" || doc.Meta.Created != 1726412345678 {
		t.Errorf("Unmarshal = %+v", doc)
	}
	if err := Unmarshal([]byte("a: [1"), &doc); err == nil {
		t.Error("Unmarshal of invalid YAML")
	}
}

// TestRealWorldFixtures checks that real-world style documents parse to the
// same data as their JSON equivalents (produced with PyYAML set to the YAML
// 1.2 core schema; see docs/yaml.md).
func TestRealWorldFixtures(t *testing.T) {
	for _, name := range []string{"insomnia-v5", "openapi-petstore"} {
		t.Run(name, func(t *testing.T) {
			y, err := os.ReadFile(filepath.Join("testdata", name+".yaml"))
			if err != nil {
				t.Fatal(err)
			}
			j, err := os.ReadFile(filepath.Join("testdata", name+".json"))
			if err != nil {
				t.Fatal(err)
			}
			var got, want any
			if err := Unmarshal(y, &got); err != nil {
				t.Fatal(AsErr(err))
			}
			if err := json.Unmarshal(j, &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				gj, _ := json.MarshalIndent(got, "", "  ")
				t.Errorf("YAML and JSON differ; got:\n%s", gj)
			}
		})
	}
}
