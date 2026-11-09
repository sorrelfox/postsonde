package http

import (
	nethttp "net/http"
	"net/http/httptest"
	"testing"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// TestSendFolderHeaders: the headers of the folders above a request are
// sent (COL-03), with {{variables}} rendered; a request header overrides a
// folder header of the same name, and a nested folder its parent's.
func TestSendFolderHeaders(t *testing.T) {
	t.Parallel()
	var got nethttp.Header
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		got = r.Header.Clone()
	}))
	defer srv.Close()

	src := httpSource(srv.URL + "/")
	src.Request.Headers = []projectfile.KeyValue{{Name: "x-override", Value: "request"}}
	src.FolderVariables = [][]projectfile.Variable{{{Name: "team", Value: "core"}}}
	src.FolderHeaders = []request.FolderHeaders{
		{FolderID: "fld_top", FolderName: "Top", Rows: []projectfile.KeyValue{
			{Name: "X-Team", Value: "{{team}}"},
			{Name: "X-Override", Value: "top"},
			{Name: "X-Level", Value: "top"},
		}},
		{FolderID: "fld_sub", FolderName: "Sub", Rows: []projectfile.KeyValue{{Name: "X-Level", Value: "sub"}}},
	}
	f := newFixture(t, src, nil)
	if done := f.rec.wait(t, f.send(t)); done.Status != operation.StatusOK {
		t.Fatalf("done = %+v", done)
	}
	if got.Get("X-Team") != "core" || got.Get("X-Level") != "sub" || len(got.Values("X-Level")) != 1 ||
		got.Get("X-Override") != "request" || len(got.Values("X-Override")) != 1 {
		t.Errorf("server got %v", got)
	}
}
