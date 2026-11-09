package app

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sorrelfox/postsonde/internal/app/services/collection"
	httpservice "github.com/sorrelfox/postsonde/internal/app/services/http"
	"github.com/sorrelfox/postsonde/internal/app/services/workspace"
	"github.com/sorrelfox/postsonde/internal/core/oauth2/tokenstore"
)

// appServices are the bound services a test drives.
type appServices struct {
	workspace  *workspace.WorkspaceService
	collection *collection.CollectionService
	http       *httpservice.HTTPService
}

func boundServices(t *testing.T, data string) appServices {
	t.Helper()
	var out appServices
	for _, s := range services(Config{DataDir: data}) {
		switch v := s.Instance().(type) {
		case *workspace.WorkspaceService:
			out.workspace = v
		case *collection.CollectionService:
			out.collection = v
		case *httpservice.HTTPService:
			out.http = v
		}
	}
	if out.workspace == nil || out.collection == nil || out.http == nil {
		t.Fatal("a service is not bound")
	}
	return out
}

// The tokens an oauth2 auth gets are kept in the
// project's local directory, never in the project folder, and are there
// again after a restart of the app.
func TestOAuth2TokensStayOutOfTheProject(t *testing.T) {
	t.Parallel()
	const secret = "idp-client-secret-4f1c"
	var mu sync.Mutex
	var issued []string
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, s, _ := r.BasicAuth(); s != secret {
			w.WriteHeader(401)
			return
		}
		mu.Lock()
		n := len(issued) + 1
		tok := fmt.Sprintf("app-access-token-%d-9c2e", n)
		issued = append(issued, tok, fmt.Sprintf("app-refresh-token-%d-7d1a", n))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": tok, "refresh_token": fmt.Sprintf("app-refresh-token-%d-7d1a", n), "expires_in": 3600})
	}))
	t.Cleanup(idp.Close)

	data, project := t.TempDir(), filepath.Join(t.TempDir(), "proj")
	ctx := t.Context()
	app := boundServices(t, data)
	ws, err := app.workspace.Create(ctx, workspace.CreateInputDTO{Path: project})
	if err != nil {
		t.Fatal(err)
	}
	created, err := app.collection.CreateRequest(ctx, collection.CreateRequestInputDTO{Name: "Orders", Kind: "http", Index: -1})
	if err != nil {
		t.Fatal(err)
	}
	req, err := app.collection.Request(ctx, created.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	req.URL = "https://api.test/orders"
	req.Auth = collection.AuthDTO{Type: "oauth2", ClientSecret: secret,
		OAuth2: &collection.OAuth2AuthDTO{TokenURL: idp.URL + "/token", ClientID: "app", Scope: "orders"}}
	if _, err := app.collection.UpdateRequest(ctx, collection.UpdateRequestInputDTO{Request: req}); err != nil {
		t.Fatal(err)
	}
	st, err := app.http.OAuth2GetToken(ctx, httpservice.OAuth2GetTokenInputDTO{RequestID: created.NodeID})
	if err != nil || st.Status != httpservice.OAuth2TokenValid {
		t.Fatalf("OAuth2GetToken = %+v, %v", st, err)
	}
	if err := app.collection.FlushAll(ctx); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	tokens := append([]string(nil), issued...)
	mu.Unlock()
	scanned := 0
	err = filepath.WalkDir(project, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		scanned++
		for _, s := range append(tokens, secret) {
			if strings.Contains(string(b), s) {
				t.Errorf("%s holds %q", path, s)
			}
		}
		return nil
	})
	if err != nil || scanned < 2 {
		t.Fatalf("scanned %d project files: %v", scanned, err)
	}
	side, err := os.ReadFile(filepath.Join(ws.LocalDir, filepath.FromSlash(tokenstore.FileName)))
	if err != nil || !strings.Contains(string(side), tokens[0]) || !strings.Contains(string(side), tokens[1]) {
		t.Fatalf("token file: %s, %v", side, err)
	}

	// A restart: the token is there, for the same auth.
	again := boundServices(t, data)
	if _, err := again.workspace.Open(ctx, project); err != nil {
		t.Fatal(err)
	}
	after, err := again.http.OAuth2Token(ctx, httpservice.OAuth2TokenInputDTO{RequestID: created.NodeID})
	if err != nil || after.Status != httpservice.OAuth2TokenValid || after.Key != st.Key || !after.Refreshable {
		t.Errorf("after a restart = %+v, %v (before %+v)", after, err, st)
	}
}
