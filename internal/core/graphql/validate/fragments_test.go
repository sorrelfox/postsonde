package validate

import (
	"fmt"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/graphql/parser"
)

func TestKnownFragmentNames(t *testing.T) {
	runCases(t, []ruleCase{
		{
			name: "misspelled", schema: "github",
			query: `{ viewer { ...⟦UserFeilds⟧ } } fragment ⟦UserFields⟧ on User { id }`,
			want: []want{
				{CodeUnknownFragment, `Unknown fragment "UserFeilds". Did you mean "UserFields"?`, []string{"UserFields"}},
				{CodeUnusedFragment, `Fragment "UserFields" is never used.`, nil},
			},
		},
		{
			name: "no suggestion", schema: "github",
			query: `{ viewer { ...⟦Zzz⟧ } }`,
			want:  []want{{CodeUnknownFragment, `Unknown fragment "Zzz".`, []string{}}},
		},
		{
			name: "in a fragment", schema: "github",
			query: `fragment A on User { ...⟦B⟧ } { viewer { ...A } }`,
			want:  []want{{CodeUnknownFragment, "", nil}},
		},
	})
}

func TestUniqueFragmentNames(t *testing.T) {
	runCases(t, []ruleCase{
		{
			name: "duplicate", schema: "github",
			query: `{ viewer { ...F } } fragment F on User { id } fragment ⟦F⟧ on User { login } fragment ⟦F⟧ on Issue { title }`,
			want: []want{
				{CodeDuplicateFragmentName, `There can be only one fragment named "F".`, nil},
				{CodeDuplicateFragmentName, `There can be only one fragment named "F".`, nil},
			},
		},
	})
}

func TestFragmentsOnCompositeTypes(t *testing.T) {
	runCases(t, []ruleCase{
		{
			name: "fragment definition on a scalar", schema: "github",
			query: `fragment F on ⟦String⟧ { length } { viewer { ...F } }`,
			want:  []want{{CodeFragmentOnNonComposite, `Fragment "F" cannot condition on non composite type "String".`, nil}},
		},
		{
			name: "inline fragments on an enum and an input object", schema: "github",
			query: `{ viewer { login ... on ⟦IssueState⟧ { x } ... on ⟦IssueOrder⟧ { field } } }`,
			want: []want{
				{CodeFragmentOnNonComposite, `Fragment cannot condition on non composite type "IssueState".`, nil},
				{CodeFragmentOnNonComposite, `Fragment cannot condition on non composite type "IssueOrder".`, nil},
			},
		},
		{
			name: "composite types", schema: "github",
			query: `{ search(query: "x", type: ISSUE) { nodes { ... on SearchResultItem { __typename } ... on Node { id } ...R } } } fragment R on Repository { id }`,
		},
	})
}

func TestPossibleFragmentSpreads(t *testing.T) {
	runCases(t, []ruleCase{
		{
			name: "object in another object", schema: "github",
			query: `{ viewer { ... on ⟦Issue⟧ { title } } }`,
			want: []want{{CodeImpossibleFragmentSpread,
				`Fragment cannot be spread here as objects of type "User" can never be of type "Issue".`, nil}},
		},
		{
			name: "named fragment, at the spread name", schema: "github",
			query: `{ viewer { ...⟦I⟧ } } fragment I on Issue { title }`,
			want: []want{{CodeImpossibleFragmentSpread,
				`Fragment "I" cannot be spread here as objects of type "User" can never be of type "Issue".`, nil}},
		},
		{
			name: "interface the object does not implement", schema: "github",
			query: `{ viewer { ... on ⟦Starrable⟧ { stargazerCount } } }`,
			want:  []want{{CodeImpossibleFragmentSpread, "", nil}},
		},
		{
			name: "abstract types without a common object", schema: "github",
			query: `{ repositoryOwner(login: "a") { ... on ⟦Starrable⟧ { stargazerCount } } }`,
			want: []want{{CodeImpossibleFragmentSpread,
				`Fragment cannot be spread here as objects of type "RepositoryOwner" can never be of type "Starrable".`, nil}},
		},
		{
			name: "overlapping types", schema: "github",
			query: `{ node(id: 1) { ... on User { login } ... on Starrable { stargazerCount } } repositoryOwner(login: "a") { ... on Actor { login } ...O } search(query: "x", type: USER) { nodes { ... on RepositoryOwner { login } } } } fragment O on Organization { name }`,
		},
		{
			name: "unknown types and fragments are other rules'", schema: "github",
			query: `{ viewer { ... on ⟦Nope⟧ { a } ...⟦Missing⟧ } }`,
			want:  []want{{CodeUnknownType, "", nil}, {CodeUnknownFragment, "", nil}},
		},
	})
}

func TestNoUnusedFragments(t *testing.T) {
	runCases(t, []ruleCase{
		{
			name: "used only by unused fragments", schema: "github",
			query: `{ viewer { ...A } } fragment A on User { ...B } fragment B on User { id } fragment ⟦C⟧ on User { ...D } fragment ⟦D⟧ on User { id }`,
			want: []want{
				{CodeUnusedFragment, `Fragment "C" is never used.`, nil},
				{CodeUnusedFragment, `Fragment "D" is never used.`, nil},
			},
		},
		{
			name: "no operations", schema: "github",
			query: `fragment ⟦F⟧ on User { id }`,
			want:  []want{{CodeUnusedFragment, "", nil}},
		},
		{
			name: "used by any of several operations", schema: "github",
			query: `query A { viewer { ...F } } query B { viewer { id } } fragment F on User { id }`,
		},
	})
}

func TestNoFragmentCycles(t *testing.T) {
	runCases(t, []ruleCase{
		{
			name: "itself", schema: "github",
			query: `{ viewer { ...A } } fragment A on User { ...⟦A⟧ }`,
			want:  []want{{CodeFragmentCycle, `Cannot spread fragment "A" within itself.`, nil}},
		},
		{
			name: "through another fragment", schema: "github",
			query: `{ viewer { ...A } } fragment A on User { ...B } fragment B on User { ...⟦A⟧ }`,
			want:  []want{{CodeFragmentCycle, `Cannot spread fragment "A" within itself via "B".`, nil}},
		},
		{
			name: "through fields and inline fragments", schema: "github",
			query: `{ node(id: 1) { ...A } } fragment A on Node { ... on User { ...B } } fragment B on User { url ...C } fragment C on Node { id ... on User { ...⟦A⟧ } }`,
			want:  []want{{CodeFragmentCycle, `Cannot spread fragment "A" within itself via "B", "C".`, nil}},
		},
		{
			name: "two cycles through the same fragment", schema: "github",
			query: `fragment A on User { ...B ...C } fragment B on User { ...⟦A⟧ } fragment C on User { ...⟦A⟧ } { viewer { ...A } }`,
			want: []want{
				{CodeFragmentCycle, `Cannot spread fragment "A" within itself via "B".`, nil},
				{CodeFragmentCycle, `Cannot spread fragment "A" within itself via "C".`, nil},
			},
		},
		{
			name: "reported once, where the search closes it", schema: "github",
			query: `fragment B on User { ...A } fragment A on User { ...⟦B⟧ } { viewer { ...A } }`,
			want:  []want{{CodeFragmentCycle, `Cannot spread fragment "B" within itself via "A".`, nil}},
		},
		{
			name: "no cycle with repeated spreads", schema: "github",
			query: `fragment A on User { ...B ...B ... { ...B } } fragment B on User { id } { viewer { ...A ...B } }`,
		},
	})
}

// TestLargeFragmentGraph checks that fragment cycles and transitive variable
// usages stay cheap on a long chain of fragments shared by many operations.
func TestLargeFragmentGraph(t *testing.T) {
	const frags, ops = 2000, 300
	var b strings.Builder
	for i := range ops {
		fmt.Fprintf(&b, "query Q%d($s: Int) { viewer { ...F0 } }\n", i)
	}
	for i := range frags {
		fmt.Fprintf(&b, "fragment F%d on User { avatarUrl(size: $s) ...F%d }\n", i, (i+1)%frags)
	}
	src := b.String()
	doc, errs := parser.Parse(src)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	got := Validate(loadSchema(t, "github"), doc)
	if len(got) != 1 || got[0].Code != CodeFragmentCycle || src[got[0].Start:got[0].End] != "F0" {
		t.Fatalf("got %d diagnostics, want the one cycle:\n%s", len(got), describe(src, got[:min(len(got), 5)]))
	}
	if want := `Cannot spread fragment "F0" within itself via "F1", "F2", "F3", "F4", "F5", "F6", "F7", "F8", "F9", "F10" and 1989 more.`; got[0].Message != want {
		t.Errorf("message\n got: %s\nwant: %s", got[0].Message, want)
	}
}
