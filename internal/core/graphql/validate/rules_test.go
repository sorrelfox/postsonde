package validate

import "testing"

func TestFieldsOnCorrectType(t *testing.T) {
	runCases(t, []ruleCase{
		{
			name: "misspelled field", schema: "github",
			query: `{ viewer { ⟦logn⟧ } }`,
			want:  []want{{CodeUnknownField, `Cannot query field "logn" on type "User". Did you mean "login"?`, []string{"login"}}},
		},
		{
			name: "no suggestion", schema: "github",
			query: `{ viewer { ⟦zzzzzz⟧ } }`,
			want:  []want{{CodeUnknownField, `Cannot query field "zzzzzz" on type "User".`, []string{}}},
		},
		{
			name: "query root not named Query", schema: "swapi",
			query: `{ ⟦allFilm⟧ { totalCount } }`,
			want:  []want{{CodeUnknownField, `Cannot query field "allFilm" on type "Root". Did you mean "allFilms" or "film"?`, []string{"allFilms", "film"}}},
		},
		{
			name: "union suggests inline fragments", schema: "github",
			query: `{ search(query: "x", type: ISSUE) { nodes { ⟦title⟧ } } }`,
			want: []want{{CodeUnknownField,
				`Cannot query field "title" on type "SearchResultItem". Did you mean to use an inline fragment on "Issue" or "PullRequest"?`, []string{}}},
		},
		{
			name: "interface suggests interfaces first", schema: "github",
			query: `{ node(id: "1") { ⟦stargazerCount⟧ } }`,
			want: []want{{CodeUnknownField,
				`Cannot query field "stargazerCount" on type "Node". Did you mean to use an inline fragment on "Starrable" or "Repository"?`, nil}},
		},
		{
			name: "interface suggestions ordered by usage", schema: "github",
			query: `{ node(id: "1") { ⟦url⟧ } }`,
			want: []want{{CodeUnknownField,
				`Cannot query field "url" on type "Node". Did you mean to use an inline fragment on "RepositoryOwner", "UniformResourceLocatable", "Organization", "Repository", or "User"?`, nil}},
		},
		{
			name: "field suggestions when no type has the field", schema: "github",
			query: `{ repositoryOwner(login: "a") { ⟦logins⟧ } }`,
			want:  []want{{CodeUnknownField, `Cannot query field "logins" on type "RepositoryOwner". Did you mean "login"?`, []string{"login"}}},
		},
		{
			name: "range is the name, not the alias", schema: "github",
			query: `{ me: ⟦viewr⟧ { login } }`,
			want:  []want{{CodeUnknownField, "", []string{"viewer"}}},
		},
		{
			name: "no cascade below an unknown field", schema: "github",
			query: `{ ⟦viewr⟧ { login bogus(x: 1) { a { b } } } }`,
			want:  []want{{CodeUnknownField, "", nil}},
		},
		{
			name: "inline fragment with a known type below an unknown field", schema: "github",
			query: `{ ⟦viewr⟧ { ... on Issue { ⟦titel⟧ } } }`,
			want: []want{
				{CodeUnknownField, "", []string{"viewer"}},
				{CodeUnknownField, `Cannot query field "titel" on type "Issue". Did you mean "title" or "state"?`, nil},
			},
		},
		{
			name: "meta fields", schema: "github",
			query: `{ __typename viewer { __typename ⟦__schema⟧ { description } } search(query: "x", type: USER) { nodes { __typename } } }`,
			want:  []want{{CodeUnknownField, `Cannot query field "__schema" on type "User".`, nil}},
		},
		{
			name: "mutation and subscription roots", schema: "github",
			query: "mutation M { ⟦addStars⟧(input: {starrableId: 1}) { clientMutationId } }\nsubscription S { issueEvents(repositoryId: 1) { ⟦actions⟧ } }",
			want: []want{
				{CodeUnknownField, `Cannot query field "addStars" on type "Mutation". Did you mean "addStar"?`, nil},
				{CodeUnknownField, `Cannot query field "actions" on type "IssueEvent". Did you mean "action"?`, nil},
			},
		},
		{
			name: "no cascade below an operation type the schema lacks", schema: "countries",
			query: `⟦mutation⟧ { anything { at all } }`,
			want:  []want{{CodeUnknownOperationType, "", nil}},
		},
		{
			name: "fragment definition", schema: "rickandmorty",
			query: `fragment C on Character { name ⟦specie⟧ } { character(id: 1) { ...C } }`,
			want:  []want{{CodeUnknownField, "", []string{"species"}}},
		},
	})
}

func TestKnownArgumentNames(t *testing.T) {
	runCases(t, []ruleCase{
		{
			name: "field argument", schema: "github",
			query: `{ repository(owner: "a", name: "b", ⟦folowRenames⟧: true) { id } }`,
			want: []want{{CodeUnknownArgument,
				`Unknown argument "folowRenames" on field "Query.repository". Did you mean "followRenames"?`, []string{"followRenames"}}},
		},
		{
			name: "field without arguments", schema: "countries",
			query: `{ countries { code(⟦x⟧: 1) } }`,
			want:  []want{{CodeUnknownArgument, `Unknown argument "x" on field "Country.code".`, []string{}}},
		},
		{
			name: "directive argument", schema: "github",
			query: `{ viewer @include(if: true, ⟦iff⟧: false) { id } }`,
			want:  []want{{CodeUnknownArgument, `Unknown argument "iff" on directive "@include". Did you mean "if"?`, []string{"if"}}},
		},
		{
			name: "directive arguments are not the field's", schema: "github",
			query: `{ repository(owner: "a", name: "b") @⟦skip⟧(⟦owner⟧: 1) { id } }`,
			want: []want{
				{CodeMissingArgument, `Directive "@skip" argument "if" of type "Boolean!" is required, but it was not provided.`, nil},
				{CodeUnknownArgument, `Unknown argument "owner" on directive "@skip".`, nil},
			},
		},
		{
			name: "custom directive", schema: "github",
			query: `{ viewer @cost(⟦weigth⟧: 2) { id } }`,
			want:  []want{{CodeUnknownArgument, "", []string{"weight"}}},
		},
		{
			name: "arguments of an unknown directive are not checked", schema: "github",
			query: `{ viewer @⟦nope⟧(x: "y") { id } }`,
			want:  []want{{CodeUnknownDirective, "", nil}},
		},
		{
			name: "meta field argument", schema: "swapi",
			query: `{ __type(name: "Film", ⟦nam⟧: "x") { name } }`,
			want:  []want{{CodeUnknownArgument, "", []string{"name"}}},
		},
	})
}

func TestProvidedRequiredArguments(t *testing.T) {
	runCases(t, []ruleCase{
		{
			name: "one missing", schema: "github",
			query: `{ ⟦repository⟧(owner: "a") { id } }`,
			want: []want{{CodeMissingArgument,
				`Field "repository" argument "name" of type "String!" is required, but it was not provided.`, nil}},
		},
		{
			name: "all missing, in definition order", schema: "github",
			query: `{ r: ⟦⟦repository⟧⟧ { id } }`,
			want: []want{
				{CodeMissingArgument, `Field "repository" argument "owner" of type "String!" is required, but it was not provided.`, nil},
				{CodeMissingArgument, `Field "repository" argument "name" of type "String!" is required, but it was not provided.`, nil},
			},
		},
		{
			name: "defaults and nullable arguments are optional", schema: "github",
			query: `{ repository(owner: "a", name: "b") { issues { totalCount } } viewer @cost { ... @defer { id } } }`,
		},
		{
			name: "list and input object arguments", schema: "github",
			query: "query Q { ⟦nodes⟧ { id } }\nmutation M { ⟦addStar⟧ { clientMutationId } }",
			want: []want{
				{CodeMissingArgument, `Field "nodes" argument "ids" of type "[ID!]!" is required, but it was not provided.`, nil},
				{CodeMissingArgument, `Field "addStar" argument "input" of type "AddStarInput!" is required, but it was not provided.`, nil},
			},
		},
		{
			name: "directive", schema: "github",
			query: `{ viewer @⟦include⟧ { id } }`,
			want: []want{{CodeMissingArgument,
				`Directive "@include" argument "if" of type "Boolean!" is required, but it was not provided.`, nil}},
		},
		{
			name: "meta field", schema: "countries",
			query: `{ ⟦__type⟧ { name } ⟦country⟧ { name } }`,
			want: []want{
				{CodeMissingArgument, `Field "__type" argument "name" of type "String!" is required, but it was not provided.`, nil},
				{CodeMissingArgument, `Field "country" argument "code" of type "ID!" is required, but it was not provided.`, nil},
			},
		},
		{
			name: "null counts as given", schema: "rickandmorty",
			query: `{ character(id: ⟦null⟧) { name } }`,
			want:  []want{{CodeInvalidValue, `Expected value of type "ID!", found null.`, nil}},
		},
	})
}

func TestValuesOfCorrectType(t *testing.T) {
	runCases(t, []ruleCase{
		// Scalars.
		{
			name: "Int", schema: "github",
			query: `{ search(query: "x", type: ISSUE, first: ⟦"10"⟧) { a: issueCount } b: search(query: "x", type: ISSUE, first: ⟦TEN⟧) { issueCount } }`,
			want: []want{
				{CodeInvalidValue, `Expected value of type "Int", found "10".`, nil},
				{CodeInvalidValue, `Expected value of type "Int", found TEN.`, nil},
			},
		},
		{
			name: "Int range", schema: "github",
			query: `{ a: search(query: "x", type: ISSUE, first: ⟦2147483648⟧) { issueCount } b: search(query: "x", type: ISSUE, first: -2147483648) { issueCount } c: search(query: "x", type: ISSUE, first: ⟦-99999999999999999999⟧) { issueCount } }`,
			want: []want{
				{CodeInvalidValue, `Expected value of type "Int", found 2147483648; Int cannot represent non 32-bit signed integer value: 2147483648`, nil},
				{CodeInvalidValue, "", nil},
			},
		},
		{
			name: "Float", schema: "github",
			ignore: []string{CodeUnusedVariable},
			query:  `query ($a: Float = 1, $b: Float = -1.5e3, $c: Float = ⟦"x"⟧, $d: Float! = ⟦1e400⟧, $e: Float = ⟦true⟧) { viewer { id } }`,
			want: []want{
				{CodeInvalidValue, `Expected value of type "Float", found "x".`, nil},
				{CodeInvalidValue, `Expected value of type "Float!", found 1e400; Float cannot represent non numeric value: 1e400`, nil},
				{CodeInvalidValue, "", nil},
			},
		},
		{
			name: "String", schema: "github",
			query: `{ repository(owner: ⟦1⟧, name: """block""") { id } }`,
			want:  []want{{CodeInvalidValue, `Expected value of type "String!", found 1.`, nil}},
		},
		{
			name: "Boolean", schema: "github",
			query: `{ repository(owner: "a", name: "b", followRenames: ⟦"yes"⟧) { id } viewer @skip(if: ⟦0⟧) { id } }`,
			want: []want{
				{CodeInvalidValue, `Expected value of type "Boolean", found "yes".`, nil},
				{CodeInvalidValue, `Expected value of type "Boolean!", found 0.`, nil},
			},
		},
		{
			name: "ID accepts strings and ints only", schema: "github",
			query: `{ a: node(id: "x") { id } b: node(id: 42) { id } c: node(id: ⟦4.5⟧) { id } d: node(id: ⟦true⟧) { id } e: node(id: ⟦X⟧) { id } }`,
			want: []want{
				{CodeInvalidValue, `Expected value of type "ID!", found 4.5.`, nil},
				{CodeInvalidValue, `Expected value of type "ID!", found true.`, nil},
				{CodeInvalidValue, `Expected value of type "ID!", found X.`, nil},
			},
		},
		{
			name: "custom scalars accept any literal", schema: "github",
			ignore: []string{CodeUnusedVariable},
			query:  `query ($d: DateTime = {any: [1, "x", ENUM, null]}, $u: URI! = 3, $b: [BigInt] = [[1]]) { viewer { id } }`,
		},
		{
			name: "null", schema: "github",
			query: `{ node(id: ⟦null⟧) { id } repository(owner: "a", name: "b", followRenames: null) { id } }`,
			want:  []want{{CodeInvalidValue, `Expected value of type "ID!", found null.`, nil}},
		},
		// Enums.
		{
			name: "unknown enum value", schema: "github",
			query: `{ search(query: "x", type: ⟦ISSUES⟧) { issueCount } }`,
			want: []want{{CodeUnknownEnumValue,
				`Value "ISSUES" does not exist in "SearchType" enum. Did you mean the enum value "ISSUE"?`, []string{"ISSUE"}}},
		},
		{
			name: "string for an enum", schema: "github",
			query: `{ search(query: "x", type: ⟦"ISSUE"⟧) { issueCount } }`,
			want: []want{{CodeInvalidValue,
				`Expected value of type "SearchType!", found "ISSUE"; Enum "SearchType" cannot represent non-enum value: "ISSUE". Did you mean the enum value "ISSUE"?`, []string{"ISSUE"}}},
		},
		{
			name: "other literals for an enum", schema: "github",
			query: `{ search(query: "x", type: ⟦1⟧) { issueCount } }`,
			want:  []want{{CodeInvalidValue, `Expected value of type "SearchType!", found 1.`, nil}},
		},
		{
			name: "introspection enum", schema: "countries",
			query: `{ __schema { types { fields(includeDeprecated: ⟦TRUE⟧) { name } } } }`,
			want:  []want{{CodeInvalidValue, `Expected value of type "Boolean", found TRUE.`, nil}},
		},
		// Lists.
		{
			name: "list items", schema: "github",
			query: `{ nodes(ids: ["a", 1, ⟦null⟧, ⟦2.5⟧]) { id } }`,
			want: []want{
				{CodeInvalidValue, `Expected value of type "ID!", found null.`, nil},
				{CodeInvalidValue, `Expected value of type "ID!", found 2.5.`, nil},
			},
		},
		{
			name: "single value coerced to a list", schema: "github",
			query: `{ a: nodes(ids: "a") { id } b: nodes(ids: ⟦true⟧) { id } c: nodes(ids: ⟦null⟧) { id } }`,
			want: []want{
				{CodeInvalidValue, `Expected value of type "ID!", found true.`, nil},
				{CodeInvalidValue, `Expected value of type "[ID!]!", found null.`, nil},
			},
		},
		{
			name: "enum lists", schema: "github",
			query: `{ repository(owner: "a", name: "b") { a: issues(states: OPEN) { totalCount } b: issues(states: [OPEN, ⟦MERGED⟧]) { totalCount } } }`,
			want:  []want{{CodeUnknownEnumValue, `Value "MERGED" does not exist in "IssueState" enum.`, []string{}}},
		},
		{
			name: "nested lists", schema: "github",
			ignore: []string{CodeUnusedVariable},
			query:  `query ($a: [[String!]!] = [["a"], [⟦1⟧], "b"], $b: [[Int]] = 1, $c: [[Int]] = [[⟦[1]⟧]]) { viewer { id } }`,
			want: []want{
				{CodeInvalidValue, `Expected value of type "String!", found 1.`, nil},
				{CodeInvalidValue, `Expected value of type "Int", found [1].`, nil},
			},
		},
		{
			name: "list where no list is expected", schema: "github",
			query: `{ search(query: ⟦["x"]⟧, type: ISSUE) { issueCount } }`,
			want:  []want{{CodeInvalidValue, `Expected value of type "String!", found ["x"].`, nil}},
		},
		// Input objects.
		{
			name: "input object", schema: "github",
			query: `{ repository(owner: "a", name: "b") { issues(orderBy: {field: CREATED_AT, direction: ASC}, labels: "bug") { totalCount } } }`,
		},
		{
			name: "missing required input field", schema: "github",
			query: `{ repository(owner: "a", name: "b") { issues(orderBy: ⟦{field: CREATED_AT}⟧) { totalCount } } }`,
			want: []want{{CodeMissingInputField,
				`Field "IssueOrder.direction" of required type "OrderDirection!" was not provided.`, nil}},
		},
		{
			name: "unknown input field", schema: "github",
			query: `{ repository(owner: "a", name: "b") { issues(orderBy: {field: CREATED_AT, direction: ASC, ⟦directon⟧: DESC}) { totalCount } } }`,
			want: []want{{CodeUnknownInputField,
				`Field "directon" is not defined by type "IssueOrder". Did you mean "direction"?`, []string{"direction"}}},
		},
		{
			name: "input field values", schema: "github",
			query: `{ repository(owner: "a", name: "b") { issues(orderBy: {field: ⟦"CREATED_AT"⟧, direction: ⟦null⟧}) { totalCount } } }`,
			want: []want{
				{CodeInvalidValue, "", []string{"CREATED_AT", "UPDATED_AT"}},
				{CodeInvalidValue, `Expected value of type "OrderDirection!", found null.`, nil},
			},
		},
		{
			name: "wrong kinds for input objects", schema: "github",
			query: `{ repository(owner: "a", name: "b") { a: issues(orderBy: ⟦CREATED_AT⟧) { totalCount } b: issues(first: ⟦{a: 1, b: "c"}⟧) { totalCount } } }`,
			want: []want{
				{CodeInvalidValue, `Expected value of type "IssueOrder", found CREATED_AT.`, nil},
				{CodeInvalidValue, `Expected value of type "Int", found {a: 1, b: "c"}.`, nil},
			},
		},
		{
			name: "nested input objects", schema: "countries",
			query: `{ countries(filter: {code: {eq: "DE", in: ["AT", ⟦1⟧]}, name: {⟦regexp⟧: "^A"}}) { code } }`,
			want: []want{
				{CodeInvalidValue, `Expected value of type "String!", found 1.`, nil},
				{CodeUnknownInputField, `Field "regexp" is not defined by type "StringQueryOperatorInput". Did you mean "regex"?`, []string{"regex"}},
			},
		},
		{
			name: "mutation input with list default", schema: "github",
			query: `mutation { createIssue(input: ⟦{title: "t", labelIds: "1"}⟧) { issue { id } } }`,
			want: []want{{CodeMissingInputField,
				`Field "CreateIssueInput.repositoryId" of required type "ID!" was not provided.`, nil}},
		},
		{
			name: "oneOf", schema: "github",
			query: `{ a: lookupRepository(by: {id: "1"}) { id } b: lookupRepository(by: ⟦{id: "1", nameWithOwner: "a/b"}⟧) { id } c: lookupRepository(by: ⟦{}⟧) { id } d: lookupRepository(by: {nameWithOwner: ⟦null⟧}) { id } }`,
			want: []want{
				{CodeOneOf, `OneOf Input Object "RepositoryLookup" must specify exactly one key.`, nil},
				{CodeOneOf, `OneOf Input Object "RepositoryLookup" must specify exactly one key.`, nil},
				{CodeOneOf, `Field "RepositoryLookup.nameWithOwner" must be non-null.`, nil},
			},
		},
		{
			name: "oneOf with a variable", schema: "github",
			query: `query ($id: ID!) { lookupRepository(by: {id: $id}) { id } }`,
		},
		// Variables and other positions.
		{
			name: "variables are checked by other rules", schema: "github",
			query: `query ($n: Int) { repository(owner: ⟦$n⟧, name: ⟦$undefined⟧) { issues(orderBy: ⟦$n⟧, states: [⟦$n⟧]) { totalCount } } }`,
			want: []want{
				{CodeVariablePosition, "", nil},
				{CodeUndefinedVariable, "", nil},
				{CodeVariablePosition, "", nil},
				{CodeVariablePosition, "", nil},
			},
		},
		{
			name: "variable default values", schema: "github",
			ignore: []string{CodeUnusedVariable},
			query:  `query ($first: Int = ⟦"ten"⟧, $order: IssueOrder = ⟦{field: CREATED_AT}⟧, $s: [IssueState!] = [OPEN, ⟦open⟧]) { viewer { id } }`,
			want: []want{
				{CodeInvalidValue, `Expected value of type "Int", found "ten".`, nil},
				{CodeMissingInputField, "", nil},
				{CodeUnknownEnumValue, `Value "open" does not exist in "IssueState" enum. Did you mean the enum value "OPEN"?`, []string{"OPEN"}},
			},
		},
		{
			name: "variable with unknown or output type", schema: "github",
			query:  `query ($a: ⟦User⟧ = 1, $b: ⟦Nope⟧ = 2) { viewer { id } }`,
			want:   []want{{CodeNonInputVariableType, "", nil}, {CodeUnknownType, "", nil}},
			ignore: []string{CodeUnusedVariable},
		},
		{
			name: "directive argument values", schema: "github",
			query: `{ viewer @include(if: ⟦"yes"⟧) @cost(weight: ⟦1.5⟧) { id } }`,
			want: []want{
				{CodeInvalidValue, `Expected value of type "Boolean!", found "yes".`, nil},
				{CodeInvalidValue, `Expected value of type "Int!", found 1.5.`, nil},
			},
		},
		{
			name: "unknown argument values are not checked", schema: "github",
			query: `{ viewer { avatarUrl(⟦sise⟧: "big") } }`,
			want:  []want{{CodeUnknownArgument, "", []string{"size"}}},
		},
	})
}

func TestKnownTypeNames(t *testing.T) {
	runCases(t, []ruleCase{
		{
			name: "variable types", schema: "github",
			ignore: []string{CodeUnusedVariable},
			query:  `query ($a: ⟦Strin⟧, $b: [⟦IssueStat⟧!]!, $c: ⟦Zzzz⟧!) { viewer { id } }`,
			want: []want{
				{CodeUnknownType, `Unknown type "Strin". Did you mean "String" or "URI"?`, []string{"String", "URI"}},
				{CodeUnknownType, `Unknown type "IssueStat". Did you mean "IssueState", "Issue", or "IssueEvent"?`, []string{"IssueState", "Issue", "IssueEvent"}},
				{CodeUnknownType, `Unknown type "Zzzz".`, []string{}},
			},
		},
		{
			name: "built-in types not declared by the server", schema: "github",
			ignore: []string{CodeUnusedVariable},
			query:  `query ($f: Float, $g: ⟦Flaot⟧) { __schema { types { ... on __Type { name } } } }`,
			want:   []want{{CodeUnknownType, "", []string{"Float"}}},
		},
		{
			name: "inline fragment", schema: "github",
			query: `{ search(query: "x", type: ISSUE) { nodes { ... on ⟦Isue⟧ { title bogus } ... on Issue { title } } } }`,
			want:  []want{{CodeUnknownType, "", []string{"Issue"}}},
		},
		{
			name: "fragment definition", schema: "rickandmorty",
			query:  `fragment F on ⟦Charactr⟧ { anything { at all } }`,
			ignore: []string{CodeUnusedFragment},
			want:   []want{{CodeUnknownType, `Unknown type "Charactr". Did you mean "Character" or "Characters"?`, nil}},
		},
	})
}

func TestScalarLeafs(t *testing.T) {
	runCases(t, []ruleCase{
		{
			name: "selection on a scalar", schema: "github",
			query: `{ viewer { login ⟦{ x { y } }⟧ } }`,
			want: []want{{CodeSelectionOnLeaf,
				`Field "login" must not have a selection since type "String!" has no subfields.`, nil}},
		},
		{
			name: "selection on an enum", schema: "github",
			query: `{ node(id: "1") { ... on Issue { state ⟦{ name }⟧ } } }`,
			want: []want{{CodeSelectionOnLeaf,
				`Field "state" must not have a selection since type "IssueState!" has no subfields.`, nil}},
		},
		{
			name: "object without selection", schema: "github",
			query: `{ ⟦viewer⟧ }`,
			want: []want{{CodeMissingSelection,
				`Field "viewer" of type "User!" must have a selection of subfields. Did you mean "viewer { ... }"?`, nil}},
		},
		{
			name: "union and interface lists without selection", schema: "github",
			query: `{ search(query: "x", type: ISSUE) { ⟦nodes⟧ } ⟦nodes⟧(ids: []) @skip(if: false) }`,
			want: []want{
				{CodeMissingSelection, `Field "nodes" of type "[SearchResultItem]" must have a selection of subfields. Did you mean "nodes { ... }"?`, nil},
				{CodeMissingSelection, `Field "nodes" of type "[Node]!" must have a selection of subfields. Did you mean "nodes { ... }"?`, nil},
			},
		},
		{
			name: "nested list of scalars", schema: "github",
			query: `{ repository(owner: "a", name: "b") { topicMatrix } }`,
		},
		{
			name: "aliased", schema: "rickandmorty",
			query: `{ c: ⟦characters⟧(page: 1) episode(id: 1) { name ⟦{ a }⟧ } }`,
			want: []want{
				{CodeMissingSelection, "", nil},
				{CodeSelectionOnLeaf, "", nil},
			},
		},
	})
}

// TestHalfTyped checks documents with syntax errors: placeholders are never
// reported, the rest of the document still is.
func TestHalfTyped(t *testing.T) {
	cases := []ruleCase{
		{name: "open selection set", query: `{ viewer { `},
		{name: "empty selection set", query: `{ viewer { } }`},
		{name: "partial field name", query: `{ viewer { ⟦logi⟧`, want: []want{{CodeUnknownField, "", []string{"login"}}}},
		{
			name:  "open argument list",
			query: `{ ⟦repository⟧(owner: `,
			want:  []want{{CodeMissingArgument, "", nil}},
		},
		{name: "missing argument value", query: `{ search(query: "x", type: ) { issueCount } }`},
		{name: "missing argument name", query: `{ search(query: "x", type: ISSUE, : 1) { issueCount } }`},
		{name: "partial argument name", query: `{ search(query: "x", type: ISSUE, ⟦fir⟧) { issueCount } }`, want: []want{{CodeUnknownArgument, "", []string{"first"}}}},
		{name: "missing directive name", query: `{ viewer @ { id } }`},
		{name: "missing variable type", query: `query ($x: ) { viewer { id: __typename @include(if: $x) } }`},
		{name: "missing type condition", query: `{ node(id: 1) { ... on { anything } } }`},
		{name: "missing fragment name", query: `{ viewer { ... } }`},
		{name: "fragment without type condition", query: `fragment F { anything } { viewer { ...F } }`},
		{name: "unterminated string", query: `{ node(id: "abc) { id } }`},
		{name: "missing object field value", query: `{ lookupRepository(by: {id: }) { id } }`},
		{name: "missing object field name", query: `{ lookupRepository(by: {: "x"}) { id } }`},
		{
			name:  "partial required input field",
			query: `{ repository(owner: "a", name: "b") { issues(orderBy: {field: CREATED_AT, ⟦d⟧}) { totalCount } } }`,
			want:  []want{{CodeUnknownInputField, "", nil}},
		},
		{name: "unclosed object value", query: `{ repository(owner: "a", name: "b") { issues(orderBy: {field: CREATED_AT, `},
		{name: "operation without selection set", query: `query Q`},
		{name: "bare spread keeps fragments used", query: `fragment F on User { id } { viewer { ... } }`},
		{name: "bare spread keeps variables used", query: `query Q($a: Int) { viewer { ... } }`},
		{name: "unclosed operation keeps variables used", query: `query Q($a: Int, $b: String) { viewer { avatarUrl(size: $a) `},
		{name: "missing variable name in a usage", query: `{ search(query: "x", type: $) { issueCount } }`},
		{name: "missing variable name in a definition", query: `query Q($: Int) { viewer { id } }`},
		{name: "missing fragment name in a definition", query: `query ($a: Int) { viewer { avatarUrl(size: $a) } } fragment { id }`},
		{
			name:  "second operation being typed",
			query: `query A { viewer { id } } ⟦{⟧ viewer { ⟦logi⟧`,
			want:  []want{{CodeAnonymousOperation, "", nil}, {CodeUnknownField, "", nil}},
		},
		{
			name:  "subscription being typed",
			query: `subscription S($r: ID!) { issueEvents(repositoryId: $r) { action } ⟦⟦iss⟧⟧`,
			want:  []want{{CodeSingleFieldSubscription, "", nil}, {CodeUnknownField, "", nil}},
		},
		{
			name:  "SDL before an operation",
			query: "type T { a: Int }\n{ viewer { ⟦logn⟧ } }",
			want:  []want{{CodeUnknownField, "", nil}},
		},
	}
	for i := range cases {
		cases[i].schema = "github"
		cases[i].syntax = true
	}
	runCases(t, cases)
}
