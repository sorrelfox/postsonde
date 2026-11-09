package request

import (
	"reflect"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// HTTP-09: BuildHTTP records where each header comes from, and which
// automatic headers are overridden by a row or turned off.
func TestBuildHTTPHeaderSources(t *testing.T) {
	t.Parallel()
	f := projectfile.HTTPRequest{
		Method:  "POST",
		URL:     "https://example.com/",
		Headers: []projectfile.KeyValue{{Name: "X-Own", Value: "1"}},
		Body:    projectfile.Body{Mode: projectfile.BodyRaw, Type: projectfile.RawJSON, Text: "{}"},
	}
	auth := projectfile.Auth{Type: projectfile.AuthBearer, Token: "tok"}
	folders := []FolderHeaders{{FolderID: "f", Rows: []projectfile.KeyValue{{Name: "X-Folder", Value: "2"}}}}
	b, err := BuildHTTP(f, auth, HTTPOptions{FolderHeaders: folders})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, h := range b.Request.Headers {
		names = append(names, h.Name)
	}
	if want := []string{"X-Folder", "X-Own", "Authorization"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("headers = %v, want %v", names, want)
	}
	if want := []string{HeaderFromFolder, HeaderFromRequest, HeaderFromAuth}; !reflect.DeepEqual(b.HeaderSources, want) {
		t.Errorf("sources = %v, want %v", b.HeaderSources, want)
	}
	if len(b.Suppressed) != 0 {
		t.Errorf("suppressed = %+v", b.Suppressed)
	}
	if b.Request.Body.ContentType() != "application/json" {
		t.Errorf("content type = %q", b.Request.Body.ContentType())
	}

	// Rows override the auth header and the body's type.
	f.Headers = append(f.Headers, projectfile.KeyValue{Name: "authorization", Value: "Custom x"}, projectfile.KeyValue{Name: "content-type", Value: "text/plain"})
	b, err = BuildHTTP(f, auth, HTTPOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := []SuppressedHeader{
		{Name: "Authorization", Value: "Bearer tok", Source: HeaderFromAuth, Reason: SuppressedOverridden},
		{Name: "Content-Type", Value: "application/json", Source: HeaderFromBody, Reason: SuppressedOverridden},
	}
	if !reflect.DeepEqual(b.Suppressed, want) {
		t.Errorf("suppressed = %+v, want %+v", b.Suppressed, want)
	}
}

func TestBuildHTTPOmitHeaders(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeFile(t, dir, "a.png", []byte("png"))
	bodies := map[string]projectfile.Body{
		"raw":        {Mode: projectfile.BodyRaw, Type: projectfile.RawJSON, Text: "{}"},
		"urlencoded": {Mode: projectfile.BodyURLEncoded, Fields: []projectfile.KeyValue{{Name: "a", Value: "1"}}},
		"multipart":  {Mode: projectfile.BodyMultipart, Parts: []projectfile.Part{{Name: "a", Value: "1"}}},
		"file":       {Mode: projectfile.BodyFile, Path: "a.png"},
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := projectfile.HTTPRequest{
				Method:   "POST",
				URL:      "https://example.com/",
				Body:     body,
				Settings: projectfile.RequestSettings{OmitHeaders: []string{"content-type", "X-API-KEY"}},
			}
			auth := projectfile.Auth{Type: projectfile.AuthAPIKey, Key: "X-Api-Key", Value: "k"}
			b, err := BuildHTTP(f, auth, HTTPOptions{Root: dir})
			if err != nil {
				t.Fatal(err)
			}
			if ct := b.Request.Body.ContentType(); ct != "" {
				t.Errorf("content type = %q, want none", ct)
			}
			if len(b.Request.Headers) != 0 {
				t.Errorf("headers = %v, want none", b.Request.Headers)
			}
			if len(b.Suppressed) != 2 || b.Suppressed[0].Name != "X-Api-Key" || b.Suppressed[0].Reason != SuppressedOmitted ||
				b.Suppressed[1].Name != "Content-Type" || b.Suppressed[1].Reason != SuppressedOmitted || b.Suppressed[1].Value == "" {
				t.Errorf("suppressed = %+v", b.Suppressed)
			}
			// The bytes are unchanged.
			full, err := BuildHTTP(projectfile.HTTPRequest{Method: "POST", URL: "https://example.com/", Body: body}, auth, HTTPOptions{Root: dir})
			if err != nil {
				t.Fatal(err)
			}
			got, want := bodyText(t, b.Request.Body), bodyText(t, full.Request.Body)
			if name == "multipart" {
				// Each build draws its own boundary.
				got = strings.ReplaceAll(got, b.Request.Body.(*MultipartBody).Boundary(), "B")
				want = strings.ReplaceAll(want, full.Request.Body.(*MultipartBody).Boundary(), "B")
			}
			if got != want {
				t.Errorf("body = %q, want %q", got, want)
			}
		})
	}
}

func TestBuildHTTPOmitDigest(t *testing.T) {
	t.Parallel()
	f := projectfile.HTTPRequest{Method: "GET", URL: "https://example.com/", Settings: projectfile.RequestSettings{OmitHeaders: []string{"Authorization"}}}
	b, err := BuildHTTP(f, projectfile.Auth{Type: projectfile.AuthDigest, Username: "u", Password: "p"}, HTTPOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if b.Request.Digest != nil {
		t.Error("Digest credentials kept")
	}
	want := []SuppressedHeader{{Name: "Authorization", Value: DigestPlaceholder, Source: HeaderFromAuth, Reason: SuppressedOmitted}}
	if !reflect.DeepEqual(b.Suppressed, want) {
		t.Errorf("suppressed = %+v", b.Suppressed)
	}
	var _ httpclient.Body = untypedBody{}
}
