package snippet

import (
	"context"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netx"
)

// runner runs or checks the code of one language with a tool installed on
// the machine (the tests that use it are skipped without it, and with
// -short). The code of each case is in root/<case>/<file>.
type runner struct {
	lang string
	// tool must be on PATH; ready, when set, checks the rest (a package).
	tool  string
	ready func() bool
	file  string
	// build, when set, prepares the code of every case at once (compiles
	// it) before run runs one.
	build func(t *testing.T, root string, names []string)
	run   func(ctx context.Context, root, name string) *exec.Cmd
	// syntax, when set, checks the code without running it.
	syntax func(t *testing.T, root string, names []string)
	// skip lists the cases the client cannot send as PostSonde does,
	// with why (docs/snippets.md).
	skip map[string]string
	// noTextPartTypes: a text part's Content-Type is not sent.
	noTextPartTypes bool
	// posix: the code is a POSIX shell command (shellFor).
	posix bool
}

// shellRun runs the command in root/name/cmd.sh with shellFor, with
// extra words after it.
func shellRun(extra string) func(ctx context.Context, root, name string) *exec.Cmd {
	return func(ctx context.Context, root, name string) *exec.Cmd {
		code, _ := os.ReadFile(filepath.Join(root, name, "cmd.sh"))
		return exec.CommandContext(ctx, shellFor(string(code)), "-c", string(code)+extra)
	}
}

// noPOSIXShell is why the POSIX shell commands are not run on Windows:
// there a POSIX shell (Git Bash) hands a native program such as curl.exe
// its arguments through the C runtime's quoting and the ANSI code page,
// which drops backslashes before a quote and turns text that is not ASCII
// into question marks. The commands are for a POSIX system; cmd and
// PowerShell have commands of their own.
const noPOSIXShell = "POSIX shell commands are not run on Windows"

// shellFor returns the shell to run the POSIX command cmd in: sh, or,
// for a command with $'…' words (control characters, bytes that are not
// UTF-8), bash where sh cannot read them. POSIX.1-2024 has $'…' and dash
// reads it from 0.5.13 on, but the sh of Debian 13 is dash 0.5.12. It
// returns "" when no shell here can run cmd.
func shellFor(cmd string) string {
	if !strings.Contains(cmd, "$'") {
		return "sh"
	}
	return dollarQuoteShell()
}

// dollarQuoteShell is sh, or bash, whichever reads $'…' first; "" for
// neither.
var dollarQuoteShell = sync.OnceValue(func() string {
	for _, sh := range []string{"sh", "bash"} {
		out, err := exec.Command(sh, "-c", `printf '%s' $'a\tb'`).Output()
		if err == nil && string(out) == "a\tb" {
			return sh
		}
	}
	return ""
})

func runners() []runner {
	return []runner{
		{lang: LanguageCurl, tool: "curl", file: "cmd.sh", run: shellRun(" -s -S -o /dev/null"), posix: true},
		{lang: "python", tool: "python3", file: "main.py",
			ready: func() bool { return exec.Command("python3", "-c", "import requests").Run() == nil },
			run: func(ctx context.Context, root, name string) *exec.Cmd {
				return exec.CommandContext(ctx, "python3", filepath.Join(root, name, "main.py"))
			},
			syntax: func(t *testing.T, root string, names []string) {
				args := []string{"-m", "py_compile"}
				for _, n := range names {
					args = append(args, filepath.Join(root, n, "main.py"))
				}
				cmd := exec.Command("python3", args...)
				cmd.Env = append(os.Environ(), "PYTHONPYCACHEPREFIX="+filepath.Join(root, "pycache"))
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Errorf("py_compile: %v\n%s", err, out)
				}
			},
			skip: map[string]string{
				"url_as_typed":  "requests percent-encodes { } [ ] | \" and removes dot segments",
				"custom_method": "requests upper-cases the method",
			}},
		{lang: "fetch", tool: "node", file: "main.mjs",
			run: func(ctx context.Context, root, name string) *exec.Cmd {
				return exec.CommandContext(ctx, "node", filepath.Join(root, name, "main.mjs"))
			},
			syntax: func(t *testing.T, root string, names []string) {
				for _, n := range names {
					if out, err := exec.Command("node", "--check", filepath.Join(root, n, "main.mjs")).CombinedOutput(); err != nil {
						t.Errorf("node --check %s: %v\n%s", n, err, out)
					}
				}
			},
			skip: map[string]string{
				"url_as_typed":      "the URL parser encodes \" and removes dot segments",
				"get_with_body":     "fetch refuses a body with GET",
				"quoting_multipart": "FormData sends a line feed in a text part as CRLF",
			},
			noTextPartTypes: true},
		{lang: "wget", tool: "wget", file: "cmd.sh", run: shellRun(" -q"), posix: true,
			skip: map[string]string{
				"url_as_typed":  "wget percent-encodes { } | \" and removes dot segments",
				"custom_method": "wget upper-cases the method",
			}},
		{lang: "go", tool: "go", file: "main.go",
			// One module with a main package per case: go build and go vet
			// take them all at once.
			build: func(t *testing.T, root string, _ []string) {
				goTool(t, root, "build", "-o", filepath.Join(root, "bin")+string(filepath.Separator), "./...")
			},
			run: func(ctx context.Context, root, name string) *exec.Cmd {
				return exec.CommandContext(ctx, filepath.Join(root, "bin", name))
			},
			syntax: func(t *testing.T, root string, _ []string) { goTool(t, root, "vet", "./...") }},
		{lang: "java", tool: "javac", file: "Main.java",
			build: func(t *testing.T, root string, names []string) { javac(t, root, names) },
			run: func(ctx context.Context, root, name string) *exec.Cmd {
				return exec.CommandContext(ctx, "java", "-cp", filepath.Join(root, "classes"), javaClass(name))
			},
			syntax: javac,
			skip: map[string]string{
				"url_as_typed":    "URI.create refuses { } | \"",
				"get_headers":     "HttpClient sends a header character that is not ASCII as ?",
				"quoting_headers": "HttpClient sends a header character that is not ASCII as ?",
			}},
		{lang: "csharp", tool: "dotnet", file: "Program.cs",
			// dotnet run runs a single .cs file from .NET 10 on.
			ready: func() bool {
				out, err := exec.Command("dotnet", "--version").Output()
				major, _, _ := strings.Cut(strings.TrimSpace(string(out)), ".")
				n, _ := strconv.Atoi(major)
				return err == nil && n >= 10
			},
			run: func(ctx context.Context, root, name string) *exec.Cmd {
				cmd := exec.CommandContext(ctx, "dotnet", "run", "Program.cs")
				cmd.Dir = filepath.Join(root, name)
				return cmd
			}},
		{lang: "httpie", tool: "http", file: "cmd.sh", run: shellRun(""), noTextPartTypes: true, posix: true},
	}
}

// goTool runs go with args in root, a module of the cases' main packages.
func goTool(t *testing.T, root string, args ...string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module snippets\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local", "GOFLAGS=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go %s: %v\n%s", args[0], err, out)
	}
}

// javaClass is the class a case's Main is renamed to, so that one javac
// compiles every case.
func javaClass(name string) string { return "Snippet_" + name }

// javac compiles every case's Main.java, renamed, into root/classes, with
// every lint warning an error. The files are UTF-8, which javac before
// Java 18 does not assume (on Windows it reads windows-1252).
func javac(t *testing.T, root string, names []string) {
	t.Helper()
	args := []string{"-encoding", "UTF-8", "-Xlint:all", "-Werror", "-d", filepath.Join(root, "classes")}
	for _, n := range names {
		code, err := os.ReadFile(filepath.Join(root, n, "Main.java"))
		if err != nil {
			t.Fatal(err)
		}
		src := filepath.Join(root, javaClass(n)+".java")
		code = []byte(strings.Replace(string(code), "public class Main {", "public class "+javaClass(n)+" {", 1))
		if err := os.WriteFile(src, code, 0o644); err != nil {
			t.Fatal(err)
		}
		args = append(args, src)
	}
	if out, err := exec.Command("javac", args...).CombinedOutput(); err != nil {
		t.Fatalf("javac: %v\n%s", err, out)
	}
}

// available returns the runners whose tools are installed.
func available(t *testing.T) []runner {
	t.Helper()
	if testing.Short() {
		t.Skip("-short")
	}
	var out []runner
	for _, r := range runners() {
		if r.posix && runtime.GOOS == "windows" {
			t.Logf("%s: %s, skipped", r.lang, noPOSIXShell)
			continue
		}
		if _, err := exec.LookPath(r.tool); err != nil {
			t.Logf("%s: %s is not installed, skipped", r.lang, r.tool)
			continue
		}
		if r.ready != nil && !r.ready() {
			t.Logf("%s: %s cannot run it, skipped", r.lang, r.tool)
			continue
		}
		out = append(out, r)
	}
	return out
}

// writeCases writes the code of each request in root/<name>/<file> and
// returns the names of those the language writes.
func writeCases(t *testing.T, root, file string, lang Language, reqs []namedRequest) []string {
	t.Helper()
	var names []string
	for _, c := range reqs {
		code, err := lang.Generate(c.r)
		if err != nil {
			continue
		}
		if err := os.MkdirAll(filepath.Join(root, c.name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, c.name, file), []byte(code), 0o644); err != nil {
			t.Fatal(err)
		}
		names = append(names, c.name)
	}
	return names
}

// TestSnippetsCompile checks the code of every golden request with the
// language's own tool (python3 -m py_compile, node --check, go vet, javac),
// each skipped when the tool is not installed.
func TestSnippetsCompile(t *testing.T) {
	reqs := goldenRequests(t)
	for _, run := range available(t) {
		if run.syntax == nil {
			continue
		}
		t.Run(run.lang, func(t *testing.T) {
			lang, err := LanguageByID(run.lang)
			if err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			run.syntax(t, root, writeCases(t, root, run.file, lang, reqs))
		})
	}
}

// TestSnippetsSend runs the code of every language that has a runner
// against a test server, and checks the server receives what PostSonde's
// own client sends for the same request, apart from the headers each
// client adds of its own.
func TestSnippetsSend(t *testing.T) {
	runs := available(t)
	got := make(chan received, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got <- received{method: r.Method, uri: r.RequestURI, header: r.Header.Clone(), body: body}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	client := httpclient.New(netx.NewDirect(netx.Options{}), httpclient.TransportOptions{})
	defer client.CloseIdleConnections()

	// The cases, on the test server.
	var cases []curlCase
	var reqs []namedRequest
	for _, c := range languageCases() {
		switch c.name {
		case "digest_auth", // needs a challenge
			"no_redirects_http2": // the test server has no h2c
			continue
		}
		if c.secrets != nil {
			continue
		}
		local := func(u string) string {
			u = strings.Replace(u, "https://", "http://", 1)
			return strings.Replace(u, "api.test", host, 1)
		}
		if c.graphql != nil {
			g := *c.graphql
			g.URL = local(g.URL)
			c.graphql = &g
		} else {
			c.req.URL = local(c.req.URL)
		}
		cases = append(cases, c)
		reqs = append(reqs, namedRequest{name: c.name, r: requestOf(t, c)})
	}

	for _, run := range runs {
		lang, err := LanguageByID(run.lang)
		if err != nil {
			t.Fatal(err)
		}
		root := t.TempDir()
		written := writeCases(t, root, run.file, lang, reqs)
		if run.build != nil {
			run.build(t, root, written)
		}
		for i, c := range cases {
			t.Run(run.lang+"/"+c.name, func(t *testing.T) {
				if why, ok := run.skip[c.name]; ok {
					t.Skip(why)
				}
				if !slices.Contains(written, c.name) {
					_, err := lang.Generate(reqs[i].r)
					t.Skip(err)
				}
				code, _ := os.ReadFile(filepath.Join(root, c.name, run.file))
				if run.posix && shellFor(string(code)) == "" {
					t.Skip("neither sh nor bash reads $'…'")
				}
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				defer cancel()
				cl := client
				if c.cookies != nil {
					cl = client.WithCookieJar(fixedJar(c.cookies))
				}
				if _, err := cl.Send(ctx, buildCase(t, c).Request); err != nil {
					t.Fatalf("send: %v", err)
				}
				want := <-got

				cmd := run.run(ctx, root, c.name)
				if cmd.Dir == "" {
					cmd.Dir = filepath.Join(root, c.name)
				}
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("%s: %v\n%s\ncode:\n%s", run.lang, err, out, code)
				}
				var have received
				select {
				case have = <-got:
				case <-ctx.Done():
					t.Fatalf("nothing was sent:\n%s", code)
				}
				compareSent(t, string(code), have, want, run.noTextPartTypes)
			})
		}
	}
}

// ownHeaders are the headers a client adds of its own, ignored when
// PostSonde sends none of that name.
var ownHeaders = []string{"User-Agent", "Accept", "Accept-Encoding", "Accept-Language", "Connection", "Keep-Alive",
	"Sec-Fetch-Mode", "Upgrade", "Http2-Settings"}

// compareSent checks that a snippet's request (have) is PostSonde's
// (want), apart from ownHeaders, Content-Length (a client may stream) and
// the multipart boundary; with noTextPartTypes, apart from the
// Content-Type of text parts.
func compareSent(t *testing.T, code string, have, want received, noTextPartTypes bool) {
	t.Helper()
	if have.method != want.method || have.uri != want.uri {
		t.Errorf("request line %s %s, want %s %s\n%s", have.method, have.uri, want.method, want.uri, code)
	}
	for _, name := range ownHeaders {
		if _, ok := want.header[name]; !ok {
			delete(have.header, name)
		}
	}
	multi := strings.HasPrefix(want.header.Get("Content-Type"), "multipart/")
	for _, h := range []http.Header{have.header, want.header} {
		h.Del("Content-Length")
		if multi {
			mt, _, _ := mime.ParseMediaType(h.Get("Content-Type"))
			h.Set("Content-Type", mt)
		}
	}
	names := func(h http.Header) []string {
		var out []string
		for k, v := range h {
			out = append(out, k+": "+strings.Join(v, " | "))
		}
		slices.Sort(out)
		return out
	}
	if h, w := names(have.header), names(want.header); !slices.Equal(h, w) {
		t.Errorf("headers\n%s\nwant\n%s\ncode:\n%s", strings.Join(h, "\n"), strings.Join(w, "\n"), code)
	}
	if !multi {
		if string(have.body) != string(want.body) {
			t.Errorf("body %q, want %q\n%s", have.body, want.body, code)
		}
		return
	}
	h, w := parts(t, have), parts(t, want)
	if noTextPartTypes {
		for _, ps := range [][]string{h, w} {
			for i, p := range ps {
				if f := strings.SplitN(p, " | ", 3); !strings.Contains(f[0], "filename=") {
					ps[i] = f[0] + " |  | " + f[2]
				}
			}
		}
	}
	if !slices.Equal(h, w) {
		t.Errorf("parts\n%s\nwant\n%s\ncode:\n%s", strings.Join(h, "\n"), strings.Join(w, "\n"), code)
	}
}
