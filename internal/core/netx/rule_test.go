package netx

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

// bannedDials are the identifiers that open connections behind netx's
// back: a dialer, the default HTTP client or transport, a TLS dial, or a
// DNS lookup (which sends queries to the system's resolvers; PRV-05,
// docs/privacy.md#audit-what-could-phone-home).
var bannedDials = map[string][]string{
	"net": {"Dial", "DialTimeout", "DialTCP", "DialUDP", "DialIP", "DialUnix", "Dialer",
		"DefaultResolver", "LookupHost", "LookupIP", "LookupAddr", "LookupCNAME", "LookupMX", "LookupNS", "LookupSRV", "LookupTXT"},
	"net/http":   {"DefaultClient", "DefaultTransport", "Get", "Head", "Post", "PostForm"},
	"crypto/tls": {"Dial", "DialWithDialer", "Dialer"},
}

// directDials returns the uses of bannedDials in f.
func directDials(fset *token.FileSet, f *ast.File) []string {
	names := map[string]string{} // local package name -> import path
	for _, imp := range f.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil || bannedDials[p] == nil {
			continue
		}
		name := path.Base(p)
		if imp.Name != nil {
			name = imp.Name.Name
		}
		names[name] = p
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if p, ok := names[id.Name]; ok && slices.Contains(bannedDials[p], sel.Sel.Name) {
			out = append(out, fmt.Sprintf("%s: %s.%s", fset.Position(sel.Pos()), p, sel.Sel.Name))
		}
		return true
	})
	return out
}

func TestDirectDialsDetector(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		src  string
		want int
	}{
		{"net.Dial", `package p; import "net"; func f() { net.Dial("tcp", "x:1") }`, 1},
		{"net.DialTimeout", `package p; import "net"; func f() { net.DialTimeout("tcp", "x:1", 0) }`, 1},
		{"net.Dialer literal", `package p; import "net"; func f() { (&net.Dialer{}).Dial("tcp", "x:1") }`, 1},
		{"default client", `package p; import "net/http"; func f() { http.DefaultClient.Do(nil) }`, 1},
		{"default transport", `package p; import "net/http"; var t = http.DefaultTransport`, 1},
		{"http.Get", `package p; import "net/http"; func f() { http.Get("http://x") }`, 1},
		{"aliased import", `package p; import stdnet "net"; func f() { stdnet.Dial("tcp", "x:1") }`, 1},
		{"tls.Dial", `package p; import "crypto/tls"; func f() { tls.Dial("tcp", "x:1", nil) }`, 1},
		{"DNS lookup", `package p; import "net"; func f() { net.LookupHost("x") }`, 1},
		{"default resolver", `package p; import ("context"; "net"); func f() { net.DefaultResolver.LookupNetIP(context.Background(), "ip", "x") }`, 1},
		{"own resolver type is fine", `package p; import "net"; var r *net.Resolver`, 0},
		{"listen is fine", `package p; import "net"; func f() { net.Listen("tcp", ":0") }`, 0},
		{"own transport is fine", `package p; import "net/http"; var t = &http.Transport{}; var m = http.MethodGet`, 0},
		{"unrelated Dial", `package p; type d struct{}; func (d) Dial() {}; func f() { var x d; x.Dial() }`, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, "x.go", tt.src, parser.SkipObjectResolution)
			if err != nil {
				t.Fatal(err)
			}
			if got := directDials(fset, f); len(got) != tt.want {
				t.Errorf("found %v, want %d", got, tt.want)
			}
		})
	}
}

// TestNoDirectDials enforces the rule in docs/network.md: outside
// internal/core/netx, non-test Go code never dials or resolves a name by
// itself or uses the default HTTP client or transport.
func TestNoDirectDials(t *testing.T) {
	t.Parallel()
	root := moduleRoot(t)
	own := filepath.Join(root, "internal", "core", "netx")
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
		found = append(found, directDials(fset, f)...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) > 0 {
		t.Errorf("open connections through a netx.Dialer, not directly (docs/network.md):\n  %s",
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
