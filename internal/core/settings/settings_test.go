package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/testutil/golden"
)

// writeSettings writes content as settings.json in a new temp dir and
// returns its path.
func writeSettings(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func with(f func(*Settings)) Settings {
	s := Defaults()
	f(&s)
	return s
}

// problem is the part of a CodeInvalidValue error the tests check.
type problem struct{ field, value, expected string }

func problemsOf(t *testing.T, got []*errs.Error) []problem {
	t.Helper()
	var out []problem
	for _, e := range got {
		if e.Code != CodeInvalidValue {
			continue
		}
		if e.Kind != errs.InvalidArgument {
			t.Errorf("%s: kind = %q, want invalid_argument", e.Details["field"], e.Kind)
		}
		if e.Hint == "" {
			t.Errorf("%s: no hint", e.Details["field"])
		}
		out = append(out, problem{e.Details["field"], e.Details["value"], e.Details["expected"]})
	}
	return out
}

func codesOf(got []*errs.Error) []string {
	var out []string
	for _, e := range got {
		out = append(out, e.Code)
	}
	return out
}

func TestDefaults(t *testing.T) {
	t.Parallel()
	want := Settings{
		Version: 1,
		UI:      UI{Theme: "system", SidebarCollapsed: false, SplitRatio: 0.5, ResponsePosition: "beside", SplitRatioBelow: 0.5, Locale: "system"},
		Network: Network{DefaultTimeoutMs: 0, VerifyTLS: true, MaxResponseBodyMb: 512, ProxyMode: "system"},
		Editor:  Editor{FontFamily: "JetBrains Mono", FontSize: 12, TabSize: 2, WordWrap: false, LineNumbers: true, MatchBrackets: true},
		Privacy: Privacy{SecretsKeyStore: "keychain"},
	}
	if got := Defaults(); !got.Equal(want) {
		t.Errorf("Defaults() = %+v, want %+v", got, want)
	}
	if err := Defaults().Validate(); err != nil {
		t.Errorf("defaults do not validate: %v", err)
	}
}

func TestLoadMissingFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), FileName)
	s, r, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !s.Equal(Defaults()) {
		t.Errorf("settings = %+v, want the defaults", s)
	}
	if !reflect.DeepEqual(r, Report{}) {
		t.Errorf("report = %+v, want empty", r)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Load created the file (stat: %v)", err)
	}
}

func TestLoad(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		content      string
		want         Settings
		wantProblems []problem
		wantCodes    []string // codes of all problems, when not only invalid values
		wantUnknown  Unknown
		wantVersion  int
		wantNewer    bool
	}{
		{name: "empty file", content: "", want: Defaults()},
		{name: "whitespace only", content: " \n\t", want: Defaults()},
		{name: "empty object", content: "{}", want: Defaults(), wantVersion: 1},
		{
			name: "every field",
			content: `{"version":1,"ui":{"theme":"dark","sidebarCollapsed":true,"splitRatio":0.3,"responsePosition":"below","splitRatioBelow":0.7},
				"editor":{"fontFamily":"Fira Code","fontSize":14,"tabSize":4,"wordWrap":true,"lineNumbers":false,"matchBrackets":false}}`,
			want: with(func(s *Settings) {
				s.UI = UI{Theme: "dark", SidebarCollapsed: true, SplitRatio: 0.3, ResponsePosition: "below", SplitRatioBelow: 0.7, Locale: "system"}
				s.Editor = Editor{FontFamily: "Fira Code", FontSize: 14, TabSize: 4, WordWrap: true, LineNumbers: false, MatchBrackets: false}
			}),
			wantVersion: 1,
		},
		{
			name:        "missing version is 1",
			content:     `{"ui":{"splitRatio":0.25}}`,
			want:        with(func(s *Settings) { s.UI.SplitRatio = 0.25 }),
			wantVersion: 1,
		},
		{
			name:        "null values take the default",
			content:     `{"version":null,"ui":{"theme":null,"splitRatio":null}}`,
			want:        Defaults(),
			wantVersion: 1,
		},
		{name: "null section", content: `{"ui":null}`, want: Defaults(), wantVersion: 1},
		{
			name:        "bounds are allowed",
			content:     `{"ui":{"splitRatio":0.8}}`,
			want:        with(func(s *Settings) { s.UI.SplitRatio = 0.8 }),
			wantVersion: 1,
		},
		{
			name:         "unknown theme",
			content:      `{"ui":{"theme":"blue","splitRatio":0.4}}`,
			want:         with(func(s *Settings) { s.UI.SplitRatio = 0.4 }),
			wantProblems: []problem{{"ui.theme", `"blue"`, themeExpected}},
			wantVersion:  1,
		},
		{
			name:         "theme of the wrong type",
			content:      `{"ui":{"theme":1}}`,
			want:         Defaults(),
			wantProblems: []problem{{"ui.theme", `1`, themeExpected}},
			wantVersion:  1,
		},
		{
			name:         "theme in the wrong case",
			content:      `{"ui":{"theme":"Dark"}}`,
			want:         Defaults(),
			wantProblems: []problem{{"ui.theme", `"Dark"`, themeExpected}},
			wantVersion:  1,
		},
		{
			name:         "sidebarCollapsed not a boolean",
			content:      `{"ui":{"sidebarCollapsed":"yes"}}`,
			want:         Defaults(),
			wantProblems: []problem{{"ui.sidebarCollapsed", `"yes"`, boolExpected}},
			wantVersion:  1,
		},
		{
			name:         "splitRatio too small",
			content:      `{"ui":{"splitRatio":0.1}}`,
			want:         Defaults(),
			wantProblems: []problem{{"ui.splitRatio", `0.1`, splitRatioExpected}},
			wantVersion:  1,
		},
		{
			name:         "splitRatio too large",
			content:      `{"ui":{"splitRatio":2}}`,
			want:         Defaults(),
			wantProblems: []problem{{"ui.splitRatio", `2`, splitRatioExpected}},
			wantVersion:  1,
		},
		{
			name:         "splitRatio as a string",
			content:      `{"ui":{"splitRatio":"0.5"}}`,
			want:         Defaults(),
			wantProblems: []problem{{"ui.splitRatio", `"0.5"`, splitRatioExpected}},
			wantVersion:  1,
		},
		{
			name:         "splitRatio out of float range",
			content:      `{"ui":{"splitRatio":1e400}}`,
			want:         Defaults(),
			wantProblems: []problem{{"ui.splitRatio", `1e400`, splitRatioExpected}},
			wantVersion:  1,
		},
		{
			name:        "default timeout",
			content:     `{"network":{"defaultTimeoutMs":30000}}`,
			want:        with(func(s *Settings) { s.Network.DefaultTimeoutMs = 30000 }),
			wantVersion: 1,
		},
		{
			name:        "body limit",
			content:     `{"network":{"maxResponseBodyMb":4096}}`,
			want:        with(func(s *Settings) { s.Network.MaxResponseBodyMb = 4096 }),
			wantVersion: 1,
		},
		{
			name:         "body limit out of range",
			content:      `{"network":{"maxResponseBodyMb":0}}`,
			want:         Defaults(),
			wantProblems: []problem{{"network.maxResponseBodyMb", `0`, maxResponseBodyExpected}},
			wantVersion:  1,
		},
		{
			name:        "tls verification off",
			content:     `{"network":{"verifyTls":false}}`,
			want:        with(func(s *Settings) { s.Network.VerifyTLS = false }),
			wantVersion: 1,
		},
		{
			name:         "tls verification not a bool",
			content:      `{"network":{"verifyTls":"no"}}`,
			want:         Defaults(),
			wantProblems: []problem{{"network.verifyTls", `"no"`, boolExpected}},
			wantVersion:  1,
		},
		{
			name:        "default timeout bounds and zero are allowed",
			content:     `{"network":{"defaultTimeoutMs":86400000}}`,
			want:        with(func(s *Settings) { s.Network.DefaultTimeoutMs = MaxDefaultTimeoutMs }),
			wantVersion: 1,
		},
		{
			name:         "default timeout below a second",
			content:      `{"network":{"defaultTimeoutMs":500}}`,
			want:         Defaults(),
			wantProblems: []problem{{"network.defaultTimeoutMs", `500`, defaultTimeoutExpected}},
			wantVersion:  1,
		},
		{
			name:         "default timeout negative",
			content:      `{"network":{"defaultTimeoutMs":-5000}}`,
			want:         Defaults(),
			wantProblems: []problem{{"network.defaultTimeoutMs", `-5000`, defaultTimeoutExpected}},
			wantVersion:  1,
		},
		{
			name:         "default timeout not whole",
			content:      `{"network":{"defaultTimeoutMs":1500.5}}`,
			want:         Defaults(),
			wantProblems: []problem{{"network.defaultTimeoutMs", `1500.5`, defaultTimeoutExpected}},
			wantVersion:  1,
		},
		{
			name:         "unknown response position",
			content:      `{"ui":{"responsePosition":"left","splitRatioBelow":0.6}}`,
			want:         with(func(s *Settings) { s.UI.SplitRatioBelow = 0.6 }),
			wantProblems: []problem{{"ui.responsePosition", `"left"`, responsePositionExpected}},
			wantVersion:  1,
		},
		{
			name:        "custom theme",
			content:     `{"ui":{"customTheme":"ocean.json"}}`,
			want:        with(func(s *Settings) { s.UI.CustomTheme = "ocean.json" }),
			wantVersion: 1,
		},
		{
			name:         "custom theme with a path",
			content:      `{"ui":{"customTheme":"/etc/passwd"}}`,
			want:         Defaults(),
			wantProblems: []problem{{"ui.customTheme", `"/etc/passwd"`, customThemeExpected}},
			wantVersion:  1,
		},
		{
			name:        "locale",
			content:     `{"ui":{"locale":"en-XA"}}`,
			want:        with(func(s *Settings) { s.UI.Locale = "en-XA" }),
			wantVersion: 1,
		},
		{
			name:         "locale that is not a language tag",
			content:      `{"ui":{"locale":"english"}}`,
			want:         Defaults(),
			wantProblems: []problem{{"ui.locale", `"english"`, localeExpected}},
			wantVersion:  1,
		},
		{
			name:         "splitRatioBelow out of range",
			content:      `{"ui":{"responsePosition":"below","splitRatioBelow":0.9}}`,
			want:         with(func(s *Settings) { s.UI.ResponsePosition = "below" }),
			wantProblems: []problem{{"ui.splitRatioBelow", `0.9`, splitRatioExpected}},
			wantVersion:  1,
		},
		{
			name:        "editor bounds are allowed",
			content:     `{"editor":{"fontSize":32,"tabSize":1,"fontFamily":"ui-monospace"}}`,
			want:        with(func(s *Settings) { s.Editor.FontSize = 32; s.Editor.TabSize = 1; s.Editor.FontFamily = "ui-monospace" }),
			wantVersion: 1,
		},
		{
			name: "invalid editor values fall back one by one",
			content: `{"editor":{"fontFamily":"Menlo; color: red","fontSize":7,"tabSize":9,
				"wordWrap":"yes","lineNumbers":0,"matchBrackets":false}}`,
			want: with(func(s *Settings) { s.Editor.MatchBrackets = false }),
			wantProblems: []problem{
				{"editor.fontFamily", `"Menlo; color: red"`, fontFamilyExpected},
				{"editor.fontSize", `7`, fontSizeExpected},
				{"editor.tabSize", `9`, tabSizeExpected},
				{"editor.wordWrap", `"yes"`, boolExpected},
				{"editor.lineNumbers", `0`, boolExpected},
			},
			wantVersion: 1,
		},
		{
			name:         "font size not whole",
			content:      `{"editor":{"fontSize":12.5}}`,
			want:         Defaults(),
			wantProblems: []problem{{"editor.fontSize", `12.5`, fontSizeExpected}},
			wantVersion:  1,
		},
		{
			name:         "editor section not an object",
			content:      `{"editor":true}`,
			want:         Defaults(),
			wantProblems: []problem{{"editor", `true`, sectionExpected}},
			wantVersion:  1,
		},
		{
			name:        "unknown editor keys are kept",
			content:     `{"editor":{"ligatures":true,"tabSize":8}}`,
			want:        with(func(s *Settings) { s.Editor.TabSize = 8 }),
			wantUnknown: Unknown{"editor": {"ligatures": json.RawMessage(`true`)}},
			wantVersion: 1,
		},
		{
			name:         "network section not an object",
			content:      `{"network":"fast"}`,
			want:         Defaults(),
			wantProblems: []problem{{"network", `"fast"`, sectionExpected}},
			wantVersion:  1,
		},
		{
			name:         "section not an object",
			content:      `{"ui":[1, 2]}`,
			want:         Defaults(),
			wantProblems: []problem{{"ui", `[1,2]`, sectionExpected}},
			wantVersion:  1,
		},
		{
			name:         "version not a number",
			content:      `{"version":"1","ui":{"theme":"light"}}`,
			want:         with(func(s *Settings) { s.UI.Theme = "light" }),
			wantProblems: []problem{{"version", `"1"`, versionExpected}},
			wantVersion:  1,
		},
		{
			name:         "version zero",
			content:      `{"version":0}`,
			want:         Defaults(),
			wantProblems: []problem{{"version", `0`, versionExpected}},
			wantVersion:  1,
		},
		{
			name:         "version not whole",
			content:      `{"version":1.5}`,
			want:         Defaults(),
			wantProblems: []problem{{"version", `1.5`, versionExpected}},
			wantVersion:  1,
		},
		{
			name:    "several invalid values, the rest still load",
			content: `{"ui":{"theme":"neon","sidebarCollapsed":true,"splitRatio":-1}}`,
			want:    with(func(s *Settings) { s.UI.SidebarCollapsed = true }),
			wantProblems: []problem{
				{"ui.theme", `"neon"`, themeExpected},
				{"ui.splitRatio", `-1`, splitRatioExpected},
			},
			wantVersion: 1,
		},
		{
			name:         "long values are cut in details",
			content:      fmt.Sprintf(`{"ui":{"theme":"%070d"}}`, 0),
			want:         Defaults(),
			wantProblems: []problem{{"ui.theme", fmt.Sprintf(`"%063d…`, 0), themeExpected}},
			wantVersion:  1,
		},
		{
			name: "unknown keys, top level and nested",
			content: `{"version":1,"zeta":[1,{"b":2}],"network":{"proxy":"http://p:8080"},
				"ui":{"theme":"dark","fontSize":14,"panels":{"x":true}}}`,
			want: with(func(s *Settings) { s.UI.Theme = "dark" }),
			wantUnknown: Unknown{
				RootSection: {"zeta": json.RawMessage(`[1,{"b":2}]`)},
				"network":   {"proxy": json.RawMessage(`"http://p:8080"`)},
				"ui":        {"fontSize": json.RawMessage(`14`), "panels": json.RawMessage(`{"x":true}`)},
			},
			wantVersion: 1,
		},
		{
			name:        "newer version loads the known fields",
			content:     `{"version":7,"ui":{"theme":"light","splitRatio":0.7,"layout":"grid"},"sync":{"on":true}}`,
			want:        with(func(s *Settings) { s.UI.Theme = "light"; s.UI.SplitRatio = 0.7 }),
			wantCodes:   []string{CodeNewerVersion},
			wantVersion: 7,
			wantNewer:   true,
			wantUnknown: Unknown{
				RootSection: {"sync": json.RawMessage(`{"on":true}`)},
				"ui":        {"layout": json.RawMessage(`"grid"`)},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := writeSettings(t, tt.content)
			got, r, err := Load(path)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if !got.Equal(tt.want) {
				t.Errorf("settings = %+v, want %+v", got, tt.want)
			}
			if !r.Exists {
				t.Error("Exists = false")
			}
			if r.FileVersion != tt.wantVersion || r.Newer != tt.wantNewer {
				t.Errorf("FileVersion, Newer = %d, %v; want %d, %v", r.FileVersion, r.Newer, tt.wantVersion, tt.wantNewer)
			}
			if p := problemsOf(t, r.Problems); !reflect.DeepEqual(p, tt.wantProblems) {
				t.Errorf("problems = %q, want %q", p, tt.wantProblems)
			}
			if tt.wantCodes != nil {
				if c := codesOf(r.Problems); !reflect.DeepEqual(c, tt.wantCodes) {
					t.Errorf("problem codes = %q, want %q", c, tt.wantCodes)
				}
			}
			if !equalUnknown(r.Unknown, tt.wantUnknown) {
				t.Errorf("unknown = %s, want %s", fmtUnknown(r.Unknown), fmtUnknown(tt.wantUnknown))
			}
			if got := readFile(t, path); got != tt.content {
				t.Errorf("Load changed the file to %q", got)
			}
		})
	}
}

func equalUnknown(a, b Unknown) bool {
	return maps.EqualFunc(a, b, func(x, y map[string]json.RawMessage) bool {
		return maps.EqualFunc(x, y, func(p, q json.RawMessage) bool { return string(p) == string(q) })
	})
}

func fmtUnknown(u Unknown) string {
	b, _ := json.Marshal(u)
	return string(b)
}

func TestInvalidValueError(t *testing.T) {
	t.Parallel()
	_, r, err := Load(writeSettings(t, `{"ui":{"splitRatio":2}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Problems) != 1 {
		t.Fatalf("problems = %v, want one", r.Problems)
	}
	e := r.Problems[0]
	want := &errs.Error{
		Kind:    errs.InvalidArgument,
		Code:    CodeInvalidValue,
		Message: `The setting "ui.splitRatio" has an invalid value, 2.`,
		Hint:    "Use a number from 0.2 to 0.8 in settings.json. PostSonde uses the default, 0.5, until then.",
		Details: map[string]string{"field": "ui.splitRatio", "value": "2", "expected": "a number from 0.2 to 0.8"},
	}
	if !reflect.DeepEqual(e, want) {
		t.Errorf("problem = %#v\nwant %#v", e, want)
	}
}

func TestLoadBroken(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		content  string
		wantLine string // "" when the error is not a syntax error
		wantCol  string
	}{
		// The position is the byte the decoder stopped at; for a truncated
		// file, the end of its last line.
		{name: "truncated", content: "{\n  \"ui\": {\n    \"theme\": \"dark\",\n", wantLine: "3", wantCol: "21"},
		{name: "trailing comma", content: "{\n  \"ui\": {\"theme\": \"dark\",}\n}", wantLine: "2", wantCol: "26"},
		{name: "not JSON", content: "theme = dark", wantLine: "1", wantCol: "2"},
		{name: "trailing garbage", content: "{} x", wantLine: "1", wantCol: "4"},
		{name: "array", content: "[1, 2]"},
		{name: "string", content: `"dark"`},
		{name: "null", content: "null"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := writeSettings(t, tt.content)
			s, r, err := Load(path)
			if !s.Equal(Defaults()) {
				t.Errorf("settings = %+v, want the defaults", s)
			}
			if !r.Exists || len(r.Problems) != 0 {
				t.Errorf("report = %+v, want Exists and no problems", r)
			}
			var e *errs.Error
			if !errors.As(err, &e) {
				t.Fatalf("err = %v, want an *errs.Error", err)
			}
			if e.Kind != errs.Parse || e.Code != CodeParseFailed || e.Hint == "" {
				t.Errorf("err = %#v, want kind parse, code %s and a hint", e, CodeParseFailed)
			}
			if e.Details["path"] != path || e.Details["line"] != tt.wantLine || e.Details["column"] != tt.wantCol {
				t.Errorf("details = %v, want path %s, line %q, column %q", e.Details, path, tt.wantLine, tt.wantCol)
			}
			if got := readFile(t, path); got != tt.content {
				t.Errorf("Load changed the file to %q", got)
			}
		})
	}
}

func TestLoadUnreadable(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("permission bits do not stop reads on Windows")
	}
	path := writeSettings(t, `{"ui":{"theme":"dark"}}`)
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(path); err == nil {
		t.Skip("running with permission to read anything (root?)")
	}
	s, r, err := Load(path)
	if !s.Equal(Defaults()) || !r.Exists {
		t.Errorf("settings, report = %+v, %+v; want the defaults and Exists", s, r)
	}
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != CodeReadFailed || e.Kind != errs.PermissionDenied {
		t.Errorf("err = %#v, want permission_denied with code %s", err, CodeReadFailed)
	}
}

func TestMigrateWith(t *testing.T) {
	t.Parallel()
	// v1 → v2 renames "dark" (a bool) to ui.theme; v2 → v3 adds "extra".
	steps := map[int]migration{
		1: func(top map[string]json.RawMessage) (map[string]json.RawMessage, error) {
			if string(top["dark"]) == "true" {
				top["ui"] = json.RawMessage(`{"theme":"dark"}`)
			}
			delete(top, "dark")
			return top, nil
		},
		2: func(top map[string]json.RawMessage) (map[string]json.RawMessage, error) {
			top["extra"] = json.RawMessage(`1`)
			return top, nil
		},
	}
	fail := map[int]migration{
		1: func(map[string]json.RawMessage) (map[string]json.RawMessage, error) { return nil, errors.New("boom") },
	}

	tests := []struct {
		name     string
		steps    map[int]migration
		from, to int
		want     string
		wantErr  bool
	}{
		{name: "current version is left alone", steps: steps, from: 3, to: 3, want: `{"dark":true}`},
		{name: "two steps", steps: steps, from: 1, to: 3, want: `{"extra":1,"ui":{"theme":"dark"}}`},
		{name: "one step", steps: steps, from: 2, to: 3, want: `{"dark":true,"extra":1}`},
		{name: "missing step", steps: steps, from: 1, to: 4, wantErr: true},
		{name: "failing step", steps: fail, from: 1, to: 2, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			top := map[string]json.RawMessage{"dark": json.RawMessage(`true`)}
			got, err := migrateWith(tt.steps, tt.from, tt.to, top)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			b, _ := json.Marshal(got)
			if string(b) != tt.want {
				t.Errorf("migrated = %s, want %s", b, tt.want)
			}
		})
	}

	if len(migrations) != CurrentVersion-1 {
		t.Errorf("%d migrations for CurrentVersion %d; each version bump needs a step", len(migrations), CurrentVersion)
	}
	for v := 1; v < CurrentVersion; v++ {
		if migrations[v] == nil {
			t.Errorf("no migration from version %d", v)
		}
	}
}

func TestApply(t *testing.T) {
	t.Parallel()
	str := func(v string) *string { return &v }
	num := func(v float64) *float64 { return &v }
	yes := true
	ms := func(v int) *int { return &v }

	base := with(func(s *Settings) { s.UI.Theme = "dark" })
	tests := []struct {
		name      string
		patch     Patch
		want      Settings
		wantField string // field of the CodeInvalidValue error, "" for success
	}{
		{name: "empty patch", patch: Patch{}, want: base},
		{
			name: "every field",
			patch: Patch{
				UI: UIPatch{Theme: str("light"), SidebarCollapsed: &yes, SplitRatio: num(0.2), ResponsePosition: str("below"), SplitRatioBelow: num(0.8)},
				Editor: EditorPatch{
					FontFamily: str("Cascadia Code"), FontSize: ms(16), TabSize: ms(4),
					WordWrap: &yes, LineNumbers: ptrBool(false), MatchBrackets: ptrBool(false),
				},
			},
			want: with(func(s *Settings) {
				s.UI = UI{Theme: "light", SidebarCollapsed: true, SplitRatio: 0.2, ResponsePosition: "below", SplitRatioBelow: 0.8, Locale: "system"}
				s.Editor = Editor{FontFamily: "Cascadia Code", FontSize: 16, TabSize: 4, WordWrap: true, LineNumbers: false, MatchBrackets: false}
			}),
		},
		{name: "unknown response position", patch: Patch{UI: UIPatch{ResponsePosition: str("right")}}, wantField: "ui.responsePosition"},
		{name: "custom theme", patch: Patch{UI: UIPatch{CustomTheme: str("ocean.json")}}, want: with(func(s *Settings) { s.UI.Theme = "dark"; s.UI.CustomTheme = "ocean.json" })},
		{name: "custom theme outside the folder", patch: Patch{UI: UIPatch{CustomTheme: str("../ocean.json")}}, wantField: "ui.customTheme"},
		{name: "custom theme not json", patch: Patch{UI: UIPatch{CustomTheme: str("ocean.css")}}, wantField: "ui.customTheme"},
		{name: "locale", patch: Patch{UI: UIPatch{Locale: str("en")}}, want: with(func(s *Settings) { s.UI.Theme = "dark"; s.UI.Locale = "en" })},
		{name: "locale with region and script", patch: Patch{UI: UIPatch{Locale: str("zh-Hant-TW")}}, want: with(func(s *Settings) { s.UI.Theme = "dark"; s.UI.Locale = "zh-Hant-TW" })},
		{name: "locale back to system", patch: Patch{UI: UIPatch{Locale: str("system")}}, want: with(func(s *Settings) { s.UI.Theme = "dark" })},
		{name: "locale empty", patch: Patch{UI: UIPatch{Locale: str("")}}, wantField: "ui.locale"},
		{name: "locale with underscore", patch: Patch{UI: UIPatch{Locale: str("en_US")}}, wantField: "ui.locale"},
		{name: "locale with a digit first", patch: Patch{UI: UIPatch{Locale: str("1en")}}, wantField: "ui.locale"},
		{name: "locale subtag too long", patch: Patch{UI: UIPatch{Locale: str("en-abcdefghi")}}, wantField: "ui.locale"},
		{name: "locale empty subtag", patch: Patch{UI: UIPatch{Locale: str("en--US")}}, wantField: "ui.locale"},
		{name: "below ratio too large", patch: Patch{UI: UIPatch{SplitRatioBelow: num(0.81)}}, wantField: "ui.splitRatioBelow"},
		{name: "font family empty", patch: Patch{Editor: EditorPatch{FontFamily: str("")}}, wantField: "editor.fontFamily"},
		{name: "font family padded", patch: Patch{Editor: EditorPatch{FontFamily: str(" Menlo")}}, wantField: "editor.fontFamily"},
		{name: "font family with a quote", patch: Patch{Editor: EditorPatch{FontFamily: str(`Menlo"`)}}, wantField: "editor.fontFamily"},
		{name: "font family list", patch: Patch{Editor: EditorPatch{FontFamily: str("Menlo, monospace")}}, wantField: "editor.fontFamily"},
		{name: "font family with a brace", patch: Patch{Editor: EditorPatch{FontFamily: str("Menlo}")}}, wantField: "editor.fontFamily"},
		{name: "font family with a control character", patch: Patch{Editor: EditorPatch{FontFamily: str("Men\nlo")}}, wantField: "editor.fontFamily"},
		{name: "font family too long", patch: Patch{Editor: EditorPatch{FontFamily: str(strings.Repeat("a", MaxEditorFontFamilyLen+1))}}, wantField: "editor.fontFamily"},
		{
			name:  "font family at the length limit, not ASCII",
			patch: Patch{Editor: EditorPatch{FontFamily: str(strings.Repeat("é", MaxEditorFontFamilyLen))}},
			want: with(func(s *Settings) {
				s.UI.Theme = "dark"
				s.Editor.FontFamily = strings.Repeat("é", MaxEditorFontFamilyLen)
			}),
		},
		{name: "font size too small", patch: Patch{Editor: EditorPatch{FontSize: ms(MinEditorFontSize - 1)}}, wantField: "editor.fontSize"},
		{name: "font size too large", patch: Patch{Editor: EditorPatch{FontSize: ms(MaxEditorFontSize + 1)}}, wantField: "editor.fontSize"},
		{name: "tab size zero", patch: Patch{Editor: EditorPatch{TabSize: ms(0)}}, wantField: "editor.tabSize"},
		{name: "tab size too large", patch: Patch{Editor: EditorPatch{TabSize: ms(MaxEditorTabSize + 1)}}, wantField: "editor.tabSize"},
		{name: "one field", patch: Patch{UI: UIPatch{SplitRatio: num(0.65)}}, want: with(func(s *Settings) { s.UI.Theme = "dark"; s.UI.SplitRatio = 0.65 })},
		{name: "default timeout", patch: Patch{Network: NetworkPatch{DefaultTimeoutMs: ms(1000)}}, want: with(func(s *Settings) { s.UI.Theme = "dark"; s.Network.DefaultTimeoutMs = 1000 })},
		{name: "no default timeout", patch: Patch{Network: NetworkPatch{DefaultTimeoutMs: ms(0)}}, want: base},
		{name: "default timeout too short", patch: Patch{Network: NetworkPatch{DefaultTimeoutMs: ms(999)}}, wantField: "network.defaultTimeoutMs"},
		{name: "default timeout negative", patch: Patch{Network: NetworkPatch{DefaultTimeoutMs: ms(-1)}}, wantField: "network.defaultTimeoutMs"},
		{name: "tls verification off", patch: Patch{Network: NetworkPatch{VerifyTLS: ptrBool(false)}}, want: with(func(s *Settings) { s.UI.Theme = "dark"; s.Network.VerifyTLS = false })},
		{name: "body limit", patch: Patch{Network: NetworkPatch{MaxResponseBodyMb: ms(2048)}}, want: with(func(s *Settings) { s.UI.Theme = "dark"; s.Network.MaxResponseBodyMb = 2048 })},
		{name: "body limit zero", patch: Patch{Network: NetworkPatch{MaxResponseBodyMb: ms(0)}}, wantField: "network.maxResponseBodyMb"},
		{name: "body limit too large", patch: Patch{Network: NetworkPatch{MaxResponseBodyMb: ms(MaxMaxResponseBodyMb + 1)}}, wantField: "network.maxResponseBodyMb"},
		{name: "default timeout too long", patch: Patch{Network: NetworkPatch{DefaultTimeoutMs: ms(MaxDefaultTimeoutMs + 1)}}, wantField: "network.defaultTimeoutMs"},
		{name: "unknown theme", patch: Patch{UI: UIPatch{Theme: str("blue")}}, wantField: "ui.theme"},
		{name: "empty theme", patch: Patch{UI: UIPatch{Theme: str("")}}, wantField: "ui.theme"},
		{name: "ratio too small", patch: Patch{UI: UIPatch{SplitRatio: num(0.19)}}, wantField: "ui.splitRatio"},
		{name: "ratio too large", patch: Patch{UI: UIPatch{SplitRatio: num(0.81)}}, wantField: "ui.splitRatio"},
		{
			name:      "one invalid field rejects the whole patch",
			patch:     Patch{UI: UIPatch{Theme: str("light"), SplitRatio: num(5)}},
			wantField: "ui.splitRatio",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := base.Apply(tt.patch)
			if tt.wantField == "" {
				if err != nil || !got.Equal(tt.want) {
					t.Errorf("Apply = %+v, %v; want %+v", got, err, tt.want)
				}
				return
			}
			var e *errs.Error
			if !errors.As(err, &e) || e.Kind != errs.InvalidArgument || e.Code != CodeInvalidValue || e.Details["field"] != tt.wantField {
				t.Fatalf("err = %#v, want invalid_argument %s for %s", err, CodeInvalidValue, tt.wantField)
			}
			if e.Hint == "" || e.Details["expected"] == "" || e.Details["value"] == "" {
				t.Errorf("err = %#v, want a hint, value and expected", e)
			}
			if !got.Equal(base) {
				t.Errorf("Apply changed the settings to %+v", got)
			}
		})
	}
}

func TestSaveGolden(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string // file to load first; "" saves the defaults
	}{
		{name: "defaults"},
		{
			name: "unknown_round_trip",
			input: `{
  "zeta": {"b": 1, "a": [true, null]},
  "ui": {"splitRatio": 0.35, "fontSize": 14, "theme": "light", "panels": {"b": 2, "a": 1}},
  "version": 1,
  "alpha": "<kept & unescaped>"
}`,
		},
		{name: "invalid_values_replaced", input: `{"ui":{"theme":"neon","splitRatio":0.9,"sidebarCollapsed":true},"network":{"defaultTimeoutMs":-1}}`},
		{name: "layout_and_editor", input: `{"ui":{"responsePosition":"below","splitRatioBelow":0.6},"editor":{"ligatures":false,"fontFamily":"Menlo","tabSize":4,"wordWrap":true}}`},
		{name: "network", input: `{"network":{"proxyUrl":"socks5h://127.0.0.1:1080","defaultTimeoutMs":30000}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, unknown := Defaults(), Unknown(nil)
			if tt.input != "" {
				var r Report
				var err error
				if s, r, err = Load(writeSettings(t, tt.input)); err != nil {
					t.Fatal(err)
				}
				unknown = r.Unknown
			}
			path := filepath.Join(t.TempDir(), FileName)
			if err := Save(path, s, unknown); err != nil {
				t.Fatalf("Save: %v", err)
			}
			first := readFile(t, path)
			golden.Assert(t, "file/"+tt.name, []byte(first))

			// Loading and saving again changes nothing.
			s2, r2, err := Load(path)
			if err != nil || len(r2.Problems) != 0 || !s2.Equal(s) {
				t.Fatalf("reload = %+v, %+v, %v; want %+v without problems", s2, r2, err, s)
			}
			if err := Save(path, s2, r2.Unknown); err != nil {
				t.Fatal(err)
			}
			if second := readFile(t, path); second != first {
				t.Errorf("second save differs:\n%s\nfirst:\n%s", second, first)
			}
		})
	}
}

func TestSaveMode(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("no Unix permission bits on Windows")
	}
	path := filepath.Join(t.TempDir(), FileName)
	if err := Save(path, Defaults(), nil); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestSaveInvalid(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), FileName)
	s := with(func(s *Settings) { s.UI.SplitRatio = 2 })
	err := Save(path, s, nil)
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != CodeInvalidValue {
		t.Errorf("err = %v, want %s", err, CodeInvalidValue)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("an invalid Save wrote the file (stat: %v)", err)
	}
}

func TestSaveFailed(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "missing-dir", FileName)
	err := Save(path, Defaults(), nil)
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != CodeSaveFailed || e.Kind != errs.NotFound {
		t.Errorf("err = %#v, want not_found with code %s", err, CodeSaveFailed)
	}
}

func TestBackup(t *testing.T) {
	t.Parallel()
	const broken = "{ not json"
	path := writeSettings(t, broken)
	now := time.Date(2026, 9, 15, 16, 30, 5, 0, time.FixedZone("CEST", 2*60*60))

	var names []string
	for range 3 {
		name, err := Backup(path, now)
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, filepath.Base(name))
		if got := readFile(t, name); got != broken {
			t.Errorf("%s holds %q, want %q", name, got, broken)
		}
		if runtime.GOOS != "windows" {
			if fi, _ := os.Stat(name); fi.Mode().Perm() != 0o600 {
				t.Errorf("%s mode = %v, want 0600", name, fi.Mode().Perm())
			}
		}
	}
	want := []string{
		"settings.json.bak-20260915T143005Z",
		"settings.json.bak-20260915T143005Z-2",
		"settings.json.bak-20260915T143005Z-3",
	}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("backups = %q, want %q", names, want)
	}
	if got := readFile(t, path); got != broken {
		t.Errorf("Backup changed the file to %q", got)
	}

	if _, err := Backup(filepath.Join(t.TempDir(), FileName), now); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Backup of a missing file: err = %v, want ErrNotExist", err)
	}
}

func ptrBool(v bool) *bool { return &v }
