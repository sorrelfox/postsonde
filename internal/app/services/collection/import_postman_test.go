package collection

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	envservice "github.com/sorrelfox/postsonde/internal/app/services/environments"
)

// A Postman collection, environment and globals export go through the
// default formats into a project: nested folders with their settings,
// requests, environments, and no secret in the project's files.
func TestImportPostman(t *testing.T) {
	t.Parallel()
	manager, dir := newProject(t)
	ev := &namedEvents{}
	svc := New(manager, ev.emit, nil)
	ctx := t.Context()
	fixture := func(name string) string {
		path, err := filepath.Abs(filepath.Join("..", "..", "..", "core", "importer", "postman", "testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		return path
	}
	if !slices.ContainsFunc(svc.ImportFormats(ctx), func(f ImportFormatDTO) bool { return f.ID == "postman" }) {
		t.Fatalf("formats = %+v", svc.ImportFormats(ctx))
	}

	path := fixture("shop.postman_collection.json")
	pre, err := svc.PreviewImport(ctx, PreviewImportInputDTO{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if pre.Format.ID != "postman" || pre.Items[0].Name != "Shop API" {
		t.Fatalf("preview = %+v", pre.Format)
	}
	if c := pre.Counts; c.Folders != 4 || c.Requests != 20 || c.Environments != 0 || c.Skipped != 1 || c.Warnings == 0 {
		t.Errorf("counts = %+v", c)
	}
	res, err := svc.Import(ctx, ImportInputDTO{Path: path, Digest: pre.Digest, Index: -1})
	if err != nil {
		t.Fatal(err)
	}
	rep := res.Report
	if rep.Counts.Requests != 20 || rep.Counts.Failed != 0 || rep.Counts.Folders != 4 {
		t.Fatalf("report counts = %+v", rep.Counts)
	}
	byName := map[string]ImportItemDTO{}
	for _, it := range rep.Items {
		byName[it.Name] = it
	}
	for name, kind := range map[string]string{"List users": "http", "Search products": "graphql", "Upload avatar": "http"} {
		if it := byName[name]; it.Status != "imported" || it.FileKind != kind || it.NodeID == "" {
			t.Errorf("%s = %+v", name, it)
		}
	}

	// Typed secrets and secret variables are in the local side files,
	// readable through the service.
	create := getRequest(t, svc, byName["Create user"].NodeID)
	if create.Auth.Type != "basic" || create.Auth.Password != "hunter2" || create.Auth.Username != "{{admin_user}}" {
		t.Errorf("create user auth = %+v", create.Auth)
	}
	token := getRequest(t, svc, byName["Request token"].NodeID)
	if token.Auth.Type != "oauth1" || token.Auth.ConsumerSecret != "oauth1-consumer-secret-5" || token.Auth.TokenSecret != "{{oauth_token_secret}}" ||
		!slices.Equal(token.Auth.LocalSecrets, []string{"consumerSecret"}) {
		t.Errorf("request token auth = %+v", token.Auth)
	}
	partner := getRequest(t, svc, byName["Partner orders"].NodeID)
	if partner.Auth.Type != "oauth1" || !strings.Contains(partner.Auth.ConsumerSecret, "rsa-key-6") || !slices.Equal(partner.Auth.LocalSecrets, []string{"consumerSecret"}) {
		t.Errorf("partner auth = %+v", partner.Auth)
	}
	avatars, err := svc.Folder(ctx, byName["Avatars"].NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(avatars.Variables) != 2 || avatars.Variables[0].Value != "s3cr3t-avatar" || avatars.Auth.Type != "apikey" {
		t.Errorf("avatars = %+v", avatars)
	}
	shop, err := svc.Folder(ctx, rep.Items[0].NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(shop.Variables) != 4 || shop.Variables[2].Value != "coll-api-key-5" || shop.Auth.Type != "bearer" {
		t.Errorf("collection folder = %+v", shop)
	}

	// The environment and the globals become environments.
	for _, name := range []string{"staging.postman_environment.json", "workspace.postman_globals.json"} {
		p := fixture(name)
		pre, err := svc.PreviewImport(ctx, PreviewImportInputDTO{Path: p})
		if err != nil {
			t.Fatal(err)
		}
		res, err := svc.Import(ctx, ImportInputDTO{Path: p, Digest: pre.Digest, Index: -1})
		if err != nil {
			t.Fatal(err)
		}
		if c := res.Report.Counts; c.Environments != 1 || c.Folders != 0 || !res.Report.IntoParent {
			t.Errorf("%s counts = %+v", name, c)
		}
	}
	if !ev.has(EventChanged) || !ev.has(envservice.EventChanged) {
		t.Errorf("events = %v", ev.names())
	}
	envs, err := envservice.New(manager, nil).List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]envservice.VariableDTO{}
	for _, e := range envs.Environments {
		for _, v := range e.Variables {
			values[e.Name+"/"+v.Name] = v
		}
	}
	if v := values["Staging/token"]; !v.Secret || v.Value != "stg-token-9f8e" {
		t.Errorf("staging token = %+v", v)
	}
	if v := values["Staging/page_size"]; v.Enabled {
		t.Errorf("staging page_size = %+v", v)
	}
	if v := values["My Workspace Globals/global_secret"]; !v.Secret || v.Value != "glob-secret-77" {
		t.Errorf("global secret = %+v (%v)", v, envs)
	}

	// No secret, typed or dropped with an auth PostSonde lacks, reaches the
	// project.
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, secret := range []string{"hunter2", "s3cr3t-avatar", "coll-api-key-5", "stg-token-9f8e", "glob-secret-77", "oauth-client-secret-1", "aws-secret-2",
			"oauth1-consumer-secret-5", "rsa-key-6", "oauth1-token-secret-7", "unused-with-rsa"} {
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
