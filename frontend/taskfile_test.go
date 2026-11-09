package frontend

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// taskIf matches a Taskfile `if:` key, on a command or a task.
var taskIf = regexp.MustCompile(`(?m)^\s*(-\s+)?if:`)

// TestTaskfilesUseNoIf guards the dist check against `wails3 dev`:
// the Task library wails3 embeds (github.com/wailsapp/task/v3 v3.40.1)
// predates `if:` and ignores it without a word, so a command meant to be
// skipped runs. That ran `npm run check:dist` on the unminified dev build,
// whose libraries' doc links fail it, and `wails3 dev` never started.
// Conditions belong in the shell command instead.
func TestTaskfilesUseNoIf(t *testing.T) {
	t.Parallel()

	files, err := filepath.Glob(filepath.Join("..", "build", "*", "Taskfile.yml"))
	if err != nil {
		t.Fatal(err)
	}
	files = append(files, filepath.Join("..", "Taskfile.yml"), filepath.Join("..", "build", "Taskfile.yml"))
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if loc := taskIf.FindIndex(data); loc != nil {
			line := strings.Count(string(data[:loc[0]]), "\n") + 1
			t.Errorf("%s:%d: `if:` is ignored by the Task in wails3; put the condition in the command", f, line)
		}
	}
}

// TestDistCheckSkipsOnlyDevBuilds checks that build:frontend runs
// check:dist behind a shell test of DEV, so production builds are checked
// and `wails3 dev` (DEV=true) isn't.
func TestDistCheckSkipsOnlyDevBuilds(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile(filepath.Join("..", "build", "Taskfile.yml"))
	if err != nil {
		t.Fatal(err)
	}
	const want = `'[ "{{.DEV}}" = "true" ] || npm run check:dist -s'`
	var found []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.Contains(line, "check:dist") && !strings.HasPrefix(strings.TrimSpace(line), "#") {
			found = append(found, strings.TrimSpace(line))
		}
	}
	if len(found) != 1 || found[0] != "- "+want {
		t.Errorf("build/Taskfile.yml: want the one check:dist command to be\n  - %s\ngot %q", want, found)
	}
}
