package themes

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/testutil/golden"
)

func TestParseColorAccepts(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"#abc":                      "#aabbcc",
		"#ABCD":                     "#aabbccdd",
		"#112233":                   "#112233",
		" #112233 ":                 "#112233",
		"#112233ff":                 "#112233",
		"#11223380":                 "#11223380",
		"transparent":               "#00000000",
		"rgb(255, 0, 0)":            "#ff0000",
		"rgba(255,0,0,0.5)":         "#ff000080",
		"rgb(100% 0% 50%)":          "#ff0080",
		"rgb(0 0 0 / 50%)":          "#00000080",
		"RGB(1, 2, 3)":              "#010203",
		"hsl(0, 100%, 50%)":         "#ff0000",
		"hsl(120deg 100% 25%)":      "#008000",
		"hsla(240, 100%, 50%, 0.1)": "#0000ff1a",
		"hsl(-120 100% 50%)":        "#0000ff",
		"rgb(79, 209, 165, .14)":    "#4fd1a524",
	}
	for in, want := range tests {
		got, err := ParseColor(in)
		if err != nil || got != want {
			t.Errorf("ParseColor(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestParseColorRejects(t *testing.T) {
	t.Parallel()

	for _, in := range []string{
		"",
		"red",
		"currentcolor",
		"inherit",
		"#12",
		"#12345",
		"#ggg",
		"#112233;",
		"#112233; background: red",
		"red } body { background: red",
		"url(https://example.com/x.png)",
		"url(data:image/png;base64,AAAA)",
		"expression(alert(1))",
		"var(--bg0)",
		"rgb(var(--x), 0, 0)",
		"rgb(calc(1+1), 0, 0)",
		"rgb(256, 0, 0)",
		"rgb(-1, 0, 0)",
		"rgb(1, 2)",
		"rgb(1, 2, 3, 4, 5)",
		"rgb(1 2 3 / )",
		"rgb(1, 2, 3 / 0.5)",
		"rgba(1, 2, 3, 2)",
		"rgb(1e2, 0, 0)",
		"rgb(0x10, 0, 0)",
		"rgb(1, 2, 3",
		"hsl(0, 100, 50%)",
		"hwb(0 0% 0%)",
		"lab(50% 40 59)",
		"color(display-p3 1 0 0)",
		`"#112233"`,
		`\23 112233`,
		"#11\n2233",
		"<script>",
		strings.Repeat("#", 65),
	} {
		if got, err := ParseColor(in); err == nil {
			t.Errorf("ParseColor(%q) = %q, want an error", in, got)
		}
	}
}

func problemCodes(th Theme) []string {
	var out []string
	for _, p := range th.Problems {
		out = append(out, p.Severity+":"+p.Code+":"+p.Key)
	}
	return out
}

func varsOf(th Theme) map[string]string {
	m := map[string]string{}
	for _, v := range th.Vars {
		m[v.Name] = v.Value
	}
	return m
}

func TestParseTheme(t *testing.T) {
	t.Parallel()

	th := Parse("ocean.json", []byte(`{
		"$comment": "hand-made",
		"name": " Ocean ",
		"base": "dark",
		"colors": {"bg0": "#001122", "accent": "rgb(0, 128, 255)", "shadow": "#000", "surface": "#123"},
		"editor": {"keyword": "#c792ea", "comment": "hsl(0, 0%, 50%)"},
		"author": "me"
	}`))
	if !th.Usable() {
		t.Fatalf("Usable() = false, problems %v", problemCodes(th))
	}
	if th.Name != "Ocean" || th.Base != BaseDark || th.ID != "ocean.json" {
		t.Errorf("theme = %q %q %q", th.ID, th.Name, th.Base)
	}
	want := map[string]string{"--bg0": "#001122", "--accent": "#0080ff", "--tk-kw": "#c792ea", "--tk-cm": "#808080"}
	got := varsOf(th)
	if len(got) != len(want) {
		t.Errorf("vars = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	codes := strings.Join(problemCodes(th), " ")
	for _, w := range []string{"warning:themes.unknown_key:author", "warning:themes.unknown_key:colors.shadow", "warning:themes.unknown_key:colors.surface"} {
		if !strings.Contains(codes, w) {
			t.Errorf("problems %s, want %s", codes, w)
		}
	}
	if strings.Contains(codes, "$comment") {
		t.Errorf("problems %s: $ members must be ignored", codes)
	}
}

func TestParseThemeErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, data, want string
	}{
		{"not json", `{`, "error:themes.invalid_json:"},
		{"array", `[]`, "error:themes.invalid_json:"},
		{"null", `null`, "error:themes.invalid_json:"},
		{"no name", `{"base": "dark"}`, "error:themes.invalid_name:name"},
		{"blank name", `{"name": "  ", "base": "dark"}`, "error:themes.invalid_name:name"},
		{"control in name", `{"name": "a\u0007", "base": "dark"}`, "error:themes.invalid_name:name"},
		{"bad base", `{"name": "x", "base": "sepia"}`, "error:themes.invalid_base:base"},
		{"colors not object", `{"name": "x", "base": "light", "colors": "#fff"}`, "error:themes.invalid_section:colors"},
		{"number colour", `{"name": "x", "base": "light", "colors": {"bg0": 123}}`, "error:themes.invalid_color:colors.bg0"},
		{"css injection", `{"name": "x", "base": "light", "colors": {"bg0": "#fff; } body { background: url(https://evil.example/x) "}}`, "error:themes.invalid_color:colors.bg0"},
		{"url", `{"name": "x", "base": "light", "editor": {"keyword": "url(https://evil.example/x)"}}`, "error:themes.invalid_color:editor.keyword"},
		{"expression", `{"name": "x", "base": "light", "colors": {"text": "expression(alert(1))"}}`, "error:themes.invalid_color:colors.text"},
		{"too large", `{"name": "x", "base": "light", "$pad": "` + strings.Repeat("a", MaxFileSize) + `"}`, "error:themes.too_large:"},
	}
	for _, tt := range tests {
		th := Parse("t.json", []byte(tt.data))
		if th.Usable() {
			t.Errorf("%s: Usable() = true", tt.name)
		}
		if codes := strings.Join(problemCodes(th), " "); !strings.Contains(codes, tt.want) {
			t.Errorf("%s: problems %q, want %q", tt.name, codes, tt.want)
		}
		for _, v := range th.Vars {
			if _, err := ParseColor(v.Value); err != nil || strings.ContainsAny(v.Value, "(); ") {
				t.Errorf("%s: var %s = %q is not a normalised colour", tt.name, v.Name, v.Value)
			}
		}
	}
}

func TestValidID(t *testing.T) {
	t.Parallel()

	for _, id := range []string{"a.json", "Solarized Dark.json", "my-theme_2.json"} {
		if !ValidID(id) {
			t.Errorf("ValidID(%q) = false", id)
		}
	}
	for _, id := range []string{"", ".json", "a.txt", ".hidden.json", "_draft.json", "../a.json", `..\a.json`, "a/b.json", "c:a.json", " a.json", "a\x00.json", strings.Repeat("a", MaxIDLength) + ".json"} {
		if ValidID(id) {
			t.Errorf("ValidID(%q) = true", id)
		}
	}
}

func TestListAndLoad(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if got, err := List(filepath.Join(dir, "missing")); err != nil || len(got) != 0 {
		t.Fatalf("List(missing) = %v, %v", got, err)
	}
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("b.json", `{"name": "B", "base": "light"}`)
	write("a.json", `{"name": "A", "base": "dark", "colors": {"bg0": "#000"}}`)
	write("broken.json", `{`)
	write("notes.txt", `x`)
	write(".hidden.json", `{"name": "H", "base": "dark"}`)
	if err := os.Mkdir(filepath.Join(dir, "sub.json"), 0o700); err != nil {
		t.Fatal(err)
	}

	list, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, th := range list {
		ids = append(ids, th.ID)
	}
	if got := strings.Join(ids, ","); got != "a.json,b.json,broken.json" {
		t.Errorf("List ids = %s", got)
	}
	if list[2].Usable() {
		t.Error("broken.json is usable")
	}

	th, err := Load(dir, "a.json")
	if err != nil || th.Name != "A" || varsOf(th)["--bg0"] != "#000000" {
		t.Errorf("Load(a.json) = %+v, %v", th, err)
	}
	for _, id := range []string{"missing.json", "../a.json", "notes.txt", "sub.json"} {
		if _, err := Load(dir, id); !errors.Is(err, &errs.Error{Code: CodeNotFound}) {
			t.Errorf("Load(%q) err = %v, want %s", id, err, CodeNotFound)
		}
	}
}

// The example theme in the docs sets every token and has no problems.
func TestDocsExample(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "examples", "solarized-dark.theme.json"))
	if err != nil {
		t.Fatal(err)
	}
	th := Parse("solarized-dark.theme.json", data)
	if len(th.Problems) != 0 || !th.Usable() {
		t.Errorf("problems: %v", problemCodes(th))
	}
	if want := len(ColorTokens) + len(EditorTokens); len(th.Vars) != want {
		t.Errorf("%d vars, want every token (%d)", len(th.Vars), want)
	}
}

// TestTokenTable writes the token table that
// frontend/src/styles/customTheme.test.ts checks THEME_TOKENS and
// public/theme-init.js against; go test -update rewrites it when a token
// changes.
func TestTokenTable(t *testing.T) {
	t.Parallel()
	out, err := json.MarshalIndent(map[string]map[string]string{"colors": ColorTokens, "editor": EditorTokens}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, "tokens.json", append(out, '\n'))
}

// Every token a theme can set exists in both built-in themes of the UI.
func TestTokensExistInTokensCSS(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile(filepath.Join("..", "..", "..", "frontend", "src", "styles", "tokens.css"))
	if err != nil {
		t.Skipf("tokens.css not readable: %v", err)
	}
	css := string(data)
	light := css[strings.Index(css, `[data-theme="light"] {`):]
	dark := css[strings.Index(css, `[data-theme="dark"] {`):strings.Index(css, `[data-theme="light"] {`)]
	check := func(tokens map[string]string) {
		for _, prop := range tokens {
			re := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(prop) + `:`)
			if !re.MatchString(dark) || !re.MatchString(light) {
				t.Errorf("%s is not set by both themes in tokens.css", prop)
			}
		}
	}
	check(ColorTokens)
	check(EditorTokens)
}
