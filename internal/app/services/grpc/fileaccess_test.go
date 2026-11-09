package grpc

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/fileaccess"
)

// PRV-10: .proto files and import paths are read from the project folder
// or an allowed directory only. A refused one is a
// fileaccess.not_allowed diagnostic in Describe and refuses a call; a
// link out of the project is refused, and an import through such a link
// is not found.
func TestProtoFilesAllowlist(t *testing.T) {
	t.Parallel()
	s, dir := newProject(t, nil)
	allowed, outside := tempDir(t), tempDir(t)
	writeFile(t, allowed, "greeter.proto", greeter)
	writeFile(t, outside, "greeter.proto", greeter)
	writeFile(t, outside, "lib/dep.proto", `syntax = "proto3"; package lib; message Dep {}`)
	writeFile(t, dir, "demo/uses_dep.proto", `syntax = "proto3"; import "lib/dep.proto"; message U { lib.Dep d = 1; }`)
	for target, link := range map[string]string{filepath.Join(outside, "greeter.proto"): "escape.proto", filepath.Join(outside, "lib"): "demo/lib"} {
		if err := os.Symlink(target, filepath.Join(dir, filepath.FromSlash(link))); err != nil {
			if runtime.GOOS == "windows" {
				t.Skip("symbolic links need privileges on Windows:", err)
			}
			t.Fatal(err)
		}
	}
	WithAllowedDirs(func() []string { return []string{allowed} })(s)

	refused := func(res DescribeResultDTO, file, subject string) bool {
		for _, d := range res.Diagnostics {
			if d.Code == fileaccess.CodeNotAllowed && d.Severity == "error" && d.File == file &&
				strings.HasPrefix(d.Message, subject+" ") && strings.Contains(d.Message, "outside the directories allowed") {
				return true
			}
		}
		return false
	}
	for _, tc := range []struct {
		name, file string
		allowed    bool
	}{
		{"project-relative", "demo/greeter.proto", true},
		{"allowed directory", filepath.Join(allowed, "greeter.proto"), true},
		{"outside", filepath.Join(outside, "greeter.proto"), false},
		{"link escaping", "escape.proto", false},
		{"folder link escaping", "demo/lib/dep.proto", false},
	} {
		res := describe(t, s, DescribeInputDTO{Files: []string{tc.file}})
		_, _, callErr := s.findMethod(t.Context(), []string{tc.file}, nil, "demo.v1.Greeter", "SayHello")
		if tc.allowed {
			if res.Errors != 0 || len(res.Services) != 1 || callErr != nil {
				t.Errorf("%s: Describe = %+v, findMethod = %v", tc.name, res.Diagnostics, callErr)
			}
			continue
		}
		if !refused(res, tc.file, "The .proto file") || len(res.Services) != 0 {
			t.Errorf("%s: Describe = %+v", tc.name, res.Diagnostics)
		}
		if e, ok := callErr.(*errs.Error); !ok || e.Code != fileaccess.CodeNotAllowed || e.Hint != fileaccess.HintAllow {
			t.Errorf("%s: findMethod = %v", tc.name, callErr)
		}
	}

	// An import path outside is refused; one inside the project works.
	res := describe(t, s, DescribeInputDTO{Files: []string{"demo/greeter.proto"}, ImportPaths: []string{outside, "demo"}})
	if !refused(res, outside, "The import path") || len(res.Services) != 1 {
		t.Errorf("import path outside: %+v", res.Diagnostics)
	}
	if _, _, err := s.findMethod(t.Context(), []string{"demo/greeter.proto"}, []string{outside}, "demo.v1.Greeter", "SayHello"); err == nil {
		t.Error("a call with an import path outside was not refused")
	}
	// An import that leads out through a link in an import path is not
	// found, and nothing is read from outside.
	res = describe(t, s, DescribeInputDTO{Files: []string{"demo/uses_dep.proto"}, ImportPaths: []string{"demo"}})
	found := false
	for _, d := range res.Diagnostics {
		found = found || (d.Code == "proto.import_not_found" && strings.Contains(d.Message, "lib/dep.proto"))
	}
	if !found {
		t.Errorf("import through a link: %+v", res.Diagnostics)
	}
}

// A setup linked while its folder was allowed is not served from the
// cache once the folder is removed from the allowlist.
func TestProtoAllowlistChangeSkipsTheCache(t *testing.T) {
	t.Parallel()
	s, _ := newProject(t, nil)
	allowed := tempDir(t)
	writeFile(t, allowed, "greeter.proto", greeter)
	dirs := []string{allowed}
	WithAllowedDirs(func() []string { return dirs })(s)
	in := DescribeInputDTO{Files: []string{filepath.Join(allowed, "greeter.proto")}}
	if res := describe(t, s, in); res.Errors != 0 {
		t.Fatalf("allowed: %+v", res.Diagnostics)
	}
	if res := describe(t, s, in); !res.Cached {
		t.Fatal("not cached")
	}
	dirs = nil
	if res := describe(t, s, in); res.Cached || res.Errors != 1 || len(res.Services) != 0 {
		t.Errorf("after removing the folder: %+v", res)
	}
}
