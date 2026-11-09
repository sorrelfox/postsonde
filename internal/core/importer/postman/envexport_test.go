package postman_test

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/environments"
	"github.com/sorrelfox/postsonde/internal/core/importer"
	"github.com/sorrelfox/postsonde/internal/core/importer/postman"
)

func TestExportEnvironmentRoundTrip(t *testing.T) {
	t.Parallel()
	env := environments.Environment{
		ID:   "env_1",
		Name: "Staging",
		Variables: []environments.Variable{
			{Name: "host", Value: "api.example.test", Description: "the API"},
			// A secret value never reaches the file, even when the caller
			// passes one.
			{Name: "token", Value: "tok-SECRET", Secret: true},
			{Name: "off", Value: "x", Disabled: true},
			// Local values are never exported either.
			{Name: "port", Value: "443", Local: true, LocalValue: "8080-LOCAL"},
		},
	}
	now := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	data, err := postman.ExportEnvironment(env, "1.2.3", now)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"SECRET", "LOCAL"} {
		if strings.Contains(string(data), leak) {
			t.Errorf("export holds %q:\n%s", leak, data)
		}
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["_postman_variable_scope"] != "environment" || raw["_postman_exported_using"] != "PostSonde/1.2.3" ||
		raw["_postman_exported_at"] != "2026-09-23T10:00:00.000Z" {
		t.Errorf("export = %s", data)
	}
	again, _ := postman.ExportEnvironment(env, "1.2.3", now)
	if string(again) != string(data) {
		t.Error("exporting the same environment twice gives different files")
	}

	im := postman.Importer{}
	src := importer.Source{Name: "staging" + postman.EnvironmentExtension, Data: data}
	if got := im.Detect(src); got != importer.Sure {
		t.Errorf("Detect = %d, want Sure", got)
	}
	m, err := im.Parse(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Environments) != 1 || m.Environments[0].Name != "Staging" {
		t.Fatalf("model = %+v", m)
	}
	want := []environments.Variable{
		{Name: "host", Value: "api.example.test", Description: "the API"},
		{Name: "token", Secret: true},
		{Name: "off", Value: "x", Disabled: true},
		{Name: "port", Value: "443"},
	}
	if got := m.Environments[0].Variables; !reflect.DeepEqual(got, want) {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}
