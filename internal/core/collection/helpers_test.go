package collection

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// tempDir returns a symlink-resolved temporary directory, as project roots
// are.
func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// copySample copies testdata/sample into a new temporary project and sets
// every modification time to a fixed past instant, so a write shows.
func copySample(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(tempDir(t), "sample")
	if err := os.CopyFS(dir, os.DirFS(filepath.Join("testdata", "sample"))); err != nil {
		t.Fatal(err)
	}
	past := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(path, past, past)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// writeFile writes content to dir/rel, creating the directories.
func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, dir, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// snapshot maps every entry below root to its type, mode, modification
// time and content hash, to detect any change or write. It reads each
// entry's own metadata: on Windows a directory listing has a copy of it
// that NTFS updates lazily, so a file created in a folder before a
// snapshot can change the folder's time in a later one.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := os.Lstat(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		entry := fmt.Sprintf("%v %d", fi.Mode(), fi.ModTime().UnixNano())
		if fi.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			entry += " " + hex.EncodeToString(sum[:])
		}
		out[filepath.ToSlash(rel)] = entry
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// diffSnapshots lists the entries that differ.
func diffSnapshots(before, after map[string]string) []string {
	var diffs []string
	for k, v := range before {
		if after[k] != v {
			diffs = append(diffs, fmt.Sprintf("%s: %q -> %q", k, v, after[k]))
		}
	}
	for k := range after {
		if _, ok := before[k]; !ok {
			diffs = append(diffs, k+": new")
		}
	}
	slices.Sort(diffs)
	return diffs
}

// dump renders the tree and its problems as text, with dir replaced by
// <root>, for golden files.
func dump(tree *Tree, problems []Problem) string {
	var b strings.Builder
	tree.Walk(func(n *Node) bool {
		depth := 0
		for p := n.Parent; p != nil; p = p.Parent {
			depth++
		}
		fmt.Fprintf(&b, "%s%s %s", strings.Repeat("  ", depth), n.Kind, n.ID)
		if n.FileKind != "" {
			fmt.Fprintf(&b, " kind=%s", n.FileKind)
		}
		fmt.Fprintf(&b, " name=%q path=%q", n.DisplayName(), n.Path)
		if n.ReadOnly != Writable {
			fmt.Fprintf(&b, " readOnly=%s", n.ReadOnly)
		}
		if n.IDChanged {
			b.WriteString(" idChanged")
		}
		b.WriteString("\n")
		return true
	})
	b.WriteString("problems:\n")
	for _, p := range problems {
		fmt.Fprintf(&b, "  %s node=%s kind=%s code=%s\n", p.Path, p.NodeID, errs.KindOf(p.Err), p.Err.Code)
		keys := make([]string, 0, len(p.Err.Details))
		for k := range p.Err.Details {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			v := strings.ReplaceAll(filepath.ToSlash(p.Err.Details[k]), filepath.ToSlash(tree.Dir()), "<root>")
			fmt.Fprintf(&b, "    %s=%s\n", k, v)
		}
	}
	return b.String()
}

// checkErr checks an error's kind and code, and that it has a hint.
func checkErr(t *testing.T, err error, kind errs.Kind, code string) {
	t.Helper()
	var e *errs.Error
	if !errors.As(err, &e) {
		t.Fatalf("err = %v, want an *errs.Error with code %s", err, code)
	}
	if errs.KindOf(err) != kind || e.Code != code || e.Hint == "" {
		t.Errorf("err = kind %s code %q hint %q, want kind %s code %q and a hint", errs.KindOf(err), e.Code, e.Hint, kind, code)
	}
}
