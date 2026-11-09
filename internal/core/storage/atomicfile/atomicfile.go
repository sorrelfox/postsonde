package atomicfile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/sorrelfox/postsonde/internal/core/storage/stablejson"
)

// hooks lets tests interrupt WriteFile between its steps. All fields are
// nil in production. Tests that set them must not run in parallel.
var hooks struct {
	write        func(f *os.File, data []byte) error // replaces writing data to the temp file
	beforeSync   func() error
	beforeRename func() error
}

// WriteFile atomically replaces the file at path with data. If the file
// exists its permission bits are kept; otherwise it is created with perm.
// On error the file at path is left as it was and no temporary file
// remains (except after a crash; see the package documentation).
func WriteFile(path string, data []byte, perm fs.FileMode) (err error) {
	perm &= fs.ModePerm
	switch fi, statErr := os.Stat(path); {
	case statErr == nil && fi.IsDir():
		return fmt.Errorf("atomicfile: write %s: is a directory", path)
	case statErr == nil:
		perm = fi.Mode().Perm()
	case !errors.Is(statErr, fs.ErrNotExist):
		return fmt.Errorf("atomicfile: %w", statErr)
	}

	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("atomicfile: create temp file for %s: %w", path, err)
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			_ = f.Close() // may already be closed
			_ = os.Remove(tmp)
		}
	}()

	if hooks.write != nil {
		err = hooks.write(f, data)
	} else {
		_, err = f.Write(data)
	}
	if err != nil {
		return fmt.Errorf("atomicfile: write %s: %w", tmp, err)
	}
	// Set the mode before the fsync so it is made durable with the data.
	if err = f.Chmod(perm); err != nil {
		return fmt.Errorf("atomicfile: %w", err)
	}
	if hooks.beforeSync != nil {
		if err = hooks.beforeSync(); err != nil {
			return fmt.Errorf("atomicfile: sync %s: %w", tmp, err)
		}
	}
	if err = f.Sync(); err != nil {
		return fmt.Errorf("atomicfile: %w", err)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("atomicfile: %w", err)
	}
	if hooks.beforeRename != nil {
		if err = hooks.beforeRename(); err != nil {
			return fmt.Errorf("atomicfile: rename %s: %w", tmp, err)
		}
	}
	if err = os.Rename(tmp, path); err != nil {
		return fmt.Errorf("atomicfile: %w", err)
	}

	// The new content is in place; the temp file no longer exists.
	if syncErr := syncDir(dir); syncErr != nil {
		return fmt.Errorf("atomicfile: sync directory of %s: %w", path, syncErr)
	}
	return nil
}

// WriteJSON encodes v with [stablejson.Marshal] and writes it to path with
// [WriteFile]. Nothing is written if v cannot be encoded.
func WriteJSON(path string, v any, perm fs.FileMode) error {
	data, err := stablejson.Marshal(v)
	if err != nil {
		return fmt.Errorf("atomicfile: encode %s: %w", path, err)
	}
	return WriteFile(path, data, perm)
}
