package listen

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// bannedListens are the identifiers that open a listening socket behind this
// package's back, skipping the loopback default and the LAN opt-in.
var bannedListens = map[string][]string{
	"net":        {"Listen", "ListenTCP", "ListenUDP", "ListenPacket", "ListenIP", "ListenMulticastUDP", "ListenConfig"},
	"crypto/tls": {"Listen"},
	"net/http":   {"ListenAndServe", "ListenAndServeTLS"},
}

// httpServerListens are http.Server methods that listen by themselves.
// They are matched on any receiver in a file importing net/http.
var httpServerListens = []string{"ListenAndServe", "ListenAndServeTLS"}

// directListens returns the uses of bannedListens in f.
func directListens(fset *token.FileSet, f *ast.File) []string {
	names := map[string]string{} // local package name -> import path
	importsHTTP := false
	for _, imp := range f.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil || bannedListens[p] == nil {
			continue
		}
		importsHTTP = importsHTTP || p == "net/http"
		name := path.Base(p)
		if imp.Name != nil {
			name = imp.Name.Name
		}
		names[name] = p
	}
	if len(names) == 0 {
		return nil
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok {
			if p, ok := names[id.Name]; ok {
				if slices.Contains(bannedListens[p], sel.Sel.Name) {
					out = append(out, fmt.Sprintf("%s: %s.%s", fset.Position(sel.Pos()), p, sel.Sel.Name))
				}
				return true
			}
		}
		if importsHTTP && slices.Contains(httpServerListens, sel.Sel.Name) {
			out = append(out, fmt.Sprintf("%s: (*http.Server).%s", fset.Position(sel.Pos()), sel.Sel.Name))
		}
		return true
	})
	return out
}

func TestDirectListensDetector(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		src  string
		want int
	}{
		{"net.Listen", `package p; import "net"; func f() { net.Listen("tcp", ":0") }`, 1},
		{"net.ListenPacket", `package p; import "net"; func f() { net.ListenPacket("udp", ":0") }`, 1},
		{"net.ListenTCP", `package p; import "net"; func f() { net.ListenTCP("tcp", nil) }`, 1},
		{"net.ListenUDP", `package p; import "net"; func f() { net.ListenUDP("udp", nil) }`, 1},
		{"net.ListenConfig", `package p; import "net"; var lc net.ListenConfig`, 1},
		{"aliased import", `package p; import stdnet "net"; func f() { stdnet.Listen("tcp", ":0") }`, 1},
		{"tls.Listen", `package p; import "crypto/tls"; func f() { tls.Listen("tcp", ":0", nil) }`, 1},
		{"http.ListenAndServe", `package p; import "net/http"; func f() { http.ListenAndServe(":80", nil) }`, 1},
		{"server method", `package p; import "net/http"; func f() { s := &http.Server{}; s.ListenAndServeTLS("", "") }`, 1},
		{"serve on a given listener", `package p; import ("net"; "net/http"); func f(ln net.Listener) { http.Serve(ln, nil); (&http.Server{}).Serve(ln) }`, 0},
		{"tls.NewListener", `package p; import ("crypto/tls"; "net"); func f(ln net.Listener) { tls.NewListener(ln, nil) }`, 0},
		{"dial is not a listen", `package p; import "net"; func f() { net.Dial("tcp", "x:1") }`, 0},
		{"unrelated Listen", `package p; type l struct{}; func (l) Listen() {}; func f() { var x l; x.Listen() }`, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, "x.go", tt.src, parser.SkipObjectResolution)
			if err != nil {
				t.Fatal(err)
			}
			if got := directListens(fset, f); len(got) != tt.want {
				t.Errorf("found %v, want %d", got, tt.want)
			}
		})
	}
}

// TestNoDirectListens enforces the rule in docs/network.md: outside
// internal/core/netx/listen, non-test Go code never opens a listening socket
// by itself.
func TestNoDirectListens(t *testing.T) {
	t.Parallel()
	root := moduleRoot(t)
	own := filepath.Join(root, "internal", "core", "netx", "listen")
	fset := token.NewFileSet()
	var found []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if p != root && (strings.HasPrefix(name, ".") || name == "node_modules" || name == "testdata" || name == "vendor") || p == own {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		found = append(found, directListens(fset, f)...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) > 0 {
		t.Errorf("open listening sockets with listen.Listen or listen.ListenPacket, not directly (docs/network.md):\n  %s",
			strings.Join(found, "\n  "))
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}
