package cookies

import (
	"fmt"
	"net/url"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/windowctx"
	core "github.com/sorrelfox/postsonde/internal/core/cookies"
	"github.com/sorrelfox/postsonde/internal/core/workspace"
)

// TestWindowsShareTheJar (APP-06, docs/windows.md): every window of the
// project sends through, and edits in the cookie manager, the one jar of
// the project. Windows storing response cookies, editing, deleting and
// listing at once keep every cookie they stored, and what is saved to
// disk is the jar as it ends up. Run with -race.
func TestWindowsShareTheJar(t *testing.T) {
	t.Parallel()
	m := workspace.New(tempDir(t))
	project := filepath.Join(tempDir(t), "shop")
	if _, err := m.Create(project, "Shop"); err != nil {
		t.Fatal(err)
	}
	registry := core.NewRegistry(core.Options{SaveDelay: time.Millisecond})
	jars := NewJars(registry, m)
	svc := New(jars, nil)
	u, err := url.Parse("https://api.example.test/")
	if err != nil {
		t.Fatal(err)
	}
	const windows, rounds = 4, 30

	var wg sync.WaitGroup
	for w := range windows {
		wg.Go(func() {
			ctx := windowctx.With(t.Context(), uint(w+1))
			for i := range rounds {
				// A send's Set-Cookie (the HTTP service's path).
				if _, rejected, err := jars.SetCookieLines(u, []string{fmt.Sprintf("w%d_%d=v; Path=/", w, i)}); err != nil || len(rejected) != 0 {
					t.Errorf("SetCookieLines: %v, rejected %v", err, rejected)
					return
				}
				// The cookie manager: add one, then delete it again.
				c, err := svc.Set(ctx, SetInputDTO{Cookie: CookieDTO{Name: fmt.Sprintf("m%d_%d", w, i), Value: "x", Domain: "api.example.test", HostOnly: true, Path: "/"}})
				if err != nil {
					t.Errorf("Set: %v", err)
					return
				}
				if err := svc.Delete(ctx, c.Key); err != nil {
					t.Errorf("Delete: %v", err)
					return
				}
				if _, err := svc.List(ctx); err != nil {
					t.Errorf("List: %v", err)
					return
				}
				if _, err := jars.Header(u); err != nil {
					t.Errorf("Header: %v", err)
					return
				}
			}
		})
	}
	wg.Wait()

	list, err := svc.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Cookies) != windows*rounds {
		t.Fatalf("%d cookies, want %d", len(list.Cookies), windows*rounds)
	}
	if err := jars.CloseAll(); err != nil {
		t.Fatal(err)
	}
	// Saved as it ended up: a fresh registry reads every stored cookie.
	again := NewJars(core.NewRegistry(core.Options{SaveDelay: time.Hour}), m)
	t.Cleanup(func() { _ = again.CloseAll() })
	jar, err := again.Jar()
	if err != nil {
		t.Fatal(err)
	}
	if n := jar.Len(); n != windows*rounds {
		t.Errorf("%d cookies on disk, want %d", n, windows*rounds)
	}
}
