package validate

import (
	"slices"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/graphql/ast"
	"github.com/sorrelfox/postsonde/internal/core/graphql/parser"
	"github.com/sorrelfox/postsonde/internal/core/graphql/schema"
)

// validDocuments are realistic operations for each fixture that use every
// construct the rules look at; they must produce no diagnostics.
var validDocuments = []struct{ schema, src string }{
	{"countries", `query Europe($code: ID! = "DE", $lang: String) {
  country(code: $code) {
    code name(lang: $lang) native emoji currencies
    continent { code name countries { code } }
    languages { code name rtl }
    states { code name }
    subdivisions { code emoji name }
  }
  countries(filter: {continent: {eq: "EU"}, code: {in: ["DE", "AT"], nin: "CH"}, name: {regex: "^A"}}) {
    ...CountryBits
  }
  continents(filter: {}) { code }
  languages { ... on Language { code } }
  __schema { queryType { name } types { name kind fields(includeDeprecated: true) { name args { name defaultValue } } } }
  __type(name: "Country") { name kind ofType { name } }
}
fragment CountryBits on Country { __typename code phone phones capital currency awsRegion }`},

	{"swapi", `query Films($first: Int = 3, $after: String, $withPlanets: Boolean!) {
  allFilms(first: $first, after: $after) {
    totalCount
    pageInfo { hasNextPage endCursor }
    edges { cursor node { ...FilmFields } }
  }
  node(id: "ZmlsbXM6MQ==") {
    id
    __typename
    ... on Film { title }
    ... on Planet @include(if: $withPlanets) { name population climates }
    ...StarshipFields
  }
  person(personID: 1) { name mass homeworld { name } filmConnection(first: 2) { films { title } } }
  planet(id: "x", planetID: "1") { name }
}
fragment FilmFields on Film { id title episodeID releaseDate producers created }
fragment StarshipFields on Starship { name MGLT hyperdriveRating manufacturers }`},

	{"rickandmorty", `query Characters($page: Int, $name: String = "rick") {
  characters(page: $page, filter: {name: $name, status: "alive"}) {
    info { count pages next prev }
    results { id name status species image origin { name dimension } episode { id episode air_date } }
  }
  charactersByIds(ids: [1, "2", 3]) { ...CharacterName }
  locations(filter: {name: "Earth"}) { results { residents { name } } }
  episodesByIds(ids: 1) { name characters { name } }
  episodes(filter: {episode: "S01"}) { results { name } }
}
fragment CharacterName on Character { name created }`},

	{"github", `query Search($q: String!, $type: SearchType = REPOSITORY, $after: String, $states: [IssueState!] = [OPEN], $order: IssueOrder = {field: UPDATED_AT, direction: ASC}, $skipOwner: Boolean = false) {
  search(query: $q, type: $type, first: 20, after: $after) {
    issueCount
    pageInfo { endCursor hasNextPage }
    nodes {
      __typename
      ... on Node { id }
      ... on Issue { number title state author { login avatarUrl(size: 40) } labels { name color } }
      ... on PullRequest { number merged }
      ... on Repository { nameWithOwner stargazerCount @cost(weight: 2) topicMatrix owner @skip(if: $skipOwner) { login url ... on User { name } } }
      ...Actor
    }
  }
  repository(owner: "octocat", name: "hello-world", followRenames: null) {
    issues(first: 10, states: $states, orderBy: $order, labels: ["bug", "help wanted"]) {
      totalCount
      nodes { ...IssueFields }
    }
    alt: issues(states: CLOSED, orderBy: {field: COMMENTS, direction: DESC}, labels: "bug") { totalCount }
    isPrivate pushedAt mirrorUrl hasWikiEnabled
  }
  lookupRepository(by: {nameWithOwner: "octocat/hello-world"}) { id }
  byId: lookupRepository(by: {id: 42}) { id }
  nodes(ids: ["a", "b"]) { id }
  viewer { login createdAt databaseId fullDatabaseId resourcePath }
  repositoryOwner(login: "github") { ... on Organization { name } ... { id } }
  __type(name: "RepositoryLookup") { isOneOf inputFields { name } }
}
fragment Actor on Actor { login }
fragment IssueFields on Issue { id title body }
mutation Star($id: ID!) {
  addStar(input: {starrableId: $id, clientMutationId: "m1"}) { clientMutationId starrable { stargazerCount ... on Repository { url } } }
  createIssue(input: {repositoryId: $id, title: "Bug", labelIds: ["l1"], assigneeIds: []}) { issue { id } }
}
subscription Events($repo: ID!) {
  issueEvents(repositoryId: $repo) { action at issue { title } }
}`},

	// Several operations sharing fragments, with variables used only
	// through fragments.
	{"github", `query Viewer($size: Int = 40) { viewer { ...UserBits } }
query Owner($login: String!, $size: Int, $withUrl: Boolean!) {
  repositoryOwner(login: $login) { ... on User { ...UserBits } ...OwnerBits @include(if: $withUrl) }
}
fragment UserBits on User { login avatarUrl(size: $size) ...NodeId }
fragment OwnerBits on RepositoryOwner { login url }
fragment NodeId on Node { id }
mutation Star($id: ID!) { addStar(input: {starrableId: $id}) { starrable { ...NodeId } } }
subscription Watch($repo: ID!) { events: issueEvents(repositoryId: $repo) { ...EventBits } }
fragment EventBits on IssueEvent { action issue { ...NodeId } }`},
}

func TestValidDocuments(t *testing.T) {
	for _, d := range validDocuments {
		t.Run(d.schema, func(t *testing.T) {
			doc, errs := parser.Parse(d.src)
			if len(errs) > 0 {
				t.Fatalf("syntax errors: %v", errs)
			}
			if got := Validate(loadSchema(t, d.schema), doc); len(got) > 0 {
				t.Errorf("diagnostics:\n%s", describe(d.src, got))
			}
		})
	}
}

func TestValidateNil(t *testing.T) {
	doc, _ := parser.Parse(`{ a }`)
	if got := Validate(nil, doc); got != nil {
		t.Errorf("nil schema: %v", got)
	}
	if got := Validate(loadSchema(t, "github"), nil); got != nil {
		t.Errorf("nil document: %v", got)
	}
}

// TestValidateBuiltinsOnly checks a schema without root types: nothing in
// the operation can be resolved, so nothing is reported except the missing
// root type and unknown types.
func TestValidateBuiltinsOnly(t *testing.T) {
	src := `query ($a: Int, $b: Nope) { anything(x: $a, y: $b) { ...F } } fragment F on __Type { name bogus }`
	doc, _ := parser.Parse(src)
	got := Validate(schema.Builtins(), doc)
	var codes []string
	for _, d := range got {
		codes = append(codes, d.Code+" "+src[d.Start:d.End])
	}
	if want := []string{CodeUnknownOperationType + " query", CodeUnknownType + " Nope", CodeUnknownField + " bogus"}; !slices.Equal(codes, want) {
		t.Errorf("got %q, want %q", codes, want)
	}
}

func TestDiagnosticsSortedByStart(t *testing.T) {
	src := `query ($x: Nope = 1) { viewer @include(if: "no") { logn } repository }`
	doc, _ := parser.Parse(src)
	got := Validate(loadSchema(t, "github"), doc)
	if len(got) < 4 {
		t.Fatalf("got %d diagnostics:\n%s", len(got), describe(src, got))
	}
	if !slices.IsSortedFunc(got, func(a, b Diagnostic) int { return a.Start - b.Start }) {
		t.Errorf("not sorted:\n%s", describe(src, got))
	}
}

func TestWalkSkipsPerVisitor(t *testing.T) {
	doc, _ := parser.Parse(`{ a { b } c }`)
	var skipper, other []string
	name := func(n ast.Node) (string, bool) {
		if f, ok := n.(*ast.Field); ok {
			return f.Name.Value, true
		}
		return "", false
	}
	Walk(doc, nil,
		Visitor{
			Enter: func(n ast.Node) bool {
				if s, ok := name(n); ok {
					skipper = append(skipper, "enter "+s)
					return s != "a"
				}
				return true
			},
			Leave: func(n ast.Node) {
				if s, ok := name(n); ok {
					skipper = append(skipper, "leave "+s)
				}
			},
		},
		Visitor{Enter: func(n ast.Node) bool {
			if s, ok := name(n); ok {
				other = append(other, s)
			}
			return true
		}},
	)
	if want := []string{"enter a", "enter c", "leave c"}; !slices.Equal(skipper, want) {
		t.Errorf("skipping visitor saw %q, want %q", skipper, want)
	}
	if want := []string{"a", "b", "c"}; !slices.Equal(other, want) {
		t.Errorf("other visitor saw %q, want %q", other, want)
	}
}

func TestContextFragment(t *testing.T) {
	src := `fragment A on User { id } fragment A on Issue { id } fragment on on User { id } { viewer { ...A } }`
	doc, _ := parser.Parse(src)
	var c *Context
	ValidateRules(loadSchema(t, "github"), doc, []Rule{{Name: "capture", New: func(ctx *Context) Visitor {
		c = ctx
		return Visitor{}
	}}})
	f := c.Fragment("A")
	if f == nil || f.TypeCondition.Type.Name.Value != "User" {
		t.Errorf("Fragment(A) = %+v, want the first definition", f)
	}
	if c.Fragment("B") != nil || c.Fragment("") != nil {
		t.Error("unknown fragments resolved")
	}
}

func TestPrintValue(t *testing.T) {
	for _, tc := range []struct{ src, want string }{
		{`1`, `1`},
		{`-1.5e3`, `-1.5e3`},
		{"\"a\\\"b\\\\c\\nd\\u0001é\"", "\"a\\\"b\\\\c\\nd\\u0001é\""},
		{`"""block "quote" """`, `"block \"quote\" "`},
		{`true`, `true`},
		{`null`, `null`},
		{`ENUM`, `ENUM`},
		{`$var`, `$var`},
		{"[1,2 , [3]]", `[1, 2, [3]]`},
		{`{a: 1, b: {c: [X]}}`, `{a: 1, b: {c: [X]}}`},
		{`"` + strings.Repeat("x", 100) + `"`, `"` + strings.Repeat("x", 56) + `...`},
		{`[` + strings.Repeat(`"ab", `, 200) + `]`, `[` + strings.Repeat(`"ab", `, 9) + `"a...`},
	} {
		doc, errs := parser.Parse(`{ f(a: ` + tc.src + `) }`)
		if len(errs) > 0 {
			t.Fatalf("%s: %v", tc.src, errs)
		}
		v := doc.Definitions[0].(*ast.OperationDefinition).SelectionSet.Selections[0].(*ast.Field).Arguments.Arguments[0].Value
		if got := printValue(v); got != tc.want {
			t.Errorf("printValue(%s) = %s, want %s", tc.src, got, tc.want)
		}
	}
}
