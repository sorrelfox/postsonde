package app

import (
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	appcookies "github.com/sorrelfox/postsonde/internal/app/services/cookies"
	historyservice "github.com/sorrelfox/postsonde/internal/app/services/history"
	"github.com/sorrelfox/postsonde/internal/app/services/localdata"
	"github.com/sorrelfox/postsonde/internal/app/services/runtimevars"
	"github.com/sorrelfox/postsonde/internal/app/services/secrets"
	"github.com/sorrelfox/postsonde/internal/app/services/workspace"
	"github.com/sorrelfox/postsonde/internal/core/cookies"
	corehistory "github.com/sorrelfox/postsonde/internal/core/history"
	corelocaldata "github.com/sorrelfox/postsonde/internal/core/localdata"
	"github.com/sorrelfox/postsonde/internal/core/localvalues"
	"github.com/sorrelfox/postsonde/internal/core/oauth2"
	"github.com/sorrelfox/postsonde/internal/core/oauth2/tokenstore"
	"github.com/sorrelfox/postsonde/internal/core/requestsecrets"
	"github.com/sorrelfox/postsonde/internal/core/runner/report"
	"github.com/sorrelfox/postsonde/internal/core/runner/runstore"
	coreruntimevars "github.com/sorrelfox/postsonde/internal/core/runtimevars"
)

// localDataApp are the bound services the clear-local-data test drives.
type localDataApp struct {
	workspace *workspace.WorkspaceService
	history   *historyservice.HistoryService
	runtime   *runtimevars.RuntimeVarsService
	cookies   *appcookies.CookiesService
	secrets   *secrets.SecretsService
	localdata *localdata.LocalDataService
}

func localDataServices(t *testing.T, data string) localDataApp {
	t.Helper()
	var out localDataApp
	for _, s := range services(Config{DataDir: data}) {
		switch v := s.Instance().(type) {
		case *workspace.WorkspaceService:
			out.workspace = v
		case *historyservice.HistoryService:
			out.history = v
		case *runtimevars.RuntimeVarsService:
			out.runtime = v
		case *appcookies.CookiesService:
			out.cookies = v
		case *secrets.SecretsService:
			out.secrets = v
		case *localdata.LocalDataService:
			out.localdata = v
		}
	}
	if out.workspace == nil || out.history == nil || out.runtime == nil || out.cookies == nil || out.secrets == nil || out.localdata == nil {
		t.Fatal("a service is not bound")
	}
	return out
}

// fillLocalData puts something of every kind of local data on disk in
// the local directory dir, through the stores that write it, and the
// files that are not local data of those kinds (they must stay). Every
// value holds "LEAK-".
func fillLocalData(t *testing.T, dir string) {
	t.Helper()
	hist, err := corehistory.Open(filepath.Join(dir, corehistory.DirName), corehistory.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hist.Append(corehistory.Entry{Summary: corehistory.Summary{Method: "GET", URL: "https://api.test/LEAK-history"}}); err != nil {
		t.Fatal(err)
	}
	_ = hist.Close()
	if err := runstore.Open(filepath.Join(dir, runstore.Dir), runstore.Limits{}).Save(report.Run{Summary: report.Summary{ID: "run_1", Folder: "LEAK-run"}}); err != nil {
		t.Fatal(err)
	}
	for _, jar := range []string{"", "env_1"} {
		st, err := cookies.OpenJar(dir, jar, cookies.Options{})
		if err != nil {
			t.Fatal(err)
		}
		st.Jar().SetCookieLines(&url.URL{Scheme: "https", Host: "api.test", Path: "/"}, []string{"sid=LEAK-cookie; Max-Age=3600"})
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
	}
	tokenstore.New(func() (string, error) { return dir, nil }).Put("k1", oauth2.Token{AccessToken: "LEAK-token", Expiry: time.Now().Add(time.Hour)})
	rt, err := coreruntimevars.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []coreruntimevars.Variable{
		{Name: "kept", Value: "LEAK-runtime", Persist: true},
		{Name: "keptSecret", Value: "LEAK-runtime-secret", Secret: true, Persist: true},
	} {
		if err := rt.Set(v); err != nil {
			t.Fatal(err)
		}
	}
	writeTestFile(t, filepath.Join(dir, "graphql-schemas", "0123abcd.json"), `{"endpoint":"https://api.test/LEAK-schema"}`)
	writeTestFile(t, filepath.Join(dir, "grpc", "reflection", "0123abcd.json"), `{"address":"grpc.test:443","services":["LEAK-grpc"]}`)
	// What an interrupted write leaves.
	writeTestFile(t, filepath.Join(dir, ".cookies.json.tmp-42"), "LEAK-cookie")
	writeTestFile(t, filepath.Join(dir, ".runtime-variables.json.tmp-7"), "LEAK-runtime")

	// Not local data of those kinds: kept by every clear.
	if err := requestsecrets.New(dir).Write("req_1", map[string]string{"auth.token": "KEEP-request-secret"}); err != nil {
		t.Fatal(err)
	}
	if err := localvalues.New(dir).Write(localvalues.Global("ws_x"), map[string]string{"region": "KEEP-local-value"}); err != nil {
		t.Fatal(err)
	}
	if err := cookies.WriteSettings(dir, cookies.Settings{PerEnvironment: true}); err != nil {
		t.Fatal(err)
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// leftovers returns the files under dir of the categories cats (by path)
// or holding "LEAK-" (for the categories in plain files).
func leftovers(t *testing.T, dir string, cats []corelocaldata.Category) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		rel = filepath.ToSlash(rel)
		for _, c := range cats {
			if categoryOf(rel) == c {
				out = append(out, rel)
				return nil
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// categoryOf names the category a file of the data directory belongs to
// ("" for none), from its path: workspaces/<id>/<entry>….
func categoryOf(rel string) corelocaldata.Category {
	parts := strings.SplitN(rel, "/", 3)
	if len(parts) < 3 || parts[0] != "workspaces" {
		return ""
	}
	p := parts[2]
	switch {
	case strings.HasPrefix(p, "history/"):
		return corelocaldata.History
	case strings.HasPrefix(p, "runs/"):
		return corelocaldata.Runs
	case p == "cookies.json" || strings.HasPrefix(p, "cookies/") || strings.HasPrefix(p, ".cookies.json.tmp-"):
		return corelocaldata.Cookies
	case strings.HasPrefix(p, "secrets/oauth2/"):
		return corelocaldata.Tokens
	case strings.HasPrefix(p, "graphql-schemas/") || strings.HasPrefix(p, "grpc/reflection/"):
		return corelocaldata.Schemas
	case p == "runtime-variables.json" || strings.HasPrefix(p, "secrets/runtime/") || strings.HasPrefix(p, ".runtime-variables.json.tmp-"):
		return corelocaldata.RuntimeVariables
	}
	return ""
}

// After clearing, nothing of the history, run history,
// cookies, tokens, cached schemas and runtime variables is left on disk,
// in memory either (nothing is written back), with the secrets encrypted
// and locked; what was not ticked, and every other file, stays.
func TestClearLocalData(t *testing.T) {
	t.Parallel()
	data := t.TempDir()
	ctx := t.Context()
	app := localDataServices(t, data)

	// Project B is opened, then left for project A, which stays open.
	b, err := app.workspace.Create(ctx, workspace.CreateInputDTO{Path: filepath.Join(t.TempDir(), "b")})
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.workspace.Create(ctx, workspace.CreateInputDTO{Path: filepath.Join(t.TempDir(), "a")})
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{a.LocalDir, b.LocalDir} {
		fillLocalData(t, dir)
	}
	// The open project holds some in memory too: a history entry recorded
	// by a send, a session runtime variable, a cookie not saved yet.
	historyservice.Recorder(app.history)()(corehistory.Entry{Summary: corehistory.Summary{Method: "POST", URL: "https://api.test/LEAK-send"}})
	if _, err := app.runtime.Set(ctx, runtimevars.SetInputDTO{Name: "session", Value: "LEAK-session-secret", Secret: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.cookies.Set(ctx, appcookies.SetInputDTO{Cookie: appcookies.CookieDTO{Name: "mem", Value: "LEAK-memory-cookie", Domain: "api.test", HostOnly: true, Path: "/"}}); err != nil {
		t.Fatal(err)
	}

	// The secrets are encrypted, then locked: clearing needs no password.
	if _, err := app.secrets.Enable(ctx, secrets.PasswordInputDTO{Password: "correct horse battery"}); err != nil {
		t.Fatal(err)
	}
	if st, err := app.secrets.Lock(ctx); err != nil || !st.Locked {
		t.Fatalf("Lock = %+v, %v", st, err)
	}

	usage, err := app.localdata.Usage(ctx, localdata.UsageInputDTO{Scope: localdata.ScopeAll})
	if err != nil {
		t.Fatal(err)
	}
	if usage.Projects != 2 || usage.Project != "a" || len(usage.Categories) != len(corelocaldata.All()) {
		t.Fatalf("usage = %+v", usage)
	}
	for _, c := range usage.Categories {
		if c.Files == 0 || c.Bytes == 0 {
			t.Errorf("usage of %s = %+v: the store was not filled", c.Category, c)
		}
	}

	// 1. The open project only, runs and cookies not ticked.
	some := []corelocaldata.Category{corelocaldata.History, corelocaldata.Tokens, corelocaldata.Schemas, corelocaldata.RuntimeVariables}
	res, err := app.localdata.Clear(ctx, localdata.ClearInputDTO{Scope: localdata.ScopeProject, Categories: names(some)})
	if err != nil || len(res.Failed) != 0 || res.Projects != 1 || len(res.Removed) != len(some) {
		t.Fatalf("Clear = %+v, %v", res, err)
	}
	for _, r := range res.Removed {
		if r.Files == 0 {
			t.Errorf("%s: nothing removed", r.Category)
		}
	}
	if left := leftovers(t, a.LocalDir, some); len(left) > 0 {
		t.Errorf("left in the open project: %v", left)
	}
	for _, c := range []corelocaldata.Category{corelocaldata.Runs, corelocaldata.Cookies} {
		if corelocaldata.Measure(a.LocalDir, c).Files == 0 {
			t.Errorf("%s of the open project were not ticked but are gone", c)
		}
	}
	for _, c := range corelocaldata.All() {
		if corelocaldata.Measure(b.LocalDir, c).Files == 0 {
			t.Errorf("%s of the other project are gone", c)
		}
	}
	if page, err := app.history.List(ctx, historyservice.ListInputDTO{}); err != nil || page.All != 0 {
		t.Errorf("history after clearing = %+v, %v", page, err)
	}
	if vars, err := app.runtime.List(ctx); err != nil || len(vars.Variables) != 0 {
		t.Errorf("runtime variables after clearing = %+v, %v", vars, err)
	}

	// 2. Everything, every project.
	res, err = app.localdata.Clear(ctx, localdata.ClearInputDTO{Scope: localdata.ScopeAll, Categories: names(corelocaldata.All())})
	if err != nil || len(res.Failed) != 0 || res.Projects != 2 {
		t.Fatalf("Clear all = %+v, %v", res, err)
	}
	// Past the cookie jars' save delay: nothing is written back.
	time.Sleep(cookies.DefaultSaveDelay + 300*time.Millisecond)
	if left := leftovers(t, data, corelocaldata.All()); len(left) > 0 {
		t.Errorf("left in the data directory: %v", left)
	}
	err = filepath.WalkDir(data, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		content, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(content), "LEAK-") {
			t.Errorf("%s still holds cleared data", path)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if list, err := app.cookies.List(ctx); err != nil || len(list.Cookies) != 0 {
		t.Errorf("cookies after clearing = %+v, %v", list, err)
	}
	usage, err = app.localdata.Usage(ctx, localdata.UsageInputDTO{Scope: localdata.ScopeAll})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range usage.Categories {
		if c.Files != 0 {
			t.Errorf("usage of %s after clearing = %+v", c.Category, c)
		}
	}

	// What is not local data of those kinds stays, encrypted secrets
	// included.
	for _, dir := range []string{a.LocalDir, b.LocalDir} {
		for _, keep := range []string{
			filepath.Join("secrets", "requests", "req_1.json"),
			filepath.Join("local-values", "global", "ws_x.json"),
			cookies.SettingsFileName,
			coreworkspaceLocalFile,
		} {
			if _, err := os.Stat(filepath.Join(dir, keep)); err != nil {
				t.Errorf("%s was removed: %v", keep, err)
			}
		}
	}
	for _, keep := range []string{filepath.Join(data, "secrets", "vault.json"), filepath.Join(a.Path, "postsonde.json")} {
		if _, err := os.Stat(keep); err != nil {
			t.Errorf("%s was removed: %v", keep, err)
		}
	}
}

// coreworkspaceLocalFile is the file that ties a local directory to its
// project.
const coreworkspaceLocalFile = "local.json"

func names(cats []corelocaldata.Category) []string {
	out := make([]string, 0, len(cats))
	for _, c := range cats {
		out = append(out, string(c))
	}
	return slices.Clip(out)
}
