package core

import (
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestCoreImportsStdlibOnly enforces the dependency rule: internal/core and
// its tests may import only the standard library and this module's own
// packages. scripts/check-core-deps.sh runs the same check for CI.
func TestCoreImportsStdlibOnly(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go tool not in PATH")
	}

	root := filepath.Dir(goOutput(t, goBin, ".", "env", "GOMOD"))
	mod := goOutput(t, goBin, root, "list", "-m")
	out := goOutput(t, goBin, root, "list", "-e", "-deps", "-test",
		"-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", "./internal/core/...")

	var bad []string
	for line := range strings.Lines(out) {
		fields := strings.Fields(line) // "path [pkg.test]" for recompiled packages
		if len(fields) == 0 || allowedImport(mod, fields[0]) {
			continue
		}
		bad = append(bad, fields[0])
	}
	slices.Sort(bad)
	bad = slices.Compact(bad)
	if len(bad) > 0 {
		t.Errorf("internal/core must import only the standard library and %s; found:\n  %s",
			mod, strings.Join(bad, "\n  "))
	}
}

// allowedImport reports whether a non-standard package path belongs to
// module mod.
func allowedImport(mod, path string) bool {
	return path == mod || strings.HasPrefix(path, mod+"/")
}

func TestAllowedImport(t *testing.T) {
	const mod = "github.com/sorrelfox/postsonde"
	tests := []struct {
		name string
		path string
		want bool
	}{
		{"module root", mod, true},
		{"own core package", mod + "/internal/core", true},
		{"own test helper", mod + "/internal/testutil/golden", true},
		{"own test binary", mod + "/internal/core.test", true},
		{"third party", "github.com/wailsapp/wails/v3/pkg/application", false},
		{"x repo", "golang.org/x/net/http2", false},
		{"lookalike prefix", mod + "-fork/internal/core", false},
		{"parent path", "github.com/sorrelfox", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := allowedImport(mod, tt.path); got != tt.want {
				t.Errorf("allowedImport(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func goOutput(t *testing.T, goBin, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(goBin, args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		msg := err.Error()
		if ee, ok := err.(*exec.ExitError); ok {
			msg += ": " + string(ee.Stderr)
		}
		t.Fatalf("go %s: %s", strings.Join(args, " "), msg)
	}
	return strings.TrimSpace(string(out))
}
