package validate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/graphql/schema"
)

func TestKnownDirectives(t *testing.T) {
	runCases(t, []ruleCase{
		{
			name: "unknown, with suggestions", schema: "github",
			query: `{ viewer @⟦inclde⟧(if: true) { id } }`,
			want: []want{{CodeUnknownDirective,
				`Unknown directive "@inclde". Did you mean "@include"?`, []string{"include"}}},
		},
		{
			name: "unknown, no suggestions", schema: "countries",
			query: `{ continents @⟦zzzz⟧ { code } }`,
			want:  []want{{CodeUnknownDirective, `Unknown directive "@zzzz".`, []string{}}},
		},
		{
			name: "every location", schema: "github",
			query: "query Q($a: Int @⟦skip⟧(if: true)) @⟦include⟧(if: true) { viewer @⟦defer⟧ { avatarUrl(size: $a) ...F @⟦cost⟧ ... @⟦cost⟧ { id } } }\n" +
				"fragment F on User @⟦skip⟧(if: false) { id }\n" +
				"mutation M @⟦oneOf⟧ { addStar(input: {starrableId: 1}) { clientMutationId } }\n" +
				"subscription S @⟦deprecated⟧ { issueEvents(repositoryId: 1) { action } }",
			want: []want{
				{CodeMisplacedDirective, `Directive "@skip" may not be used on VARIABLE_DEFINITION.`, nil},
				{CodeMisplacedDirective, `Directive "@include" may not be used on QUERY.`, nil},
				{CodeMisplacedDirective, `Directive "@defer" may not be used on FIELD.`, nil},
				{CodeMisplacedDirective, `Directive "@cost" may not be used on FRAGMENT_SPREAD.`, nil},
				{CodeMisplacedDirective, `Directive "@cost" may not be used on INLINE_FRAGMENT.`, nil},
				{CodeMisplacedDirective, `Directive "@skip" may not be used on FRAGMENT_DEFINITION.`, nil},
				{CodeMisplacedDirective, `Directive "@oneOf" may not be used on MUTATION.`, nil},
				{CodeMisplacedDirective, `Directive "@deprecated" may not be used on SUBSCRIPTION.`, nil},
			},
		},
		{
			name: "allowed locations", schema: "github",
			query: `query Q($inc: Boolean!) { viewer @include(if: $inc) @cost(weight: 2) { ... @defer { id } ...F @skip(if: false) } } fragment F on User { id }`,
		},
		{
			name: "spec directives the server did not list", schema: "countries",
			query: `query Q($x: Boolean!) { continents @skip(if: $x) { code @include(if: true) } }`,
		},
	})
}

func TestUniqueDirectivesPerLocation(t *testing.T) {
	runCases(t, []ruleCase{
		{
			name: "non-repeatable", schema: "github",
			query: `{ viewer @include(if: true) @⟦include⟧(if: false) @skip(if: false) @⟦include⟧(if: true) { id } }`,
			want: []want{
				{CodeDuplicateDirective, `The directive "@include" can only be used once at this location.`, nil},
				{CodeDuplicateDirective, `The directive "@include" can only be used once at this location.`, nil},
			},
		},
		{
			name: "different nodes", schema: "github",
			query: `{ viewer @skip(if: false) { id @skip(if: false) ... @skip(if: false) { login } } }`,
		},
		{
			name: "unknown directives are not checked", schema: "github",
			query: `{ viewer @⟦nope⟧ @⟦nope⟧ { id } }`,
			want: []want{
				{CodeUnknownDirective, "", nil},
				{CodeUnknownDirective, "", nil},
			},
		},
		{
			name: "repeatable", schema: "github+tag",
			query: `{ viewer @tag(name: "a") @tag(name: "b") { id @include(if: true) @⟦include⟧(if: true) } }`,
			want:  []want{{CodeDuplicateDirective, "", nil}},
		},
	})
}

func TestUniqueArgumentNames(t *testing.T) {
	runCases(t, []ruleCase{
		{
			name: "fields and directives", schema: "github",
			query: `{ repository(owner: "a", name: "b", ⟦owner⟧: "c") { id } viewer @include(if: true, ⟦if⟧: false) { id } }`,
			want: []want{
				{CodeDuplicateArgument, `There can be only one argument named "owner".`, nil},
				{CodeDuplicateArgument, `There can be only one argument named "if".`, nil},
			},
		},
		{
			name: "same name on different fields", schema: "github",
			query: `{ repository(owner: "a", name: "b") { issues(first: 1) { totalCount } } search(query: "x", type: ISSUE, first: 1) { issueCount } }`,
		},
	})
}

func TestUniqueInputFieldNames(t *testing.T) {
	runCases(t, []ruleCase{
		{
			name: "duplicate", schema: "github",
			query: `{ repository(owner: "a", name: "b") { issues(orderBy: {field: CREATED_AT, direction: ASC, ⟦field⟧: UPDATED_AT}) { totalCount } } }`,
			want:  []want{{CodeDuplicateInputField, `There can be only one input field named "field".`, nil}},
		},
		{
			name: "nested objects are separate", schema: "countries",
			query: `{ countries(filter: {code: {eq: "DE", ⟦eq⟧: "AT"}, name: {eq: "x"}}) { code } }`,
			want:  []want{{CodeDuplicateInputField, "", nil}},
		},
		{
			name: "variable default values", schema: "github",
			query: `query Q($o: IssueOrder = {field: CREATED_AT, direction: ASC, ⟦direction⟧: DESC}) { repository(owner: "a", name: "b") { issues(orderBy: $o) { totalCount } } }`,
			want:  []want{{CodeDuplicateInputField, "", nil}},
		},
	})
}

// githubWithTag returns the GitHub subset with a repeatable @tag directive
// on fields (fixture "github+tag").
func githubWithTag(t testing.TB) *schema.Schema {
	data, err := os.ReadFile(filepath.Join("..", "schema", "testdata", fixtureFiles["github"]))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]map[string]map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	s := doc["data"]["__schema"]
	s["directives"] = append(s["directives"].([]any), map[string]any{
		"name": "tag", "isRepeatable": true, "locations": []string{"FIELD"},
		"args": []any{map[string]any{
			"name": "name",
			"type": map[string]any{"kind": "NON_NULL", "ofType": map[string]any{"kind": "SCALAR", "name": "String"}},
		}},
	})
	if data, err = json.Marshal(doc); err != nil {
		t.Fatal(err)
	}
	parsed, err := schema.ParseIntrospection(data)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
