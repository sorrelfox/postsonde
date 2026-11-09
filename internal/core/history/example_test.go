package history

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// exampleEntry is an entry recorded without masking: a secret in every
// part a response example keeps.
func exampleEntry() Entry {
	return Entry{
		Summary: Summary{ID: "h_1", Kind: KindHTTP, RequestID: "req_1", Method: "POST", URL: "https://api.test/u?key=s3cr3t-value",
			Outcome: OutcomeOK, StatusCode: 201, Status: "201 Created"},
		Request: RequestSnapshot{
			Method: "POST", URL: "https://api.test/u?key=s3cr3t-value", Proto: "HTTP/2.0",
			Headers: []Header{
				{Name: ":authority", Value: "api.test"},
				{Name: "content-type", Value: "application/json; charset=utf-8"},
				{Name: "authorization", Value: "Bearer tok-123"},
				{Name: "x-api-key", Value: "s3cr3t-value"},
			},
			Body: Body{Text: `{"password":"s3cr3t-value"}`, Size: 27},
		},
		Response: &ResponseSnapshot{
			Proto: "HTTP/2.0", StatusCode: 201, Status: "201 Created",
			Headers: []Header{{Name: "Content-Type", Value: "application/json"}, {Name: "X-Echo", Value: "s3cr3t-value"}},
			Body:    Body{Text: `{"echo":"s3cr3t-value"}`, Size: 23},
		},
	}
}

func TestExampleMasksSecrets(t *testing.T) {
	t.Parallel()
	m := httpclient.NewSecretMasker([]string{"s3cr3t-value"})
	at := time.Date(2026, 9, 23, 10, 15, 2, 0, time.FixedZone("CEST", 2*3600))
	ex, masked, err := Example(exampleEntry(), "", at, m)
	if err != nil {
		t.Fatal(err)
	}
	if !masked {
		t.Error("masked = false, want true")
	}
	data, err := projectfile.Marshal(&projectfile.HTTPRequest{ID: "req_1", Method: "POST", ResponseExamples: []projectfile.ResponseExample{ex}})
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"s3cr3t-value", "tok-123"} {
		if strings.Contains(string(data), secret) {
			t.Errorf("the example holds %q:\n%s", secret, data)
		}
	}
	tests := []struct {
		name, got, want string
	}{
		{"name", ex.Name, "201 Created"},
		{"savedAt", ex.SavedAt, "2026-09-23T08:15:02Z"},
		{"statusText", ex.StatusText, "Created"},
		{"url", ex.Request.URL, "https://api.test/u?key=" + httpclient.Mask},
		{"authorization", ex.Request.Headers[1].Value, "Bearer " + httpclient.Mask},
		{"api key", ex.Request.Headers[2].Value, httpclient.Mask},
		{"request body", ex.Request.Body.Text, `{"password":"` + httpclient.Mask + `"}`},
		{"request body type", ex.Request.Body.Type, projectfile.RawJSON},
		{"response header", ex.Headers[1].Value, httpclient.Mask},
		{"response body", ex.Body, `{"echo":"` + httpclient.Mask + `"}`},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s = %q, want %q", tt.name, tt.got, tt.want)
		}
	}
	if len(ex.Request.Headers) != 3 {
		t.Errorf("request headers = %+v, want the pseudo-header field left out", ex.Request.Headers)
	}
	if ex.StatusCode != 201 || ex.BodyClipped || ex.BodyEncoding != "" {
		t.Errorf("status %d, clipped %v, encoding %q", ex.StatusCode, ex.BodyClipped, ex.BodyEncoding)
	}
}

func TestExampleBodies(t *testing.T) {
	t.Parallel()
	binary := []byte{0xff, 0x00, 'o', 'k', 0xfe}
	tests := []struct {
		name     string
		body     Body
		wantBody string
		wantEnc  string
		clipped  bool
		size     int64
		masked   bool
	}{
		{"text", Body{Text: "hello", Size: 5}, "hello", "", false, 0, false},
		{"clipped", Body{Text: "hel", Size: 5000, Clipped: true}, "hel", "", true, 5000, false},
		{"binary", Body{Text: base64.StdEncoding.EncodeToString(binary), Base64: true, Size: 5}, base64.StdEncoding.EncodeToString(binary), projectfile.ExampleBodyBase64, false, 0, false},
		{"binary secret", Body{Text: base64.StdEncoding.EncodeToString(append([]byte{0xff}, "sk"...)), Base64: true, Size: 3},
			base64.StdEncoding.EncodeToString(append([]byte{0xff}, httpclient.Mask...)), projectfile.ExampleBodyBase64, false, 0, true},
	}
	m := httpclient.NewSecretMasker([]string{"sk"})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := Entry{Summary: Summary{ID: "h_2"}, Response: &ResponseSnapshot{StatusCode: 200, Status: "200 OK", Body: tt.body}}
			ex, masked, err := Example(e, "  Mine ", time.Time{}, m)
			if err != nil {
				t.Fatal(err)
			}
			if ex.Name != "Mine" || ex.SavedAt != "" {
				t.Errorf("name %q, savedAt %q", ex.Name, ex.SavedAt)
			}
			if ex.Body != tt.wantBody || ex.BodyEncoding != tt.wantEnc || ex.BodyClipped != tt.clipped || ex.BodySize != tt.size || masked != tt.masked {
				t.Errorf("got body %q %q clipped %v size %d masked %v", ex.Body, ex.BodyEncoding, ex.BodyClipped, ex.BodySize, masked)
			}
			if ex.Request == nil || ex.Request.Body.Mode != "" {
				t.Errorf("request = %+v, want one without a body", ex.Request)
			}
		})
	}
}

func TestExampleWithoutResponse(t *testing.T) {
	t.Parallel()
	_, _, err := Example(Entry{Summary: Summary{ID: "h_3", Outcome: OutcomeError}}, "", time.Now(), nil)
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != CodeNoResponse || errs.KindOf(err) != errs.InvalidArgument {
		t.Fatalf("err = %v, want %s", err, CodeNoResponse)
	}
}

func TestStatusText(t *testing.T) {
	t.Parallel()
	tests := []struct {
		code   int
		status string
		want   string
	}{
		{200, "200 OK", "OK"},
		{418, "418 I'm a teapot", "I'm a teapot"},
		{299, "299", ""},
		{200, "OK", "OK"},
	}
	for _, tt := range tests {
		if got := StatusText(tt.code, tt.status); got != tt.want {
			t.Errorf("StatusText(%d, %q) = %q, want %q", tt.code, tt.status, got, tt.want)
		}
	}
}
