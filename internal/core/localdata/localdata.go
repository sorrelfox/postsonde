// Package localdata knows where the data PostSonde keeps about a project
// on this computer lives, by kind, so it can be measured and cleared
// (PRV-13, docs/privacy.md#clearing-local-data): the request
// history, the run history, the cookie jars, the OAuth 2.0 tokens, the
// cached GraphQL schemas and gRPC reflection snapshots, and the persisted
// runtime variables of a workspace's local directory (docs/workspace.md#local-side-files).
//
// It deletes files; it never reads what they hold, so an encrypted file
// (SEC-03) is removed while the secrets are locked as well. Services that
// keep some of this in memory are told by the app layer
// (internal/app/services/localdata); this package only handles the disk.
package localdata

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/cookies"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/graphql/schemacache"
	"github.com/sorrelfox/postsonde/internal/core/grpc/reflection"
	"github.com/sorrelfox/postsonde/internal/core/history"
	"github.com/sorrelfox/postsonde/internal/core/oauth2/tokenstore"
	"github.com/sorrelfox/postsonde/internal/core/runner/runstore"
	"github.com/sorrelfox/postsonde/internal/core/runtimevars"
	"github.com/sorrelfox/postsonde/internal/core/workspace"
)

// Category is a kind of local data.
type Category string

// The categories, in the order the UI lists them.
const (
	// History is the request history (NAV-02): <local>/history/.
	History Category = "history"
	// Runs is the run history of the folder runner (RUN-09): <local>/runs/.
	Runs Category = "runs"
	// Cookies are every cookie jar (CK-03): <local>/cookies.json and
	// <local>/cookies/. The cookie settings (cookie-settings.json) are a
	// setting, not data, and stay.
	Cookies Category = "cookies"
	// Tokens are the OAuth 2.0 tokens (AUTH-13): <local>/secrets/oauth2/.
	Tokens Category = "tokens"
	// Schemas are the GraphQL schemas fetched by introspection (GQL-10):
	// <local>/graphql-schemas/, and the gRPC services fetched by server
	// reflection (GRPC-03): <local>/grpc/reflection/. Schema and .proto
	// files of the project are not cached and are not touched.
	Schemas Category = "schemas"
	// RuntimeVariables are the persisted runtime variables (ENV-15) and
	// their secret values: <local>/runtime-variables.json and
	// <local>/secrets/runtime/.
	RuntimeVariables Category = "runtimeVariables"
)

// CodeInvalidCategory: a category name that is not one of All (kind
// invalid_argument). Details: category.
const CodeInvalidCategory = "localdata.invalid_category"

// CodeClearFailed: a file could not be removed (the kind of the cause).
// Details: path, category.
const CodeClearFailed = "localdata.clear_failed"

// All returns every category, in the order the UI lists them.
func All() []Category {
	return []Category{History, Runs, Cookies, Tokens, Schemas, RuntimeVariables}
}

// Parse returns the category named name, or CodeInvalidCategory.
func Parse(name string) (Category, error) {
	c := Category(name)
	if slices.Contains(All(), c) {
		return c, nil
	}
	return "", errs.New(errs.InvalidArgument, "The kind of local data is not known.").
		WithCode(CodeInvalidCategory).
		WithHint("Use history, runs, cookies, tokens, schemas or runtimeVariables.").
		WithDetail("category", name)
}

// entries are a category's names in a local directory: directories and
// files, slash-separated.
func entries(c Category) []string {
	switch c {
	case History:
		return []string{history.DirName}
	case Runs:
		return []string{runstore.Dir}
	case Cookies:
		return []string{cookies.FileName, cookies.JarsDir}
	case Tokens:
		// The whole directory: tokens.json and the temporary files of an
		// interrupted write, which hold tokens too.
		return []string{filepath.ToSlash(filepath.Dir(filepath.FromSlash(tokenstore.FileName)))}
	case Schemas:
		return []string{schemacache.DirName, reflection.CacheDir}
	case RuntimeVariables:
		return []string{runtimevars.FileName, runtimevars.SecretsDir}
	}
	return nil
}

// Paths returns the paths of category c in the local directory localDir
// that exist: its files and directories, and what interrupted atomic
// writes left of its files next to them (".<name>.tmp-*").
func Paths(localDir string, c Category) []string {
	var out []string
	for _, name := range entries(c) {
		p := filepath.Join(localDir, filepath.FromSlash(name))
		if _, err := os.Lstat(p); err == nil {
			out = append(out, p)
		}
		dir, base := filepath.Split(p)
		// Temporary files of atomicfile: ".<base>.tmp-<random>".
		matches, _ := filepath.Glob(filepath.Join(dir, "."+globEscape(base)+".tmp-*"))
		out = append(out, matches...)
	}
	return out
}

// globEscape escapes the characters filepath.Match treats specially.
func globEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(`*?[\`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Usage is how much a category takes on disk.
type Usage struct {
	// Files counts the regular files; Bytes their sizes.
	Files int
	Bytes int64
}

// Add returns u plus v.
func (u Usage) Add(v Usage) Usage { return Usage{Files: u.Files + v.Files, Bytes: u.Bytes + v.Bytes} }

// Measure returns how much category c takes in localDir. Files that
// cannot be looked at are skipped.
func Measure(localDir string, c Category) Usage {
	var u Usage
	for _, p := range Paths(localDir, c) {
		u = u.Add(measurePath(p))
	}
	return u
}

// Remove deletes category c from localDir and returns what it took. A
// category with nothing on disk is not an error. It goes on past a path
// that cannot be removed and returns the first failure (CodeClearFailed)
// with what was removed before it; what could not be removed is not
// counted.
func Remove(localDir string, c Category) (Usage, error) {
	var removed Usage
	var first error
	for _, p := range Paths(localDir, c) {
		u := measurePath(p)
		if err := os.RemoveAll(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			if first == nil {
				first = errs.Wrap(err, errs.KindOf(err), "PostSonde could not remove some of its local data.").
					WithCode(CodeClearFailed).
					WithHint("Check that the PostSonde data directory can be written to, then clear again.").
					WithDetail("path", p).
					WithDetail("category", string(c))
			}
			continue
		}
		removed = removed.Add(u)
	}
	return removed, first
}

// measurePath is Measure for one path.
func measurePath(p string) Usage {
	var u Usage
	_ = filepath.WalkDir(p, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				u.Files++
				u.Bytes += info.Size()
			}
		}
		return nil
	})
	return u
}

// LocalDirs returns the local directories of every project the data
// directory dataDir holds (<data>/workspaces/*/, copies included), sorted.
// None, and no error, when there are none yet.
func LocalDirs(dataDir string) ([]string, error) {
	if dataDir == "" {
		return nil, nil
	}
	base := filepath.Join(dataDir, workspace.DirName)
	des, err := os.ReadDir(base)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, errs.Wrap(err, errs.KindOf(err), "PostSonde could not list the local data of its projects.").
			WithCode(CodeClearFailed).
			WithHint("Check that the PostSonde data directory can be read.").
			WithDetail("path", base)
	}
	var out []string
	for _, de := range des {
		// Directories only: recent.json sits next to them. A link is not
		// followed out of the data directory.
		if de.IsDir() && !strings.HasPrefix(de.Name(), ".") {
			out = append(out, filepath.Join(base, de.Name()))
		}
	}
	return out, nil
}
