package validate

import (
	"bufio"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/graphql/ast"
	"github.com/sorrelfox/postsonde/internal/core/graphql/parser"
)

// FuzzValidate checks that validation never panics on whatever the parser
// makes of arbitrary input, against every fixture schema; that diagnostics
// have a known code, a message, a non-empty range within the source (so no
// placeholder is reported), at most MaxSuggestions suggestions and are
// sorted; that validation is deterministic; and that TypeInfo survives
// entering the path at any offset.
//
// Seeds: the parser's golden documents and fuzz corpus, and the queries of
// this package's tests. Run it with
// go test ./internal/core/graphql/validate -run '^$' -fuzz FuzzValidate -fuzztime 60s
func FuzzValidate(f *testing.F) {
	for _, s := range fuzzSeeds(f) {
		f.Add(s)
	}
	names := []string{"github", "countries", "swapi", "rickandmorty"}
	for _, n := range names {
		loadSchema(f, n)
	}
	codes := map[string]bool{
		CodeUnknownField: true, CodeUnknownArgument: true, CodeMissingArgument: true, CodeInvalidValue: true,
		CodeUnknownEnumValue: true, CodeUnknownInputField: true, CodeMissingInputField: true, CodeOneOf: true,
		CodeUnknownType: true, CodeSelectionOnLeaf: true, CodeMissingSelection: true,
		CodeDuplicateOperationName: true, CodeAnonymousOperation: true, CodeUnknownOperationType: true,
		CodeSingleFieldSubscription: true, CodeUnknownFragment: true, CodeDuplicateFragmentName: true,
		CodeFragmentOnNonComposite: true, CodeImpossibleFragmentSpread: true, CodeUnusedFragment: true,
		CodeFragmentCycle: true, CodeDuplicateVariable: true, CodeNonInputVariableType: true,
		CodeUndefinedVariable: true, CodeUnusedVariable: true, CodeVariablePosition: true,
		CodeUnknownDirective: true, CodeMisplacedDirective: true, CodeDuplicateDirective: true,
		CodeDuplicateArgument: true, CodeDuplicateInputField: true,
	}
	f.Fuzz(func(t *testing.T, src string) {
		doc, _ := parser.Config{MaxDepth: 20, MaxErrors: 30}.Parse(src)
		s := loadSchema(t, names[len(src)%len(names)])
		got := Validate(s, doc)
		for i, d := range got {
			if !codes[d.Code] || d.Message == "" || d.Severity != SeverityError {
				t.Fatalf("bad diagnostic %+v", d)
			}
			if d.Start < 0 || d.Start >= d.End || d.End > len(src) {
				t.Fatalf("bad range %+v in %d bytes", d, len(src))
			}
			if len(d.Suggestions) > MaxSuggestions {
				t.Fatalf("%d suggestions", len(d.Suggestions))
			}
			if i > 0 && got[i-1].Start > d.Start {
				t.Fatalf("not sorted: %+v before %+v", got[i-1], d)
			}
		}
		if again := Validate(s, doc); !reflect.DeepEqual(got, again) {
			t.Fatalf("not deterministic:\n%+v\n%+v", got, again)
		}
		for _, off := range []int{0, len(src) / 3, len(src) / 2, len(src)} {
			ti := NewTypeInfo(s)
			ti.EnterPath(ast.NodePathAt(doc, off))
			_, _, _, _ = ti.ParentType(), ti.InputType(), ti.ParentInputType(), ti.Argument()
		}
	})
}

// fuzzSeeds collects the sources of the parser's golden files, the parser's
// fuzz corpus entries and the test queries of this package.
func fuzzSeeds(tb testing.TB) []string {
	var seeds []string
	parserData := filepath.Join("..", "parser", "testdata")
	golden, _ := filepath.Glob(filepath.Join(parserData, "*", "*.golden"))
	for _, path := range golden {
		data, err := os.ReadFile(path)
		if err != nil {
			tb.Fatal(err)
		}
		// "# source\n<src>\n# tree\n..."
		text := string(data)
		if src, ok := strings.CutPrefix(text, "# source\n"); ok {
			if i := strings.Index(src, "\n# tree\n"); i >= 0 {
				// Undo the golden files' escaping of CR and the byte order mark.
				bom := string(rune(0xFEFF))
				r := strings.NewReplacer(`\r`, "\r", `\`+"uFEFF", bom)
				seeds = append(seeds, r.Replace(src[:i]))
			}
		}
	}
	corpus, _ := filepath.Glob(filepath.Join(parserData, "fuzz", "FuzzParse", "*"))
	for _, path := range corpus {
		file, err := os.Open(path)
		if err != nil {
			tb.Fatal(err)
		}
		sc := bufio.NewScanner(file)
		sc.Buffer(nil, 1<<20)
		for sc.Scan() {
			line := sc.Text()
			if inner, ok := strings.CutPrefix(line, "string("); ok {
				if s, err := strconv.Unquote(strings.TrimSuffix(inner, ")")); err == nil {
					seeds = append(seeds, s)
				}
			}
		}
		file.Close()
	}
	if len(golden) == 0 {
		tb.Fatal("no parser golden files found")
	}
	for _, d := range validDocuments {
		seeds = append(seeds, d.src)
	}
	for _, q := range []string{
		`{ viewer { logn } repository(owner: 1) { issues(orderBy: {field: "X", dir: ASC}) { nodes { title { x } } } } }`,
		`query ($a: [[Int!]!] = [[null], 1, [2147483648]], $b: RepositoryLookup = {id: null, nameWithOwner: "x"}) { lookupRepository(by: {}) { id } }`,
		`{ search(query: "x", type: "ISSUE") { nodes { title ... on Nope { a } ... on Issue { state { b } } } } }`,
		`{ node(id: 1) @include(if: $x) @skip @nope(a: 1) { ... on Repository { owner } } }`,
		`fragment F on Repository { issues(states: [OPEN, MERGED], first: 1.5) { nodes { author { avatarUrl(size: "x") } } } }`,
		`query A($s: Int, $s: String, $u: User) { viewer { ...U ...Nope } } query A { node(id: $id) { ...U @skip(if: $s) @skip(if: true) } } fragment U on User { avatarUrl(size: $s, size: $t) ...V } fragment V on Node { ...U ... on Issue { id } } fragment U on String { x }`,
		`{ a } subscription @include(if: true) { ...S __typename b: issueEvents(repositoryId: $r) { action } } fragment S on Subscription { issueEvents(repositoryId: {x: 1, x: 2}) { ...S } }`,
		`query ($o: RepositoryLookup, $id: ID) @nope { lookupRepository(by: {id: $id}) { id } other: lookupRepository(by: $o) @cost(weight: $w) @cost { ... on Repository @defer(if: $b) { id } } }`,
	} {
		seeds = append(seeds, q)
	}
	return seeds
}
