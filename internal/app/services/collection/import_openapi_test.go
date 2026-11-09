package collection

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// An OpenAPI 3.0 document goes through the default formats into a
// project: a folder per tag, a request per operation with its example
// body, baseUrl and the auth variables in the collection's folder, and
// the credential variables as secrets without values.
func TestImportOpenAPI(t *testing.T) {
	t.Parallel()
	manager, dir := newProject(t)
	svc := New(manager, (&namedEvents{}).emit, nil)
	ctx := t.Context()
	path, err := filepath.Abs(filepath.Join("..", "..", "..", "core", "openapi", "testdata", "petstore-3.0.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(svc.ImportFormats(ctx), func(f ImportFormatDTO) bool { return f.ID == "openapi" }) {
		t.Fatalf("formats = %+v", svc.ImportFormats(ctx))
	}

	pre, err := svc.PreviewImport(ctx, PreviewImportInputDTO{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if pre.Format.ID != "openapi" || pre.Items[0].Name != "Petstore 3.0" {
		t.Fatalf("preview = %+v, %q", pre.Format, pre.Items[0].Name)
	}
	if c := pre.Counts; c.Folders != 4 || c.Requests != 7 || c.Skipped != 0 || c.Warnings != 2 {
		t.Errorf("counts = %+v", c)
	}
	res, err := svc.Import(ctx, ImportInputDTO{Path: path, Digest: pre.Digest, Index: -1})
	if err != nil {
		t.Fatal(err)
	}
	rep := res.Report
	if c := rep.Counts; c.Requests != 7 || c.Failed != 0 || c.Folders != 4 {
		t.Fatalf("report counts = %+v", c)
	}
	byName := map[string]ImportItemDTO{}
	for _, it := range rep.Items {
		byName[it.Name] = it
	}
	if it := byName["Create a pet"]; it.Status != "imported" || it.Method != "POST" || it.Detail != "{{baseUrl}}/pets" {
		t.Errorf("create a pet = %+v", it)
	}

	create := getRequest(t, svc, byName["Create a pet"].NodeID)
	if create.Body.Mode != "raw" || create.Body.Type != "json" || !strings.Contains(create.Body.Text, `"petType": "dog"`) || create.Auth.Type != "inherit" {
		t.Errorf("create a pet = %+v", create)
	}
	show := getRequest(t, svc, byName["Info for a specific pet"].NodeID)
	if show.URL != "{{baseUrl}}/pets/{petId}" || len(show.PathParams) != 1 || show.PathParams[0].Name != "petId" {
		t.Errorf("show pet = %+v", show)
	}
	del := getRequest(t, svc, byName["deletePet"].NodeID)
	if del.Auth.Type != "apikey" || del.Auth.Key != "api_key" || del.Auth.Value != "{{api_key.key}}" {
		t.Errorf("delete pet auth = %+v", del.Auth)
	}

	coll, err := svc.Folder(ctx, rep.Items[0].NodeID)
	if err != nil {
		t.Fatal(err)
	}
	vars := map[string]KeyValueDTO{}
	for _, v := range coll.Variables {
		vars[v.Name] = v
	}
	if v := vars["baseUrl"]; v.Value != "{{scheme}}://petstore.example/{{basePath}}" {
		t.Errorf("baseUrl = %+v", v)
	}
	if v := vars["api_key.key"]; !strings.Contains(v.Extra, "secret") || v.Value != "" {
		t.Errorf("api_key.key = %+v", v)
	}
	if coll.Auth.Type != "bearer" {
		t.Errorf("collection auth = %+v", coll.Auth)
	}

	// The tag folders hold their operations.
	var pets *NodeDTO
	for i := range res.Tree.Nodes {
		if res.Tree.Nodes[i].ID == byName["pets"].NodeID {
			pets = &res.Tree.Nodes[i]
		}
	}
	if pets == nil || len(pets.ChildIDs) != 5 {
		t.Fatalf("pets folder = %+v", pets)
	}

	// Credential variables are declared, never filled in the project.
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, "_folder.json") {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), `"petstore_auth.token"`) && !strings.Contains(string(data), `"secret": true`) {
			t.Errorf("%s: token variable is not secret", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A Swagger 2.0 document goes through the same format: detected, converted
// and imported as working requests.
func TestImportSwagger(t *testing.T) {
	t.Parallel()
	for _, ext := range []string{"yaml", "json"} {
		t.Run(ext, func(t *testing.T) {
			t.Parallel()
			manager, _ := newProject(t)
			svc := New(manager, (&namedEvents{}).emit, nil)
			ctx := t.Context()
			path, err := filepath.Abs(filepath.Join("..", "..", "..", "core", "openapi", "testdata", "petstore-2.0."+ext))
			if err != nil {
				t.Fatal(err)
			}
			pre, err := svc.PreviewImport(ctx, PreviewImportInputDTO{Path: path})
			if err != nil {
				t.Fatal(err)
			}
			if pre.Format.ID != "openapi" || pre.Items[0].Name != "Petstore 2.0" {
				t.Fatalf("preview = %+v, %q", pre.Format, pre.Items[0].Name)
			}
			if c := pre.Counts; c.Folders != 4 || c.Requests != 10 || c.Skipped != 0 || c.Warnings != 1 {
				t.Errorf("counts = %+v", c)
			}
			res, err := svc.Import(ctx, ImportInputDTO{Path: path, Digest: pre.Digest, Index: -1})
			if err != nil {
				t.Fatal(err)
			}
			rep := res.Report
			if c := rep.Counts; c.Requests != 10 || c.Failed != 0 || c.Folders != 4 {
				t.Fatalf("report counts = %+v", c)
			}
			byName := map[string]ImportItemDTO{}
			for _, it := range rep.Items {
				byName[it.Name] = it
			}
			create := getRequest(t, svc, byName["Create a pet"].NodeID)
			if create.Method != "POST" || create.URL != "{{baseUrl}}/pets" || create.Body.Type != "json" ||
				!strings.Contains(create.Body.Text, `"name": "Rex"`) || create.Auth.Type != "apikey" {
				t.Errorf("create a pet = %+v", create)
			}
			upload := getRequest(t, svc, byName["Uploads an image"].NodeID)
			if upload.Body.Mode != "multipart" || len(upload.Body.Parts) != 2 || upload.Body.Parts[1].Name != "file" {
				t.Errorf("upload = %+v", upload.Body)
			}
			form := getRequest(t, svc, byName["Update a pet with form data"].NodeID)
			if form.Body.Mode != "urlencoded" || len(form.PathParams) != 1 || form.PathParams[0].Name != "petId" {
				t.Errorf("form = %+v", form)
			}
			login := getRequest(t, svc, byName["Logs user into the system"].NodeID)
			if login.Auth.Type != "basic" || login.Auth.Username != "{{basic_auth.username}}" {
				t.Errorf("login auth = %+v", login.Auth)
			}
			coll, err := svc.Folder(ctx, rep.Items[0].NodeID)
			if err != nil {
				t.Fatal(err)
			}
			for _, v := range coll.Variables {
				if v.Name == "baseUrl" && v.Value != "https://petstore.example/v2" {
					t.Errorf("baseUrl = %+v", v)
				}
			}
		})
	}
}
