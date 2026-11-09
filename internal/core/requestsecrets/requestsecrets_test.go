package requestsecrets

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

func TestIsReference(t *testing.T) {
	tests := map[string]bool{
		"":                      false,
		"   ":                   false,
		"{{token}}":             true,
		" {{ token }} ":         true,
		"{{user}} {{suffix}}":   true,
		"{{$uuid}}":             true,
		"abc{{x}}":              false,
		"{{x}}abc":              false,
		"{{a}}:{{b}}":           false,
		`\{{x}}`:                false,
		"{{x":                   false,
		"hunter2":               false,
		"{{{x}}":                false,
		"{{x}}\n":               true,
		"Bearer {{token}}":      false,
		"{{token}}{{token}}":    true,
		"sk_live_{{suffix}}":    false,
		"{{outer {{inner}} }}}": false,
	}
	for in, want := range tests {
		if got := IsReference(in); got != want {
			t.Errorf("IsReference(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestSplitMerge(t *testing.T) {
	local := func(m string) []string { return []string{m} }
	tests := []struct {
		name     string
		in       projectfile.Auth
		wantFile projectfile.Auth
		values   map[string]string
	}{
		{
			name:     "typed password goes local",
			in:       projectfile.Auth{Type: projectfile.AuthBasic, Username: "zoë", Password: "pässwörd"},
			wantFile: projectfile.Auth{Type: projectfile.AuthBasic, Username: "zoë", LocalSecrets: local("password")},
			values:   map[string]string{"auth.password": "pässwörd"},
		},
		{
			name:     "reference stays in the file",
			in:       projectfile.Auth{Type: projectfile.AuthBearer, Token: "{{token}}", LocalSecrets: local("token")},
			wantFile: projectfile.Auth{Type: projectfile.AuthBearer, Token: "{{token}}"},
		},
		{
			name:     "typed api key",
			in:       projectfile.Auth{Type: projectfile.AuthAPIKey, Key: "X-Key", Value: "abc{{x}}", In: projectfile.APIKeyInQuery},
			wantFile: projectfile.Auth{Type: projectfile.AuthAPIKey, Key: "X-Key", In: projectfile.APIKeyInQuery, LocalSecrets: local("value")},
			values:   map[string]string{"auth.value": "abc{{x}}"},
		},
		{
			name:     "empty and marked stays marked",
			in:       projectfile.Auth{Type: projectfile.AuthBearer, LocalSecrets: local("token")},
			wantFile: projectfile.Auth{Type: projectfile.AuthBearer, LocalSecrets: local("token")},
		},
		{
			name:     "empty and unmarked",
			in:       projectfile.Auth{Type: projectfile.AuthBearer, LocalSecrets: local("password")},
			wantFile: projectfile.Auth{Type: projectfile.AuthBearer},
		},
		{
			name:     "no secret member",
			in:       projectfile.Auth{Type: projectfile.AuthNone},
			wantFile: projectfile.Auth{Type: projectfile.AuthNone},
		},
		{
			name:     "unknown type untouched",
			in:       projectfile.Auth{Type: "edgegrid", Password: "hunter2"},
			wantFile: projectfile.Auth{Type: "edgegrid", Password: "hunter2"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file, values := Split(tt.in)
			if !reflect.DeepEqual(file, tt.wantFile) {
				t.Errorf("file = %+v, want %+v", file, tt.wantFile)
			}
			if !reflect.DeepEqual(values, tt.values) {
				t.Errorf("values = %v, want %v", values, tt.values)
			}
			// Merge restores what the editor had, apart from marks.
			merged := Merge(file, values)
			if merged.Secret() != tt.in.Secret() {
				t.Errorf("Merge secret = %q, want %q", merged.Secret(), tt.in.Secret())
			}
		})
	}

	marked := projectfile.Auth{Type: projectfile.AuthBasic, LocalSecrets: local("password")}
	if !Missing(Merge(marked, nil)) {
		t.Error("a marked secret without a local value is not Missing")
	}
	if Missing(Merge(marked, map[string]string{"auth.password": "p"})) {
		t.Error("a marked secret with a local value is Missing")
	}
	// A value in the file wins over a local one.
	inFile := projectfile.Auth{Type: projectfile.AuthBasic, Password: "{{pw}}", LocalSecrets: local("password")}
	if got := Merge(inFile, map[string]string{"auth.password": "p"}).Password; got != "{{pw}}" {
		t.Errorf("Merge replaced a value in the file: %q", got)
	}
	// Unmarked: a stale local value is not used.
	if got := Merge(projectfile.Auth{Type: projectfile.AuthBasic}, map[string]string{"auth.password": "p"}).Password; got != "" {
		t.Errorf("Merge used an unmarked local value: %q", got)
	}
}

func TestStore(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	const id = "req_0123456789abcdef"

	got, err := s.Read(id)
	if err != nil || len(got) != 0 {
		t.Fatalf("Read without a file = %v, %v", got, err)
	}
	values := map[string]string{"auth.token": "tök€n"}
	if err := s.Write(id, values); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "secrets", "requests", id+".json")
	if s.Path(id) != path {
		t.Errorf("Path = %s, want %s", s.Path(id), path)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("file mode = %v", fi.Mode().Perm())
		}
		di, _ := os.Stat(filepath.Dir(path))
		if di.Mode().Perm() != 0o700 {
			t.Errorf("dir mode = %v", di.Mode().Perm())
		}
	}
	if got, err := s.Read(id); err != nil || !reflect.DeepEqual(got, values) {
		t.Errorf("Read = %v, %v", got, err)
	}

	if err := s.Copy(id, "req_copy"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Read("req_copy"); !reflect.DeepEqual(got, values) {
		t.Errorf("copy = %v", got)
	}
	if err := s.Copy("req_none", "req_other"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.Path("req_other")); !errors.Is(err, os.ErrNotExist) {
		t.Error("copying nothing created a file")
	}

	if err := s.Write(id, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Error("writing no values kept the file")
	}
	if err := s.Remove(id); err != nil {
		t.Errorf("Remove of a missing file: %v", err)
	}

	if err := os.WriteFile(s.Path("req_bad"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = s.Read("req_bad")
	if !errors.Is(err, &errs.Error{Code: CodeLocalDataFailed}) {
		t.Errorf("broken file: %v", err)
	}
	if strings.Contains(err.Error(), "{") {
		t.Errorf("error shows file content: %v", err)
	}
}

func TestKey(t *testing.T) {
	for id, plain := range map[string]bool{
		"req_0123456789abcdef":           true,
		"fld_a-b_c":                      true,
		"req_ABC":                        false,
		"../../etc":                      false,
		"req_":                           false,
		"_x":                             false,
		"Req_x":                          false,
		"req_" + strings.Repeat("a", 70): false,
	} {
		k := Key(id)
		if (k == id) != plain {
			t.Errorf("Key(%q) = %q", id, k)
		}
		if !plain && (!strings.HasPrefix(k, "x.") || strings.ContainsAny(k, `/\`)) {
			t.Errorf("Key(%q) = %q, want a hash", id, k)
		}
	}
}
