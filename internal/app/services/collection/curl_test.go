package collection

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/curl"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/requestsecrets"
	"github.com/sorrelfox/postsonde/internal/testutil/golden"
)

const curlCommand = `curl 'https://api.example.com/v1/orders?page=2' \
  -H 'content-type: application/json' \
  -u 'ada:s3cret' -k \
  --data-raw '{"note":"café ☕ 😀"}' --compressed`

func TestParseCurl(t *testing.T) {
	t.Parallel()
	svc := New(nil, nil, nil)
	got, err := svc.ParseCurl(t.Context(), curlCommand)
	if err != nil {
		t.Fatal(err)
	}
	out, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, "dto/curl-import", append(out, '\n'))

	if !got.OK || got.Name != "orders" || got.Request.Kind != "http" || got.Request.Method != "POST" {
		t.Errorf("import = ok %v name %q kind %q method %q", got.OK, got.Name, got.Request.Kind, got.Request.Method)
	}

	bad, err := svc.ParseCurl(t.Context(), "curl -H")
	if err != nil {
		t.Fatal(err)
	}
	if bad.OK || len(bad.Diagnostics) == 0 || bad.Diagnostics[0].Severity != "error" {
		t.Errorf("bad import = %+v", bad)
	}
}

func TestCurlDiagnosticOffsetsAreUTF16(t *testing.T) {
	t.Parallel()
	text := "curl 'https://h/😀' --compressed"
	ds := curl.Import(text).Diagnostics
	got := toCurlDiagnosticDTOs(text, ds)
	if len(got) != 1 {
		t.Fatalf("diagnostics = %+v", got)
	}
	// "--compressed" starts after the emoji: 4 bytes, 2 UTF-16 units.
	byteStart := strings.Index(text, "--compressed")
	if got[0].From != byteStart-2 || got[0].To != len(text)-2 {
		t.Errorf("range = [%d,%d), want [%d,%d)", got[0].From, got[0].To, byteStart-2, len(text)-2)
	}
}

func TestImportCurl(t *testing.T) {
	t.Parallel()
	svc, _, dir, _, _ := newAutosaveService(t)
	ctx := t.Context()

	res, err := svc.ImportCurl(ctx, ImportCurlInputDTO{ParentID: "fld_users", Name: "  ", Text: curlCommand, Index: -1})
	if err != nil {
		t.Fatal(err)
	}
	var node *NodeDTO
	for i := range res.Tree.Nodes {
		if res.Tree.Nodes[i].ID == res.NodeID {
			node = &res.Tree.Nodes[i]
		}
	}
	if node == nil || node.Name != "orders" || node.Path != "users/orders.json" || node.Method != "POST" {
		t.Fatalf("node = %+v", node)
	}

	// The typed password is kept locally, not in the project.
	data := readFile(t, dir, "users/orders.json")
	if strings.Contains(data, "s3cret") || !strings.Contains(data, `"localSecrets"`) || !strings.Contains(data, `"verifyTls": false`) {
		t.Errorf("file = %s", data)
	}
	if _, err := os.Stat(requestsecrets.New(svc.manager.Current().LocalDir).Path(res.NodeID)); err != nil {
		t.Errorf("secrets side file: %v", err)
	}
	req := getRequest(t, svc, res.NodeID)
	if req.Auth.Type != "basic" || req.Auth.Username != "ada" || req.Auth.Password != "s3cret" || req.Body.Type != "json" {
		t.Errorf("request = %+v", req)
	}

	// --digest imports Digest auth, its password kept locally too.
	dg, err := svc.ImportCurl(ctx, ImportCurlInputDTO{Name: "Digest", Text: "curl --digest -u 'bob:d1gest-pw' https://h/private", Index: -1})
	if err != nil {
		t.Fatal(err)
	}
	if data := readFile(t, dir, "digest.json"); strings.Contains(data, "d1gest-pw") || !strings.Contains(data, `"type": "digest"`) {
		t.Errorf("digest file = %s", data)
	}
	if req := getRequest(t, svc, dg.NodeID); req.Auth.Type != "digest" || req.Auth.Username != "bob" || req.Auth.Password != "d1gest-pw" {
		t.Errorf("digest request auth = %+v", req.Auth)
	}

	// A named import at the top level.
	named, err := svc.ImportCurl(ctx, ImportCurlInputDTO{Name: "Health check", Text: "curl https://h/health", Index: 0})
	if err != nil {
		t.Fatal(err)
	}
	if named.Tree.RootIDs[0] != named.NodeID {
		t.Errorf("root ids = %v", named.Tree.RootIDs)
	}

	_, err = svc.ImportCurl(ctx, ImportCurlInputDTO{Text: "wget https://h/"})
	checkCode(t, err, errs.Parse, curl.CodeInvalidCommand)
	_, err = svc.ImportCurl(ctx, ImportCurlInputDTO{ParentID: "fld_nope", Text: "curl https://h/"})
	if err == nil {
		t.Error("import into a missing folder succeeded")
	}
}
