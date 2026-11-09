package collection

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// An import (SEC-01) shows what it found and what it did, never the
// secrets it keeps on this computer: the preview and the report name
// items and variables, not their secret values.
func TestImportPreviewAndReportHoldNoSecret(t *testing.T) {
	t.Parallel()
	manager, _ := newProject(t)
	svc := New(manager, nil, nil)
	ctx := t.Context()
	secrets := []string{"hunter2", "s3cr3t-avatar", "coll-api-key-5", "oauth1-consumer-secret-5", "rsa-key-6", "stg-token-9f8e", "glob-secret-77"}
	for _, name := range []string{"shop.postman_collection.json", "staging.postman_environment.json", "workspace.postman_globals.json"} {
		path, err := filepath.Abs(filepath.Join("..", "..", "..", "core", "importer", "postman", "testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		pre, err := svc.PreviewImport(ctx, PreviewImportInputDTO{Path: path})
		if err != nil {
			t.Fatal(err)
		}
		res, err := svc.Import(ctx, ImportInputDTO{Path: path, Digest: pre.Digest, Index: -1})
		if err != nil {
			t.Fatal(err)
		}
		for what, v := range map[string]any{"preview": pre, "report": res} {
			data, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range secrets {
				if strings.Contains(string(data), s) {
					t.Errorf("the %s of %s shows the secret %q", what, name, s)
				}
			}
		}
	}
}
