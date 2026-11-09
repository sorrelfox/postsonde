package parser

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/graphql/ast"
	"github.com/sorrelfox/postsonde/internal/core/graphql/lexer"
	"github.com/sorrelfox/postsonde/internal/testutil/golden"
	"github.com/sorrelfox/postsonde/internal/testutil/testenv"
)

// render is the golden form of a parse: the source, the tree and the errors
// with their line:column. Carriage returns and byte order marks in the
// source are escaped so that git's line ending conversion cannot touch them.
func render(src string) string {
	doc, errs := Parse(src)
	var b strings.Builder
	shown := strings.NewReplacer("\r", `\r`, "\uFEFF", `\uFEFF`).Replace(src)
	fmt.Fprintf(&b, "# source\n%s\n# tree\n%s# errors\n", shown, ast.Dump(doc))
	for _, e := range errs {
		fmt.Fprintf(&b, "%v [%d:%d] %s: %s\n", doc.Position(e.Start), e.Start, e.End, e.Code, e.Message)
	}
	return b.String()
}

func codes(errs []Error) []Code {
	var cs []Code
	for _, e := range errs {
		cs = append(cs, e.Code)
	}
	return cs
}

// Documents from the GraphQL spec's executable grammar and examples
// (sections 2.3 to 2.12), plus corner cases of names and ignored tokens.
var validDocuments = []struct{ name, src string }{
	{"shorthand", `{ user(id: 4) { name } }`},
	{"named query with variables", `query getZuckProfile($devicePicSize: Int = 60) {
  user(id: 4) {
    id
    name
    profilePic(size: $devicePicSize)
  }
}`},
	{"mutation", `mutation { likeStory(storyID: 12345) { story { likeCount } } }`},
	{"subscription", `subscription StoryLikeSubscription($input: StoryLikeSubscribeInput) {
  storyLikeSubscribe(input: $input) { story { likers { count } } }
}`},
	{"aliases", `{ user(id: 4) { id name smallPic: profilePic(size: 64) bigPic: profilePic(size: 1024) } }`},
	{"fragments", `query withNestedFragments {
  user(id: 4) {
    friends(first: 10) { ...friendFields }
    mutualFriends(first: 10) { ...friendFields }
  }
}

fragment friendFields on User {
  id
  name
  ...standardProfilePic
}

fragment standardProfilePic on User @dir {
  profilePic(size: 50)
}`},
	{"inline fragments", `query inlineFragmentTyping {
  profiles(handles: ["zuck", "coca-cola"]) {
    handle
    ... on User { friends { count } }
    ... on Page @include(if: true) { likers { count } }
  }
}

query inlineFragmentNoType($expandedInfo: Boolean) {
  user(handle: "zuck") {
    id
    ... @include(if: $expandedInfo) { firstName lastName }
    ... { birthday }
  }
}`},
	{"values", `{
  f(
    int: -12, zero: 0, float: 1.5e-3, str: "a\né", block: """
      two
        lines
    """,
    yes: true, no: false, nothing: null, enum: RED, variable: $v,
    list: [1, "two", [3], []], object: {a: 1, b: {c: [null]}}, empty: {}
  )
}`},
	{"variable definitions", `query Q(
  $a: [[Int!]]!
  $b: String = "x" @deprecated(reason: "no")
  $c: Input = {a: [1, {b: null}], e: ENUM}
  $d: Boolean! = false @x @y
) @operationDirective(v: $a) { f }`},
	{"directives everywhere", `query Q @a(x: 1) { f @b @c(y: $v) { ...S @d } ... on T @e { g } } fragment S on T @f(z: [$v]) { h }`},
	{"keywords as names", `{ query mutation subscription fragment on true: null type: schema(input: enum) ... on on { on } ... query }`},
	{"ignored tokens", "\xef\xbb\xbf# leading comment\n{,,a,,b # trailing\r\n,c\r}\n"},
	{"multiple anonymous and named", `{ a } query B { b } mutation C { c } subscription D { d }`},
}

func TestValidDocuments(t *testing.T) {
	t.Parallel()

	for _, tt := range validDocuments {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			doc, errs := Parse(tt.src)
			if len(errs) != 0 {
				t.Fatalf("Parse(%q) errors: %v", tt.src, errs)
			}
			ast.Inspect(doc, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.Name:
					if n.Missing() {
						t.Errorf("missing name at %d in a valid document", n.Start)
					}
				case *ast.InvalidValue:
					t.Errorf("invalid value at %d in a valid document", n.Start)
				}
				return true
			})
			golden.Assert(t, "valid/"+strings.ReplaceAll(tt.name, " ", "_"), []byte(render(tt.src)))
		})
	}
}

func TestValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		src  string
		want ast.Value
	}{
		{"-12", &ast.IntValue{Loc: ast.Loc{Start: 7, End: 10}, Raw: "-12"}},
		{"6.02e23", &ast.FloatValue{Loc: ast.Loc{Start: 7, End: 14}, Raw: "6.02e23"}},
		{`"a\tb"`, &ast.StringValue{Loc: ast.Loc{Start: 7, End: 13}, Value: "a\tb"}},
		{`"""  x\n  y"""`, &ast.StringValue{Loc: ast.Loc{Start: 7, End: 21}, Block: true, Value: `  x\n  y`}},
		{"true", &ast.BooleanValue{Loc: ast.Loc{Start: 7, End: 11}, Value: true}},
		{"false", &ast.BooleanValue{Loc: ast.Loc{Start: 7, End: 12}}},
		{"null", &ast.NullValue{Loc: ast.Loc{Start: 7, End: 11}}},
		{"ENUM_1", &ast.EnumValue{Loc: ast.Loc{Start: 7, End: 13}, Value: "ENUM_1"}},
		{"$var", &ast.Variable{Loc: ast.Loc{Start: 7, End: 11}, Dollar: 7, Name: &ast.Name{Loc: ast.Loc{Start: 8, End: 11}, Value: "var"}}},
		{"[]", &ast.ListValue{Loc: ast.Loc{Start: 7, End: 9}, LBracket: 7, RBracket: 8}},
		{"{}", &ast.ObjectValue{Loc: ast.Loc{Start: 7, End: 9}, LBrace: 7, RBrace: 8}},
	}
	for _, tt := range tests {
		src := "{ f(a: " + tt.src + ") }"
		doc, errs := Parse(src)
		if len(errs) != 0 {
			t.Errorf("Parse(%q) errors: %v", src, errs)
			continue
		}
		got := doc.Definitions[0].(*ast.OperationDefinition).SelectionSet.Selections[0].(*ast.Field).Arguments.Arguments[0].Value
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("value of %q = %s, want %s", tt.src, ast.Dump(got), ast.Dump(tt.want))
		}
	}
}

func TestTypes(t *testing.T) {
	t.Parallel()

	src := "query($a: [[Int!]]!) { f }"
	doc, errs := Parse(src)
	if len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	typ := doc.Definitions[0].(*ast.OperationDefinition).VariableDefinitions.Definitions[0].Type
	var shape []string
	for typ != nil {
		switch n := typ.(type) {
		case *ast.NonNullType:
			shape, typ = append(shape, "!"), n.Type
		case *ast.ListType:
			shape, typ = append(shape, "[]"), n.Type
		case *ast.NamedType:
			shape, typ = append(shape, n.Name.Value), nil
		}
	}
	if want := []string{"!", "[]", "[]", "!", "Int"}; !slices.Equal(shape, want) {
		t.Errorf("type shape = %v, want %v", shape, want)
	}
}

// Half-typed documents: what the editor sees while the user types. The
// golden files hold the partial tree and the errors.
var halfTyped = []struct {
	name, src string
	want      []Code
}{
	{"open brace", "{", []Code{ErrUnclosed, ErrUnexpectedToken}},
	{"empty selection set", "{ user { } }", []Code{ErrUnexpectedToken}},
	{"empty document braces", "{}", []Code{ErrUnexpectedToken}},
	{"field then eof", "{ user", []Code{ErrUnclosed}},
	{"open arguments", "{ user(", []Code{ErrUnclosed, ErrUnclosed, ErrUnexpectedToken}},
	{"argument name", "{ user(id", []Code{ErrUnclosed, ErrUnclosed, ErrUnexpectedToken}},
	{"argument colon", "{ user(id: ", []Code{ErrUnclosed, ErrUnclosed, ErrUnexpectedToken}},
	{"argument colon before close", "{ user(id: ) { name } }", []Code{ErrUnexpectedToken}},
	{"empty arguments", "{ user() { name } }", []Code{ErrUnexpectedToken}},
	{"argument missing colon", "{ user(id 4) { name } }", []Code{ErrUnexpectedToken}},
	{"arguments closed by brace", "{ user(id: 4 { name } }", []Code{ErrUnclosed}},
	{"alias colon", "{ pic: }", []Code{ErrUnexpectedToken}},
	{"query keyword", "query", []Code{ErrUnexpectedToken}},
	{"query name", "query Q", []Code{ErrUnexpectedToken}},
	{"query open variables", "query Q(", []Code{ErrUnclosed, ErrUnexpectedToken}},
	{"variable dollar", "query Q($", []Code{ErrUnclosed, ErrUnexpectedToken}},
	{"variable colon", "query Q($id: ) { a }", []Code{ErrUnexpectedToken}},
	{"variable missing dollar", "query Q(id: ID!) { a }", []Code{ErrUnexpectedToken}},
	{"variable default", "query Q($id: ID = ) { a }", []Code{ErrUnexpectedToken}},
	{"variable list type", "query Q($ids: [ID) { a }", []Code{ErrUnclosed}},
	{"spread", "{ ...", []Code{ErrUnclosed, ErrUnexpectedToken}},
	{"spread before brace", "{ ... }", []Code{ErrUnexpectedToken}},
	{"inline fragment on", "{ ... on ", []Code{ErrUnclosed, ErrUnexpectedToken}},
	{"inline fragment type", "{ ... on User }", []Code{ErrUnexpectedToken}},
	{"fragment keyword", "fragment", []Code{ErrUnexpectedToken}},
	{"fragment name", "fragment F", []Code{ErrUnexpectedToken}},
	{"fragment on", "fragment F on", []Code{ErrUnexpectedToken}},
	{"fragment missing name", "fragment on User { id }", []Code{ErrUnexpectedToken}},
	{"fragment missing on", "fragment F User { id }", []Code{ErrUnexpectedToken}},
	{"trailing at", "{ a @", []Code{ErrUnclosed, ErrUnexpectedToken}},
	{"directive arguments", "{ a @include(if: ", []Code{ErrUnclosed, ErrUnclosed, ErrUnexpectedToken}},
	{"unterminated string", "{ user(name: \"zu\n) { id } }", []Code{ErrLexical}},
	{"unterminated block string", "{ user(bio: \"\"\"abc) { id } }", []Code{ErrUnclosed, ErrUnclosed, ErrLexical}},
	{"open list value", "{ f(a: [1, 2 }", []Code{ErrUnclosed, ErrUnclosed}},
	{"open object value", "{ f(a: {b: 1, c: ) }", []Code{ErrUnclosed, ErrUnexpectedToken}},
	{"object field colon", "{ f(a: {b }) }", []Code{ErrUnexpectedToken}},
	{"nested unclosed before next operation", "query A {\n  a {\n    b\n\nquery B {\n  c\n}\n", []Code{ErrUnclosed, ErrUnclosed}},
	{"unclosed before fragment", "{\n  a(x: \n\nfragment F on T { b }", []Code{ErrUnclosed, ErrUnclosed, ErrUnexpectedToken}},
	{"stray tokens", "{ a ) ] : b } }", []Code{ErrUnexpectedToken, ErrUnexpectedToken, ErrUnexpectedToken, ErrUnexpectedToken}},
}

func TestHalfTyped(t *testing.T) {
	t.Parallel()

	for _, tt := range halfTyped {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, errs := Parse(tt.src)
			if got := codes(errs); !slices.Equal(got, tt.want) {
				t.Errorf("Parse(%q) error codes = %v, want %v\nerrors: %v", tt.src, got, tt.want, errs)
			}
			golden.Assert(t, "partial/"+strings.ReplaceAll(tt.name, " ", "_"), []byte(render(tt.src)))
		})
	}
}

// TestCursorContext checks the innermost nodes at a cursor ("|") in
// half-typed documents, as the completion engine will see them.
func TestCursorContext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		src  string
		want string // innermost node types, outermost first, after the SelectionSet/definition prefix
		// check, if set, inspects the path.
		check func(t *testing.T, path []ast.Node)
	}{
		{src: "{ | }", want: "SelectionSet Field Name"},
		{src: "{ user { | } }", want: "SelectionSet Field SelectionSet Field Name"},
		{src: "{ user { name } |", want: "SelectionSet"},
		{src: "{ us|", want: "SelectionSet Field Name"},
		{src: "{ user(|", want: "SelectionSet Field ArgumentList"},
		{src: "{ user(| ) }", want: "SelectionSet Field ArgumentList"},
		{src: "{ user(id: |", want: "Field ArgumentList Argument InvalidValue"},
		{src: "{ user(id: | ) { name } }", want: "ArgumentList Argument InvalidValue"},
		{src: "{ user(id: 4, |", want: "SelectionSet Field ArgumentList"},
		{src: "{ user(filter: {|", want: "ArgumentList Argument ObjectValue"},
		{src: "{ user(filter: {name: |", want: "Argument ObjectValue ObjectField InvalidValue"},
		{src: "{ user(ids: [1, |", want: "ArgumentList Argument ListValue"},
		{src: "{ user @|", want: "SelectionSet Field Directive Name"},
		{src: "{ user @include(|", want: "Field Directive ArgumentList"},
		{src: "{ ...|", want: "SelectionSet FragmentSpread Name"},
		{src: "{ ...Fr|", want: "SelectionSet FragmentSpread Name"},
		{src: "{ ... on |", want: "InlineFragment TypeCondition NamedType Name"},
		{src: "{ ... on User { | } }", want: "InlineFragment SelectionSet Field Name"},
		{src: "fragment F on |", want: "FragmentDefinition TypeCondition NamedType Name"},
		{src: "query Q($id: |", want: "VariableDefinitionList VariableDefinition NamedType Name"},
		{src: "query Q($|", want: "VariableDefinition Variable Name"},
		{src: "query Q($id: ID = |", want: "VariableDefinitionList VariableDefinition InvalidValue"},
		{src: "query Q(| ) { a }", want: "OperationDefinition VariableDefinitionList", check: func(t *testing.T, path []ast.Node) {
			l := path[len(path)-1].(*ast.VariableDefinitionList)
			if l.LParen != 7 || l.RParen != 9 {
				t.Errorf("parens = %d, %d", l.LParen, l.RParen)
			}
		}},
		{src: "{ a(x: 1)| }", want: "SelectionSet Field ArgumentList", check: func(t *testing.T, path []ast.Node) {
			if l := path[len(path)-1].(*ast.ArgumentList); l.RParen+1 != 9 {
				t.Errorf("cursor should be right after RParen, got RParen=%d", l.RParen)
			}
		}},
	}
	for _, tt := range tests {
		off := strings.IndexByte(tt.src, '|')
		src := tt.src[:off] + tt.src[off+1:]
		doc, _ := Parse(src)
		path := ast.NodePathAt(doc, off)
		var names []string
		for _, n := range path {
			names = append(names, reflect.TypeOf(n).Elem().Name())
		}
		if got := strings.Join(names, " "); !strings.HasSuffix(got, tt.want) {
			t.Errorf("%q: path = %s, want suffix %s", tt.src, got, tt.want)
			continue
		}
		if tt.check != nil {
			tt.check(t, path)
		}
	}
}

func TestErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		src  string
		want []string // "code [start:end] message"
	}{
		{"type Query { a: Int }\n{ a }", []string{`not executable [0:21] "type" definition is not allowed in an executable document`}},
		{`"""desc""" scalar Date @specifiedBy(url: "x") query { a }`,
			[]string{`not executable [0:45] "scalar" definition is not allowed in an executable document`}},
		{"extend type A @k { b: B }\nunion U = A | B\nschema { query: Q }\ndirective @d(a: I = {x: 1}) on FIELD | QUERY\n{ a }",
			[]string{
				`not executable [0:25] "extend" definition is not allowed in an executable document`,
				`not executable [26:41] "union" definition is not allowed in an executable document`,
				`not executable [42:61] "schema" definition is not allowed in an executable document`,
				`not executable [62:106] "directive" definition is not allowed in an executable document`,
			}},
		{"query Q($a: Int = $b @d(x: $c)) { f(x: $a) }", []string{
			"variable in constant value [18:20] variable not allowed in a constant value",
			"variable in constant value [27:29] variable not allowed in a constant value",
		}},
		{"fragment on on T { a }", []string{`reserved name [9:11] a fragment cannot be named "on"`}},
		{"{ a(x: 01) }", []string{"lexical error [7:9] invalid number: unexpected digit after 0"}},
		{"{ a ? b }", []string{`lexical error [4:5] unexpected character '?' (U+003F)`}},
		{"foo bar { a }", []string{`unexpected token [0:7] expected query, mutation, subscription, fragment or "{", found name "foo"`}},
		{"} { a }", []string{`unexpected token [0:1] expected query, mutation, subscription, fragment or "{", found "}"`}},
		{"{ a(x: 1 }", []string{`unclosed delimiter [3:4] "(" has no matching ")"`}},
		{"query ($a: [Int) { a }", []string{`unclosed delimiter [11:12] "[" has no matching "]"`}},
	}
	for _, tt := range tests {
		_, errs := Parse(tt.src)
		var got []string
		for _, e := range errs {
			got = append(got, fmt.Sprintf("%v [%d:%d] %s", e.Code, e.Start, e.End, e.Message))
		}
		if !slices.Equal(got, tt.want) {
			t.Errorf("Parse(%q) errors:\n  %s\nwant:\n  %s", tt.src, strings.Join(got, "\n  "), strings.Join(tt.want, "\n  "))
		}
	}

	if _, errs := Parse("{ a(x: 01) }"); errs[0].LexCode != lexer.ErrInvalidNumber {
		t.Errorf("LexCode = %v, want %v", errs[0].LexCode, lexer.ErrInvalidNumber)
	}
	e := Error{Code: ErrUnclosed, Start: 3, Message: "boom"}
	if got := e.Error(); got != "graphql: boom at byte 3" {
		t.Errorf("Error() = %q", got)
	}
	if got := Code(200).String(); got != "Code(200)" {
		t.Errorf("Code(200).String() = %q", got)
	}
}

func TestDeepNesting(t *testing.T) {
	t.Parallel()

	const n = 100_000
	tests := map[string]string{
		"selection sets":          strings.Repeat("{a", n) + strings.Repeat("}", n),
		"unclosed selection sets": strings.Repeat("{a", n),
		"lists":                   "{ f(a: " + strings.Repeat("[", n) + strings.Repeat("]", n) + ") }",
		"objects":                 "{ f(a: " + strings.Repeat("{a: ", n) + strings.Repeat("}", n) + ") }",
		"list types":              "query($a: " + strings.Repeat("[", n) + "Int" + strings.Repeat("]", n) + ") { a }",
		"unclosed mixed values":   "{ f(a: " + strings.Repeat("[{a: ", n),
	}
	// Braces that open no selection set are just errors, bounded by MaxErrors.
	if _, errs := Parse(strings.Repeat("{", n)); len(errs) != DefaultMaxErrors {
		t.Errorf("%d braces: %d errors", n, len(errs))
	}

	for name, src := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			start := time.Now()
			doc, errs := Parse(src)
			if !slices.Contains(codes(errs), ErrTooDeep) {
				t.Errorf("no ErrTooDeep in %v", codes(errs))
			}
			depth := maxDepth(doc)
			if depth > 4*DefaultMaxDepth+10 {
				t.Errorf("tree depth %d exceeds the limit", depth)
			}
			if d := time.Since(start); d > testenv.Budget(5*time.Second) {
				t.Errorf("took %v", d)
			}
		})
	}

	// Within the limit nothing is skipped.
	src := strings.Repeat("{a", 50) + strings.Repeat("}", 50)
	if _, errs := (Config{MaxDepth: 50}).Parse(src); len(errs) != 0 {
		t.Errorf("depth 50 with MaxDepth 50: %v", errs)
	}
	if _, errs := (Config{MaxDepth: 49}).Parse(src); !slices.Equal(codes(errs), []Code{ErrTooDeep}) {
		t.Errorf("depth 50 with MaxDepth 49: %v", errs)
	}
}

func maxDepth(n ast.Node) int {
	d := 0
	for _, c := range ast.Children(n) {
		d = max(d, maxDepth(c))
	}
	return d + 1
}

func TestMaxErrors(t *testing.T) {
	t.Parallel()

	src := "{ " + strings.Repeat(") ", 1000) + "}"
	_, errs := (Config{MaxErrors: 10}).Parse(src)
	if len(errs) != 10 || errs[9].Code != ErrTooManyErrors {
		t.Fatalf("got %d errors, last %v", len(errs), errs[len(errs)-1])
	}
	if slices.Contains(codes(errs[:9]), ErrTooManyErrors) {
		t.Errorf("ErrTooManyErrors before the end: %v", codes(errs))
	}
	_, errs = Parse(src)
	if len(errs) != DefaultMaxErrors {
		t.Errorf("default: %d errors, want %d", len(errs), DefaultMaxErrors)
	}
}

func TestLinearTime(t *testing.T) {
	t.Parallel()

	// Garbage that exercises recovery everywhere must still parse quickly.
	unit := "query Q($a: [Int = { f(x: [1, {y: $z ) @d( ... on ] fragment } type T { a } "
	src := strings.Repeat(unit, 5000)
	start := time.Now()
	doc, _ := Parse(src)
	if d := time.Since(start); d > testenv.Budget(5*time.Second) {
		t.Errorf("took %v for %d bytes", d, len(src))
	}
	if len(doc.Definitions) == 0 {
		t.Errorf("no definitions")
	}
}
