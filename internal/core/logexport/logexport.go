// Package logexport masks secrets in timeline and log entries the user
// copies or saves (DBG-07): the HTTP response timeline, the console, the
// connection log and the message logs of streaming connections and local
// servers. The UI formats the entries; everything it copies or saves goes
// through a Masker first, so the clipboard and the saved file never hold a
// secret the app knows, whatever a view showed.
//
// Masking is by value, like the timeline's (httpclient.SecretMasker): every
// occurrence of a known secret, also in its URL encodings, becomes
// httpclient.Mask. On top of that the credentials of each line that starts
// with an Authorization or Proxy-Authorization header field are masked
// after the scheme, and in JSON the string value of an "authorization" or
// "proxy-authorization" member. The known secrets are the values PostSonde
// keeps as secrets (ReadSecrets and the app-wide side file). See
// docs/privacy.md#copied-and-saved-logs.
//
// Standard library and this module only.
package logexport

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/secretstore"
)

// Format is how saved or copied text is laid out.
type Format string

const (
	// Text is plain text, masked as a whole.
	Text Format = "text"
	// JSON is a JSON document: each string in it is masked, and the
	// document stays valid.
	JSON Format = "json"
)

// Valid reports whether f is a known format.
func (f Format) Valid() bool { return f == Text || f == JSON }

// Ext is the file name extension of f, with the dot.
func (f Format) Ext() string {
	if f == JSON {
		return ".json"
	}
	return ".txt"
}

// ErrInvalidJSON is returned for JSON input that does not parse.
var ErrInvalidJSON = errors.New("logexport: invalid JSON")

// Masker hides known secrets in text. The zero value masks only the
// credentials of Authorization lines.
type Masker struct {
	m *httpclient.SecretMasker
}

// NewMasker returns a Masker for secrets. Empty secrets are ignored.
func NewMasker(secrets []string) *Masker {
	return &Masker{m: httpclient.NewSecretMasker(secrets)}
}

func (m *Masker) masker() *httpclient.SecretMasker {
	if m == nil || m.m == nil {
		return &httpclient.SecretMasker{}
	}
	return m.m
}

// Mask masks s laid out as f, and reports whether anything was hidden.
// JSON that does not parse fails with ErrInvalidJSON.
func (m *Masker) Mask(s string, f Format) (string, bool, error) {
	if f == JSON {
		return m.JSON(s)
	}
	out := m.Text(s)
	return out, out != s, nil
}

// Text returns s with the credentials of its Authorization lines and every
// known secret masked.
func (m *Masker) Text(s string) string {
	sm := m.masker()
	return sm.MaskText(maskAuthLines(s, sm))
}

// JSON returns the JSON document s with each of its strings, member names
// included, masked as Text, and the string value of an Authorization or
// Proxy-Authorization member (any spelling) masked as that header field.
// Everything else, the layout included, is kept. It reports whether
// anything was hidden.
func (m *Masker) JSON(s string) (string, bool, error) {
	if !json.Valid([]byte(s)) {
		return "", false, ErrInvalidJSON
	}
	sm := m.masker()
	var b strings.Builder
	b.Grow(len(s))
	changed := false
	lastKey := ""
	for i := 0; i < len(s); {
		if s[i] != '"' {
			b.WriteByte(s[i])
			i++
			continue
		}
		end := stringEnd(s, i)
		lit := s[i:end]
		var v string
		if err := json.Unmarshal([]byte(lit), &v); err != nil {
			return "", false, ErrInvalidJSON // cannot happen after json.Valid
		}
		var out string
		if isKey(s, end) {
			lastKey = v
			out = m.Text(v)
		} else if afterColon(s, i) && isAuthField(lastKey) {
			out = sm.MaskText(sm.MaskHeader(lastKey, v))
		} else {
			out = m.Text(v)
		}
		if out == v {
			b.WriteString(lit)
		} else {
			changed = true
			b.WriteString(quote(out))
		}
		i = end
	}
	return b.String(), changed, nil
}

// stringEnd returns the index just after the string literal that starts at
// s[i] (a quote) in valid JSON.
func stringEnd(s string, i int) int {
	for k := i + 1; k < len(s); k++ {
		switch s[k] {
		case '\\':
			k++
		case '"':
			return k + 1
		}
	}
	return len(s)
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

// isKey reports whether the string literal that ends at s[end] is a member
// name: a colon follows it.
func isKey(s string, end int) bool {
	for k := end; k < len(s); k++ {
		if !isSpace(s[k]) {
			return s[k] == ':'
		}
	}
	return false
}

// afterColon reports whether the string literal at s[i] is a member value.
func afterColon(s string, i int) bool {
	for k := i - 1; k >= 0; k-- {
		if !isSpace(s[k]) {
			return s[k] == ':'
		}
	}
	return false
}

// quote returns the JSON string literal of v, without escaping HTML
// characters.
func quote(v string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v) // a string always encodes
	return strings.TrimSuffix(buf.String(), "\n")
}

func isAuthField(name string) bool {
	return strings.EqualFold(name, "Authorization") || strings.EqualFold(name, "Proxy-Authorization")
}

// maskAuthLines masks the value of each line of s that is an Authorization
// or Proxy-Authorization header field ("Authorization: Bearer x", any
// spelling, leading blanks allowed) as sm.MaskHeader does.
func maskAuthLines(s string, sm *httpclient.SecretMasker) string {
	lower := strings.ToLower(s)
	if !strings.Contains(lower, "authorization") {
		return s
	}
	lines := strings.SplitAfter(s, "\n")
	changed := false
	for i, line := range lines {
		body := strings.TrimRight(line, "\r\n")
		eol := line[len(body):]
		indent := len(body) - len(strings.TrimLeft(body, " \t"))
		name, value, ok := strings.Cut(body[indent:], ":")
		if !ok || !isAuthField(strings.TrimSpace(name)) {
			continue
		}
		v := strings.TrimSpace(value)
		if v == "" {
			continue
		}
		masked := sm.MaskHeader(strings.TrimSpace(name), v)
		if masked == v {
			continue
		}
		lines[i] = body[:indent] + name + ": " + masked + eol
		changed = true
	}
	if !changed {
		return s
	}
	return strings.Join(lines, "")
}

// ReadSecrets returns the secret values kept in the side files under dirs
// (the workspace's secrets/requests and secrets/environments directories):
// every value of the "values" object of each *.json file in them, sorted,
// without duplicates or empty values. A missing directory has none. A file
// that cannot be read or parsed (or is encrypted while the secrets are
// locked) is skipped; its error is returned, joined
// with the others, next to the values that could be read.
func ReadSecrets(dirs ...string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	var problems []error
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) && path == dir {
					return nil
				}
				problems = append(problems, err)
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if d.IsDir() || !strings.EqualFold(filepath.Ext(path), ".json") {
				return nil
			}
			data, err := secretstore.ReadFile(path)
			if err != nil {
				problems = append(problems, err)
				return nil
			}
			var f struct {
				Values map[string]string `json:"values"`
			}
			if err := json.Unmarshal(data, &f); err != nil {
				problems = append(problems, &fs.PathError{Op: "parse", Path: path, Err: err})
				return nil
			}
			for _, v := range f.Values {
				if v != "" && !seen[v] {
					seen[v] = true
					out = append(out, v)
				}
			}
			return nil
		})
		if err != nil {
			problems = append(problems, err)
		}
	}
	slices.Sort(out)
	return out, errors.Join(problems...)
}
