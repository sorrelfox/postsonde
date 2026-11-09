package collection

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/importer"
)

// DetectImport, the detection behind files dropped on the window (IMP-11),
// picks the right format of every fixture of every default format, agrees
// with PreviewImport, and needs no open project.
func TestDetectImportFixtures(t *testing.T) {
	t.Parallel()
	svc := New(nil, nil, nil)
	ctx := t.Context()
	fixture := func(parts ...string) string {
		path, err := filepath.Abs(filepath.Join(append([]string{"..", "..", "..", "core"}, parts...)...))
		if err != nil {
			t.Fatal(err)
		}
		return path
	}
	want := map[string]string{
		writeImportFile(t, "orders.sh", importScript):                                  "curl",
		writeImportFile(t, "copied.txt", "curl https://api.test/health\n"):             "curl",
		fixture("importer", "postman", "testdata", "shop.postman_collection.json"):     "postman",
		fixture("importer", "postman", "testdata", "legacy.postman_collection.json"):   "postman",
		fixture("importer", "postman", "testdata", "staging.postman_environment.json"): "postman",
		fixture("importer", "postman", "testdata", "workspace.postman_globals.json"):   "postman",
		fixture("importer", "insomnia", "testdata", "shop-v4.json"):                    "insomnia",
		fixture("importer", "insomnia", "testdata", "multi-v4.json"):                   "insomnia",
		fixture("importer", "insomnia", "testdata", "shop-v4.yaml"):                    "insomnia",
		fixture("importer", "insomnia", "testdata", "shop-v5.yaml"):                    "insomnia",
		fixture("importer", "insomnia", "testdata", "env-v5.yaml"):                     "insomnia",
		fixture("importer", "insomnia", "testdata", "spec-v5.yaml"):                    "insomnia",
		fixture("openapi", "testdata", "petstore-3.0.yaml"):                            "openapi",
		fixture("openapi", "testdata", "petstore-3.0.json"):                            "openapi",
		fixture("openapi", "testdata", "petstore-3.1.yaml"):                            "openapi",
		fixture("openapi", "testdata", "petstore-3.1.json"):                            "openapi",
		fixture("openapi", "testdata", "petstore-2.0.yaml"):                            "openapi",
		fixture("openapi", "testdata", "petstore-2.0.json"):                            "openapi",
		fixture("importer", "openapiimport", "testdata", "params.yaml"):                "openapi",
		fixture("importer", "harimport", "testdata", "chrome.har"):                     "har",
		fixture("importer", "harimport", "testdata", "firefox.har"):                    "har",
		fixture("importer", "altair", "testdata", "github-v5.agq"):                     "altair",
		fixture("importer", "altair", "testdata", "starwars-v4.agq"):                   "altair",
		fixture("importer", "altair", "testdata", "shop-v8.agc"):                       "altair",
		fixture("importer", "altair", "testdata", "countries-flat-v5.agc"):             "altair",
		fixture("importer", "altair", "testdata", "staging-environment.json"):          "altair",
	}
	paths := make([]string, 0, len(want))
	for path := range want {
		paths = append(paths, path)
	}
	// One call per MaxDetectFiles files, as a drop would.
	for start := 0; start < len(paths); start += MaxDetectFiles {
		batch := paths[start:min(start+MaxDetectFiles, len(paths))]
		got, err := svc.DetectImport(ctx, DetectImportInputDTO{Paths: batch})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(batch) {
			t.Fatalf("got %d entries for %d paths", len(got), len(batch))
		}
		for i, d := range got {
			path := batch[i]
			if d.Path != path || d.FileName != filepath.Base(path) {
				t.Errorf("entry %d = %+v, want %s", i, d, path)
			}
			if d.Error != nil || d.Format.ID != want[path] || d.Confidence <= importer.No {
				t.Errorf("%s: format %q (confidence %d, error %+v), want %q", filepath.Base(path), d.Format.ID, d.Confidence, d.Error, want[path])
				continue
			}
			pre, err := svc.PreviewImport(ctx, PreviewImportInputDTO{Path: path})
			if err != nil {
				t.Errorf("%s: preview: %v", filepath.Base(path), err)
			} else if pre.Format.ID != d.Format.ID {
				t.Errorf("%s: preview detects %q, DetectImport %q", filepath.Base(path), pre.Format.ID, d.Format.ID)
			}
		}
	}
}

// Files that cannot be imported are entries with the reason, in order;
// only a bad call fails.
func TestDetectImportProblems(t *testing.T) {
	t.Parallel()
	svc := New(nil, nil, nil)
	ctx := t.Context()
	dir := t.TempDir()
	image := writeImportFile(t, "photo.png", "\x89PNG\r\n\x1a\n")
	notes := writeImportFile(t, "notes.txt", "Remember to buy milk.\n")
	missing := filepath.Join(dir, "gone.json")
	folder := filepath.Join(dir, "folder")
	if err := os.Mkdir(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	collection := writeImportFile(t, "c.json", `{"info": {"name": "C", "schema": "https://schema.getpostman.com/json/collection/v2.1.0/collection.json"}, "item": []}`)

	got, err := svc.DetectImport(ctx, DetectImportInputDTO{Paths: []string{image, notes, missing, folder, collection}})
	if err != nil {
		t.Fatal(err)
	}
	wantCodes := []string{importer.CodeUnknownFormat, importer.CodeUnknownFormat, importer.CodeReadFailed, importer.CodeReadFailed, ""}
	for i, d := range got {
		code := ""
		if d.Error != nil {
			code = d.Error.Code
			if d.Error.Message == "" || d.Format.ID != "" || d.Confidence != 0 || d.Format.Extensions == nil {
				t.Errorf("entry %d = %+v", i, d)
			}
		}
		if code != wantCodes[i] {
			t.Errorf("entry %d (%s) code = %q, want %q", i, d.FileName, code, wantCodes[i])
		}
	}
	if got[4].Format.ID != "postman" || got[4].Confidence != importer.Sure {
		t.Errorf("collection = %+v", got[4])
	}

	for _, tc := range []struct {
		name  string
		paths []string
		code  string
	}{
		{"none", nil, importer.CodeReadFailed},
		{"relative", []string{"c.json"}, importer.CodeReadFailed},
		{"too many", make([]string, MaxDetectFiles+1), CodeTooManyFiles},
	} {
		_, err := svc.DetectImport(ctx, DetectImportInputDTO{Paths: tc.paths})
		e, ok := errors.AsType[*errs.Error](err)
		if !ok || e.Code != tc.code || errs.KindOf(err) != errs.InvalidArgument {
			t.Errorf("%s: err = %v, want %s", tc.name, err, tc.code)
		}
	}
	_, err = svc.DetectImport(ctx, DetectImportInputDTO{Paths: make([]string, MaxDetectFiles+1)})
	if e, ok := errors.AsType[*errs.Error](err); !ok || e.Details["limit"] != strconv.Itoa(MaxDetectFiles) {
		t.Errorf("too many: %+v", err)
	}
}
