package http

import (
	"context"
	"errors"
	"io"
	nethttp "net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/har"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// EXP-06: a saved exchange holds the whole request as sent and the whole
// response, far past the history's 64 KiB and the view's 1 MiB, with every
// secret masked: the send's and the ones the app keeps.
func TestSaveExchange(t *testing.T) {
	t.Parallel()
	const sendSecret, storedSecret = "s3nd-SECRET-key", "st0red-SECRET-token"
	reqPad := strings.Repeat("r", 200<<10)
	respPad := strings.Repeat("x", 3<<20)
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("X-Echo-Key", r.URL.Query().Get("k"))
		// The request's secret echoed, a stored secret the send never
		// used, and a marker at the very end.
		_, _ = io.WriteString(w, "echo "+sendSecret+" "+storedSecret+" ")
		_, _ = io.WriteString(w, respPad)
		_, _ = w.Write(body[len(body)-10:])
		_, _ = io.WriteString(w, " END-OF-BODY")
	}))
	defer srv.Close()

	src := httpSource(srv.URL + "/items?k={{apiKey}}")
	src.Request.Name = "Upload: items"
	src.Request.Method = "POST"
	src.Request.Body = projectfile.Body{Mode: projectfile.BodyRaw, Type: projectfile.RawText, Text: "key={{apiKey}} " + reqPad + "REQ-TAIL"}
	src.Auth = projectfile.Auth{Type: projectfile.AuthBearer, Token: "b3arer-tok"}
	src.FolderVariables = [][]projectfile.Variable{{{Name: "apiKey", Value: sendSecret, Secret: true}}}

	out := t.TempDir()
	answer := ""
	var dialogs []SaveDialog
	pick := func(_ context.Context, d SaveDialog) (string, error) {
		dialogs = append(dialogs, d)
		return answer, nil
	}
	f := newFixture(t, src, nil, WithSaveBody(filepath.Join(t.TempDir(), "spool"), pick), WithVersion("3.1.0"),
		WithExchangeSecrets(func(context.Context) ([]string, error) { return []string{storedSecret}, nil }))
	id := f.send(t)
	f.rec.wait(t, id)
	resp := f.rec.response(t, id)
	if resp.BodyID == "" || !resp.BodyClipped {
		t.Fatalf("response: bodyId %q, clipped %v", resp.BodyID, resp.BodyClipped)
	}

	// Cancelled: nothing written.
	res, err := f.svc.SaveExchange(t.Context(), SaveExchangeInputDTO{BodyID: resp.BodyID, Name: "Upload: items"})
	if err != nil || res.Saved || !res.Masked {
		t.Fatalf("cancelled = %+v, %v", res, err)
	}
	if len(dialogs) != 1 || dialogs[0].Filename != "Upload- items.har" || !strings.Contains(dialogs[0].Message, "secrets masked") {
		t.Errorf("dialog = %+v", dialogs)
	}

	answer = filepath.Join(out, "exchange.har")
	for range 2 { // twice: saving leaves the kept exchange as it was
		res, err = f.svc.SaveExchange(t.Context(), SaveExchangeInputDTO{BodyID: resp.BodyID, Name: "Upload: items"})
		if err != nil || !res.Saved || res.Path != answer || !res.Masked || res.Size == 0 {
			t.Fatalf("save = %+v, %v", res, err)
		}
		data := readFile(t, answer)
		if problems := har.Validate([]byte(data)); len(problems) > 0 {
			t.Fatalf("invalid HAR: %v", problems)
		}
		for _, leak := range []string{"SECRET", "b3arer-tok"} {
			if strings.Contains(data, leak) {
				t.Errorf("the file holds %q", leak)
			}
		}
		file := decodeHAR(t, data)
		if len(file.Log.Entries) != 1 || file.Log.Creator.Version != "3.1.0" || file.Log.Pages[0].Title != "Upload: items" {
			t.Fatalf("file = %+v", file.Log)
		}
		e := file.Log.Entries[0]
		if e.Request.Method != "POST" || e.Request.URL != srv.URL+"/items?k="+httpclient.Mask || e.Response.Status != 200 {
			t.Errorf("entry: %s %s -> %d", e.Request.Method, e.Request.URL, e.Response.Status)
		}
		// The whole request body, masked.
		pd := e.Request.PostData
		if pd == nil || !strings.HasPrefix(pd.Text, "key="+httpclient.Mask+" ") || !strings.HasSuffix(pd.Text, "REQ-TAIL") || pd.Comment != "" {
			t.Errorf("request body: %d bytes, comment %q", len(pd.Text), pd.Comment)
		}
		// The whole response body, masked.
		c := e.Response.Content
		want := "echo " + httpclient.Mask + " " + httpclient.Mask + " "
		if !strings.HasPrefix(c.Text, want) || !strings.HasSuffix(c.Text, "rrREQ-TAIL END-OF-BODY") || len(c.Text) < len(respPad) || c.Comment != "" {
			t.Errorf("response body: %d bytes, starts %q, comment %q", len(c.Text), c.Text[:min(len(c.Text), 60)], c.Comment)
		}
		var headers []string
		for _, h := range e.Request.Headers {
			headers = append(headers, h.Name+": "+h.Value)
		}
		for _, h := range e.Response.Headers {
			headers = append(headers, h.Name+": "+h.Value)
		}
		got := strings.Join(headers, "\n")
		if !strings.Contains(got, "Authorization: Bearer "+httpclient.Mask) || !strings.Contains(got, "X-Echo-Key: "+httpclient.Mask) {
			t.Errorf("headers = %s", got)
		}
	}

	// Unknown or dropped body.
	_, err = f.svc.SaveExchange(t.Context(), SaveExchangeInputDTO{BodyID: "body_nope"})
	var e *errs.Error
	errors.As(err, &e)
	checkErr(t, e, errs.NotFound, CodeBodyGone)

	// Without a save dialog.
	g := newFixture(t, src, nil)
	_, err = g.svc.SaveExchange(t.Context(), SaveExchangeInputDTO{BodyID: resp.BodyID})
	e = nil
	errors.As(err, &e)
	checkErr(t, e, errs.Unsupported, CodeUnavailable)
}

// A binary body is saved in Base64, masked in its bytes.
func TestSaveExchangeBinary(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte("\x00\xff tok-SECRET \xfe"))
	}))
	defer srv.Close()
	src := httpSource(srv.URL + "/bin")
	answer := filepath.Join(t.TempDir(), "bin.har")
	f := newFixture(t, src, nil, WithSaveBody("", func(context.Context, SaveDialog) (string, error) { return answer, nil }),
		WithExchangeSecrets(func(context.Context) ([]string, error) { return []string{"tok-SECRET"}, nil }))
	id := f.send(t)
	f.rec.wait(t, id)
	resp := f.rec.response(t, id)
	res, err := f.svc.SaveExchange(t.Context(), SaveExchangeInputDTO{BodyID: resp.BodyID})
	if err != nil || !res.Saved || !res.Masked {
		t.Fatalf("save = %+v, %v", res, err)
	}
	c := decodeHAR(t, readFile(t, answer)).Log.Entries[0].Response.Content
	if c.Encoding != "base64" || strings.Contains(readFile(t, answer), "dG9rLVNFQ1JFVC") {
		t.Errorf("content = %+v", c)
	}
	if got := readFile(t, answer); !strings.Contains(got, `"title": "exchange"`) {
		t.Errorf("page title: %s", got[:min(len(got), 300)])
	}
}
