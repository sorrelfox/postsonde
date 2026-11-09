package http

import (
	"encoding/xml"
	"fmt"
	"io"
	"mime"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/app/services/collection"
	"github.com/sorrelfox/postsonde/internal/core/soap"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// soapEcho is a SOAP echo service for both versions: it checks the
// version's Content-Type and action, parses the envelope and answers with
// an envelope of the same version whose Body is EchoResponse holding the
// action and the operation element's name and text. A request that breaks
// the rules gets a SOAP fault with status 500.
func soapEcho() *httptest.Server {
	return httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		var ns, action string
		switch {
		case err != nil:
			fault(w, soap.NS11, "bad Content-Type")
			return
		case mt == soap.MediaType11:
			ns = soap.NS11
			raw, ok := r.Header[nethttp.CanonicalHeaderKey(soap.ActionHeader)]
			if !ok || len(raw) != 1 || !strings.HasPrefix(raw[0], `"`) || !strings.HasSuffix(raw[0], `"`) {
				fault(w, ns, "SOAPAction must be one quoted string")
				return
			}
			action = strings.Trim(raw[0], `"`)
		case mt == soap.MediaType12:
			ns = soap.NS12
			if r.Header.Get(soap.ActionHeader) != "" {
				fault(w, ns, "SOAP 1.2 sends no SOAPAction")
				return
			}
			action = params["action"]
		default:
			fault(w, soap.NS11, "not a SOAP content type: "+mt)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var env struct {
			XMLName xml.Name `xml:"Envelope"`
			Body    struct {
				XMLName xml.Name `xml:"Body"`
				Op      struct {
					XMLName xml.Name
					Text    string `xml:",chardata"`
				} `xml:",any"`
			} `xml:"Body"`
		}
		if err := xml.Unmarshal(body, &env); err != nil {
			fault(w, ns, "not XML: "+err.Error())
			return
		}
		if env.XMLName.Space != ns || env.Body.XMLName.Space != ns {
			fault(w, ns, "envelope namespace "+env.XMLName.Space+" does not match "+mt)
			return
		}
		w.Header().Set("Content-Type", mt+"; charset=utf-8")
		var out strings.Builder
		_ = xml.EscapeText(&out, []byte(env.Body.Op.XMLName.Local+"|"+strings.TrimSpace(env.Body.Op.Text)+"|"+action))
		fmt.Fprintf(w, `<soap:Envelope xmlns:soap="%s"><soap:Body><EchoResponse>%s</EchoResponse></soap:Body></soap:Envelope>`, ns, out.String())
	}))
}

func fault(w nethttp.ResponseWriter, ns, msg string) {
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	w.WriteHeader(nethttp.StatusInternalServerError)
	fmt.Fprintf(w, `<soap:Envelope xmlns:soap="%s"><soap:Body><soap:Fault>%s</soap:Fault></soap:Body></soap:Envelope>`, ns, msg)
}

// soapRequest is the request the body editor's SOAP helper makes: the
// envelope around op as a raw XML body, and the version's headers.
func soapRequest(t *testing.T, url, version, action, op string) collection.HTTPSource {
	t.Helper()
	text, err := soap.Envelope(version, op)
	if err != nil {
		t.Fatal(err)
	}
	headers, err := soap.Headers(version, action)
	if err != nil {
		t.Fatal(err)
	}
	src := httpSource(url)
	src.Request.Method = "POST"
	src.Request.Body = projectfile.Body{Mode: projectfile.BodyRaw, Type: projectfile.RawXML, Text: text}
	for _, h := range headers {
		src.Request.Headers = append(src.Request.Headers, projectfile.KeyValue{Name: h.Name, Value: h.Value})
	}
	return src
}

// TestSendSOAP calls a SOAP echo service with a request made by the SOAP
// helper, in both versions (HTTP-18), with {{variables}} in the action and
// the body rendered as usual.
func TestSendSOAP(t *testing.T) {
	t.Parallel()
	srv := soapEcho()
	t.Cleanup(srv.Close)

	for _, version := range []string{soap.V11, soap.V12} {
		t.Run(version, func(t *testing.T) {
			t.Parallel()
			src := soapRequest(t, srv.URL+"/StockQuote", version, "urn:example:{{op}}",
				`<m:GetQuote xmlns:m="urn:example">{{symbol}}</m:GetQuote>`)
			src.FolderVariables = [][]projectfile.Variable{{{Name: "op", Value: "GetQuote"}, {Name: "symbol", Value: "ACME"}}}
			f := newFixture(t, src, nil)
			id := f.send(t)
			if done := f.rec.wait(t, id); done.Status != operation.StatusOK {
				t.Fatalf("done = %+v", done)
			}
			resp := f.rec.response(t, id)
			if resp.StatusCode != 200 || !strings.Contains(resp.Body, "<EchoResponse>GetQuote|ACME|urn:example:GetQuote</EchoResponse>") {
				t.Errorf("response %d %s", resp.StatusCode, resp.Body)
			}
		})
	}

	// The helper's 1.1 headers with a 1.2 envelope are refused: the echo
	// service really checks.
	src := soapRequest(t, srv.URL, soap.V11, "a", "<Ping/>")
	src.Request.Body.Text = strings.ReplaceAll(src.Request.Body.Text, soap.NS11, soap.NS12)
	f := newFixture(t, src, nil)
	id := f.send(t)
	f.rec.wait(t, id)
	if resp := f.rec.response(t, id); resp.StatusCode != 500 || !strings.Contains(resp.Body, "does not match") {
		t.Errorf("mismatched response %d %s", resp.StatusCode, resp.Body)
	}
}

// TestSendActiveBodyExample checks that the body sent is the active body
// example's (HTTP-17): switching examples moves the bodies between Body
// and the list, and the next send carries the new one.
func TestSendActiveBodyExample(t *testing.T) {
	t.Parallel()
	got := make(chan string, 2)
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- r.Header.Get("Content-Type") + " " + string(b)
	}))
	defer srv.Close()

	small := projectfile.Body{Mode: projectfile.BodyRaw, Type: projectfile.RawJSON, Text: `{"size":"small"}`}
	form := projectfile.Body{Mode: projectfile.BodyURLEncoded, Fields: []projectfile.KeyValue{{Name: "size", Value: "large"}}}
	src := httpSource(srv.URL)
	src.Request.Method = "POST"
	src.Request.Body = small
	src.Request.BodyExamples = []projectfile.BodyExample{{Name: "Small", Active: true}, {Name: "Large", Body: form}}

	f := newFixture(t, src, nil)
	f.rec.wait(t, f.send(t))
	if s := <-got; s != `application/json {"size":"small"}` {
		t.Errorf("first send = %q", s)
	}

	switched := src
	switched.Request.Body = form
	switched.Request.BodyExamples = []projectfile.BodyExample{{Name: "Small", Body: small}, {Name: "Large", Active: true}}
	f.src = switched
	f.rec.wait(t, f.send(t))
	if s := <-got; s != "application/x-www-form-urlencoded size=large" {
		t.Errorf("after the switch = %q", s)
	}
}
