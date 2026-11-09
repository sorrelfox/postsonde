package validate

import "testing"

func TestUniqueVariableNames(t *testing.T) {
	runCases(t, []ruleCase{
		{
			name: "duplicates", schema: "github",
			query: `query ($a: Int, ⟦$a⟧: String, $b: String, ⟦$a⟧: Int) { search(query: "x", type: ISSUE, first: $a, after: $b) { issueCount } }`,
			want: []want{
				{CodeDuplicateVariable, `There can be only one variable named "$a".`, nil},
				{CodeDuplicateVariable, `There can be only one variable named "$a".`, nil},
			},
		},
		{
			name: "same name in different operations", schema: "github",
			query: `query A($a: Int) { search(query: "x", type: ISSUE, first: $a) { issueCount } } query B($a: Int) { search(query: "x", type: ISSUE, first: $a) { issueCount } }`,
		},
	})
}

func TestVariablesAreInputTypes(t *testing.T) {
	runCases(t, []ruleCase{
		{
			name: "output types", schema: "github",
			query:  `query ($a: ⟦User⟧, $b: ⟦[SearchResultItem!]!⟧, $c: [IssueOrder!], $d: ⟦Node⟧ = 1, $e: DateTime, $f: IssueState) { viewer { id } }`,
			ignore: []string{CodeUnusedVariable},
			want: []want{
				{CodeNonInputVariableType, `Variable "$a" cannot be non-input type "User".`, nil},
				{CodeNonInputVariableType, `Variable "$b" cannot be non-input type "[SearchResultItem!]!".`, nil},
				{CodeNonInputVariableType, `Variable "$d" cannot be non-input type "Node".`, nil},
			},
		},
	})
}

func TestNoUndefinedVariables(t *testing.T) {
	runCases(t, []ruleCase{
		{
			name: "anonymous operation", schema: "github",
			query: `{ search(query: ⟦$q⟧, type: ISSUE) { issueCount } }`,
			want:  []want{{CodeUndefinedVariable, `Variable "$q" is not defined.`, nil}},
		},
		{
			name: "arguments and directives, at every usage", schema: "github",
			query: `query Q($a: String!) { search(query: $a, type: ⟦$t⟧) { issueCount } viewer @include(if: ⟦$inc⟧) { id } again: search(query: $a, type: ⟦$t⟧) { issueCount } }`,
			want: []want{
				{CodeUndefinedVariable, `Variable "$t" is not defined by operation "Q".`, nil},
				{CodeUndefinedVariable, `Variable "$inc" is not defined by operation "Q".`, nil},
				{CodeUndefinedVariable, `Variable "$t" is not defined by operation "Q".`, nil},
			},
		},
		{
			name: "lists and input objects", schema: "github",
			query: `mutation M { createIssue(input: {repositoryId: ⟦$r⟧, title: "t", labelIds: [⟦$l⟧]}) { issue { id } } }`,
			want: []want{
				{CodeUndefinedVariable, `Variable "$r" is not defined by operation "M".`, nil},
				{CodeUndefinedVariable, `Variable "$l" is not defined by operation "M".`, nil},
			},
		},
		{
			name: "fragments shared by operations", schema: "github",
			query: "query A($id: ID!) { node(id: $id) { ...F } }\n" +
				"query B { node(id: ⟦$id⟧) { ...F } }\n" +
				"fragment F on Node { ... on User { avatarUrl(size: ⟦⟦$size⟧⟧) } }",
			want: []want{
				{CodeUndefinedVariable, `Variable "$id" is not defined by operation "B".`, nil},
				{CodeUndefinedVariable, `Variable "$size" is not defined by operation "A".`, nil},
				{CodeUndefinedVariable, `Variable "$size" is not defined by operation "B".`, nil},
			},
		},
		{
			name: "fragments through fragments", schema: "github",
			query: `query Q($s: Int) { viewer { ...A } } fragment A on User { ...B avatarUrl(size: $s) } fragment B on User { a: avatarUrl(size: ⟦$t⟧) }`,
			want:  []want{{CodeUndefinedVariable, `Variable "$t" is not defined by operation "Q".`, nil}},
		},
		{
			name: "cyclic fragments", schema: "github",
			query: `query Q { viewer { ...A } } fragment A on User { avatarUrl(size: ⟦$s⟧) ...⟦A⟧ }`,
			want: []want{
				{CodeUndefinedVariable, "", nil},
				{CodeFragmentCycle, "", nil},
			},
		},
		{
			name: "unused fragments are not checked", schema: "github",
			query: `query Q { viewer { id } } fragment ⟦F⟧ on User { avatarUrl(size: $s) }`,
			want:  []want{{CodeUnusedFragment, "", nil}},
		},
	})
}

func TestNoUnusedVariables(t *testing.T) {
	runCases(t, []ruleCase{
		{
			name: "named operation", schema: "github",
			query: `query Q($a: Int, ⟦$b⟧: String, $c: Boolean!) { search(query: "x", type: ISSUE, first: $a) @include(if: $c) { issueCount } }`,
			want:  []want{{CodeUnusedVariable, `Variable "$b" is never used in operation "Q".`, nil}},
		},
		{
			name: "anonymous operation", schema: "github",
			query: `query (⟦$a⟧: Int) { viewer { id } }`,
			want:  []want{{CodeUnusedVariable, `Variable "$a" is never used.`, nil}},
		},
		{
			name: "used through a fragment by one operation only", schema: "github",
			query: "query A($s: Int) { viewer { ...F } }\nquery B(⟦$s⟧: Int) { viewer { id } }\nfragment F on User { avatarUrl(size: $s) }",
			want:  []want{{CodeUnusedVariable, `Variable "$s" is never used in operation "B".`, nil}},
		},
		{
			name: "used in nested values", schema: "github",
			query: `mutation M($r: ID!, $l: ID!, $t: String!) { createIssue(input: {repositoryId: $r, title: $t, labelIds: [$l]}) { issue { id } } }`,
		},
		{
			name: "not checked with an unknown fragment", schema: "github",
			query: `query Q($a: Int) { viewer { ...⟦Missing⟧ } }`,
			want:  []want{{CodeUnknownFragment, "", nil}},
		},
		{
			name: "not checked with an unknown fragment further down", schema: "github",
			query: `query Q($a: Int) { viewer { ...A } } fragment A on User { ...⟦Missing⟧ }`,
			want:  []want{{CodeUnknownFragment, "", nil}},
		},
	})
}

func TestVariablesInAllowedPosition(t *testing.T) {
	runCases(t, []ruleCase{
		{
			name: "nullable where non-null is expected", schema: "github",
			query: `query Q($q: String) { search(query: ⟦$q⟧, type: ISSUE) { issueCount } }`,
			want: []want{{CodeVariablePosition,
				`Variable "$q" of type "String" used in position expecting type "String!".`, nil}},
		},
		{
			name: "nullable with a default", schema: "github",
			query: `query Q($q: String = "x", $inc: Boolean = true) { search(query: $q, type: ISSUE) @include(if: $inc) { issueCount } }`,
		},
		{
			name: "null default is not enough", schema: "github",
			query: `query Q($q: String = null) { search(query: ⟦$q⟧, type: ISSUE) { issueCount } }`,
			want:  []want{{CodeVariablePosition, "", nil}},
		},
		{
			name: "location with a default", schema: "github",
			query: `query Q($w: Int, $d: Boolean) { viewer @cost(weight: $w) { ... @defer(if: $d) { id } } }`,
		},
		{
			name: "non-null where nullable is expected", schema: "github",
			query: `query Q($n: Int!, $s: [IssueState!]!) { search(query: "x", type: ISSUE, first: $n) { issueCount } repository(owner: "a", name: "b") { issues(states: $s) { totalCount } } }`,
		},
		{
			name: "lists", schema: "github",
			query: `query Q($id: ID!, $ids: [ID], $good: [ID!]!) { nodes(ids: ⟦$id⟧) { id } more: nodes(ids: ⟦$ids⟧) { id } good: nodes(ids: $good) { id } }`,
			want: []want{
				{CodeVariablePosition, `Variable "$id" of type "ID!" used in position expecting type "[ID!]!".`, nil},
				{CodeVariablePosition, `Variable "$ids" of type "[ID]" used in position expecting type "[ID!]!".`, nil},
			},
		},
		{
			name: "list items", schema: "github",
			query: `query Q($s: IssueState, $t: IssueState!) { repository(owner: "a", name: "b") { issues(states: [$t, ⟦$s⟧]) { totalCount } } }`,
			want: []want{{CodeVariablePosition,
				`Variable "$s" of type "IssueState" used in position expecting type "IssueState!".`, nil}},
		},
		{
			name: "different named types", schema: "github",
			query: `query Q($n: Int!, $e: SearchType) { node(id: ⟦$n⟧) { id } search(query: "x", type: ⟦$e⟧) { issueCount } }`,
			want: []want{
				{CodeVariablePosition, `Variable "$n" of type "Int!" used in position expecting type "ID!".`, nil},
				{CodeVariablePosition, `Variable "$e" of type "SearchType" used in position expecting type "SearchType!".`, nil},
			},
		},
		{
			name: "fragments are checked per operation", schema: "github",
			query: "query A($size: Int) { viewer { ...F } }\nquery B($size: String) { viewer { ...F } }\nfragment F on User { avatarUrl(size: ⟦$size⟧) }",
			want: []want{{CodeVariablePosition,
				`Variable "$size" of type "String" used in position expecting type "Int".`, nil}},
		},
		{
			name: "oneOf fields need non-null variables", schema: "github",
			query: `query Q($id: ID, $n: String!) { a: lookupRepository(by: {id: ⟦$id⟧}) { id } b: lookupRepository(by: {nameWithOwner: $n}) { id } }`,
			want: []want{{CodeOneOf,
				`Variable "$id" is of type "ID" but must be non-nullable to be used for OneOf Input Object "RepositoryLookup".`, nil}},
		},
		{
			name: "input object variables", schema: "github",
			query: `query Q($o: IssueOrder, $l: RepositoryLookup!, $bad: IssueOrder) { repository(owner: "a", name: "b") { issues(orderBy: $o) { totalCount } } lookupRepository(by: $l) { id } other: lookupRepository(by: ⟦$bad⟧) { id } }`,
			want: []want{{CodeVariablePosition,
				`Variable "$bad" of type "IssueOrder" used in position expecting type "RepositoryLookup!".`, nil}},
		},
		{
			name: "unknown and output variable types are other rules'", schema: "github",
			query: `query Q($a: ⟦Nope⟧, $b: ⟦User⟧) { node(id: $a) { id } nodes(ids: [$b]) { id } }`,
			want: []want{
				{CodeUnknownType, "", nil},
				{CodeNonInputVariableType, "", nil},
			},
		},
		{
			name: "unknown positions are not checked", schema: "github",
			query: `query Q($a: Int) { viewer { ⟦bogus⟧(x: $a) avatarUrl(⟦sise⟧: $a) } }`,
			want: []want{
				{CodeUnknownField, "", nil},
				{CodeUnknownArgument, "", nil},
			},
		},
	})
}
