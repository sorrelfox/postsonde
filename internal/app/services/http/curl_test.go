package http

import (
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/cookies"
	"github.com/sorrelfox/postsonde/internal/core/curl"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

func TestCurlCommand(t *testing.T) {
	t.Parallel()
	src := httpSource("https://api.test/items/{{id}}?k={{apiKey}}")
	src.Request.Method = "POST"
	src.Request.Headers = []projectfile.KeyValue{{Name: "X-Trace", Value: "{{missing}}"}, {Name: "X-Folder", Value: "{{plain}}"}}
	src.Request.Body = projectfile.Body{Mode: projectfile.BodyRaw, Type: projectfile.RawJSON, Text: `{"key":"{{apiKey}}"}`}
	src.Request.Settings = projectfile.RequestSettings{HTTPVersion: projectfile.HTTPVersion1}
	src.Auth = projectfile.Auth{Type: projectfile.AuthBearer, Token: "b3arer-tok"}
	src.FolderVariables = [][]projectfile.Variable{{
		{Name: "apiKey", Value: "s3cret-key", Secret: true},
		{Name: "plain", Value: "visible"},
		{Name: "id", Value: "7"},
	}}
	jar := cookies.New(cookies.Options{})
	if _, rejected := jar.SetCookieLines(&url.URL{Scheme: "https", Host: "api.test", Path: "/"}, []string{"sid=abc; Path=/"}); len(rejected) > 0 {
		t.Fatalf("rejected %v", rejected)
	}
	f := newFixture(t, src, fixedJar{jar})

	masked, err := f.svc.CurlCommand(t.Context(), CurlInputDTO{RequestID: "req_1"})
	if err != nil {
		t.Fatal(err)
	}
	if !masked.Masked {
		t.Error("Masked is not set")
	}
	if !slices.Equal(masked.Unresolved, []string{"missing"}) {
		t.Errorf("Unresolved = %v", masked.Unresolved)
	}
	for _, secret := range []string{"s3cret-key", "b3arer-tok"} {
		if strings.Contains(masked.Command, secret) {
			t.Errorf("masked command holds %q:\n%s", secret, masked.Command)
		}
	}
	for _, want := range []string{
		"'https://api.test/items/7?k=" + httpclient.Mask + "'",
		"-H 'Authorization: Bearer " + httpclient.Mask + "'",
		"-H 'X-Trace: {{missing}}'",
		"-H 'X-Folder: visible'",
		"-b sid=abc",
		"--http1.1",
	} {
		if !strings.Contains(masked.Command, want) {
			t.Errorf("masked command lacks %s:\n%s", want, masked.Command)
		}
	}

	full, err := f.svc.CurlCommand(t.Context(), CurlInputDTO{RequestID: "req_1", IncludeSecrets: true})
	if err != nil {
		t.Fatal(err)
	}
	if full.Masked {
		t.Error("Masked is set with IncludeSecrets")
	}
	for _, want := range []string{"k=s3cret-key", "Bearer b3arer-tok", `"key":"s3cret-key"`} {
		if !strings.Contains(full.Command, want) {
			t.Errorf("command lacks %s:\n%s", want, full.Command)
		}
	}
	// The command reads back as the same request.
	imp := curl.Import(full.Command)
	if imp.Request.Method != "POST" || imp.Request.Auth.Type != projectfile.AuthInherit || !strings.HasPrefix(imp.Request.URL, "https://api.test/items/7?k=s3cret-key") {
		t.Errorf("imported %+v", imp.Request)
	}

	// Cookies off in the settings: no jar cookies.
	f.src.Request.Settings.SendCookies = new(bool)
	off, err := f.svc.CurlCommand(t.Context(), CurlInputDTO{RequestID: "req_1"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(off.Command, "sid=abc") {
		t.Errorf("jar cookies copied with sendCookies false:\n%s", off.Command)
	}

	_, err = f.svc.CurlCommand(t.Context(), CurlInputDTO{RequestID: "nope"})
	if errs.KindOf(err) != errs.NotFound {
		t.Errorf("unknown request: %v", err)
	}
}
