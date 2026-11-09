// Package themes reads custom UI themes (UI-05): local JSON files in the
// app data directory's themes folder that override the UI's colour tokens
// and the code editor's syntax colours. Only known tokens are accepted and
// every value must parse as a colour (ParseColor), so a theme file can
// never inject other CSS. Themes are never fetched from anywhere else. See
// docs/themes.md.
package themes

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// DirName is the themes folder in the app data directory.
const DirName = "themes"

// Limits.
const (
	// MaxFileSize is the largest theme file read, in bytes.
	MaxFileSize = 256 << 10
	// MaxThemes is how many theme files List reads; later ones (by file
	// name) are left out.
	MaxThemes = 200
	// MaxNameLength is the longest theme name, in characters.
	MaxNameLength = 100
	// MaxIDLength is the longest theme file name, in bytes.
	MaxIDLength = 128
)

// Base themes: the built-in token set a custom theme starts from.
const (
	BaseDark  = "dark"
	BaseLight = "light"
)

// Severities of a Problem.
const (
	// SeverityError: the theme cannot be used.
	SeverityError = "error"
	// SeverityWarning: the theme is used; the member is ignored.
	SeverityWarning = "warning"
)

// Problem codes. The errors (as errs.Error) are the codes of the
// functions that fail; the others describe one file.
const (
	// CodeListFailed: the themes folder could not be read (an error).
	// Details: path.
	CodeListFailed = "themes.list_failed"
	// CodeNotFound: no theme file has the ID (an error). Details: id.
	CodeNotFound = "themes.not_found"
	// CodeReadFailed: the file could not be read.
	CodeReadFailed = "themes.read_failed"
	// CodeTooLarge: the file is larger than MaxFileSize.
	CodeTooLarge = "themes.too_large"
	// CodeInvalidJSON: the file is not a JSON object.
	CodeInvalidJSON = "themes.invalid_json"
	// CodeInvalidName: "name" is missing, not a string, blank or too long.
	CodeInvalidName = "themes.invalid_name"
	// CodeInvalidBase: "base" is not "dark" or "light".
	CodeInvalidBase = "themes.invalid_base"
	// CodeInvalidSection: "colors" or "editor" is not an object.
	CodeInvalidSection = "themes.invalid_section"
	// CodeInvalidColor: a token's value is not a string or not a colour.
	CodeInvalidColor = "themes.invalid_color"
	// CodeUnknownKey: a member PostSonde does not know (a warning).
	CodeUnknownKey = "themes.unknown_key"
)

// ColorTokens maps the keys of a theme's "colors" to the UI's CSS custom
// properties (frontend/src/styles/tokens.css). --shadow is left out: it is
// a shadow, not a colour.
var ColorTokens = map[string]string{
	"bg0": "--bg0", "bg1": "--bg1", "bg2": "--bg2", "bg3": "--bg3",
	"line": "--line", "line2": "--line2",
	"text": "--text", "text2": "--text2", "text3": "--text3",
	"accent": "--accent", "accent-ink": "--accent-ink", "accent-dim": "--accent-dim",
	"m-get": "--m-get", "m-post": "--m-post", "m-put": "--m-put", "m-patch": "--m-patch",
	"m-del": "--m-del", "m-gql": "--m-gql", "m-dir": "--m-dir", "m-ws": "--m-ws",
	"ok": "--ok", "warn": "--warn", "bad": "--bad",
	"sel": "--sel", "scrim": "--scrim",
}

// EditorTokens maps the keys of a theme's "editor" to the code editor's
// syntax colour properties (--tk-*).
var EditorTokens = map[string]string{
	"keyword":     "--tk-kw",
	"field":       "--tk-field",
	"argument":    "--tk-arg",
	"variable":    "--tk-var",
	"string":      "--tk-str",
	"number":      "--tk-num",
	"type":        "--tk-type",
	"directive":   "--tk-dir",
	"punctuation": "--tk-punct",
	"comment":     "--tk-cm",
	"key":         "--tk-key",
	"boolean":     "--tk-bool",
	"plain":       "--tk-plain",
	"error":       "--tk-err",
}

// Problem is something wrong with a theme file.
type Problem struct {
	// Severity is SeverityError or SeverityWarning.
	Severity string
	Code     string
	// Key is the member, e.g. "base" or "colors.bg0"; "" for the file.
	Key     string
	Message string
}

// Var is a CSS custom property a theme sets.
type Var struct {
	// Name is the property, e.g. "--bg0".
	Name string
	// Value is the colour as "#rrggbb" or "#rrggbbaa" (ParseColor).
	Value string
}

// Theme is a theme file as read.
type Theme struct {
	// ID is the file name in the themes folder, e.g. "solarized.json".
	ID string
	// Name is the theme's name; the ID when the file has none.
	Name string
	// Base is BaseDark or BaseLight ("" when the file has none).
	Base string
	// Vars are the properties to set, sorted by name.
	Vars     []Var
	Problems []Problem
}

// Usable reports whether the theme can be applied: it has no errors.
func (t Theme) Usable() bool {
	return !slices.ContainsFunc(t.Problems, func(p Problem) bool { return p.Severity == SeverityError })
}

func (t *Theme) problem(severity, code, key, msg string) {
	t.Problems = append(t.Problems, Problem{Severity: severity, Code: code, Key: key, Message: msg})
}

// Parse reads the theme file id with content data. It never fails: what
// is wrong is in the Problems, and a theme with errors is not Usable.
func Parse(id string, data []byte) Theme {
	t := Theme{ID: id, Name: id}
	if len(data) > MaxFileSize {
		t.problem(SeverityError, CodeTooLarge, "", fmt.Sprintf("The file is larger than %d KiB.", MaxFileSize>>10))
		return t
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil || top == nil {
		t.problem(SeverityError, CodeInvalidJSON, "", "The file is not a JSON object.")
		return t
	}

	var name string
	if raw, ok := top["name"]; !ok || json.Unmarshal(raw, &name) != nil {
		t.problem(SeverityError, CodeInvalidName, "name", `"name" is missing or not a string.`)
	} else if name = strings.TrimSpace(name); name == "" || utf8.RuneCountInString(name) > MaxNameLength || !printable(name) {
		t.problem(SeverityError, CodeInvalidName, "name", fmt.Sprintf(`"name" must be 1 to %d printable characters.`, MaxNameLength))
	} else {
		t.Name = name
	}

	var base string
	if raw, ok := top["base"]; !ok || json.Unmarshal(raw, &base) != nil || (base != BaseDark && base != BaseLight) {
		t.problem(SeverityError, CodeInvalidBase, "base", `"base" must be "dark" or "light".`)
	} else {
		t.Base = base
	}

	vars := map[string]string{}
	t.section(top, "colors", ColorTokens, vars)
	t.section(top, "editor", EditorTokens, vars)

	keys := make([]string, 0, len(top))
	for k := range top {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		switch {
		case k == "name" || k == "base" || k == "colors" || k == "editor":
		case strings.HasPrefix(k, "$"): // "$schema", "$comment"
		default:
			t.problem(SeverityWarning, CodeUnknownKey, k, fmt.Sprintf("Unknown member %q is ignored.", k))
		}
	}

	for name, value := range vars {
		t.Vars = append(t.Vars, Var{Name: name, Value: value})
	}
	slices.SortFunc(t.Vars, func(a, b Var) int { return strings.Compare(a.Name, b.Name) })
	return t
}

// section reads the colours of one section ("colors" or "editor") into vars.
func (t *Theme) section(top map[string]json.RawMessage, key string, tokens map[string]string, vars map[string]string) {
	raw, ok := top[key]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.problem(SeverityError, CodeInvalidSection, key, fmt.Sprintf("%q must be an object of colours.", key))
		return
	}
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		path := key + "." + k
		prop, known := tokens[k]
		if !known {
			if !strings.HasPrefix(k, "$") {
				t.problem(SeverityWarning, CodeUnknownKey, path, fmt.Sprintf("Unknown token %q is ignored.", path))
			}
			continue
		}
		var s string
		if err := json.Unmarshal(m[k], &s); err != nil {
			t.problem(SeverityError, CodeInvalidColor, path, fmt.Sprintf("%q must be a colour string.", path))
			continue
		}
		c, err := ParseColor(s)
		if err != nil {
			t.problem(SeverityError, CodeInvalidColor, path, fmt.Sprintf("%q is not a colour: %v.", path, err))
			continue
		}
		vars[prop] = c
	}
}

func printable(s string) bool {
	for _, r := range s {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

// ValidID reports whether id can name a theme file: a .json file name
// without a directory, not starting with "." or "_", printable, at most
// MaxIDLength bytes and without characters that are not allowed in file
// names on some OS.
func ValidID(id string) bool {
	if len(id) > MaxIDLength || !utf8.ValidString(id) || !strings.HasSuffix(id, ".json") || len(id) == len(".json") {
		return false
	}
	if strings.HasPrefix(id, ".") || strings.HasPrefix(id, "_") || strings.TrimSpace(id) != id {
		return false
	}
	for _, r := range id {
		if !unicode.IsPrint(r) || strings.ContainsRune(`/\:*?"<>|`, r) {
			return false
		}
	}
	return true
}

// List reads every theme file directly in dir, sorted by file name. A
// missing folder has no themes. Only an unreadable folder fails (code
// CodeListFailed); files that cannot be read are themes with problems.
func List(dir string) ([]Theme, error) {
	des, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return []Theme{}, nil
	}
	if err != nil {
		return nil, errs.Wrap(err, errs.KindOf(err), "The themes folder could not be read.").
			WithCode(CodeListFailed).
			WithDetail("path", dir)
	}
	themes := []Theme{}
	for _, de := range des {
		if len(themes) == MaxThemes {
			break
		}
		if de.IsDir() || !ValidID(de.Name()) {
			continue
		}
		t, ok := read(dir, de.Name())
		if ok {
			themes = append(themes, t)
		}
	}
	return themes, nil
}

// Load reads the theme file id in dir. A missing file, or an id that
// cannot name one, is a not_found error with code CodeNotFound; a file
// that cannot be read is a theme with problems.
func Load(dir, id string) (Theme, error) {
	if ValidID(id) {
		if t, ok := read(dir, id); ok {
			return t, nil
		}
	}
	return Theme{}, errs.New(errs.NotFound, "The theme file was not found.").
		WithCode(CodeNotFound).
		WithHint("Put it in the themes folder, or choose another theme.").
		WithDetail("id", id)
}

// read reads one theme file; false when it is not a regular file.
func read(dir, id string) (Theme, bool) {
	path := filepath.Join(dir, id)
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Theme{}, false
		}
		t := Theme{ID: id, Name: id}
		t.problem(SeverityError, CodeReadFailed, "", "The file could not be read.")
		return t, true
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return Theme{}, false
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxFileSize+1))
	if err != nil {
		t := Theme{ID: id, Name: id}
		t.problem(SeverityError, CodeReadFailed, "", "The file could not be read.")
		return t, true
	}
	return Parse(id, data), true
}
