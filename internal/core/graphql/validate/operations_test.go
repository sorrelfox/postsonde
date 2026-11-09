package validate

import "testing"

func TestUniqueOperationNames(t *testing.T) {
	runCases(t, []ruleCase{
		{
			name: "duplicates across operation types", schema: "github",
			query: "query A { viewer { id } }\nquery ⟦A⟧ { viewer { login } }\nmutation ⟦A⟧ { addStar(input: {starrableId: 1}) { clientMutationId } }",
			want: []want{
				{CodeDuplicateOperationName, `There can be only one operation named "A".`, nil},
				{CodeDuplicateOperationName, `There can be only one operation named "A".`, nil},
			},
		},
		{
			name: "fragments do not share the namespace", schema: "github",
			query: `query A { viewer { ...A } } query B { viewer { id } } fragment A on User { id }`,
		},
	})
}

func TestLoneAnonymousOperation(t *testing.T) {
	runCases(t, []ruleCase{
		{
			name: "shorthand next to a named operation", schema: "github",
			query: `⟦{⟧ viewer { id } } query B { viewer { id } }`,
			want:  []want{{CodeAnonymousOperation, `This anonymous operation must be the only defined operation.`, nil}},
		},
		{
			name: "every anonymous operation", schema: "github",
			query: `⟦query⟧ { viewer { id } } ⟦mutation⟧ { addStar(input: {starrableId: 1}) { clientMutationId } }`,
			want: []want{
				{CodeAnonymousOperation, "", nil},
				{CodeAnonymousOperation, "", nil},
			},
		},
		{
			name: "alone with fragments", schema: "github",
			query: `{ viewer { ...U } } fragment U on User { id }`,
		},
	})
}

func TestKnownOperationTypes(t *testing.T) {
	runCases(t, []ruleCase{
		{
			name: "read-only schema", schema: "countries",
			query: "⟦mutation⟧ M { anything }\n⟦subscription⟧ S { anything { at all } }\nquery Q { continents { code } }",
			want: []want{
				{CodeUnknownOperationType, `The mutation operation is not supported by the schema.`, nil},
				{CodeUnknownOperationType, `The subscription operation is not supported by the schema.`, nil},
			},
		},
		{
			name: "variables are still checked by name", schema: "countries",
			query: `⟦mutation⟧ ($id: ID!) { add(id: $id, other: ⟦$other⟧) }`,
			want: []want{
				{CodeUnknownOperationType, "", nil},
				{CodeUndefinedVariable, `Variable "$other" is not defined.`, nil},
			},
		},
		{
			name: "every root", schema: "github",
			query: `query Q { viewer { id } } mutation M { addStar(input: {starrableId: 1}) { clientMutationId } } subscription S { issueEvents(repositoryId: 1) { action } }`,
		},
	})
}

func TestSingleFieldSubscriptions(t *testing.T) {
	runCases(t, []ruleCase{
		{
			name: "one field", schema: "github",
			query: `subscription { issueEvents(repositoryId: 1) { action } }`,
		},
		{
			name: "second root field, at its name", schema: "github",
			query: `subscription S { issueEvents(repositoryId: 1) { action } b: ⟦issueEvents⟧(repositoryId: 2) { action } }`,
			want:  []want{{CodeSingleFieldSubscription, `Subscription "S" must select only one top level field.`, nil}},
		},
		{
			name: "same response key is one field", schema: "github",
			query: `subscription { issueEvents(repositoryId: 1) { action } issueEvents(repositoryId: 1) { at } }`,
		},
		{
			name: "through fragments", schema: "github",
			query: "subscription { ...E ... on Subscription { b: ⟦issueEvents⟧(repositoryId: 2) { action } } }\n" +
				"fragment E on Subscription { issueEvents(repositoryId: 1) { action } ...E2 }\n" +
				"fragment E2 on Subscription { c: ⟦issueEvents⟧(repositoryId: 3) { action } }",
			want: []want{
				{CodeSingleFieldSubscription, `Anonymous Subscription must select only one top level field.`, nil},
				{CodeSingleFieldSubscription, `Anonymous Subscription must select only one top level field.`, nil},
			},
		},
		{
			name: "introspection field", schema: "github",
			query: `subscription { ⟦__typename⟧ }`,
			want:  []want{{CodeSingleFieldSubscription, `Anonymous Subscription must not select an introspection top level field.`, nil}},
		},
		{
			name: "extra introspection field", schema: "github",
			query: `subscription S { issueEvents(repositoryId: 1) { action } ⟦⟦__typename⟧⟧ }`,
			want: []want{
				{CodeSingleFieldSubscription, `Subscription "S" must select only one top level field.`, nil},
				{CodeSingleFieldSubscription, `Subscription "S" must not select an introspection top level field.`, nil},
			},
		},
		{
			name: "unknown and cyclic fragments", schema: "github",
			query: `subscription { ...⟦Nope⟧ ...C } fragment C on Subscription { ...⟦C⟧ issueEvents(repositoryId: 1) { action } }`,
			want: []want{
				{CodeUnknownFragment, `Unknown fragment "Nope".`, []string{}},
				{CodeFragmentCycle, `Cannot spread fragment "C" within itself.`, nil},
			},
		},
	})
}
