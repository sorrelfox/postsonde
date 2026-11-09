package validate

import (
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/graphql/ast"
	"github.com/sorrelfox/postsonde/internal/core/graphql/parser"
)

// TestTypeInfoAt checks the context at a cursor the way the completion
// engine uses it: ast.NodePathAt, then EnterPath. "|" marks the cursor.
func TestTypeInfoAt(t *testing.T) {
	type ctx struct {
		parent, typ, field, directive, arg, input, parentInput, enum, def string
	}
	for _, tc := range []struct {
		name, query string
		want        ctx
	}{
		{
			name:  "field in nested selection",
			query: `{ repository(owner: "a", name: "b") { issues { nodes { tit|le } } } }`,
			want:  ctx{parent: "Issue", typ: "String!", field: "title"},
		},
		{
			name:  "enum inside an input object argument",
			query: `{ repository(owner: "a", name: "b") { issues(orderBy: {field: CREATED|_AT}) { totalCount } } }`,
			want:  ctx{parent: "Repository", typ: "IssueConnection!", field: "issues", arg: "orderBy", input: "IssueOrderField!", parentInput: "IssueOrder", enum: "CREATED_AT"},
		},
		{
			name:  "argument with a default",
			query: `{ repository(owner: "a", name: "b") { issues(orderBy: |{field: CREATED_AT, direction: ASC}) { totalCount } } }`,
			want:  ctx{parent: "Repository", typ: "IssueConnection!", field: "issues", arg: "orderBy", input: "IssueOrder", def: "{field: CREATED_AT, direction: DESC}"},
		},
		{
			name:  "input field with a default",
			query: `mutation ($ids: [ID!]) { createIssue(input: {repositoryId: 1, title: "t", assigneeIds: $i|ds}) { issue { id } } }`,
			want:  ctx{parent: "Mutation", typ: "CreateIssuePayload", field: "createIssue", arg: "input", input: "[ID!]", parentInput: "CreateIssueInput!", def: "[]"},
		},
		{
			name:  "list items have no default",
			query: `mutation { createIssue(input: {repositoryId: 1, title: "t", assigneeIds: ["|a"]}) { issue { id } } }`,
			want:  ctx{parent: "Mutation", typ: "CreateIssuePayload", field: "createIssue", arg: "input", input: "ID!", parentInput: "[ID!]"},
		},
		{
			name:  "directive argument with a default",
			query: `{ viewer @cost(weight: |2) { id } }`,
			want:  ctx{parent: "Query", typ: "User!", field: "viewer", directive: "cost", arg: "weight", input: "Int!", def: "1"},
		},
		{
			name:  "missing value in an input object",
			query: `{ repository(owner: "a", name: "b") { issues(orderBy: {direction: | }) { totalCount } } }`,
			want:  ctx{parent: "Repository", typ: "IssueConnection!", field: "issues", arg: "orderBy", input: "OrderDirection!", parentInput: "IssueOrder"},
		},
		{
			name:  "list item",
			query: `{ nodes(ids: ["|a"]) { id } }`,
			want:  ctx{parent: "Query", typ: "[Node]!", field: "nodes", arg: "ids", input: "ID!", parentInput: "[ID!]!"},
		},
		{
			name:  "directive argument on a field",
			query: `{ viewer @include(if: tr|ue) { id } }`,
			want:  ctx{parent: "Query", typ: "User!", field: "viewer", directive: "include", arg: "if", input: "Boolean!"},
		},
		{
			name:  "unknown directive argument is not the field's",
			query: `{ repositoryOwner(login: "a") @nope(login: "|x") { id } }`,
			want:  ctx{parent: "Query", typ: "RepositoryOwner", field: "repositoryOwner"},
		},
		{
			name:  "inline fragment without type condition",
			query: `{ viewer { ... { log|in } } }`,
			want:  ctx{parent: "User", typ: "String!", field: "login"},
		},
		{
			name:  "inline fragment on an abstract type",
			query: `{ search(query: "x", type: ISSUE) { nodes { ... on PullRequest { mer|ged } } } }`,
			want:  ctx{parent: "PullRequest", typ: "Boolean!", field: "merged"},
		},
		{
			name:  "empty selection set",
			query: `{ viewer { | } }`,
			want:  ctx{parent: "User"},
		},
		{
			name:  "fragment definition",
			query: `fragment F on Repository { owner { log|in } }`,
			want:  ctx{parent: "RepositoryOwner", typ: "String!", field: "login"},
		},
		{
			name:  "variable default value",
			query: `query ($s: [IssueState!] = [OP|EN]) { viewer { id } }`,
			want:  ctx{typ: "Query", input: "IssueState!", parentInput: "[IssueState!]", enum: "OPEN"},
		},
		{
			name:  "single value coerced to a list",
			query: `query ($s: [IssueState!] = OP|EN) { viewer { id } }`,
			want:  ctx{typ: "Query", input: "[IssueState!]", enum: "OPEN"},
		},
		{
			name:  "meta field",
			query: `{ __type(name: "|X") { name } }`,
			want:  ctx{parent: "Query", typ: "__Type", field: "__type", arg: "name", input: "String!"},
		},
		{
			name:  "mutation root",
			query: `mutation { addStar(input: {starrableId: "|1"}) { clientMutationId } }`,
			want:  ctx{parent: "Mutation", typ: "AddStarPayload", field: "addStar", arg: "input", input: "ID!", parentInput: "AddStarInput!"},
		},
		{
			name:  "below an unknown field",
			query: `{ bogus { na|me } }`,
			want:  ctx{},
		},
		{
			name:  "below a leaf field",
			query: `{ viewer { login { na|me } } }`,
			want:  ctx{},
		},
		{
			name:  "unknown type condition",
			query: `{ viewer { ... on Nope { na|me } } }`,
			want:  ctx{},
		},
		{
			name:  "input type of an output type variable",
			query: `query ($u: User = {|}) { viewer { id } }`,
			want:  ctx{typ: "Query"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			off := strings.Index(tc.query, "|")
			src := strings.Replace(tc.query, "|", "", 1)
			doc, _ := parser.Parse(src)
			ti := NewTypeInfo(loadSchema(t, "github"))
			ti.EnterPath(ast.NodePathAt(doc, off))
			got := ctx{}
			if p := ti.ParentType(); p != nil {
				got.parent = p.Name
			}
			if r := ti.Type(); r != nil {
				got.typ = r.String()
			}
			if f := ti.FieldDef(); f != nil {
				got.field = f.Name
			}
			if d := ti.Directive(); d != nil {
				got.directive = d.Name
			}
			if a := ti.Argument(); a != nil {
				got.arg = a.Name
			}
			if r := ti.InputType(); r != nil {
				got.input = r.String()
			}
			if r := ti.ParentInputType(); r != nil {
				got.parentInput = r.String()
			}
			if e := ti.EnumValue(); e != nil {
				got.enum = e.Name
			}
			if d := ti.DefaultValue(); d != nil {
				got.def = *d
			}
			if got != tc.want {
				t.Errorf("at %d:\n got %+v\nwant %+v", off, got, tc.want)
			}
		})
	}
}

// TestTypeInfoBalanced checks that Walk leaves a TypeInfo empty, also for
// half-typed documents.
func TestTypeInfoBalanced(t *testing.T) {
	srcs := []string{`{ viewer { ... on User @include(if: true) { login } } }`, `query ($a: [Int] = [1, {b: 2}]) { nodes(ids: [`}
	for _, d := range validDocuments {
		srcs = append(srcs, d.src)
	}
	for _, src := range srcs {
		doc, _ := parser.Parse(src)
		ti := NewTypeInfo(loadSchema(t, "github"))
		Walk(doc, ti)
		if len(ti.types)+len(ti.parentTypes)+len(ti.fieldDefs)+len(ti.inputTypes)+len(ti.defaults)+len(ti.arguments) != 0 ||
			ti.directive != nil || ti.inDirective || ti.enumValue != nil {
			t.Errorf("unbalanced after %q: %+v", src, ti)
		}
	}
}
