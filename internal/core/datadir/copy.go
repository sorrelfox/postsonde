package datadir

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/sorrelfox/postsonde/internal/core/appdirs"
)

// moveMarker is created in the target before copying and removed once the
// copy is verified. Finding it means an earlier attempt was interrupted,
// and the entries it left may be replaced.
const moveMarker = ".postsonde-move"

// junk are files an OS or file manager puts in folders; a folder holding
// only these counts as empty.
var junk = map[string]bool{".DS_Store": true, ".localized": true, "Thumbs.db": true, "desktop.ini": true}

// CopyData copies the DataEntries present in from into to, then checks
// the copy file by file (size and SHA-256) and returns the entries copied.
// to is created if missing. It must not hold PostSonde data already, and
// the entries must not exist there, unless an earlier interrupted CopyData
// left them (its marker file is there).
//
// On error, whatever CopyData created in to is removed again and from is
// never changed.
func CopyData(from, to string) (_ []string, err error) {
	var entries []string
	if within(from, to) || within(to, from) {
		return nil, fmt.Errorf("%s and %s overlap", from, to)
	}
	for _, name := range DataEntries {
		if _, err := os.Lstat(filepath.Join(from, name)); err == nil {
			entries = append(entries, name)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	createdTo := false
	if _, err := os.Stat(to); errors.Is(err, fs.ErrNotExist) {
		createdTo = true
	}
	if err := os.MkdirAll(to, appdirs.DirPerm); err != nil {
		return nil, err
	}
	marker := filepath.Join(to, moveMarker)
	if _, err := os.Lstat(marker); err == nil {
		// Leftovers of an interrupted attempt.
		if err := removeEntries(to, DataEntries); err != nil {
			return nil, err
		}
	} else if hasData(to) {
		return nil, fmt.Errorf("%s already holds PostSonde data", to)
	}
	if err := os.WriteFile(marker, []byte("PostSonde is copying its data here.\n"), FilePerm); err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = removeEntries(to, entries)
			_ = os.Remove(marker)
			if createdTo {
				_ = os.Remove(to) // only when empty
			}
		}
	}()
	for _, name := range entries {
		if err := copyTree(filepath.Join(from, name), filepath.Join(to, name)); err != nil {
			return nil, err
		}
	}
	for _, name := range entries {
		if err := verifyTree(filepath.Join(from, name), filepath.Join(to, name)); err != nil {
			return nil, err
		}
	}
	if err := os.Remove(marker); err != nil {
		return nil, err
	}
	if entries == nil {
		entries = []string{}
	}
	return entries, nil
}

// copyTree copies src (a file, folder or symlink) to dst, which must not
// exist. Permissions are kept; files are synced to disk.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch mode := info.Mode(); {
		case mode.IsDir():
			return os.Mkdir(target, mode.Perm()|0o700)
		case mode&fs.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case mode.IsRegular():
			return copyFile(path, target, mode.Perm())
		default:
			return fmt.Errorf("%s: cannot copy a %v", path, mode.Type())
		}
	})
}

func copyFile(src, dst string, perm fs.FileMode) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); err == nil {
			err = cerr
		}
	}()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

// verifyTree checks that dst has everything src has: the same folders,
// symlinks with the same targets, and files with the same size and
// SHA-256.
func verifyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		want, err := os.Lstat(path)
		if err != nil {
			return err
		}
		got, err := os.Lstat(target)
		if err != nil {
			return fmt.Errorf("copy check: %w", err)
		}
		if want.Mode().Type() != got.Mode().Type() {
			return fmt.Errorf("copy check: %s is a different kind of file", target)
		}
		switch {
		case want.Mode()&fs.ModeSymlink != 0:
			a, err1 := os.Readlink(path)
			b, err2 := os.Readlink(target)
			if err := errors.Join(err1, err2); err != nil {
				return err
			}
			if a != b {
				return fmt.Errorf("copy check: %s points elsewhere", target)
			}
		case want.Mode().IsRegular():
			if want.Size() != got.Size() {
				return fmt.Errorf("copy check: %s has %d bytes, want %d", target, got.Size(), want.Size())
			}
			a, err1 := fileHash(path)
			b, err2 := fileHash(target)
			if err := errors.Join(err1, err2); err != nil {
				return err
			}
			if !bytes.Equal(a, b) {
				return fmt.Errorf("copy check: %s differs from the original", target)
			}
		}
		return nil
	})
}

func fileHash(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}

// removeEntries removes the named top-level entries of dir. Names with a
// path separator or "." / ".." are refused.
func removeEntries(dir string, names []string) error {
	var errList []error
	for _, name := range names {
		if !plainName(name) {
			errList = append(errList, fmt.Errorf("%q is not a plain name", name))
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, name)); err != nil {
			errList = append(errList, err)
		}
	}
	return errors.Join(errList...)
}

func plainName(name string) bool {
	return name != "" && name != "." && name != ".." && filepath.Base(name) == name && !filepath.IsAbs(name)
}

// hasData reports whether dir holds PostSonde data (one of DataEntries).
func hasData(dir string) bool {
	for _, name := range DataEntries {
		if _, err := os.Lstat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	return false
}

// isEmpty reports whether dir has nothing but junk files. A missing dir is
// empty.
func isEmpty(dir string) (bool, error) {
	list, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	for _, e := range list {
		if !junk[e.Name()] {
			return false, nil
		}
	}
	return true, nil
}
