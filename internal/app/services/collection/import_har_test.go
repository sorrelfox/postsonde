package collection

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Chrome and Firefox HAR files go through the default formats into a
// project: requests grouped by page, credentials as typed auth readable
// through the service, and neither the auth secrets nor the browser's
// cookies in the project's files.
func TestImportHAR(t *testing.T) {
	t.Parallel()
	manager, dir := newProject(t)
	svc := New(manager, (&namedEvents{}).emit, nil)
	ctx := t.Context()
	fixture := func(name string) string {
		path, err := filepath.Abs(filepath.Join("..", "..", "..", "core", "importer", "harimport", "testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		return path
	}
	if !slices.ContainsFunc(svc.ImportFormats(ctx), func(f ImportFormatDTO) bool { return f.ID == "har" }) {
		t.Fatalf("formats = %+v", svc.ImportFormats(ctx))
	}

	byName := map[string]ImportItemDTO{}
	for _, name := range []string{"chrome.har", "firefox.har"} {
		path := fixture(name)
		pre, err := svc.PreviewImport(ctx, PreviewImportInputDTO{Path: path})
		if err != nil {
			t.Fatal(err)
		}
		if pre.Format.ID != "har" {
			t.Fatalf("%s detected as %+v", name, pre.Format)
		}
		res, err := svc.Import(ctx, ImportInputDTO{Path: path, Digest: pre.Digest, Index: -1})
		if err != nil {
			t.Fatal(err)
		}
		if res.Report.Counts.Failed != 0 {
			t.Fatalf("%s report = %+v", name, res.Report.Counts)
		}
		for _, it := range res.Report.Items {
			byName[it.Name] = it
		}
	}
	if c := byName["chrome"]; c.Kind != "folder" || c.NodeID == "" {
		t.Errorf("chrome folder = %+v", c)
	}
	for name, kind := range map[string]string{"GET /v1/cart": "http", "ProductSearch": "graphql", "WebSocket /socket": "websocket", "Checkout – Example Shop": "", "My account": ""} {
		if it := byName[name]; it.Status != "imported" || it.FileKind != kind || it.NodeID == "" {
			t.Errorf("%s = %+v", name, it)
		}
	}

	cart := getRequest(t, svc, byName["GET /v1/cart"].NodeID)
	if cart.Auth.Type != "bearer" || cart.Auth.Token != "eyJhbGciOiJIUzI1NiJ9.chrome-bearer-7f3a" {
		t.Errorf("cart auth = %+v", cart.Auth)
	}
	admin := getRequest(t, svc, byName["POST /v1/admin/reindex"].NodeID)
	if admin.Auth.Type != "basic" || admin.Auth.Username != "admin" || admin.Auth.Password != "reindex-pass-42" {
		t.Errorf("admin auth = %+v", admin.Auth)
	}
	me := getRequest(t, svc, byName["GET /v1/me"].NodeID)
	if me.Auth.Type != "apikey" || me.Auth.Value != "ff-api-key-91c2" {
		t.Errorf("me auth = %+v", me.Auth)
	}

	// No credential or cookie of the recordings reaches the project.
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, secret := range []string{
			"chrome-bearer-7f3a", "cmVpbmRleC1wYXNzLTQy", "reindex-pass-42", "chrome-api-key-3c9d", "6629fae49393a05397450978507c4ef1",
			"chrome-session-8b1d", "firefox-session-2e4f", "ff-token-5d6e", "ff-api-key-91c2",
		} {
			if strings.Contains(string(data), secret) {
				t.Errorf("%s holds the secret %s", p, secret)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
