package projectzip_test

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/collection"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/importer"
	"github.com/sorrelfox/postsonde/internal/core/importer/projectzip"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// handSecrets are typed into the project files by hand; the export must
// leave every one out.
var handSecrets = []string{"hand-folder-var", "hand-folder-apikey", "hand-bearer-token", "hand-sse-token"}

// project writes a project with a folder of every request kind, settings
// of every kind, secrets typed by hand, and files the export leaves out.
func project(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, dir, "postsonde.json", `{"schemaVersion": 1, "kind": "workspace", "id": "ws_0123456789abcdef", "name": "Shop"}`)
	writeFile(t, dir, "users/_folder.json", `{"schemaVersion": 1, "kind": "folder", "id": "fld_users", "name": "Users", "description": "All about **users**.",
  "order": ["get.json", "admins", "ws.json"],
  "variables": [{"name": "vault", "value": "hand-folder-var", "secret": true}, {"name": "plain", "value": "not-a-secret"}],
  "headers": [{"name": "Accept", "value": "application/json"}],
  "auth": {"type": "apikey", "key": "X-Api-Key", "value": "hand-folder-apikey"},
  "grpc": {"proto": {"files": ["proto/shop.proto"]}},
  "server": {"type": "echo_http", "port": 8080},
  "x-note": {"kept": true}}`)
	writeFile(t, dir, "users/get.json", `{"schemaVersion": 1, "kind": "http", "id": "req_get", "name": "Get user", "method": "GET", "url": "{{base}}/users/1",
  "auth": {"type": "bearer", "token": "hand-bearer-token"}, "x-custom": 1}`)
	writeFile(t, dir, "users/ref.json", `{"kind": "http", "id": "req_ref", "name": "", "method": "POST", "url": "/ref", "auth": {"type": "bearer", "token": "{{token}}"}}`)
	writeFile(t, dir, "users/ws.json", `{"kind": "websocket", "id": "req_ws", "name": "Socket", "url": "wss://h/ws"}`)
	writeFile(t, dir, "users/io.json", `{"kind": "socketio", "id": "req_io", "name": "IO", "url": "https://h"}`)
	writeFile(t, dir, "users/tcp.json", `{"kind": "tcp", "id": "req_tcp", "name": "TCP", "address": "localhost:7"}`)
	writeFile(t, dir, "users/rpc.json", `{"kind": "grpc", "id": "req_rpc", "name": "RPC", "address": "localhost:50051", "proto": {"inherit": true}}`)
	writeFile(t, dir, "users/mqtt.json", `{"kind": "mqtt", "id": "req_mqtt", "name": "MQTT", "password": "mqtt-secret"}`)
	writeFile(t, dir, "users/broken.json", `{"kind": "http", "id": `)
	writeFile(t, dir, "users/fixtures/avatar.png", "PNG")
	writeFile(t, dir, "users/notes.txt", "notes")
	writeFile(t, dir, "users/admins/_folder.json", `{"kind": "folder", "id": "fld_admins", "name": "Admins", "auth": {"type": "inherit"}}`)
	writeFile(t, dir, "users/admins/events.json", `{"kind": "sse", "id": "req_sse", "name": "Events", "url": "https://h/events",
  "auth": {"type": "bearer", "token": "hand-sse-token"}}`)
	writeFile(t, dir, "users/admins/gql.json", `{"kind": "graphql", "id": "req_gql", "name": "Me", "url": "/graphql", "query": "{ me { id } }"}`)
	return dir
}

func exportUsers(t *testing.T, dir string) *projectzip.ExportResult {
	t.Helper()
	tree, _ := collection.Load(dir)
	res, err := projectzip.Export(tree.Node("fld_users"), projectzip.ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// zipContents returns every file of the zip by name, decompressed.
func zipContents(t *testing.T, data []byte) map[string]string {
	t.Helper()
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, f := range r.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		out[f.Name] = string(b)
	}
	return out
}

func TestExportHoldsNoSecrets(t *testing.T) {
	t.Parallel()
	res := exportUsers(t, project(t))
	files := zipContents(t, res.Data)
	var names []string
	for name, content := range files {
		names = append(names, name)
		for _, s := range append(handSecrets, "mqtt-secret") {
			if strings.Contains(content, s) {
				t.Errorf("%s holds the secret %q:\n%s", name, s, content)
			}
		}
	}
	slices.Sort(names)
	want := []string{"users/", "users/_export.json", "users/_folder.json", "users/admins/", "users/admins/_folder.json",
		"users/admins/events.json", "users/admins/gql.json", "users/get.json", "users/io.json", "users/ref.json",
		"users/rpc.json", "users/tcp.json", "users/ws.json"}
	if !slices.Equal(names, want) {
		t.Errorf("entries = %q\nwant %q", names, want)
	}
	if !strings.Contains(files["users/_folder.json"], "not-a-secret") || !strings.Contains(files["users/ref.json"], "{{token}}") {
		t.Errorf("shared values are missing:\n%s\n%s", files["users/_folder.json"], files["users/ref.json"])
	}
	if res.Name != "Users" || res.Folders != 2 || res.Requests != 8 || res.Skipped != 2 || res.SecretsRemoved != 4 {
		t.Errorf("result = %+v", res)
	}
	var codes []string
	for _, n := range res.Notes {
		codes = append(codes, n.Code)
	}
	if !slices.Contains(codes, projectzip.CodeExportSecret) || !slices.Contains(codes, projectzip.CodeExportSkipped) {
		t.Errorf("notes = %+v", res.Notes)
	}
	if !strings.Contains(files["users/_export.json"], `"format": "postsonde-folder"`) {
		t.Errorf("manifest = %s", files["users/_export.json"])
	}
}

func TestParseExport(t *testing.T) {
	t.Parallel()
	dir := project(t)
	res := exportUsers(t, dir)
	src := importer.Source{Name: "Users.zip", Data: res.Data}
	if got := (projectzip.Importer{}).Detect(src); got != importer.Sure {
		t.Errorf("Detect = %d", got)
	}
	m, err := projectzip.Importer{}.Parse(t.Context(), src)
	if err != nil {
		t.Fatal(err)
	}
	root := m.Root
	if m.Name != "Users" || root.Name != "Users" || root.Description != "All about **users**." {
		t.Errorf("model = %+v", m)
	}
	if len(root.Variables) != 2 || root.Variables[0].Value != "" || !root.Variables[0].Secret || root.Variables[1].Value != "not-a-secret" {
		t.Errorf("variables = %+v", root.Variables)
	}
	if root.Auth.Type != projectfile.AuthAPIKey || root.Auth.Value != "" || root.Auth.Key != "X-Api-Key" {
		t.Errorf("auth = %+v", root.Auth)
	}
	if len(root.GRPC.Proto.Files) != 1 || root.Server == nil || root.Server.Port != 8080 || string(root.Unknown["x-note"]) == "" {
		t.Errorf("other settings = %+v %+v %v", root.GRPC, root.Server, root.Unknown)
	}
	var order []string
	for _, it := range root.Items {
		switch {
		case it.Folder != nil:
			order = append(order, "folder:"+it.Folder.Name)
		case it.Request != nil:
			order = append(order, it.Request.Name)
		default:
			order = append(order, "skipped:"+it.Skipped.Name)
		}
	}
	want := []string{"Get user", "folder:Admins", "Socket", "IO", "ref", "RPC", "TCP"}
	if !slices.Equal(order, want) {
		t.Errorf("items = %q\nwant %q", order, want)
	}
	get := root.Items[0].Request.File.(*projectfile.HTTPRequest)
	if get.Auth.Token != "" || !slices.Contains(get.Auth.LocalSecrets, "token") || string(get.Unknown["x-custom"]) != "1" {
		t.Errorf("get = %+v", get)
	}
	admins := root.Items[1].Folder
	if len(admins.Items) != 2 || admins.Items[0].Request.Name != "Events" || admins.Items[1].Request.Name != "Me" {
		t.Errorf("admins = %+v", admins)
	}
	if sse := admins.Items[0].Request.File.(*projectfile.SSERequest); sse.Auth.Token != "" {
		t.Errorf("sse = %+v", sse)
	}
	if r := importer.Preview(m); r.Counts.Folders != 2 || r.Counts.Requests != 8 {
		t.Errorf("preview counts = %+v", r.Counts)
	}
}

// entrySpec is an entry of a hand-made zip.
type entrySpec struct {
	name    string
	body    string
	mode    fs.FileMode
	flags   uint16
	method  uint16
	declare uint64 // the uncompressed size the header claims, when set
	raw     []byte // the compressed bytes, written as they are
}

const okManifest = `{"format": "postsonde-folder", "formatVersion": 1, "schemaVersion": 1, "name": "Users"}`
const okFolder = `{"kind": "folder", "id": "fld_u", "name": "Users", "auth": {"type": "inherit"}}`

func makeZip(t *testing.T, entries ...entrySpec) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: e.method, Flags: e.flags}
		mode := e.mode
		if mode == 0 {
			mode = 0o644
		}
		h.SetMode(mode)
		if e.raw != nil {
			if h.Method == zip.Store {
				h.Method = zip.Deflate
			}
			h.CompressedSize64 = uint64(len(e.raw))
			h.UncompressedSize64 = e.declare
			w, err := zw.CreateRaw(h)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write(e.raw); err != nil {
				t.Fatal(err)
			}
			continue
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, e.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func base(extra ...entrySpec) []entrySpec {
	return append([]entrySpec{
		{name: "users/_export.json", body: okManifest},
		{name: "users/_folder.json", body: okFolder},
	}, extra...)
}

func deflated(t *testing.T, size int) []byte {
	t.Helper()
	var buf bytes.Buffer
	fw, err := flate.NewWriter(&buf, flate.BestCompression)
	if err != nil {
		t.Fatal(err)
	}
	chunk := make([]byte, 1<<20)
	for size > 0 {
		n := min(size, len(chunk))
		if _, err := fw.Write(chunk[:n]); err != nil {
			t.Fatal(err)
		}
		size -= n
	}
	if err := fw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestParseRefusesUnsafeZips(t *testing.T) {
	t.Parallel()
	request := `{"kind": "http", "id": "req_a", "name": "A", "method": "GET", "url": "/"}`
	many := base()
	for i := range projectzip.MaxEntries {
		many = append(many, entrySpec{name: "users/r" + strconv.Itoa(i) + ".txt"})
	}
	big := deflated(t, projectzip.MaxEntrySize+1)
	tests := []struct {
		name    string
		entries []entrySpec
		code    string
	}{
		{"zip slip", base(entrySpec{name: "users/../evil.json", body: request}), projectzip.CodeUnsafeEntry},
		{"parent in the middle", base(entrySpec{name: "users/a/../../evil.json", body: request}), projectzip.CodeUnsafeEntry},
		{"absolute path", base(entrySpec{name: "/etc/passwd", body: request}), projectzip.CodeUnsafeEntry},
		{"drive letter", base(entrySpec{name: "C:/evil.json", body: request}), projectzip.CodeUnsafeEntry},
		{"backslash", base(entrySpec{name: `users\..\evil.json`, body: request}), projectzip.CodeUnsafeEntry},
		{"dot element", base(entrySpec{name: "users/./a.json", body: request}), projectzip.CodeUnsafeEntry},
		{"empty element", base(entrySpec{name: "users//a.json", body: request}), projectzip.CodeUnsafeEntry},
		{"control character", base(entrySpec{name: "users/a\x01.json", body: request}), projectzip.CodeUnsafeEntry},
		{"symlink", base(entrySpec{name: "users/link.json", body: "/etc/passwd", mode: fs.ModeSymlink | 0o777}), projectzip.CodeUnsafeEntry},
		{"named pipe", base(entrySpec{name: "users/pipe.json", mode: fs.ModeNamedPipe | 0o644}), projectzip.CodeUnsafeEntry},
		{"device", base(entrySpec{name: "users/dev.json", mode: fs.ModeDevice | 0o644}), projectzip.CodeUnsafeEntry},
		{"encrypted", base(entrySpec{name: "users/a.json", body: request, flags: 0x1}), projectzip.CodeUnsafeEntry},
		{"duplicate", base(entrySpec{name: "users/a.json", body: request}, entrySpec{name: "users/a.json", body: request}), projectzip.CodeUnsafeEntry},
		{"file and directory", base(entrySpec{name: "users/a", body: "x"}, entrySpec{name: "users/a/b.json", body: request}), projectzip.CodeUnsafeEntry},
		{"too deep", base(entrySpec{name: "users/" + strings.Repeat("d/", collection.MaxDepth+1) + "a.json", body: request}), projectzip.CodeUnsafeEntry},
		{"too many entries", many, projectzip.CodeTooLarge},
		{"declared too large", base(entrySpec{name: "users/big.json", raw: big, declare: projectzip.MaxEntrySize + 1}), projectzip.CodeTooLarge},
		// archive/zip stops at the declared size and reports the zip damaged.
		{"bomb with a lying size", base(entrySpec{name: "users/bomb.json", raw: big, declare: 100}), importer.CodeInvalidFile},
		{"declared total too large", base(func() []entrySpec {
			var out []entrySpec
			for i := range 9 {
				out = append(out, entrySpec{name: "users/b" + strconv.Itoa(i) + ".json", raw: []byte{3, 0}, declare: 30 << 20})
			}
			return out
		}()...), projectzip.CodeTooLarge},
		{"newer format", []entrySpec{
			{name: "users/_export.json", body: `{"format": "postsonde-folder", "formatVersion": 2, "schemaVersion": 1}`},
			{name: "users/_folder.json", body: okFolder},
		}, projectzip.CodeNewerFormat},
		{"newer schema", []entrySpec{
			{name: "users/_export.json", body: `{"format": "postsonde-folder", "formatVersion": 1, "schemaVersion": 99}`},
			{name: "users/_folder.json", body: okFolder},
		}, projectzip.CodeNewerFormat},
		{"no manifest", []entrySpec{{name: "users/_folder.json", body: okFolder}}, importer.CodeInvalidFile},
		{"other manifest", []entrySpec{{name: "users/_export.json", body: `{"format": "other"}`}, {name: "users/_folder.json", body: okFolder}}, importer.CodeInvalidFile},
		{"no folder file", []entrySpec{{name: "users/_export.json", body: okManifest}}, importer.CodeInvalidFile},
		{"two top-level entries", base(entrySpec{name: "other/a.json", body: request}), importer.CodeInvalidFile},
		{"file at the top", base(entrySpec{name: "a.json", body: request}), importer.CodeInvalidFile},
		{"unknown method", base(entrySpec{name: "users/a.json", raw: []byte(request), declare: uint64(len(request)), method: 99}), importer.CodeInvalidFile},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			data := makeZip(t, tt.entries...)
			m, err := projectzip.Importer{}.Parse(t.Context(), importer.Source{Name: "x.zip", Data: data})
			var e *errs.Error
			if !errors.As(err, &e) || e.Code != tt.code {
				t.Fatalf("Parse = %v, %v; want code %s", m, err, tt.code)
			}
			if strings.Contains(e.Error(), "passwd") && tt.code != projectzip.CodeUnsafeEntry {
				t.Errorf("message = %s", e.Error())
			}
		})
	}
}

func TestParseNotAZip(t *testing.T) {
	t.Parallel()
	for _, data := range []string{"", "{}", "PK\x03\x04garbage"} {
		src := importer.Source{Name: "a.zip", Data: []byte(data)}
		if _, err := (projectzip.Importer{}).Parse(t.Context(), src); err == nil {
			t.Errorf("Parse(%q) did not fail", data)
		}
	}
	if got := (projectzip.Importer{}).Detect(importer.Source{Name: "a.json", Data: []byte("{}")}); got != importer.No {
		t.Errorf("Detect(json) = %d", got)
	}
	plain := makeZip(t, entrySpec{name: "readme.txt", body: "hi"})
	if got := (projectzip.Importer{}).Detect(importer.Source{Name: "a.zip", Data: plain}); got != importer.Maybe {
		t.Errorf("Detect(other zip) = %d", got)
	}
}

// Files a hand-edited zip may hold that are not imported: they are
// skipped items or ignored, and the rest is imported.
func TestParseSkipsWhatItCannotImport(t *testing.T) {
	t.Parallel()
	data := makeZip(t, base(
		entrySpec{name: "users/a.json", body: `{"kind": "http", "id": "req_a", "name": "A", "method": "GET", "url": "/"}`},
		entrySpec{name: "users/newer.json", body: `{"schemaVersion": 7, "kind": "http", "id": "req_n", "name": "N", "method": "GET", "url": "/"}`},
		entrySpec{name: "users/mqtt.json", body: `{"kind": "mqtt", "id": "req_m", "name": "M"}`},
		entrySpec{name: "users/broken.json", body: `{"kind": "http", "id": `},
		entrySpec{name: "users/env.json", body: `{"kind": "environment", "id": "env_x", "name": "X"}`},
		entrySpec{name: "users/plain.json", body: `{"hello": "world"}`},
		entrySpec{name: "users/readme.md", body: "# hi"},
		entrySpec{name: "users/fixtures/a.png", body: "PNG"},
		entrySpec{name: "users/sub/_folder.json", body: `{"schemaVersion": 5, "kind": "folder", "id": "fld_s", "name": "Sub", "auth": {"type": "basic"}}`},
		entrySpec{name: "users/sub/b.json", body: `{"kind": "tcp", "id": "req_b", "name": "B", "address": "h:1"}`},
	)...)
	m, err := projectzip.Importer{}.Parse(t.Context(), importer.Source{Name: "x.zip", Data: data})
	if err != nil {
		t.Fatal(err)
	}
	r := importer.Preview(m)
	if r.Counts.Requests != 2 || r.Counts.Folders != 2 || r.Counts.Skipped != 4 {
		t.Errorf("counts = %+v", r.Counts)
	}
	sub := m.Root.Items[len(m.Root.Items)-1].Folder
	if sub == nil || sub.Name != "Sub" || sub.Auth.Type != projectfile.AuthInherit || len(sub.Diagnostics) != 1 || sub.Diagnostics[0].Code != projectzip.CodeSettingsSkipped {
		t.Errorf("sub = %+v", sub)
	}
	if len(m.Diagnostics) != 1 || m.Diagnostics[0].Code != projectzip.CodeIgnored || !strings.Contains(m.Diagnostics[0].Message, "3 files") {
		t.Errorf("diagnostics = %+v", m.Diagnostics)
	}
}

func TestFileName(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{"Users": "Users.zip", "a/b:c": "a-b-c.zip", "  ": "folder.zip", "..": "folder.zip"} {
		if got := projectzip.FileName(in); got != want {
			t.Errorf("FileName(%q) = %q, want %q", in, got, want)
		}
	}
}
