package requestsecrets

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

func TestSplitMergeVariables(t *testing.T) {
	in := []projectfile.Variable{
		{Name: "host", Value: "api.example"},
		{Name: "key", Value: "k3y", Secret: true},
		{Name: "ref", Value: "{{other}}", Secret: true},
		{Name: "empty", Secret: true},
		{Name: "off", Value: "0ff", Secret: true, Disabled: true},
	}
	file, values := SplitVariables(in)
	want := []projectfile.Variable{
		{Name: "host", Value: "api.example"},
		{Name: "key", Secret: true},
		{Name: "ref", Secret: true},
		{Name: "empty", Secret: true},
		{Name: "off", Secret: true, Disabled: true},
	}
	if !reflect.DeepEqual(file, want) {
		t.Errorf("file = %+v", file)
	}
	if !reflect.DeepEqual(values, map[string]string{"var.key": "k3y", "var.ref": "{{other}}", "var.off": "0ff"}) {
		t.Errorf("values = %v", values)
	}
	if in[1].Value != "k3y" {
		t.Error("SplitVariables changed its input")
	}
	if got := MergeVariables(file, values); !reflect.DeepEqual(got, in) {
		t.Errorf("merged = %+v", got)
	}

	// Nothing secret: the same slice, no values.
	plain := []projectfile.Variable{{Name: "a", Value: "1"}}
	if f, v := SplitVariables(plain); &f[0] != &plain[0] || v != nil {
		t.Errorf("plain split = %v, %v", f, v)
	}

	// A value typed into the file by hand is kept unless there is a local
	// one, which wins.
	hand := []projectfile.Variable{{Name: "a", Value: "hand", Secret: true}, {Name: "b", Value: "hand", Secret: true}}
	got := MergeVariables(hand, map[string]string{"var.b": "local"})
	if got[0].Value != "hand" || got[1].Value != "local" {
		t.Errorf("hand merged = %+v", got)
	}
}

func TestSplitMergeFile(t *testing.T) {
	folder := &projectfile.Folder{
		ID:        "fld_a",
		Auth:      projectfile.Auth{Type: projectfile.AuthBearer, Token: "t0ken"},
		Variables: []projectfile.Variable{{Name: "v", Value: "s3cret", Secret: true}},
	}
	out, values := SplitFile(folder)
	f := out.(*projectfile.Folder)
	if f.Auth.Token != "" || !f.Auth.SecretIsLocal() || f.Variables[0].Value != "" {
		t.Errorf("split folder = %+v", f)
	}
	if !reflect.DeepEqual(values, map[string]string{"auth.token": "t0ken", "var.v": "s3cret"}) {
		t.Errorf("values = %v", values)
	}
	if folder.Auth.Token != "t0ken" || folder.Variables[0].Value != "s3cret" {
		t.Error("SplitFile changed its input")
	}
	back := MergeFile(f, values).(*projectfile.Folder)
	if back.Auth.Token != "t0ken" || back.Variables[0].Value != "s3cret" {
		t.Errorf("merged = %+v", back)
	}
	if !NeedsLocal(f) || NeedsLocal(&projectfile.Folder{}) {
		t.Error("NeedsLocal")
	}

	ws := &projectfile.Workspace{ID: "ws_a", Variables: []projectfile.Variable{{Name: "g", Value: "gl0bal", Secret: true}}}
	wout, wvalues := SplitFile(ws)
	if wout.(*projectfile.Workspace).Variables[0].Value != "" || wvalues["var.g"] != "gl0bal" {
		t.Errorf("workspace = %+v, %v", wout, wvalues)
	}

	gql := &projectfile.GraphQLRequest{ID: "req_g", Auth: projectfile.Auth{Type: projectfile.AuthDigest, Password: "pw"}}
	gout, gvalues := SplitFile(gql)
	if gout.(*projectfile.GraphQLRequest).Auth.Password != "" || gvalues["auth.password"] != "pw" {
		t.Errorf("graphql = %+v, %v", gout, gvalues)
	}

	tcp := &projectfile.TCPRequest{ID: "req_t"}
	if out, v := SplitFile(tcp); out != tcp || v != nil {
		t.Error("a file without secrets changed")
	}

	// The auth of the handshakes (AUTH-31) is split like an HTTP
	// request's.
	for _, f := range []projectfile.File{
		&projectfile.WebSocketRequest{ID: "req_w", Auth: projectfile.Auth{Type: projectfile.AuthBearer, Token: "wt"}},
		&projectfile.SocketIORequest{ID: "req_s", Auth: projectfile.Auth{Type: projectfile.AuthBearer, Token: "wt"}},
		&projectfile.GRPCRequest{ID: "req_r", Auth: projectfile.Auth{Type: projectfile.AuthBearer, Token: "wt"}},
	} {
		out, v := SplitFile(f)
		if v["auth.token"] != "wt" || !NeedsLocal(out) {
			t.Errorf("%T: values = %v", f, v)
		}
		if data, err := projectfile.Marshal(out); err != nil || strings.Contains(string(data), "wt") {
			t.Errorf("%T: file = %s, %v", f, data, err)
		}
		var token string
		switch r := MergeFile(out, v).(type) {
		case *projectfile.WebSocketRequest:
			token = r.Auth.Token
		case *projectfile.SocketIORequest:
			token = r.Auth.Token
		case *projectfile.GRPCRequest:
			token = r.Auth.Token
		}
		if token != "wt" {
			t.Errorf("%T: merged token = %q", f, token)
		}
	}
}

// MoveOut moves plaintext secrets to the side file before the file is
// written, never dropping a value in use.
func TestMoveOut(t *testing.T) {
	t.Parallel()
	s := New(t.TempDir())
	if err := s.Write("fld_a", map[string]string{"var.kept": "local-kept", "auth.token": "old-token", "other": "x"}); err != nil {
		t.Fatal(err)
	}
	f := &projectfile.Folder{
		ID:   "fld_a",
		Auth: projectfile.Auth{Type: projectfile.AuthBearer, Token: "hand-token"},
		Variables: []projectfile.Variable{
			{Name: "kept", Value: "hand-kept", Secret: true},
			{Name: "moved", Value: "hand-moved", Secret: true},
		},
	}
	out, err := s.MoveOut("fld_a", f)
	if err != nil {
		t.Fatal(err)
	}
	data, err := projectfile.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "hand-") {
		t.Errorf("written file holds a secret:\n%s", data)
	}
	got, err := s.Read("fld_a")
	if err != nil {
		t.Fatal(err)
	}
	// The file's typed token was the one in use; the local variable value
	// was the one in use, so the file's is dropped.
	want := map[string]string{"var.kept": "local-kept", "var.moved": "hand-moved", "auth.token": "hand-token", "other": "x"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("side file = %v, want %v", got, want)
	}

	// Nothing to move: no side file is written.
	if _, err := s.MoveOut("fld_b", &projectfile.Folder{ID: "fld_b"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.Path("fld_b")); !os.IsNotExist(err) {
		t.Errorf("side file for nothing: %v", err)
	}

	// A side file that cannot be read stops the move: the caller writes
	// nothing and the secret stays where it was.
	if err := os.WriteFile(s.Path("fld_a"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MoveOut("fld_a", f); err == nil {
		t.Error("MoveOut over a broken side file succeeded")
	}
}

func TestUpdateAndCopyMerge(t *testing.T) {
	t.Parallel()
	s := New(t.TempDir())
	if err := s.Write("req_a", map[string]string{"auth.token": "a", "var.x": "from-a"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Write("req_b", map[string]string{"var.x": "b", "var.y": "only-b"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Copy("req_a", "req_b"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Read("req_b")
	if !reflect.DeepEqual(got, map[string]string{"auth.token": "a", "var.x": "from-a", "var.y": "only-b"}) {
		t.Errorf("after Copy = %v", got)
	}
	if err := s.Update("req_c", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(s.Path("req_c")), "req_c.json")); !os.IsNotExist(err) {
		t.Errorf("Update with nothing wrote a file: %v", err)
	}
}
