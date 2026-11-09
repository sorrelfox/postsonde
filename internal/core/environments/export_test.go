package environments

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

func TestSharedLeavesOutSecretAndLocalValues(t *testing.T) {
	t.Parallel()
	st, root, _ := newStore(t)
	env, err := st.Create("Staging")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetColor(env.ID, "#336699"); err != nil {
		t.Fatal(err)
	}
	_, err = st.SaveVariables(env.ID, []Variable{
		{Name: "host", Value: "api.example.test", Local: true, LocalValue: "localhost:8080-LOCAL"},
		{Name: "token", Value: "tok-SECRET-1", Secret: true},
		{Name: "off", Value: "x", Description: "unused", Disabled: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	// A secret typed into another file by hand, never saved by PostSonde.
	writeFile(t, filepath.Join(root, "environments", "hand.json"),
		`{"kind":"environment","schemaVersion":1,"id":"env_hand","name":"Hand","variables":[{"name":"key","value":"hand-SECRET-2","secret":true}],"extra":"kept in the project"}`)

	tests := []struct {
		id          string
		wantLeftOut int
		wantVars    []Variable
	}{
		{env.ID, 0, []Variable{
			{Name: "host", Value: "api.example.test"},
			{Name: "token", Secret: true},
			{Name: "off", Value: "x", Description: "unused", Disabled: true},
		}},
		{"env_hand", 1, []Variable{{Name: "key", Secret: true}}},
	}
	for _, tt := range tests {
		got, leftOut, err := st.Shared(tt.id)
		if err != nil {
			t.Fatal(err)
		}
		if leftOut != tt.wantLeftOut {
			t.Errorf("%s: leftOut = %d, want %d", tt.id, leftOut, tt.wantLeftOut)
		}
		if len(got.Variables) != len(tt.wantVars) {
			t.Fatalf("%s: variables = %+v", tt.id, got.Variables)
		}
		for i, v := range got.Variables {
			if v != tt.wantVars[i] {
				t.Errorf("%s: variable %d = %+v, want %+v", tt.id, i, v, tt.wantVars[i])
			}
		}
		data, err := MarshalExport(got)
		if err != nil {
			t.Fatal(err)
		}
		for _, leak := range []string{"SECRET", "LOCAL", "extra"} {
			if strings.Contains(string(data), leak) {
				t.Errorf("%s: export holds %q:\n%s", tt.id, leak, data)
			}
		}
		f, err := projectfile.DecodeEnvironment(data)
		if err != nil {
			t.Fatalf("%s: the export is not an environment file: %v\n%s", tt.id, err, data)
		}
		if f.ID != tt.id || f.Name != got.Name || len(f.Variables) != len(tt.wantVars) {
			t.Errorf("%s: decoded %+v", tt.id, f)
		}
	}

	// The store still has the values: the export changed nothing.
	full, err := st.Get(env.ID)
	if err != nil {
		t.Fatal(err)
	}
	if full.Variables[1].Value != "tok-SECRET-1" || full.Variables[0].LocalValue != "localhost:8080-LOCAL" {
		t.Errorf("after the export: %+v", full.Variables)
	}
	if _, _, err := st.Shared("env_missing"); err == nil {
		t.Error("Shared of a missing environment: no error")
	}
}

func TestExportFileName(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, ext, want string }{
		{"Local dev (8080)", ".json", "local-dev-8080.json"},
		{"Прод", ".postman_environment.json", "environment.postman_environment.json"},
	}
	for _, tt := range tests {
		if got := ExportFileName(tt.name, tt.ext); got != tt.want {
			t.Errorf("ExportFileName(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}
