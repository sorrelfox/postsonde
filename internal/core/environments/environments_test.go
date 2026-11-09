package environments

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/localvalues"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
	"github.com/sorrelfox/postsonde/internal/core/workspace"
)

func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func newStore(t *testing.T) (*Store, string, string) {
	t.Helper()
	root, local := tempDir(t), tempDir(t)
	return New(root, local, false), root, local
}

func checkCode(t *testing.T, err error, kind errs.Kind, code string) {
	t.Helper()
	var e *errs.Error
	if !errors.As(err, &e) {
		t.Fatalf("err = %v, want an *errs.Error", err)
	}
	if errs.KindOf(err) != kind || e.Code != code {
		t.Errorf("err = kind %s code %q (%v), want kind %s code %q", errs.KindOf(err), e.Code, err, kind, code)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSlug(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, want string }{
		{"Staging", "staging"},
		{"  Local dev (8080) ", "local-dev-8080"},
		{"Prod / EU--West", "prod-eu-west"},
		{"Прод", "environment"},
		{"...", "environment"},
		{"CON", "con-env"},
		{"com1", "com1-env"},
		{"com0", "com0"},
		{strings.Repeat("a", 80), strings.Repeat("a", 60)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := slug(tt.name); got != tt.want {
				t.Errorf("slug(%q) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

func TestSecretsKey(t *testing.T) {
	t.Parallel()
	tests := []struct {
		id    string
		plain bool
	}{
		{"env_92f0d5b8a61c3e47", true},
		{"env_my-env_1", true},
		{"env_", false},
		{"../../etc", false},
		{"env_ABC", false},
		{"prod", false},
		{"env_" + strings.Repeat("a", 61), false},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			t.Parallel()
			got := SecretsKey(tt.id)
			if (got == tt.id) != tt.plain {
				t.Errorf("SecretsKey(%q) = %q, plain = %v", tt.id, got, tt.plain)
			}
			if !tt.plain && (!strings.HasPrefix(got, "x.") || strings.ContainsAny(got, `/\`)) {
				t.Errorf("SecretsKey(%q) = %q, want x.<hash>", tt.id, got)
			}
		})
	}
}

func TestCreateRenameDelete(t *testing.T) {
	t.Parallel()
	s, root, _ := newStore(t)
	dir := filepath.Join(root, "environments")

	snap, err := s.List()
	if err != nil || len(snap.Environments) != 0 || snap.ActiveID != "" || snap.Problems == nil {
		t.Fatalf("List on a new project = %+v, %v", snap, err)
	}

	staging, err := s.Create("  Staging ")
	if err != nil {
		t.Fatal(err)
	}
	if staging.Name != "Staging" || staging.FileName != "staging.json" || !strings.HasPrefix(staging.ID, "env_") {
		t.Errorf("Create = %+v", staging)
	}
	want := "{\n  \"schemaVersion\": 1,\n  \"kind\": \"environment\",\n  \"id\": \"" + staging.ID + "\",\n  \"name\": \"Staging\"\n}\n"
	if got := readFile(t, filepath.Join(dir, "staging.json")); got != want {
		t.Errorf("staging.json =\n%s\nwant\n%s", got, want)
	}

	// A second environment whose slug clashes gets a numbered file.
	other, err := s.Create("staging!")
	if err != nil {
		t.Fatal(err)
	}
	if other.FileName != "staging-2.json" {
		t.Errorf("clashing slug: file %q, want staging-2.json", other.FileName)
	}

	_, err = s.Create("STAGING")
	checkCode(t, err, errs.AlreadyExists, CodeNameTaken)
	_, err = s.Create("   ")
	checkCode(t, err, errs.InvalidArgument, CodeInvalidName)
	_, err = s.Create(strings.Repeat("x", MaxNameLength+1))
	checkCode(t, err, errs.InvalidArgument, CodeInvalidName)

	renamed, err := s.Rename(other.ID, "Production")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Name != "Production" || renamed.FileName != "production.json" || renamed.ID != other.ID {
		t.Errorf("Rename = %+v", renamed)
	}
	if _, err := os.Stat(filepath.Join(dir, "staging-2.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("old file still there: %v", err)
	}
	_, err = s.Rename(other.ID, "staging")
	checkCode(t, err, errs.AlreadyExists, CodeNameTaken)
	// Renaming to the same name in another case keeps the file.
	if env, err := s.Rename(staging.ID, "STAGING"); err != nil || env.FileName != "staging.json" || env.Name != "STAGING" {
		t.Errorf("case-only rename = %+v, %v", env, err)
	}

	snap, err = s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Environments) != 2 || snap.Environments[0].Name != "Production" || snap.Environments[1].Name != "STAGING" {
		t.Errorf("List = %+v, want Production, STAGING", snap.Environments)
	}

	if err := s.SetActive(renamed.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(renamed.ID); err != nil {
		t.Fatal(err)
	}
	snap, _ = s.List()
	if len(snap.Environments) != 1 || snap.ActiveID != "" {
		t.Errorf("after deleting the active environment: %+v", snap)
	}
	checkCode(t, s.Delete(renamed.ID), errs.NotFound, CodeNotFound)
	_, err = s.Rename("env_missing", "x")
	checkCode(t, err, errs.NotFound, CodeNotFound)
	checkCode(t, s.SetActive("env_missing"), errs.NotFound, CodeNotFound)
}

func TestSaveVariablesKeepsSecretsLocal(t *testing.T) {
	t.Parallel()
	s, root, local := newStore(t)
	env, err := s.Create("Production")
	if err != nil {
		t.Fatal(err)
	}
	vars := []Variable{
		{Name: "baseUrl", Value: "https://api.acme.example"},
		{Name: "token", Value: "prd-s3cr3t-value", Secret: true, Description: "Personal access token"},
		{Name: "old", Value: "1", Disabled: true},
	}
	saved, err := s.SaveVariables(env.ID, vars)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Variables) != 3 || saved.Variables[1].Value != "prd-s3cr3t-value" {
		t.Errorf("SaveVariables = %+v", saved)
	}

	file := readFile(t, filepath.Join(root, "environments", "production.json"))
	want := `{
  "schemaVersion": 1,
  "kind": "environment",
  "id": "` + env.ID + `",
  "name": "Production",
  "variables": [
    {
      "name": "baseUrl",
      "value": "https://api.acme.example"
    },
    {
      "name": "token",
      "value": "",
      "secret": true,
      "description": "Personal access token"
    },
    {
      "name": "old",
      "value": "1",
      "disabled": true
    }
  ]
}
`
	if file != want {
		t.Errorf("project file =\n%s\nwant\n%s", file, want)
	}

	secretsPath := filepath.Join(local, "secrets", "environments", env.ID+".json")
	if got := readFile(t, secretsPath); !strings.Contains(got, "prd-s3cr3t-value") {
		t.Errorf("secrets file = %s", got)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(secretsPath)
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("secrets file mode = %v, %v; want 0600", fi.Mode().Perm(), err)
		}
	}
	assertNotInProject(t, root, "prd-s3cr3t-value")

	got, err := s.Get(env.ID)
	if err != nil || got.Variables[1].Value != "prd-s3cr3t-value" || !got.Variables[1].Secret {
		t.Errorf("Get = %+v, %v", got, err)
	}
	if v, ok := got.Scope().Lookup("token"); !ok || !v.Secret || v.Value != "prd-s3cr3t-value" {
		t.Errorf("Scope token = %+v, %v", v, ok)
	}
	if _, ok := got.Scope().Lookup("old"); ok {
		t.Error("Scope has the disabled variable")
	}
	r := template.Resolver{Scopes: template.Chain{got.Scope()}}.Render("{{baseUrl}}/x")
	if r.Text != "https://api.acme.example/x" {
		t.Errorf("rendered = %q", r.Text)
	}

	// No secrets left: the secrets file goes away.
	if _, err := s.SaveVariables(env.ID, vars[:1]); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(secretsPath); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("secrets file still there: %v", err)
	}

	// Deleting removes the secrets file too.
	if _, err := s.SaveVariables(env.ID, vars); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(env.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(secretsPath); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("secrets file after Delete: %v", err)
	}
}

// A secret value typed into an environment file by hand is used as it is,
// and leaves the file the next time PostSonde writes it, even for a rename:
// a local value of the same name is kept instead.
func TestRenameMovesHandTypedSecretsOut(t *testing.T) {
	t.Parallel()
	s, root, local := newStore(t)
	writeFile(t, filepath.Join(root, "environments", "prod.json"), `{
  "schemaVersion": 1, "kind": "environment", "id": "env_prod", "name": "Prod",
  "variables": [
    {"name": "token", "value": "hand-typed-token", "secret": true},
    {"name": "pin", "value": "hand-typed-pin", "secret": true},
    {"name": "host", "value": "api.example"}
  ]
}`)
	writeFile(t, filepath.Join(local, "secrets", "environments", "env_prod.json"),
		`{"version": 1, "environmentId": "env_prod", "values": {"pin": "local-pin"}}`)

	got, err := s.Get("env_prod")
	if err != nil {
		t.Fatal(err)
	}
	if got.Variables[0].Value != "hand-typed-token" || got.Variables[1].Value != "local-pin" {
		t.Errorf("loaded = %+v", got.Variables)
	}

	renamed, err := s.Rename("env_prod", "Production")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Variables[0].Value != "hand-typed-token" || renamed.Variables[1].Value != "local-pin" {
		t.Errorf("renamed = %+v", renamed.Variables)
	}
	assertNotInProject(t, root, "hand-typed-token")
	assertNotInProject(t, root, "hand-typed-pin")
	secrets := readFile(t, filepath.Join(local, "secrets", "environments", "env_prod.json"))
	if !strings.Contains(secrets, "hand-typed-token") || !strings.Contains(secrets, "local-pin") {
		t.Errorf("secrets file = %s", secrets)
	}
}

func TestSaveVariablesKeepsUnknownMembersAndSkipsNoOps(t *testing.T) {
	t.Parallel()
	s, root, _ := newStore(t)
	path := filepath.Join(root, "environments", "hand.json")
	writeFile(t, path, `{
  "schemaVersion": 1,
  "kind": "environment",
  "id": "env_0000000000000001",
  "name": "Hand made",
  "color": "#d0342c",
  "variables": [
    {"name": "a", "value": "1", "note": "keep me"},
    {"name": "token", "value": "typed-by-hand", "secret": true}
  ],
  "owner": "me"
}
`)
	env, err := s.Get("env_0000000000000001")
	if err != nil {
		t.Fatal(err)
	}
	// A secret value typed into the file by hand is shown…
	if env.Variables[1].Value != "typed-by-hand" || env.Color != "#d0342c" {
		t.Errorf("Get = %+v", env)
	}
	// …and moves to the local file on save.
	if _, err := s.SaveVariables(env.ID, env.Variables); err != nil {
		t.Fatal(err)
	}
	file := readFile(t, path)
	for _, want := range []string{`"note": "keep me"`, `"owner": "me"`, `"color": "#d0342c"`} {
		if !strings.Contains(file, want) {
			t.Errorf("file lost %s:\n%s", want, file)
		}
	}
	assertNotInProject(t, root, "typed-by-hand")

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveVariables(env.ID, env.Variables); err != nil {
		t.Fatal(err)
	}
	fi2, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(fi, fi2) {
		t.Error("an unchanged save rewrote the file")
	}
}

// ENV-08: the colour round-trips through the environment file, other
// members are kept, and invalid colours are refused.
func TestSetColorRoundTrip(t *testing.T) {
	t.Parallel()
	s, root, _ := newStore(t)
	path := filepath.Join(root, "environments", "prod.json")
	writeFile(t, path, `{
  "schemaVersion": 1,
  "kind": "environment",
  "id": "env_prod",
  "name": "Production",
  "variables": [{"name": "host", "value": "api.example", "note": "keep me"}],
  "owner": "me"
}
`)
	env, err := s.SetColor("env_prod", "#D0342C")
	if err != nil {
		t.Fatal(err)
	}
	if env.Color != "#d0342c" {
		t.Errorf("SetColor returned colour %q", env.Color)
	}
	file := readFile(t, path)
	for _, want := range []string{`"color": "#d0342c"`, `"note": "keep me"`, `"owner": "me"`, `"name": "Production"`} {
		if !strings.Contains(file, want) {
			t.Errorf("file lacks %s:\n%s", want, file)
		}
	}
	// A new Store (a restart) reads it back.
	again, err := New(root, t.TempDir(), false).Get("env_prod")
	if err != nil || again.Color != "#d0342c" {
		t.Errorf("reloaded = %+v, %v", again, err)
	}

	fi, _ := os.Stat(path)
	if _, err := s.SetColor("env_prod", "#d0342c"); err != nil {
		t.Fatal(err)
	}
	if fi2, _ := os.Stat(path); !os.SameFile(fi, fi2) {
		t.Error("setting the same colour rewrote the file")
	}

	for _, bad := range []string{"red", "#d0342", "#d0342c80", "d0342c", "#d0342c;", "url(x)"} {
		_, err := s.SetColor("env_prod", bad)
		checkCode(t, err, errs.InvalidArgument, CodeInvalidColor)
	}
	if !strings.Contains(readFile(t, path), `"color": "#d0342c"`) {
		t.Error("a refused colour changed the file")
	}

	if _, err := s.SetColor("env_prod", ""); err != nil {
		t.Fatal(err)
	}
	if file := readFile(t, path); strings.Contains(file, `"color"`) {
		t.Errorf("clearing the colour left it in the file:\n%s", file)
	}
	_, err = s.SetColor("env_missing", "#000000")
	checkCode(t, err, errs.NotFound, CodeNotFound)
}

func TestListProblems(t *testing.T) {
	t.Parallel()
	s, root, _ := newStore(t)
	dir := filepath.Join(root, "environments")
	good := `{"schemaVersion": 1, "kind": "environment", "id": "env_1", "name": "Good"}`
	writeFile(t, filepath.Join(dir, "good.json"), good)
	writeFile(t, filepath.Join(dir, "copy.json"), strings.Replace(good, "Good", "Copy", 1))
	writeFile(t, filepath.Join(dir, "broken.json"), `{"kind": `)
	writeFile(t, filepath.Join(dir, "request.json"), `{"kind": "http", "id": "req_1", "name": "x", "method": "GET", "url": ""}`)
	writeFile(t, filepath.Join(dir, "noid.json"), `{"kind": "environment", "name": "No id"}`)
	writeFile(t, filepath.Join(dir, ".good.json.tmp-123"), good)
	writeFile(t, filepath.Join(dir, "_notes.json"), `{}`)
	writeFile(t, filepath.Join(dir, "readme.md"), `# notes`)
	if err := os.MkdirAll(filepath.Join(dir, "sub.json"), 0o755); err != nil {
		t.Fatal(err)
	}

	snap, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Environments) != 1 {
		t.Fatalf("Environments = %+v", snap.Environments)
	}
	codes := map[string]string{}
	for _, p := range snap.Problems {
		var e *errs.Error
		if errors.As(p.Err, &e) {
			codes[p.FileName] = e.Code
		}
	}
	// copy.json sorts before good.json, so it is the one loaded and
	// good.json is the duplicate.
	want := map[string]string{
		"good.json":    CodeDuplicateID,
		"broken.json":  projectfile.CodeParseFailed,
		"request.json": projectfile.CodeWrongKind,
		"noid.json":    CodeMissingID,
	}
	if len(codes) != len(want) {
		t.Errorf("problems = %v, want %v", codes, want)
	}
	for f, c := range want {
		if codes[f] != c {
			t.Errorf("problem for %s = %q, want %q", f, codes[f], c)
		}
	}
}

func TestReadOnly(t *testing.T) {
	t.Parallel()
	root, local := tempDir(t), tempDir(t)
	dir := filepath.Join(root, "environments")
	writeFile(t, filepath.Join(dir, "newer.json"), `{"schemaVersion": 99, "kind": "environment", "id": "env_newer", "name": "Newer"}`)
	writeFile(t, filepath.Join(dir, "current.json"), `{"schemaVersion": 1, "kind": "environment", "id": "env_current", "name": "Current"}`)

	s := New(root, local, false)
	snap, err := s.List()
	if err != nil || len(snap.Environments) != 2 || !snap.Environments[1].ReadOnly || snap.Environments[0].ReadOnly {
		t.Fatalf("List = %+v, %v", snap, err)
	}
	_, err = s.SaveVariables("env_newer", nil)
	checkCode(t, err, errs.Unsupported, CodeReadOnly)
	_, err = s.Rename("env_newer", "x")
	checkCode(t, err, errs.Unsupported, CodeReadOnly)
	checkCode(t, s.Delete("env_newer"), errs.Unsupported, CodeReadOnly)
	// The active environment is local state: a newer file can be active.
	if err := s.SetActive("env_newer"); err != nil {
		t.Errorf("SetActive on a newer file: %v", err)
	}

	ro := New(root, local, true)
	_, err = ro.Create("x")
	checkCode(t, err, errs.Unsupported, CodeReadOnly)
	_, err = ro.SaveVariables("env_current", nil)
	checkCode(t, err, errs.Unsupported, CodeReadOnly)
	checkCode(t, ro.Delete("env_current"), errs.Unsupported, CodeReadOnly)
	if err := ro.SetActive("env_current"); err != nil {
		t.Errorf("SetActive in a read-only workspace: %v", err)
	}
	if snap, _ := ro.List(); !snap.ReadOnly || snap.ActiveID != "env_current" {
		t.Errorf("read-only List = %+v", snap)
	}
}

// TestActiveSurvivesRestart opens a project with a workspace.Manager, picks
// an environment, then opens the project again with a new Manager (an app
// restart) and checks the choice is back, and that it was saved only in the
// local directory.
func TestActiveSurvivesRestart(t *testing.T) {
	t.Parallel()
	data, project := tempDir(t), filepath.Join(tempDir(t), "shop-api")

	m := workspace.New(data)
	w, err := m.Create(project, "Shop API")
	if err != nil {
		t.Fatal(err)
	}
	s := New(w.Root, w.LocalDir, w.Newer())
	if _, err := s.Create("Local"); err != nil {
		t.Fatal(err)
	}
	staging, err := s.Create("Staging")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveVariables(staging.ID, []Variable{{Name: "token", Value: "stg-secret-token", Secret: true}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetActive(staging.ID); err != nil {
		t.Fatal(err)
	}
	before := snapshotTree(t, w.Root)

	// Restart: a new Manager reopens the last project.
	m2 := workspace.New(data)
	w2, err := m2.ReopenLast()
	if err != nil || w2 == nil {
		t.Fatalf("ReopenLast = %v, %v", w2, err)
	}
	if w2.LocalDir != w.LocalDir {
		t.Fatalf("local dir changed: %s != %s", w2.LocalDir, w.LocalDir)
	}
	s2 := New(w2.Root, w2.LocalDir, w2.Newer())
	snap, err := s2.List()
	if err != nil {
		t.Fatal(err)
	}
	if snap.ActiveID != staging.ID {
		t.Errorf("active after restart = %q, want %q", snap.ActiveID, staging.ID)
	}
	active, ok, err := s2.Active()
	if err != nil || !ok || active.Name != "Staging" || active.Variables[0].Value != "stg-secret-token" {
		t.Errorf("Active after restart = %+v, %v, %v", active, ok, err)
	}

	// The choice lives in the local directory, outside the project…
	if got := readFile(t, filepath.Join(w2.LocalDir, ActiveFileName)); !strings.Contains(got, staging.ID) {
		t.Errorf("%s = %s", ActiveFileName, got)
	}
	if !strings.HasPrefix(w2.LocalDir, data) || strings.HasPrefix(w2.LocalDir, w2.Root) {
		t.Errorf("local dir %s is not under the data dir %s", w2.LocalDir, data)
	}
	// …and nothing in the project mentions it or the secret.
	assertNotInProject(t, w2.Root, "activeId", "stg-secret-token")
	for path := range snapshotTree(t, w2.Root) {
		if filepath.Base(path) == ActiveFileName || strings.Contains(path, "secrets") {
			t.Errorf("local file in the project: %s", path)
		}
	}
	// Choosing the environment did not touch the project files.
	if err := s2.SetActive(""); err != nil {
		t.Fatal(err)
	}
	after := snapshotTree(t, w2.Root)
	for path, content := range before {
		if after[path] != content {
			t.Errorf("project file %s changed by switching environments", path)
		}
	}
	if len(after) != len(before) {
		t.Errorf("project files: %d before, %d after", len(before), len(after))
	}

	// A third start sees no active environment.
	s3 := New(w2.Root, w2.LocalDir, false)
	if snap, _ := s3.List(); snap.ActiveID != "" {
		t.Errorf("active after clearing = %q", snap.ActiveID)
	}
}

func TestActiveIgnoresDeletedEnvironment(t *testing.T) {
	t.Parallel()
	s, root, _ := newStore(t)
	env, err := s.Create("Gone")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetActive(env.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "environments", env.FileName)); err != nil {
		t.Fatal(err)
	}
	snap, err := s.List()
	if err != nil || snap.ActiveID != "" {
		t.Errorf("List = %+v, %v; want no active environment", snap, err)
	}
	if _, ok, err := s.Active(); ok || err != nil {
		t.Errorf("Active = %v, %v", ok, err)
	}
}

func TestBrokenSecretsFileIsNotOverwritten(t *testing.T) {
	t.Parallel()
	s, _, local := newStore(t)
	env, err := s.Create("Prod")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveVariables(env.ID, []Variable{{Name: "t", Value: "v", Secret: true}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(local, "secrets", "environments", env.ID+".json")
	writeFile(t, path, "{broken")
	_, err = s.Get(env.ID)
	checkCode(t, err, errs.Parse, CodeLocalDataFailed)
	if _, err := s.List(); err == nil {
		t.Error("List with a broken secrets file succeeded")
	}
}

// snapshotTree returns every regular file under root with its content.
func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		out[path] = readFile(t, path)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// assertNotInProject fails if any file under root contains one of the
// strings.
func assertNotInProject(t *testing.T, root string, needles ...string) {
	t.Helper()
	for path, content := range snapshotTree(t, root) {
		for _, n := range needles {
			if strings.Contains(content, n) {
				t.Errorf("%s contains %q:\n%s", path, n, content)
			}
		}
	}
}

// Local values (SEC-02): a variable's value on this computer lives in
// the local directory and replaces the shared value here; the project file
// keeps the shared value and is not even rewritten for a local change.
func TestLocalValuesStayOutOfTheProject(t *testing.T) {
	t.Parallel()
	s, root, local := newStore(t)
	env, err := s.Create("Staging")
	if err != nil {
		t.Fatal(err)
	}
	vars := []Variable{
		{Name: "baseUrl", Value: "https://staging.example", Local: true, LocalValue: "http://localhost:8080"},
		{Name: "user", Value: "team", Local: true, LocalValue: "local-personal-user"},
		{Name: "proxy", Value: "http://proxy.example", Local: true, LocalValue: ""},
		{Name: "token", Value: "stg-s3cret", Secret: true, Local: true, LocalValue: "ignored-for-a-secret"},
		{Name: "plain", Value: "shared-only"},
	}
	saved, err := s.SaveVariables(env.ID, vars)
	if err != nil {
		t.Fatal(err)
	}
	if v := saved.Variables[0]; !v.Local || v.LocalValue != "http://localhost:8080" || v.Value != "https://staging.example" {
		t.Errorf("saved baseUrl = %+v", v)
	}
	if v := saved.Variables[3]; v.Local || v.LocalValue != "" || v.Value != "stg-s3cret" {
		t.Errorf("a secret variable got a local value: %+v", v)
	}

	projectFile := filepath.Join(root, "environments", "staging.json")
	file := readFile(t, projectFile)
	for _, shared := range []string{"https://staging.example", `"value": "team"`, "http://proxy.example", "shared-only"} {
		if !strings.Contains(file, shared) {
			t.Errorf("the project file lost %q:\n%s", shared, file)
		}
	}
	assertNotInProject(t, root, "http://localhost:8080", "local-personal-user", "ignored-for-a-secret", "stg-s3cret", "local")
	localPath := filepath.Join(local, "local-values", "environments", env.ID+".json")
	if got := readFile(t, localPath); !strings.Contains(got, "http://localhost:8080") || !strings.Contains(got, `"proxy": ""`) ||
		strings.Contains(got, "ignored-for-a-secret") || strings.Contains(got, "stg-s3cret") {
		t.Errorf("local values file = %s", got)
	}

	// The scope sends the local value, flagged, and the shared value of
	// the others; an empty local value is a value.
	got, err := s.Get(env.ID)
	if err != nil {
		t.Fatal(err)
	}
	sc := got.Scope()
	for name, want := range map[string]template.Variable{
		"baseUrl": {Value: "http://localhost:8080", Local: true},
		"user":    {Value: "local-personal-user", Local: true},
		"proxy":   {Value: "", Local: true},
		"token":   {Value: "stg-s3cret", Secret: true},
		"plain":   {Value: "shared-only"},
	} {
		if v, ok := sc.Lookup(name); !ok || v != want {
			t.Errorf("Scope %s = %+v, %v, want %+v", name, v, ok, want)
		}
	}

	// Changing only a local value leaves the project file alone.
	before, err := os.Stat(projectFile)
	if err != nil {
		t.Fatal(err)
	}
	vars[0].LocalValue = "http://127.0.0.1:9090"
	if _, err := s.SaveVariables(env.ID, vars); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(projectFile)
	if err != nil || !after.ModTime().Equal(before.ModTime()) || readFile(t, projectFile) != file {
		t.Errorf("the project file was rewritten for a local value")
	}

	// Rename and colour rewrite the project file: still no local value.
	if _, err := s.Rename(env.ID, "Stage"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetColor(env.ID, "#d0342c"); err != nil {
		t.Fatal(err)
	}
	assertNotInProject(t, root, "http://127.0.0.1:9090", "local-personal-user")
	if got, err := s.Get(env.ID); err != nil || got.Variables[0].LocalValue != "http://127.0.0.1:9090" {
		t.Errorf("after rename = %+v, %v", got, err)
	}

	// Removing the local values removes the file; deleting the
	// environment removes it too.
	for i := range vars {
		vars[i].Local = false
	}
	if _, err := s.SaveVariables(env.ID, vars); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(localPath); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("local values file after removing them: %v", err)
	}
	vars[1].Local = true
	if _, err := s.SaveVariables(env.ID, vars); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(env.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(localPath); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("local values file after Delete: %v", err)
	}
}

// A local value kept on this computer for a name the file does not have
// (a teammate renamed it), or for a variable later marked secret, is not
// used; a broken file of local values is an error, not a silent reset.
func TestLocalValuesEdgeCases(t *testing.T) {
	t.Parallel()
	s, root, local := newStore(t)
	writeFile(t, filepath.Join(root, "environments", "dev.json"), `{
  "schemaVersion": 1, "kind": "environment", "id": "env_dev", "name": "Dev",
  "variables": [
    {"name": "host", "value": "shared-host"},
    {"name": "key", "value": "", "secret": true}
  ]
}`)
	path := filepath.Join(local, "local-values", "environments", "env_dev.json")
	writeFile(t, path, `{"version": 1, "owner": "env_dev", "values": {"host": "local-host", "key": "not-used", "gone": "orphan"}}`)
	got, err := s.Get("env_dev")
	if err != nil {
		t.Fatal(err)
	}
	if v := got.Variables[0]; !v.Local || v.Effective() != "local-host" {
		t.Errorf("host = %+v", v)
	}
	if v := got.Variables[1]; v.Local || v.Effective() != "" {
		t.Errorf("secret key = %+v", v)
	}
	if _, ok := got.Scope().Lookup("gone"); ok {
		t.Error("an orphan local value is in the scope")
	}

	writeFile(t, path, "{broken")
	_, err = s.Get("env_dev")
	checkCode(t, err, errs.Parse, localvalues.CodeLocalDataFailed)
	if _, err := s.List(); err == nil {
		t.Error("List with a broken local values file succeeded")
	}
}
