package request

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

func folderRows(id string, rows ...projectfile.KeyValue) FolderHeaders {
	return FolderHeaders{FolderID: id, FolderName: strings.ToUpper(id), Rows: rows}
}

func TestInheritHeaders(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		own     []httpclient.Header
		folders []FolderHeaders
		want    []httpclient.Header
	}{
		{"nothing", nil, nil, []httpclient.Header{}},
		{"no folders keeps own", []httpclient.Header{on("A", "1")}, nil, []httpclient.Header{on("A", "1")}},
		{
			"folder headers come first, top-level first",
			[]httpclient.Header{on("Own", "r")},
			[]FolderHeaders{folderRows("top", kv("Top", "t")), folderRows("sub", kv("Sub", "s"))},
			[]httpclient.Header{on("Top", "t"), on("Sub", "s"), on("Own", "r")},
		},
		{
			"a request header overrides a folder header of the same name, ignoring case",
			[]httpclient.Header{on("accept", "text/plain")},
			[]FolderHeaders{folderRows("top", kv("Accept", "application/json"), kv("X-Team", "core"))},
			[]httpclient.Header{on("X-Team", "core"), on("accept", "text/plain")},
		},
		{
			"a nearer folder overrides a farther one",
			nil,
			[]FolderHeaders{
				folderRows("top", kv("X-Env", "top"), kv("X-Top", "1")),
				folderRows("mid", kv("X-Env", "mid")),
				folderRows("leaf", kv("X-Leaf", "1")),
			},
			[]httpclient.Header{on("X-Top", "1"), on("X-Env", "mid"), on("X-Leaf", "1")},
		},
		{
			"disabled and unnamed rows override nothing",
			nil,
			[]FolderHeaders{folderRows("top", kv("X-Env", "top")), folderRows("sub", off("X-Env", "sub"), kv("", "x"))},
			[]httpclient.Header{on("X-Env", "top")},
		},
		{
			"duplicates within one folder are kept",
			nil,
			[]FolderHeaders{folderRows("top", kv("Via", "a"), kv("Via", "b"))},
			[]httpclient.Header{on("Via", "a"), on("Via", "b")},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := InheritHeaders(tt.own, tt.folders, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestInheritHeadersExpandsAndNamesTheFolder(t *testing.T) {
	t.Parallel()
	expand := buildExpand(map[string]string{"team": "core", "name": "X-Team"})
	got, err := InheritHeaders(nil, []FolderHeaders{folderRows("top", kv("{{name}}", "{{team}}"))}, expand)
	if err != nil {
		t.Fatal(err)
	}
	if want := []httpclient.Header{on("X-Team", "core")}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}

	_, err = InheritHeaders(nil, []FolderHeaders{folderRows("top", kv("Ok", "1"), kv("Bad Name", "x"))}, nil)
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != CodeInvalidHeaderName {
		t.Fatalf("err = %v", err)
	}
	if e.Details[DetailFolder] != "top" || e.Details[DetailRow] != "1" || !strings.Contains(e.Message, `folder "TOP"`) {
		t.Errorf("err = %+v", e)
	}
}

func TestBuildHTTPFolderHeaders(t *testing.T) {
	t.Parallel()
	f := projectfile.HTTPRequest{
		Method:  "GET",
		URL:     "http://localhost:8080/",
		Headers: []projectfile.KeyValue{kv("X-Env", "request")},
	}
	o := HTTPOptions{FolderHeaders: []FolderHeaders{
		folderRows("top", kv("X-Env", "top"), kv("Accept", "application/json"), kv("Authorization", "Folder")),
		folderRows("sub", kv("Accept", "application/xml")),
	}}
	b, err := BuildHTTP(f, projectfile.Auth{Type: projectfile.AuthBearer, Token: "tok"}, o)
	if err != nil {
		t.Fatal(err)
	}
	// The request's row wins, then the nearer folder; a folder's header
	// wins over the auth's, as a typed row does.
	want := []httpclient.Header{on("Authorization", "Folder"), on("Accept", "application/xml"), on("X-Env", "request")}
	if !reflect.DeepEqual(b.Request.Headers, want) {
		t.Errorf("headers = %+v", b.Request.Headers)
	}
}

func TestBuildGraphQLFolderAccept(t *testing.T) {
	t.Parallel()
	f := projectfile.GraphQLRequest{URL: "http://localhost:8080/graphql", Query: "{ a }"}
	o := HTTPOptions{FolderHeaders: []FolderHeaders{folderRows("top", kv("Accept", "application/json"))}}
	b, err := BuildGraphQL(f, projectfile.Auth{Type: projectfile.AuthNone}, o)
	if err != nil {
		t.Fatal(err)
	}
	if want := []httpclient.Header{on("Accept", "application/json")}; !reflect.DeepEqual(b.Request.Headers, want) {
		t.Errorf("headers = %+v", b.Request.Headers)
	}
}
