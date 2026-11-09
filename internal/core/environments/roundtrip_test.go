package environments

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// TestEnvironmentFilesRoundTrip (ENV-07): loading an environment
// file and saving its variables unchanged, as the table or the raw JSON
// editor does when nothing was edited, leaves the file byte for byte as
// it was, whatever its layout, unknown members included. The canonical
// files are also exactly what PostSonde writes (their golden form).
func TestEnvironmentFilesRoundTrip(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob(filepath.Join("testdata", "roundtrip", "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no golden files: %v", err)
	}
	for _, golden := range files {
		t.Run(filepath.Base(golden), func(t *testing.T) {
			t.Parallel()
			want := readFile(t, golden)
			if strings.HasPrefix(filepath.Base(golden), "canonical-") {
				f, err := projectfile.DecodeEnvironment([]byte(want))
				if err != nil {
					t.Fatal(err)
				}
				data, err := projectfile.Marshal(f)
				if err != nil {
					t.Fatal(err)
				}
				if string(data) != want {
					t.Errorf("Marshal differs from the golden file:\n%s\nwant:\n%s", data, want)
				}
			}
			s, root, _ := newStore(t)
			path := filepath.Join(root, "environments", filepath.Base(golden))
			writeFile(t, path, want)
			snap, err := s.List()
			if err != nil || len(snap.Environments) != 1 || len(snap.Problems) != 0 {
				t.Fatalf("List = %+v, %v", snap, err)
			}
			env := snap.Environments[0]
			saved, err := s.SaveVariables(env.ID, env.Variables)
			if err != nil {
				t.Fatal(err)
			}
			if got := readFile(t, path); got != want {
				t.Errorf("SaveVariables rewrote the file:\n%s\nwant:\n%s", got, want)
			}
			if !slices.Equal(saved.Variables, env.Variables) {
				t.Errorf("saved %+v, want %+v", saved.Variables, env.Variables)
			}
			if _, err := s.SetColor(env.ID, env.Color); err != nil {
				t.Fatal(err)
			}
			if got := readFile(t, path); got != want {
				t.Errorf("SetColor rewrote the file:\n%s", got)
			}
			// A real change is written, in the canonical layout.
			changed := append(slices.Clone(env.Variables), Variable{Name: "added", Value: "x"})
			if _, err := s.SaveVariables(env.ID, changed); err != nil {
				t.Fatal(err)
			}
			if got := readFile(t, path); !strings.Contains(got, `"name": "added"`) {
				t.Errorf("a change was not written:\n%s", got)
			}
		})
	}
}
