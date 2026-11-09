package template

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// mapFiles is a FileReader over fixed contents; a missing path is a
// not-allowed error, as fileaccess would say.
type mapFiles map[string]string

var errNotAllowed = errs.New(errs.PermissionDenied, "The file is outside the directories allowed for file access.").WithCode("fileaccess.not_allowed")

func (m mapFiles) ReadFile(path string) ([]byte, error) {
	v, ok := m[path]
	if !ok {
		return nil, errNotAllowed
	}
	return []byte(v), nil
}

func TestParseFile(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, want      string
		notOne, invalid bool
	}{
		{name: "$file a.txt", want: "a.txt"},
		{name: "$file\t~/x.json", want: "~/x.json"},
		{name: `$file "my file.txt"`, want: "my file.txt"},
		{name: `$file "a|b"`, want: "a|b"},
		{name: "$files x", notOne: true},
		{name: "$uuid", notOne: true},
		{name: "file x", notOne: true},
		{name: "$file", invalid: true},
		{name: `$file ""`, invalid: true},
		{name: "$file a b", invalid: true},
		{name: `$file "open`, invalid: true},
		{name: "$file a|base64", invalid: true},
		{name: "$file a | base64", invalid: true},
	}
	for _, tt := range tests {
		path, ok, err := ParseFile(tt.name)
		switch {
		case tt.notOne:
			if ok {
				t.Errorf("ParseFile(%q) ok, want not a file reference", tt.name)
			}
		case tt.invalid:
			if !ok || err == nil {
				t.Errorf("ParseFile(%q) = ok %v err %v, want an error", tt.name, ok, err)
			}
			if IsDynamic(tt.name) {
				t.Errorf("IsDynamic(%q) = true", tt.name)
			}
		default:
			if !ok || err != nil || path != tt.want {
				t.Errorf("ParseFile(%q) = %q, %v, %v; want %q", tt.name, path, ok, err, tt.want)
			}
			if !IsDynamic(tt.name) {
				t.Errorf("IsDynamic(%q) = false", tt.name)
			}
		}
	}
	if !slices.Contains(DynamicNames(), FileName) || DynamicDescription(FileName) == "" {
		t.Error("$file is not listed with the dynamic values")
	}
}

func TestRenderFile(t *testing.T) {
	t.Parallel()
	files := mapFiles{
		"a.txt":       "hello {{name}}\n",
		"my file.txt": "spaced",
		"logo.bin":    "\x89PNG\x00\xff",
		"nul.txt":     "a\x00b",
		"empty.txt":   "",
	}
	r := Resolver{Files: files, Scopes: Chain{Vars("Env", map[string]string{"name": "x", "doc": `{"f": "{{$file a.txt}}"}`})}}
	tests := []struct{ src, want string }{
		{"{{$file a.txt}}", "hello {{name}}\n"}, // never parsed for variables
		{`<{{ $file "my file.txt" }}>`, "<spaced>"},
		{"{{$file empty.txt}}", ""},
		{"{{$file logo.bin | base64}}", "iVBORwD/"},
		{"{{$file logo.bin|hex}}", "89504e4700ff"},
		{"{{$file a.txt | sha256}}", sha256Hex("hello {{name}}\n")},
		{"{{doc}}", `{"f": "hello {{name}}` + "\n" + `"}`}, // inside a variable's value
	}
	for _, tt := range tests {
		res := r.Render(tt.src)
		if err := res.Err(); err != nil || res.Text != tt.want {
			t.Errorf("Render(%q) = %q, %v; want %q", tt.src, res.Text, err, tt.want)
		}
	}
	res := r.Render("{{$file a.txt}}")
	if ref := res.Refs[0]; ref.Scope != FileScope || ref.Secret || !ref.Resolved {
		t.Errorf("ref = %+v", ref)
	}
}

func TestRenderFileProblems(t *testing.T) {
	t.Parallel()
	files := mapFiles{"logo.bin": "\x89PNG\x00\xff", "nul.txt": "a\x00b", "latin1.txt": "caf\xe9"}
	r := Resolver{Files: files, Scopes: Chain{Vars("Env", map[string]string{"bin": "{{$file logo.bin}}"})}}
	tests := []struct {
		src, code string
		kind      errs.Kind
	}{
		{"{{$file outside.txt}}", "fileaccess.not_allowed", errs.PermissionDenied},
		{"{{$file logo.bin}}", CodeFileBinary, errs.InvalidArgument},
		{"{{$file nul.txt}}", CodeFileBinary, errs.InvalidArgument},
		{"{{$file latin1.txt}}", CodeFileBinary, errs.InvalidArgument},
		// Binary is allowed only as the pipe's input itself, not inside
		// a variable piped.
		{"{{bin | base64}}", CodeFileBinary, errs.InvalidArgument},
		{"{{$file}}", CodeInvalidDynamic, errs.InvalidArgument},
		{"{{$file a b}}", CodeInvalidDynamic, errs.InvalidArgument},
	}
	for _, tt := range tests {
		res := r.Render(tt.src)
		if res.Text != tt.src && tt.src != "{{bin | base64}}" { // the pipe encodes bin's value as written
			t.Errorf("Render(%q).Text = %q, want as written", tt.src, res.Text)
		}
		err := res.Err()
		var e *errs.Error
		if !errors.As(err, &e) || e.Code != tt.code || e.Kind != tt.kind {
			t.Errorf("Render(%q).Err() = %v (%+v), want %s %s", tt.src, err, e, tt.kind, tt.code)
			continue
		}
		if tt.code != CodeInvalidDynamic && e.Details["reference"] == "" {
			t.Errorf("Render(%q): no reference detail in %+v", tt.src, e.Details)
		}
	}

	// Without a reader the reference is refused with ErrFilesUnavailable.
	res := Resolver{}.Render("x {{$file a.txt}}")
	if len(res.Problems) != 1 || res.Problems[0].Kind != ProblemFile || !errors.Is(res.Problems[0].Cause, ErrFilesUnavailable) {
		t.Fatalf("problems = %+v", res.Problems)
	}
	if err := res.Err(); !errors.Is(err, ErrFilesUnavailable) {
		t.Errorf("Err() = %v", err)
	}
	// A scope that defines the name wins, as for every dynamic value.
	res = Resolver{Scopes: Chain{Vars("Env", map[string]string{"$file a.txt": "defined"})}}.Render("{{$file a.txt}}")
	if res.Text != "defined" || res.Err() != nil {
		t.Errorf("defined name: %q, %v", res.Text, res.Err())
	}
}

func TestRenderFileLimit(t *testing.T) {
	t.Parallel()
	r := Resolver{Files: mapFiles{"big.txt": "0123456789"}, MaxLength: 15}
	res := r.Render("{{$file big.txt}}{{$file big.txt}}")
	var e *errs.Error
	if !errors.As(res.Err(), &e) || e.Code != CodeLimit {
		t.Errorf("Err() = %v, want %s", res.Err(), CodeLimit)
	}
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
