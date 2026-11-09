package postsondeenv_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/environments"
	"github.com/sorrelfox/postsonde/internal/core/importer"
	"github.com/sorrelfox/postsonde/internal/core/importer/postsondeenv"
)

func TestDetect(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, data string
		want       int
	}{
		{"environment", `{"kind": "environment", "schemaVersion": 1, "id": "env_1", "name": "Staging"}`, importer.Sure},
		{"no version", `{"kind": "environment", "id": "env_1"}`, importer.No},
		{"request", `{"kind": "http", "schemaVersion": 1, "name": "environment"}`, importer.No},
		{"postman environment", `{"name": "environment", "values": [], "_postman_variable_scope": "environment"}`, importer.No},
		{"not JSON", `kind: environment`, importer.No},
		{"empty", ``, importer.No},
	}
	for _, tt := range tests {
		if got := (postsondeenv.Importer{}).Detect(importer.Source{Name: "x.json", Data: []byte(tt.data)}); got != tt.want {
			t.Errorf("%s: Detect = %d, want %d", tt.name, got, tt.want)
		}
	}
}

func TestParseExport(t *testing.T) {
	t.Parallel()
	env := environments.Environment{ID: "env_1", Name: "Staging", Color: "#336699", Variables: []environments.Variable{
		{Name: "host", Value: "api.example.test", Description: "the API"},
		{Name: "token", Secret: true},
		{Name: "off", Value: "x", Disabled: true},
	}}
	data, err := environments.MarshalExport(env)
	if err != nil {
		t.Fatal(err)
	}
	src := importer.Source{Name: "staging.json", Data: data}
	if got := (postsondeenv.Importer{}).Detect(src); got != importer.Sure {
		t.Fatalf("Detect = %d", got)
	}
	m, err := postsondeenv.Importer{}.Parse(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "Staging" || len(m.Environments) != 1 || m.Environments[0].Name != "Staging" || len(m.Environments[0].Diagnostics) != 0 {
		t.Fatalf("model = %+v", m)
	}
	if got := m.Environments[0].Variables; !reflect.DeepEqual(got, env.Variables) {
		t.Errorf("variables = %+v, want %+v", got, env.Variables)
	}
}

func TestParseNewerAndInvalid(t *testing.T) {
	t.Parallel()
	m, err := postsondeenv.Importer{}.Parse(context.Background(), importer.Source{Name: "later.json",
		Data: []byte(`{"kind": "environment", "schemaVersion": 99, "id": "env_9", "variables": [{"name": "a", "value": "1"}, {"name": " ", "value": "2"}]}`)})
	if err != nil {
		t.Fatal(err)
	}
	env := m.Environments[0]
	if env.Name != "later" || len(env.Variables) != 1 || len(env.Diagnostics) != 1 || env.Diagnostics[0].Code != postsondeenv.CodeNewer {
		t.Errorf("model = %+v", env)
	}
	if _, err := (postsondeenv.Importer{}).Parse(context.Background(), importer.Source{Data: []byte(`{"kind": "http", "schemaVersion": 1}`)}); err == nil {
		t.Error("a request file parsed as an environment")
	}
}
