package complete

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/graphql/lexer"
	"github.com/sorrelfox/postsonde/internal/core/graphql/schema"
)

const (
	github    = "github-subset.json"
	countries = "countries-full.json"
	swapi     = "swapi-conservative.json"
	noSchema  = "none"
)

var (
	fixtureMu    sync.Mutex
	fixtureCache = map[string]*schema.Schema{}
)

// loadSchema parses a fixture from the schema package's testdata. Schemas
// are shared and must not be modified; use parseFixture for a private copy.
func loadSchema(t testing.TB, name string) *schema.Schema {
	t.Helper()
	if name == noSchema {
		return nil
	}
	fixtureMu.Lock()
	defer fixtureMu.Unlock()
	if s, ok := fixtureCache[name]; ok {
		return s
	}
	s := parseFixture(t, name)
	fixtureCache[name] = s
	return s
}

func parseFixture(t testing.TB, name string) *schema.Schema {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "schema", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	s, err := schema.ParseIntrospection(data)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return s
}

// cursor splits a source with one "|" cursor marker.
func cursor(t testing.TB, marked string) (string, int) {
	t.Helper()
	off := strings.IndexByte(marked, '|')
	if off < 0 || strings.Count(marked, "|") != 1 {
		t.Fatalf("%q: want exactly one | cursor marker", marked)
	}
	return marked[:off] + marked[off+1:], off
}

func labels(items []Item) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Label
	}
	return out
}

type completionTest struct {
	name    string
	schema  string // fixture file; default github
	src     string // with a | cursor
	ctx     Context
	replace string   // src[From:To]
	top     []string // the first labels, in order
	all     []string // the exact labels, in order (nil: not checked)
	has     []string // labels that must be present
	not     []string // labels that must be absent
	none    bool     // no items at all

	parent, owner, expected string // checked when set
}

var completionTests = []completionTest{
	// ---- top level ----
	{name: "empty document", src: "|", ctx: ContextDocument, all: []string{"query", "mutation", "subscription", "fragment", "{"}},
	{name: "keyword prefix", src: "que|", ctx: ContextDocument, replace: "que", all: []string{"query"}},
	{name: "whole keyword", src: "query|", ctx: ContextDocument, replace: "query", all: []string{"query"}},
	{name: "fragment keyword", src: "{ a }\nfrag|", ctx: ContextDocument, replace: "frag", all: []string{"fragment"}},
	{name: "after a definition", src: "{ viewer }\n|", ctx: ContextDocument, top: []string{"query"}},
	{name: "before a comment", src: "|# note\n{ a }", ctx: ContextDocument, top: []string{"query"}},
	{name: "no mutation root", schema: countries, src: "|", ctx: ContextDocument, all: []string{"query", "fragment", "{"}},
	{name: "no schema offers all operations", schema: noSchema, src: "|", ctx: ContextDocument, all: []string{"query", "mutation", "subscription", "fragment", "{"}},
	{name: "on after fragment name", src: "fragment F |", ctx: ContextKeyword, all: []string{"on"}},
	{name: "on after second fragment name", src: "fragment F on User { id }\nfragment G |", ctx: ContextKeyword, all: []string{"on"}},
	{name: "fragment on keyword", src: "fragment F on|", ctx: ContextKeyword, replace: "on", all: []string{"on"}},
	{name: "operation name", src: "query Q|", ctx: ContextNone, replace: "Q", none: true},
	{name: "skipped SDL", src: "type Query { na| }", ctx: ContextNone, replace: "na", none: true},

	// ---- selection sets ----
	{name: "shorthand open brace", src: "{|", ctx: ContextSelection, parent: "Query",
		all: []string{"node", "nodes", "repository", "lookupRepository", "repositoryOwner", "search", "viewer", "__typename", "__schema", "__type"}},
	{name: "empty selection placeholder", src: "{ | }", ctx: ContextSelection, parent: "Query", top: []string{"node", "nodes"}},
	{name: "placeholder with spaces", src: "{ viewer {   |   } }", ctx: ContextSelection, parent: "User", top: []string{"id", "login"}},
	{name: "half-typed field", src: "{ vi|", ctx: ContextSelection, replace: "vi", all: []string{"viewer"}},
	{name: "nested half-typed field", src: "{ viewer { na| } }", ctx: ContextSelection, parent: "User", replace: "na", all: []string{"name"}},
	{name: "unclosed nested set", src: "{ viewer { |", ctx: ContextSelection, parent: "User",
		all: []string{"id", "login", "avatarUrl", "url", "resourcePath", "name", "createdAt", "fullDatabaseId", "databaseId", "__typename"}},
	{name: "after a selection", src: "{ viewer { login } |", ctx: ContextSelection, parent: "Query", top: []string{"node"}},
	{name: "after field with arguments", src: "{ a(x: 1) |", ctx: ContextSelection, parent: "Query"},
	{name: "cursor mid-word", src: "{ vie|wer }", ctx: ContextSelection, replace: "viewer", top: []string{"viewer"}},
	{name: "cursor at word start", src: "{ |viewer }", ctx: ContextSelection, replace: "viewer", top: []string{"node"}},
	{name: "field before arguments", src: "{ repository|(owner: \"a\") }", ctx: ContextSelection, replace: "repository",
		top: []string{"repository", "repositoryOwner", "lookupRepository"}},
	{name: "aliased field", src: "{ me: vi|", ctx: ContextSelection, replace: "vi", all: []string{"viewer"}},
	{name: "alias itself", src: "{ me|: viewer }", ctx: ContextNone, replace: "me", none: true},
	{name: "meta fields", src: "{ __ty|", ctx: ContextSelection, replace: "__ty", all: []string{"__typename", "__type"}},
	{name: "meta fields only on query root", src: "{ viewer { __|", ctx: ContextSelection, replace: "__", all: []string{"__typename"}},
	{name: "union selection", src: "{ search(query: \"x\", type: ISSUE) { nodes { |", ctx: ContextSelection, parent: "SearchResultItem", all: []string{"__typename"}},
	{name: "inline fragment selection", src: "{ node(id: \"1\") { ... on User { |", ctx: ContextSelection, parent: "User", top: []string{"id", "login"}},
	{name: "inline fragment without condition", src: "{ viewer { ... @include(if: true) { lo|", ctx: ContextSelection, replace: "lo", parent: "User", all: []string{"login"}},
	{name: "fragment definition selection", src: "fragment F on Repository { own|", ctx: ContextSelection, replace: "own", parent: "Repository", all: []string{"owner", "nameWithOwner"}},
	{name: "mutation root", src: "mutation { |", ctx: ContextSelection, parent: "Mutation", all: []string{"addStar", "createIssue", "__typename"}},
	{name: "subscription root", src: "subscription { |", ctx: ContextSelection, parent: "Subscription", all: []string{"issueEvents", "__typename"}},
	{name: "second operation", src: "{ viewer { login } }\n\nquery Other { |", ctx: ContextSelection, parent: "Query", top: []string{"node"}},
	{name: "unknown field", src: "{ unknownField { |", ctx: ContextSelection, parent: "", none: true},
	{name: "query root not named Query", schema: swapi, src: "{ allF|", ctx: ContextSelection, replace: "allF", parent: "Root", top: []string{"allFilms"}},
	{name: "deprecated prefix match before fuzzy", src: "{ viewer { da|", ctx: ContextSelection, replace: "da", all: []string{"databaseId", "fullDatabaseId"}},

	// ---- fragment spreads and type conditions ----
	{name: "spread", src: "{ viewer { ...| } } fragment U on User { id } fragment R on Repository { id } fragment N on Node { id }",
		ctx: ContextFragmentSpread, parent: "User", all: []string{"U", "N", "on"}, not: []string{"R"}},
	{name: "spread name prefix", src: "{ viewer { ...U| } } fragment U on User { id } fragment UR on Repository { id }",
		ctx: ContextFragmentSpread, replace: "U", all: []string{"U"}},
	{name: "spread in own fragment", src: "fragment U on User { ...| }", ctx: ContextFragmentSpread, all: []string{"on"}},
	{name: "spread with unknown type condition", src: "{ viewer { ...| } } fragment X on Nope { id }", ctx: ContextFragmentSpread, all: []string{"X", "on"}},
	{name: "on keyword after spread", src: "{ node(id: 1) { ... on|", ctx: ContextFragmentSpread, replace: "on", all: []string{"on"}},
	{name: "on without space", src: "{ ...on|", ctx: ContextFragmentSpread, replace: "on", all: []string{"on"}},
	{name: "inline on interface", src: "{ node(id: 1) { ... on |", ctx: ContextTypeCondition, parent: "Node",
		all: []string{"Node", "User", "Organization", "Repository", "Issue", "PullRequest", "Actor", "RepositoryOwner", "UniformResourceLocatable", "Starrable", "SearchResultItem"}},
	{name: "inline on union", src: "{ search(query: \"x\", type: ISSUE) { nodes { ... on |", ctx: ContextTypeCondition, parent: "SearchResultItem",
		top: []string{"SearchResultItem", "User", "Organization", "Repository", "Issue", "PullRequest", "Node"}, not: []string{"Label", "Query"}},
	{name: "inline on object", src: "{ viewer { ... on |", ctx: ContextTypeCondition, parent: "User",
		all: []string{"User", "Node", "Actor", "RepositoryOwner", "UniformResourceLocatable", "SearchResultItem"}},
	{name: "inline type prefix", src: "{ node(id: 1) { ... on Us|", ctx: ContextTypeCondition, replace: "Us", top: []string{"User"}},
	{name: "fragment definition type", src: "fragment F on |", ctx: ContextTypeCondition,
		top: []string{"Query", "Mutation", "Subscription", "User"}, has: []string{"SearchResultItem", "Node"}, not: []string{"IssueState", "CreateIssueInput", "String"}},
	{name: "fragment definition missing on", src: "fragment F o|", ctx: ContextTypeCondition, replace: "o", top: []string{"on", "Organization"}},
	{name: "introspection types last", schema: countries, src: "fragment F on |", ctx: ContextTypeCondition, top: []string{"Continent", "Country"}, has: []string{"__Type"}},

	// ---- arguments ----
	{name: "argument list open", src: "{ repository(|", ctx: ContextArgument, owner: "Query.repository", all: []string{"owner", "name", "followRenames"}},
	{name: "required arguments first", src: "{ search(|", ctx: ContextArgument, all: []string{"query", "type", "first", "after"}},
	{name: "provided arguments excluded", src: "{ repository(owner: \"a\" |", ctx: ContextArgument, all: []string{"name", "followRenames"}},
	{name: "after comma", src: "{ search(type: ISSUE, query: \"\", |", ctx: ContextArgument, all: []string{"first", "after"}},
	{name: "argument prefix", src: "{ repository(owner: \"a\", n|", ctx: ContextArgument, replace: "n", all: []string{"name"}},
	{name: "argument being edited stays", src: "{ search(|type: ISSUE)", ctx: ContextArgument, replace: "type", all: []string{"query", "type", "first", "after"}},
	{name: "argument before closing paren", src: "{ search(query: \"\" | ) { issueCount } }", ctx: ContextArgument, all: []string{"type", "first", "after"}},
	{name: "argument without colon", src: "{ repository(owner |", ctx: ContextArgument, all: []string{"name", "followRenames"}},
	{name: "deprecated argument last", src: "{ repository(owner: \"a\", name: \"b\") { issues(|", ctx: ContextArgument, owner: "Repository.issues",
		all: []string{"first", "after", "states", "orderBy", "labels"}},
	{name: "directive arguments", src: "{ viewer @include(|", ctx: ContextArgument, owner: "@include", all: []string{"if"}},
	{name: "meta field argument", src: "{ __type(|", ctx: ContextArgument, owner: "Query.__type", all: []string{"name"}},
	{name: "unknown field arguments", src: "{ unknownField(|", ctx: ContextArgument, none: true},
	{name: "swapi arguments", schema: swapi, src: "{ allFilms(f|", ctx: ContextArgument, replace: "f", all: []string{"first"}},

	// ---- values ----
	{name: "enum value", src: "{ search(type: |", ctx: ContextValue, expected: "SearchType!", all: []string{"ISSUE", "REPOSITORY", "USER", "DISCUSSION"}},
	{name: "enum value prefix", src: "{ search(type: IS|", ctx: ContextValue, replace: "IS", all: []string{"ISSUE"}},
	{name: "enum value complete", src: "{ search(type: ISSUE|", ctx: ContextValue, replace: "ISSUE", all: []string{"ISSUE"}},
	{name: "enum value before closing paren", src: "{ search(type: | ) { issueCount } }", ctx: ContextValue, top: []string{"ISSUE"}},
	{name: "boolean", src: "{ viewer @include(if: |", ctx: ContextValue, expected: "Boolean!", all: []string{"true", "false"}},
	{name: "boolean prefix", src: "{ viewer @include(if: f|", ctx: ContextValue, replace: "f", all: []string{"false"}},
	{name: "nullable boolean", src: "{ repository(owner: \"a\", name: \"b\", followRenames: |", ctx: ContextValue, all: []string{"true", "false", "null"}},
	{name: "null prefix", src: "{ viewer { avatarUrl(size: nu|", ctx: ContextValue, replace: "nu", all: []string{"null"}},
	{name: "scalar value", src: "{ node(id: |", ctx: ContextValue, expected: "ID!", none: true},
	{name: "list item", src: "{ repository(owner: \"a\", name: \"b\") { issues(states: [|", ctx: ContextValue, expected: "IssueState!", all: []string{"OPEN", "CLOSED"}},
	{name: "second list item", src: "{ repository(owner: \"a\", name: \"b\") { issues(states: [OPEN, |", ctx: ContextValue, all: []string{"OPEN", "CLOSED"}},
	{name: "single value for list", src: "{ repository(owner: \"a\", name: \"b\") { issues(states: |", ctx: ContextValue, expected: "[IssueState!]", all: []string{"OPEN", "CLOSED", "null"}},
	{name: "value in object field", src: "{ repository(owner: \"a\", name: \"b\") { issues(orderBy: {field: |", ctx: ContextValue,
		expected: "IssueOrderField!", all: []string{"CREATED_AT", "UPDATED_AT", "COMMENTS"}},
	{name: "unknown argument value", src: "{ search(nope: |", ctx: ContextValue, expected: "", none: true},
	{name: "string literal", src: "{ search(query: \"x|", ctx: ContextString, none: true},
	{name: "inside closed string", src: "{ a(s: \"x|\") }", ctx: ContextString, none: true},
	{name: "inside block string", src: "{ a(x: \"\"\"ab|c", ctx: ContextString, none: true},
	{name: "after string", src: "{ a(s: \"x\"|) }", ctx: ContextNone, none: true},
	{name: "inside number", src: "{ a(x: 12|3) }", ctx: ContextNone, none: true},
	{name: "after closing paren", src: "{ a(x: 1)| }", ctx: ContextNone, none: true},
	{name: "after closing brace", src: "{ viewer { login }|", ctx: ContextNone, none: true},

	// ---- variables ----
	{name: "variables that fit", src: "query Q($t: SearchType, $n: Int) { search(first: |", ctx: ContextValue, all: []string{"$n", "null"}},
	{name: "variables and enum values", src: "query Q($t: SearchType!, $n: Int) { search(type: |", ctx: ContextValue, all: []string{"ISSUE", "REPOSITORY", "USER", "$t", "DISCUSSION"}},
	{name: "variable after dollar", src: "query Q($t: SearchType, $n: Int) { search(type: $|", ctx: ContextVariable, replace: "$", all: []string{"$t"}},
	{name: "variable name prefix", src: "query Q($t: SearchType, $tt: SearchType) { search(type: $t|", ctx: ContextVariable, replace: "$t", all: []string{"$t", "$tt"}},
	{name: "variable without dollar", src: "query Q($first: Int) { search(first: fi|", ctx: ContextValue, replace: "fi", all: []string{"$first"}},
	{name: "list variable", src: "query Q($ids: [ID!]!, $one: ID) { nodes(ids: |", ctx: ContextValue, all: []string{"$ids"}},
	{name: "list item variable", src: "query Q($ids: [ID!]!, $one: ID) { nodes(ids: [$|", ctx: ContextVariable, replace: "$", all: []string{"$one"}},
	{name: "unknown variable type offered", src: "query Q($x: Nope) { search(first: $|", ctx: ContextVariable, replace: "$", all: []string{"$x"}},
	{name: "directive argument variable", src: "query Q($b: Boolean = true) { viewer @include(if: |", ctx: ContextValue, all: []string{"true", "false", "$b"}},
	{name: "operation directive variable", src: "query Q($a: Int) @cost(weight: |", ctx: ContextValue, all: []string{"$a"}},
	{name: "no variables in fragments", src: "query Q($id: ID) { ...F }\nfragment F on User { avatarUrl(size: $|", ctx: ContextVariable, replace: "$", none: true},
	{name: "variable definition name", src: "query Q($|", ctx: ContextNone, none: true},
	{name: "variable definition list", src: "query Q(|", ctx: ContextNone, none: true},

	// ---- variable definitions ----
	{name: "variable type", src: "query Q($id: |", ctx: ContextVariableType,
		all: []string{"SearchType", "IssueState", "IssueOrderField", "OrderDirection", "IssueEventAction", "IssueOrder", "AddStarInput", "CreateIssueInput", "RepositoryLookup", "Boolean", "ID", "Int", "String", "DateTime", "URI", "BigInt", "Float"},
		not: []string{"User", "Node", "SearchResultItem"}},
	{name: "variable type prefix", src: "query Q($id: I|", ctx: ContextVariableType, replace: "I", top: []string{"IssueState", "IssueOrderField", "IssueEventAction", "IssueOrder", "ID", "Int"}},
	{name: "list variable type", src: "query Q($id: [|", ctx: ContextVariableType, top: []string{"SearchType"}},
	{name: "second variable type", src: "query Q($a: Int, $b: Str|) { a }", ctx: ContextVariableType, replace: "Str", all: []string{"String", "AddStarInput"}},
	{name: "after non-null type", src: "query Q($id: ID!|", ctx: ContextNone, none: true},
	{name: "default value", src: "query Q($t: SearchType = |", ctx: ContextValue, expected: "SearchType", all: []string{"ISSUE", "REPOSITORY", "USER", "null", "DISCUSSION"}},
	{name: "default value no variables", src: "query Q($a: Int, $b: Int = |", ctx: ContextValue, all: []string{"null"}},
	{name: "default object value", src: "query Q($o: IssueOrder = {|", ctx: ContextObjectField, parent: "IssueOrder", all: []string{"field", "direction"}},
	{name: "variable definition directive argument is constant", src: "query Q($b: Boolean!, $a: Int = 1 @cost(weight: |", ctx: ContextValue, expected: "Int!", none: true},

	// ---- input objects ----
	{name: "object fields", src: "mutation { createIssue(input: {|", ctx: ContextObjectField, parent: "CreateIssueInput",
		all: []string{"repositoryId", "title", "clientMutationId", "body", "labelIds", "assigneeIds", "assigneeId"}},
	{name: "provided object fields excluded", src: "mutation { createIssue(input: {title: \"x\", |", ctx: ContextObjectField,
		all: []string{"repositoryId", "clientMutationId", "body", "labelIds", "assigneeIds", "assigneeId"}},
	{name: "object field prefix", src: "mutation { createIssue(input: {title: \"x\", la|", ctx: ContextObjectField, replace: "la", all: []string{"labelIds"}},
	{name: "object field without colon", src: "mutation { createIssue(input: {title |", ctx: ContextObjectField, not: []string{"title"}, top: []string{"repositoryId"}},
	{name: "object field list value", src: "mutation { createIssue(input: {labelIds: [|", ctx: ContextValue, expected: "ID!", none: true},
	{name: "one-of input", src: "{ lookupRepository(by: {|", ctx: ContextObjectField, parent: "RepositoryLookup", all: []string{"id", "nameWithOwner"}},
	{name: "nested input objects", schema: countries, src: "{ countries(filter: {code: {|", ctx: ContextObjectField, parent: "StringQueryOperatorInput",
		all: []string{"eq", "in", "ne", "nin", "regex"}},
	{name: "object value for scalar", src: "{ node(id: {|", ctx: ContextObjectField, none: true},

	// ---- directives ----
	{name: "field directives", src: "query Q($b: Boolean!) { viewer @|", ctx: ContextDirective, all: []string{"include", "skip", "cost"}, not: []string{"defer", "deprecated"}},
	{name: "directive prefix", src: "{ viewer @inc|", ctx: ContextDirective, replace: "inc", all: []string{"include"}},
	{name: "present directive excluded", src: "{ viewer @include(if: true) @|", ctx: ContextDirective, all: []string{"skip", "cost"}},
	{name: "directive being edited stays", src: "{ viewer @in| @skip(if: true) }", ctx: ContextDirective, replace: "in", all: []string{"include"}},
	{name: "inline fragment directives", src: "{ ... @|", ctx: ContextDirective, all: []string{"include", "skip", "defer"}},
	{name: "fragment spread directives", src: "{ ...F @|", ctx: ContextDirective, all: []string{"include", "skip", "defer"}},
	{name: "query directives", src: "query Q @|", ctx: ContextDirective, none: true},
	{name: "variable definition directives", src: "query Q($a: Int @|", ctx: ContextDirective, none: true},
	{name: "builtin directives without schema", schema: noSchema, src: "{ a @|", ctx: ContextDirective, all: []string{"include", "skip"}},
	{name: "builtin directives on old server", schema: swapi, src: "{ allFilms @|", ctx: ContextDirective, all: []string{"include", "skip"}},

	// ---- comments ----
	{name: "comment", src: "# comm|ent", ctx: ContextComment, none: true},
	{name: "end of comment", src: "{\n  # note|\n}", ctx: ContextComment, none: true},
	{name: "comment inside selection", src: "{ viewer { # lo|\n", ctx: ContextComment, none: true},
}

func TestComplete(t *testing.T) {
	t.Parallel()
	for _, tt := range completionTests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fixture := tt.schema
			if fixture == "" {
				fixture = github
			}
			src, off := cursor(t, tt.src)
			r := Complete(loadSchema(t, fixture), src, off)
			got := labels(r.Items)
			if r.Context != tt.ctx {
				t.Errorf("context = %s, want %s (items %v)", r.Context, tt.ctx, got)
			}
			if r.From > off || off > r.To || r.From < 0 || r.To > len(src) {
				t.Fatalf("range [%d, %d) does not hold cursor %d", r.From, r.To, off)
			}
			if rep := src[r.From:r.To]; rep != tt.replace {
				t.Errorf("replace = %q, want %q", rep, tt.replace)
			}
			if tt.all != nil && !slices.Equal(got, tt.all) {
				t.Errorf("items = %v, want %v", got, tt.all)
			}
			if len(tt.top) > 0 && (len(got) < len(tt.top) || !slices.Equal(got[:len(tt.top)], tt.top)) {
				t.Errorf("items = %v, want to start with %v", got, tt.top)
			}
			for _, l := range tt.has {
				if !slices.Contains(got, l) {
					t.Errorf("items = %v, want %s", got, l)
				}
			}
			for _, l := range tt.not {
				if slices.Contains(got, l) {
					t.Errorf("items = %v, want no %s", got, l)
				}
			}
			if tt.none && len(got) > 0 {
				t.Errorf("items = %v, want none", got)
			}
			if !tt.none && tt.all == nil && len(got) == 0 && (tt.top != nil || tt.has != nil) {
				t.Errorf("no items")
			}
			if tt.parent != "" && r.ParentType != tt.parent {
				t.Errorf("parent type = %q, want %q", r.ParentType, tt.parent)
			}
			if tt.owner != "" && r.Owner != tt.owner {
				t.Errorf("owner = %q, want %q", r.Owner, tt.owner)
			}
			if tt.expected != "" && r.ExpectedType != tt.expected {
				t.Errorf("expected type = %q, want %q", r.ExpectedType, tt.expected)
			}
		})
	}
}

func findItem(t *testing.T, r Result, label string) Item {
	t.Helper()
	for _, it := range r.Items {
		if it.Label == label {
			return it
		}
	}
	t.Fatalf("no item %q in %v", label, labels(r.Items))
	return Item{}
}

func completeAt(t *testing.T, s *schema.Schema, marked string) Result {
	t.Helper()
	src, off := cursor(t, marked)
	return Complete(s, src, off)
}

// TestItemDetails checks the type, documentation, deprecation, insert text,
// snippet hints and sort text of items.
func TestItemDetails(t *testing.T) {
	t.Parallel()
	s := loadSchema(t, github)

	r := completeAt(t, s, "{ viewer { | }")
	id := findItem(t, r, "databaseId")
	if id.Kind != KindField || id.Detail != "Int" || !id.Deprecated ||
		!strings.Contains(id.DeprecationReason, "64-bit") || id.Documentation != "Identifies the primary key from the database." {
		t.Errorf("databaseId = %+v", id)
	}
	if n := findItem(t, r, "name"); n.InsertText != "name" || n.Suffix != "" || n.Deprecated {
		t.Errorf("name = %+v", n)
	}
	if a := findItem(t, r, "avatarUrl"); a.Detail != "URI!" || a.Suffix != "" {
		t.Errorf("avatarUrl (optional args, scalar) = %+v", a)
	}
	for i, it := range r.Items {
		if want := pad(i); it.SortText != want {
			t.Errorf("item %d SortText = %q, want %q", i, it.SortText, want)
		}
	}

	r = completeAt(t, s, "{ |")
	if it := findItem(t, r, "repository"); it.Suffix != "()" || it.SuffixCursor != 1 || it.Detail != "Repository" ||
		it.Documentation != "Lookup a given repository by the owner and repository name." {
		t.Errorf("repository = %+v", it)
	}
	if it := findItem(t, r, "viewer"); it.Suffix != " {  }" || it.SuffixCursor != 3 || it.Detail != "User!" {
		t.Errorf("viewer = %+v", it)
	}
	if it := findItem(t, r, "__typename"); it.Detail != "String!" || it.Suffix != "" {
		t.Errorf("__typename = %+v", it)
	}

	// Delimiters already present suppress the hints.
	r = completeAt(t, s, "{ repo|(owner: \"a\") }")
	if it := findItem(t, r, "repository"); it.Suffix != "" {
		t.Errorf("repository before ( = %+v", it)
	}
	r = completeAt(t, s, "{ vie| { login } }")
	if it := findItem(t, r, "viewer"); it.Suffix != "" {
		t.Errorf("viewer before { = %+v", it)
	}

	r = completeAt(t, s, "{ repository(owner: \"a\", name: \"b\") { issues(|")
	if it := findItem(t, r, "orderBy"); it.Kind != KindArgument || it.Detail != "IssueOrder = {field: CREATED_AT, direction: DESC}" || it.Suffix != ": " || it.SuffixCursor != 2 {
		t.Errorf("orderBy = %+v", it)
	}
	if it := findItem(t, r, "labels"); !it.Deprecated || it.DeprecationReason != "Use `filterBy.labels` instead." {
		t.Errorf("labels = %+v", it)
	}
	r = completeAt(t, s, "{ search(ty|: ISSUE) }")
	if it := findItem(t, r, "type"); it.Suffix != "" || it.Detail != "SearchType!" {
		t.Errorf("type before : = %+v", it)
	}

	r = completeAt(t, s, "mutation { createIssue(input: {|")
	if it := findItem(t, r, "assigneeIds"); it.Kind != KindInputField || it.Detail != "[ID!] = []" || it.Suffix != ": " {
		t.Errorf("assigneeIds = %+v", it)
	}
	if it := findItem(t, r, "assigneeId"); !it.Deprecated || it.DeprecationReason != "Use `assigneeIds`." {
		t.Errorf("assigneeId = %+v", it)
	}

	r = completeAt(t, s, "{ search(type: |")
	if it := findItem(t, r, "DISCUSSION"); it.Kind != KindEnumValue || it.Detail != "SearchType" || !it.Deprecated ||
		it.Documentation != "Returns matching discussions." || it.DeprecationReason != "Discussions are searched with ISSUE." {
		t.Errorf("DISCUSSION = %+v", it)
	}

	r = completeAt(t, s, "{ viewer @|")
	if it := findItem(t, r, "include"); it.Kind != KindDirective || it.Detail != "(if: Boolean!)" || it.Suffix != "()" ||
		!strings.HasPrefix(it.Documentation, "Directs the executor to include") {
		t.Errorf("include = %+v", it)
	}
	// weight has a default, so no "()" hint.
	if it := findItem(t, r, "cost"); it.Detail != "(weight: Int!)" || it.Suffix != "" {
		t.Errorf("cost = %+v", it)
	}

	r = completeAt(t, s, "query Q($first: Int!) { search(first: $|")
	if it := findItem(t, r, "$first"); it.Kind != KindVariable || it.Detail != "Int!" || it.InsertText != "$first" {
		t.Errorf("$first = %+v", it)
	}
	r = completeAt(t, s, "query Q($x: Nope) { search(first: $|")
	if it := findItem(t, r, "$x"); it.Detail != "Nope" {
		t.Errorf("$x = %+v", it)
	}

	r = completeAt(t, s, "{ viewer { ...| } } fragment U on User { id }")
	if it := findItem(t, r, "U"); it.Kind != KindFragment || it.Detail != "on User" {
		t.Errorf("U = %+v", it)
	}
	if it := findItem(t, r, "on"); it.Kind != KindKeyword {
		t.Errorf("on = %+v", it)
	}

	r = completeAt(t, s, "{ node(id: 1) { ... on |")
	if it := findItem(t, r, "SearchResultItem"); it.Kind != KindType || it.Detail != "union" {
		t.Errorf("SearchResultItem = %+v", it)
	}
	r = completeAt(t, s, "query Q($a: |")
	if it := findItem(t, r, "IssueOrder"); it.Detail != "input object" {
		t.Errorf("IssueOrder = %+v", it)
	}
	if it := findItem(t, r, "Float"); it.Detail != "scalar" || it.Documentation == "" {
		t.Errorf("Float (built-in fallback) = %+v", it)
	}
}

// TestRanking checks match tiers, deprecation, meta fields and ties.
func TestRanking(t *testing.T) {
	t.Parallel()

	tests := []struct {
		prefix, name string
		want         int
	}{
		{"", "anything", matchPrefix},
		{"us", "user", matchPrefix},
		{"Us", "user", matchFoldPrefix},
		{"usr", "user", matchFuzzy},
		{"zz", "resourcePath", noMatch},
		{"ur", "resourcePath", noMatch},
		{"ur", "mirrorUrl", matchFuzzy},
		{"na", "hasWikiEnabled", noMatch},
		{"cr", "CREATED_AT", matchFoldPrefix},
		{"at", "CREATED_AT", matchFuzzy},
		{"rp", "resourcePath", matchFuzzy},
		{"userx", "user", noMatch},
		{"x", "", noMatch},
	}
	for _, tt := range tests {
		if got := match(tt.prefix, tt.name); got != tt.want {
			t.Errorf("match(%q, %q) = %d, want %d", tt.prefix, tt.name, got, tt.want)
		}
	}

	// Tier, then "__" names, then deprecation, then class, then insertion
	// (declaration) order.
	c := &completer{res: Result{Prefix: "us"}}
	c.add(Item{Label: "maxUsers"}, classFirst)
	c.add(Item{Label: "__user"}, classFirst)
	c.add(Item{Label: "Users"}, classFirst)
	c.add(Item{Label: "user", Deprecated: true}, classFirst)
	c.add(Item{Label: "userOptional"}, classSecond)
	c.add(Item{Label: "userName"}, classFirst)
	c.add(Item{Label: "userId"}, classFirst)
	c.add(Item{Label: "status"}, classFirst)
	c.finish()
	if got, want := labels(c.res.Items), []string{"userName", "userId", "userOptional", "user", "Users", "maxUsers", "__user"}; !slices.Equal(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}

	s := loadSchema(t, github)
	r := completeAt(t, s, "{ repository(owner: \"a\", name: \"b\") { ur| } }")
	if got, want := labels(r.Items), []string{"url", "mirrorUrl"}; !slices.Equal(got, want) {
		t.Errorf("ur (prefix, then fuzzy deprecated): %v, want %v", got, want)
	}
	r = completeAt(t, s, "{ viewer { Na| } }")
	if got, want := labels(r.Items), []string{"name"}; !slices.Equal(got, want) {
		t.Errorf("Na: %v, want %v", got, want)
	}
}

// TestRepeatableDirective checks that a repeatable directive stays offered
// when already present.
func TestRepeatableDirective(t *testing.T) {
	t.Parallel()
	s := parseFixture(t, github) // private copy: modified below
	s.Directives = append(s.Directives, &schema.Directive{
		Name: "tag", Locations: []schema.DirectiveLocation{schema.LocField}, IsRepeatable: true,
		Args: []*schema.InputValue{{Name: "name", Type: schema.NonNullOf(schema.Named(schema.KindScalar, "String"))}},
	})
	r := completeAt(t, s, "{ viewer @tag(name: \"a\") @include(if: true) @|")
	if got, want := labels(r.Items), []string{"skip", "cost", "tag"}; !slices.Equal(got, want) {
		t.Errorf("items = %v, want %v", got, want)
	}
}

// TestUTF16 shows the offset conversion an editor does around Complete.
func TestUTF16(t *testing.T) {
	t.Parallel()
	s := loadSchema(t, github)
	src := "# ünïcødé 🚀\n{ vi"
	x := lexer.NewUTF16Index(src)
	cursor16 := x.Len16() // the editor's cursor at the end, in UTF-16 units
	r := Complete(s, src, x.ToByte(cursor16))
	from16, to16 := x.ToUTF16(r.From), x.ToUTF16(r.To)
	if r.Context != ContextSelection || to16 != cursor16 || from16 != cursor16-2 {
		t.Errorf("context %s, UTF-16 range [%d, %d), cursor %d", r.Context, from16, to16, cursor16)
	}
	if got := labels(r.Items); !slices.Equal(got, []string{"viewer"}) {
		t.Errorf("items = %v", got)
	}
}

// TestOffsetClamped checks out-of-range offsets.
func TestOffsetClamped(t *testing.T) {
	t.Parallel()
	s := loadSchema(t, github)
	for _, off := range []int{-5, 100} {
		r := Complete(s, "{ vi", off)
		if r.From < 0 || r.To > 4 || r.From > r.To {
			t.Errorf("offset %d: range [%d, %d)", off, r.From, r.To)
		}
	}
}
