package importer_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/collection"
	"github.com/sorrelfox/postsonde/internal/core/environments"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/importer"
	"github.com/sorrelfox/postsonde/internal/core/requestsecrets"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// sample is a test importer: it recognises files starting with "SAMPLE"
// and returns a fixed model with nested folders, settings, secrets, a
// skipped item and environments — the shapes the Postman, Insomnia and
// OpenAPI importers produce.
type sample struct{ id string }

func (s sample) Format() importer.Format {
	return importer.Format{ID: s.id, Name: "Sample " + s.id, Extensions: []string{".sample"}}
}

func (s sample) Detect(src importer.Source) int {
	switch {
	case strings.HasPrefix(string(src.Data), "SAMPLE "+s.id):
		return importer.Sure
	case strings.HasPrefix(string(src.Data), "SAMPLE"):
		return importer.Maybe
	}
	return importer.No
}

func (s sample) Parse(_ context.Context, src importer.Source) (*importer.Model, error) {
	if strings.Contains(string(src.Data), "BROKEN") {
		return nil, importer.InvalidFile(s.Format(), "The file is broken.", importer.Location{Line: 3})
	}
	return sampleModel(), nil
}

func sampleModel() *importer.Model {
	get := &projectfile.HTTPRequest{Method: "GET", URL: "{{base}}/users",
		Auth: projectfile.Auth{Type: projectfile.AuthBearer, Token: "typed-token"}}
	post := &projectfile.HTTPRequest{Method: "POST", URL: "{{base}}/users",
		Body: projectfile.Body{Mode: projectfile.BodyRaw, Type: "json", Text: `{"a":1}`},
		Auth: projectfile.Auth{Type: projectfile.AuthOAuth1, ConsumerKey: "ck", ConsumerSecret: "typed-consumer-secret",
			AccessToken: "at", TokenSecret: "typed-token-secret"}}
	gql := &projectfile.GraphQLRequest{URL: "{{base}}/graphql", Query: "{ me { id } }"}
	return &importer.Model{
		Name: "Shop API",
		Root: importer.Folder{
			Description: "The shop.",
			Variables:   []projectfile.Variable{{Name: "base", Value: "https://shop.test"}},
			Headers:     []projectfile.KeyValue{{Name: "X-Client", Value: "postsonde"}},
			Auth:        projectfile.Auth{Type: projectfile.AuthBasic, Username: "ada", Password: "typed-password"},
			Items: []importer.Item{
				importer.FolderItem(&importer.Folder{
					Name:      "Users",
					Variables: []projectfile.Variable{{Name: "key", Value: "s3cret", Secret: true}},
					Source:    importer.Location{Line: 4, Path: "item[0]"},
					Items: []importer.Item{
						importer.RequestItem(&importer.Request{Name: "List users", File: get, Source: importer.Location{Line: 5, Path: "item[0].item[0]"},
							Diagnostics: []importer.Diagnostic{{Code: "sample.script", Severity: importer.SeverityWarning, Message: "A script was kept as a note."}}}),
						importer.RequestItem(&importer.Request{Name: "List users", File: post}),
						importer.SkippedItem(&importer.Skipped{Kind: "socket.io request", Name: "Live", Code: "sample.unsupported", Reason: "Socket.IO requests cannot be imported."}),
						importer.FolderItem(&importer.Folder{Name: "Empty"}),
					},
				}),
				importer.RequestItem(&importer.Request{Name: "Me", File: gql}),
			},
		},
		Environments: []importer.Environment{
			{Name: "Staging", Variables: []environments.Variable{{Name: "base", Value: "https://staging.test"}, {Name: "token", Value: "env-secret", Secret: true}}},
			{Name: "Production"},
		},
	}
}

// project creates an empty writable project and returns its target.
func project(t *testing.T) (string, importer.Target) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "project")
	local := filepath.Join(base, "local")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	ws := &projectfile.Workspace{ID: projectfile.NewID(projectfile.KindWorkspace), Name: "Test"}
	if err := projectfile.WriteFile(filepath.Join(root, projectfile.WorkspaceFileName), ws); err != nil {
		t.Fatal(err)
	}
	tree, _ := collection.Load(root)
	return root, importer.Target{
		Tree:         tree,
		Secrets:      requestsecrets.New(local),
		Environments: environments.New(root, local, false),
	}
}

func TestRegistryDetectAndParse(t *testing.T) {
	r := importer.NewRegistry(sample{"a"}, sample{"b"})
	if got := len(r.Formats()); got != 2 {
		t.Fatalf("formats = %d", got)
	}
	im, err := r.Detect(importer.Source{Data: []byte("SAMPLE b")})
	if err != nil || im.Format().ID != "b" {
		t.Fatalf("Detect = %v, %v; want b", im, err)
	}
	// A tie goes to the first registered.
	im, _ = r.Detect(importer.Source{Data: []byte("SAMPLE")})
	if im.Format().ID != "a" {
		t.Errorf("tie went to %s", im.Format().ID)
	}
	_, err = r.Detect(importer.Source{Name: "x.bin", Data: []byte("nope")})
	if !errors.Is(err, &errs.Error{Code: importer.CodeUnknownFormat}) {
		t.Errorf("unknown format: %v", err)
	}
	_, _, err = r.Parse(context.Background(), importer.Source{Data: []byte("SAMPLE")}, "zzz")
	if !errors.Is(err, &errs.Error{Code: importer.CodeUnknownFormat}) {
		t.Errorf("unknown id: %v", err)
	}
	_, _, err = r.Parse(context.Background(), importer.Source{Data: []byte("SAMPLE a BROKEN")}, "")
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != importer.CodeInvalidFile || e.Kind != errs.Parse || e.Details["line"] != "3" {
		t.Errorf("broken: %#v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := r.Parse(ctx, importer.Source{Data: []byte("SAMPLE a")}, ""); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
}

type emptyImporter struct{}

func (emptyImporter) Format() importer.Format    { return importer.Format{ID: "empty", Name: "Empty"} }
func (emptyImporter) Detect(importer.Source) int { return importer.Maybe }
func (emptyImporter) Parse(context.Context, importer.Source) (*importer.Model, error) {
	return nil, nil
}

func TestParseNormalisesTheModel(t *testing.T) {
	r := importer.NewRegistry(emptyImporter{})
	m, _, err := r.Parse(context.Background(), importer.Source{Name: "orders.json", Data: []byte("{}")}, "")
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "orders" {
		t.Errorf("name = %q", m.Name)
	}
	if len(m.Diagnostics) != 1 || m.Diagnostics[0].Code != importer.CodeEmpty {
		t.Errorf("diagnostics = %+v", m.Diagnostics)
	}
	m, _, _ = r.Parse(context.Background(), importer.Source{}, "")
	if m.Name != "Imported" {
		t.Errorf("fallback name = %q", m.Name)
	}
}

func TestPreview(t *testing.T) {
	r := importer.Preview(sampleModel())
	var got []string
	for _, it := range r.Items {
		got = append(got, string(it.Kind)+":"+it.Name+":"+string(it.Status))
	}
	want := []string{
		"folder:Shop API:new", "folder:Users:new", "request:List users:new", "request:List users:new",
		"skipped:Live:skipped", "folder:Empty:new", "request:Me:new",
		"environment:Staging:new", "environment:Production:new",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("items:\n got %v\nwant %v", got, want)
	}
	if r.Items[2].Parent != 1 || r.Items[1].Parent != 0 || r.Items[0].Parent != -1 || r.Items[6].Parent != 0 || r.Items[7].Parent != -1 {
		t.Errorf("parents wrong: %+v", r.Items)
	}
	if r.Items[2].Method != "GET" || r.Items[2].Detail != "{{base}}/users" || r.Items[2].FileKind != projectfile.KindHTTP {
		t.Errorf("request detail: %+v", r.Items[2])
	}
	c := r.Counts
	if c.Folders != 3 || c.Requests != 3 || c.Environments != 2 || c.Skipped != 1 || c.Warnings != 2 || c.Lossy != 1 {
		t.Errorf("counts = %+v", c)
	}
}

func TestWriteNewFolder(t *testing.T) {
	root, target := project(t)
	// An item called like the import is there already.
	if _, err := target.Tree.CreateFolder("", "Shop API", collection.End); err != nil {
		t.Fatal(err)
	}
	if _, err := target.Environments.Create("staging"); err != nil {
		t.Fatal(err)
	}
	rep, err := importer.Write(target, sampleModel(), importer.Options{Index: collection.End})
	if err != nil {
		t.Fatal(err)
	}
	top := rep.Items[0]
	if top.Status != importer.StatusImported || top.Name != "Shop API 2" || !hasCode(top.Diagnostics, importer.CodeRenamed) {
		t.Fatalf("top item = %+v", top)
	}
	c := rep.Counts
	if c.Folders != 3 || c.Requests != 3 || c.Environments != 2 || c.Skipped != 1 || c.Failed != 0 {
		t.Errorf("counts = %+v", c)
	}

	tree := target.Tree
	n := tree.Node(top.NodeID)
	if n == nil || n.Kind != collection.KindFolder || n.Path != "shop-api-2" {
		t.Fatalf("new folder node = %+v", n)
	}
	var names []string
	for _, c := range n.Children {
		names = append(names, c.DisplayName())
	}
	if strings.Join(names, ",") != "Users,Me" {
		t.Errorf("order = %v", names)
	}
	users := n.Children[0]
	names = nil
	for _, c := range users.Children {
		names = append(names, c.DisplayName()+"="+c.Entry)
	}
	if strings.Join(names, ",") != "List users=list-users.json,List users=list-users-2.json,Empty=empty" {
		t.Errorf("users = %v", names)
	}

	// Folder settings, with the typed Basic password in the side file.
	folder := n.File.(*projectfile.Folder)
	if folder.Description != "The shop." || len(folder.Variables) != 1 || len(folder.Headers) != 1 || folder.Auth.Password != "" {
		t.Errorf("folder file = %+v", folder)
	}
	if v, _ := target.Secrets.Read(n.ID); v["auth.password"] != "typed-password" {
		t.Errorf("folder secret = %v", v)
	}
	// The secret folder variable's value is kept in the side file only.
	uf := users.File.(*projectfile.Folder)
	if uf.Variables[0].Value != "" || !uf.Variables[0].Secret {
		t.Errorf("secret variable written: %+v", uf.Variables)
	}
	if v, _ := target.Secrets.Read(users.ID); v["var."+uf.Variables[0].Name] == "" {
		t.Errorf("secret variable value not kept locally: %v", v)
	}
	// The request's typed token is local only.
	get := users.Children[0]
	if f := get.File.(*projectfile.HTTPRequest); f.Auth.Token != "" || len(f.Auth.LocalSecrets) != 1 {
		t.Errorf("request auth = %+v", f.Auth)
	}
	if v, _ := target.Secrets.Read(get.ID); v["auth.token"] != "typed-token" {
		t.Errorf("request secret = %v", v)
	}
	if rep.Items[2].NodeID != get.ID {
		t.Errorf("report node = %s, tree %s", rep.Items[2].NodeID, get.ID)
	}
	// Both OAuth 1.0 secrets are local only.
	post := users.Children[1]
	if f := post.File.(*projectfile.HTTPRequest); f.Auth.ConsumerSecret != "" || f.Auth.TokenSecret != "" || len(f.Auth.LocalSecrets) != 2 || f.Auth.ConsumerKey != "ck" {
		t.Errorf("oauth1 auth = %+v", f.Auth)
	}
	if v, _ := target.Secrets.Read(post.ID); v["auth.consumerSecret"] != "typed-consumer-secret" || v["auth.tokenSecret"] != "typed-token-secret" {
		t.Errorf("oauth1 secrets = %v", v)
	}
	for _, path := range []string{"shop-api-2/_folder.json", "shop-api-2/users/list-users.json", "shop-api-2/users/list-users-2.json"} {
		data, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "typed-") || strings.Contains(string(data), "s3cret") {
			t.Errorf("%s holds a secret: %s", path, data)
		}
	}

	// Environments: "Staging" is taken (ignoring case), its secret is local.
	snap, err := target.Environments.List()
	if err != nil {
		t.Fatal(err)
	}
	var envs []string
	for _, e := range snap.Environments {
		envs = append(envs, e.Name)
		if e.Name == "Staging 2" && (len(e.Variables) != 2 || e.Variables[1].Value != "env-secret") {
			t.Errorf("staging variables = %+v", e.Variables)
		}
	}
	if strings.Join(envs, ",") != "Production,staging,Staging 2" {
		t.Errorf("environments = %v", envs)
	}
	entries, _ := os.ReadDir(filepath.Join(root, projectfile.EnvironmentsDir))
	for _, e := range entries {
		data, _ := os.ReadFile(filepath.Join(root, projectfile.EnvironmentsDir, e.Name()))
		if strings.Contains(string(data), "env-secret") {
			t.Errorf("%s holds a secret", e.Name())
		}
	}
	// Nothing was overwritten: the old folder is still there and empty.
	if old := tree.NodeAt("shop-api"); old == nil || len(old.Children) != 0 {
		t.Errorf("old folder = %+v", old)
	}
}

func TestWriteIntoParent(t *testing.T) {
	_, target := project(t)
	parent, err := target.Tree.CreateFolder("", "Existing", collection.End)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := target.Tree.CreateRequest(parent, "First", projectfile.KindHTTP, collection.End); err != nil {
		t.Fatal(err)
	}
	rep, err := importer.Write(target, sampleModel(), importer.Options{ParentID: parent, IntoParent: true, Index: 0, SkipEnvironments: true})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.IntoParent || rep.Items[0].NodeID != parent || !hasCode(rep.Items[0].Diagnostics, importer.CodeSettingsSkipped) {
		t.Errorf("top = %+v", rep.Items[0])
	}
	n := target.Tree.Node(parent)
	var names []string
	for _, c := range n.Children {
		names = append(names, c.DisplayName())
	}
	if strings.Join(names, ",") != "Users,Me,First" {
		t.Errorf("children = %v", names)
	}
	if len(n.Children[0].Children) != 3 {
		t.Errorf("users content = %d", len(n.Children[0].Children))
	}
	c := rep.Counts
	if c.Folders != 2 || c.Requests != 3 || c.Environments != 0 || c.Skipped != 3 {
		t.Errorf("counts = %+v", c)
	}
	env := rep.Items[len(rep.Items)-1]
	if env.Status != importer.StatusSkipped || !hasCode(env.Diagnostics, importer.CodeEnvironmentsSkipped) {
		t.Errorf("environment = %+v", env)
	}
	if snap, _ := target.Environments.List(); len(snap.Environments) != 0 {
		t.Errorf("environments written: %+v", snap.Environments)
	}
}

func TestWriteFailures(t *testing.T) {
	_, target := project(t)
	// An unknown request kind fails alone; the rest is written.
	m := &importer.Model{Name: "x", Root: importer.Folder{Items: []importer.Item{
		importer.RequestItem(&importer.Request{Name: "bad", File: &projectfile.Environment{}}),
		importer.RequestItem(&importer.Request{Name: "nil"}),
		importer.RequestItem(&importer.Request{Name: "ok", File: &projectfile.WebSocketRequest{URL: "wss://x.test"}}),
	}}}
	rep, err := importer.Write(target, m, importer.Options{Index: collection.End})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Items[1].Status != importer.StatusFailed || rep.Items[1].Err == nil || !hasCode(rep.Items[1].Diagnostics, importer.CodeInvalidItem) {
		t.Errorf("bad = %+v", rep.Items[1])
	}
	if rep.Items[2].Status != importer.StatusFailed || rep.Items[3].Status != importer.StatusImported {
		t.Errorf("items = %+v", rep.Items)
	}
	if rep.Counts.Failed != 2 || rep.Counts.Requests != 1 || rep.Counts.Errors != 2 {
		t.Errorf("counts = %+v", rep.Counts)
	}

	// An unknown parent fails before anything is written.
	if _, err := importer.Write(target, sampleModel(), importer.Options{ParentID: "fld_missing"}); !errors.Is(err, &errs.Error{Code: collection.CodeNotFound}) {
		t.Errorf("missing parent: %v", err)
	}
	if _, err := importer.Write(target, sampleModel(), importer.Options{ParentID: "fld_missing", IntoParent: true}); !errors.Is(err, &errs.Error{Code: collection.CodeNotFound}) {
		t.Errorf("missing parent, into: %v", err)
	}
}

func hasCode(ds []importer.Diagnostic, code string) bool {
	for _, d := range ds {
		if d.Code == code {
			return true
		}
	}
	return false
}
