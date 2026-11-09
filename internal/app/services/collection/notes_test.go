package collection

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/autosave"
)

// note is Markdown with what a JSON file must escape and what the
// renderer must never run: quotes, backslashes, raw HTML, line breaks.
const note = "# Users\n\nNeeds the `users:write` scope.\n\n- [x] \"done\"\n- [ ] C:\\path\n\n<script>alert(1)</script>\n\n```js\npm.test(\"ok\")\n```\n"

// TestRequestNoteRoundTrip: a request's Markdown notes (COL-07) are its
// description member; an edit is written to the file and read back as
// typed, also after the project is loaded again.
func TestRequestNoteRoundTrip(t *testing.T) {
	t.Parallel()
	svc, _, dir, clock, _ := newAutosaveService(t)
	writeFile(t, dir, "notes.json", `{"schemaVersion": 1, "kind": "http", "id": "req_notes", "name": "Notes", "method": "GET", "url": "/"}`)
	if _, err := svc.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}

	req := getRequest(t, svc, "req_notes")
	if req.Description != "" {
		t.Fatalf("description = %q, want none", req.Description)
	}
	req.Description = note
	update(t, svc, req, 1)
	clock.Advance(autosave.DefaultDelay)

	var file map[string]any
	if err := json.Unmarshal([]byte(readFile(t, dir, "notes.json")), &file); err != nil {
		t.Fatal(err)
	}
	if file["description"] != note {
		t.Errorf("file description = %q, want %q", file["description"], note)
	}
	if _, err := svc.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := getRequest(t, svc, "req_notes").Description; got != note {
		t.Errorf("description after reload = %q", got)
	}

	// Clearing the note leaves the member out.
	req.Description = ""
	update(t, svc, req, 2)
	clock.Advance(autosave.DefaultDelay)
	if strings.Contains(readFile(t, dir, "notes.json"), `"description"`) {
		t.Errorf("an empty note is written:\n%s", readFile(t, dir, "notes.json"))
	}
}

// TestFolderNoteRoundTrip: a folder's notes are the description of its
// _folder.json, edited with its settings; the folder's other members stay.
func TestFolderNoteRoundTrip(t *testing.T) {
	t.Parallel()
	svc, dir, _, advance := folderProject(t)

	f := getFolder(t, svc, "fld_users")
	if f.Description != "" {
		t.Fatalf("description = %q, want none", f.Description)
	}
	f.Description = note
	if _, err := svc.UpdateFolder(t.Context(), UpdateFolderInputDTO{Folder: f, Revision: 1}); err != nil {
		t.Fatal(err)
	}
	if got := getFolder(t, svc, "fld_users").Description; got != note {
		t.Errorf("pending description = %q", got)
	}
	advance(autosave.DefaultDelay)

	written := readFile(t, dir, "users/_folder.json")
	var file map[string]any
	if err := json.Unmarshal([]byte(written), &file); err != nil {
		t.Fatal(err)
	}
	if file["description"] != note {
		t.Errorf("file description = %q, want %q", file["description"], note)
	}
	for _, want := range []string{`"X-Team"`, `"x-folder": true`, `"a.proto"`} {
		if !strings.Contains(written, want) {
			t.Errorf("file lacks %s:\n%s", want, written)
		}
	}
	if _, err := svc.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := getFolder(t, svc, "fld_users").Description; got != note {
		t.Errorf("description after reload = %q", got)
	}
}
