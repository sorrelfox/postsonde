package history

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// noMask fails the test when anything of r still holds the mask.
func noMask(t *testing.T, r Restored) {
	t.Helper()
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), httpclient.Mask) {
		t.Errorf("restored request holds the mask: %s", data)
	}
}

func TestHTTPTemplateMasksByValue(t *testing.T) {
	t.Parallel()
	const secret = "tok-123456"
	r := projectfile.HTTPRequest{
		Method: "POST", URL: "{{base}}/users/:id?q={{q}}",
		Params:     []projectfile.KeyValue{{Name: "q", Value: "{{q}}"}, {Name: "off", Value: "1", Disabled: true}},
		PathParams: []projectfile.KeyValue{{Name: "id", Value: "7"}},
		Headers: []projectfile.KeyValue{
			{Name: "Authorization", Value: "Bearer {{token}}"},
			{Name: "Proxy-Authorization", Value: "Basic literal"},
			{Name: "X-Key", Value: secret, Description: "typed in"},
		},
		Body: projectfile.Body{Mode: "raw", Type: "json", Text: `{"k":"` + secret + `","v":"{{v}}"}`},
		Auth: projectfile.Auth{Type: "bearer"},
	}
	tpl := HTTPTemplate(r)
	if !MaskTemplate(tpl, httpclient.NewSecretMasker([]string{secret})) {
		t.Error("masked = false")
	}
	if tpl.URL != r.URL || tpl.Params[0].Value != "{{q}}" || !tpl.Params[1].Disabled || tpl.PathParams[0].Value != "7" {
		t.Errorf("rows = %+v", tpl)
	}
	if h := tpl.Headers; h[0].Value != "Bearer {{token}}" || h[1].Value != "Basic "+httpclient.Mask || h[2].Value != httpclient.Mask || h[2].Description != "typed in" {
		t.Errorf("headers = %+v", h)
	}
	if tpl.Body.Text != `{"k":"`+httpclient.Mask+`","v":"{{v}}"}` || tpl.Body.Type != "json" {
		t.Errorf("body = %+v", tpl.Body)
	}
	g := GraphQLTemplate(projectfile.GraphQLRequest{URL: "{{gql}}", Query: "{ me }", Variables: `{"a":1}`, OperationName: "Me"})
	if g.Body != nil || g.Query != "{ me }" || g.OperationName != "Me" {
		t.Errorf("graphql = %+v", g)
	}
	long := &Template{Body: &TemplateBody{Text: strings.Repeat("é", MaxBody)}}
	MaskTemplate(long, nil)
	if len(long.Body.Text) > MaxBody || !slices.Equal(long.Clipped, []string{SectionBody}) {
		t.Errorf("clipped = %d, %v", len(long.Body.Text), long.Clipped)
	}
}

// NAV-03: a restore gives back the editor's form, and never a mask.
func TestRestoreTemplated(t *testing.T) {
	t.Parallel()
	e := entry(1)
	e.Template = &Template{
		Method: "PUT", URL: "{{base}}/x?key=" + httpclient.Mask,
		Params:  []Field{{Name: "key", Value: httpclient.Mask}},
		Headers: []Field{{Name: "Authorization", Value: "Bearer " + httpclient.Mask}, {Name: "X-Ref", Value: "{{ref}}"}, {Name: "X-Now", Value: "became-secret"}},
		Body:    &TemplateBody{Mode: "urlencoded", Fields: []Field{{Name: "pw", Value: httpclient.Mask}, {Name: "user", Value: "{{user}}"}}},
		Clipped: []string{SectionBody},
	}
	r := Restore(e, httpclient.NewSecretMasker([]string{"became-secret"}))
	noMask(t, r)
	if !r.Templated || r.Kind != KindHTTP || r.Method != "PUT" || r.URL != "{{base}}/x?key=" {
		t.Errorf("restored = %+v", r)
	}
	if r.Headers[1].Value != "{{ref}}" || r.Headers[0].Value != "Bearer " || r.Headers[2].Value != "" || r.Body.Fields[1].Value != "{{user}}" {
		t.Errorf("rows = %+v %+v", r.Headers, r.Body.Fields)
	}
	want := []Hidden{
		{Section: SectionBody, Reason: HiddenClipped},
		{Section: SectionURL, Reason: HiddenMasked},
		{Section: SectionParams, Name: "key", Reason: HiddenMasked},
		{Section: SectionHeaders, Name: "Authorization", Reason: HiddenMasked},
		{Section: SectionHeaders, Name: "X-Now", Reason: HiddenMasked},
		{Section: SectionFields, Name: "pw", Reason: HiddenMasked},
	}
	if !slices.Equal(r.Hidden, want) {
		t.Errorf("hidden = %+v\nwant %+v", r.Hidden, want)
	}
	// The entry is not changed.
	if e.Template.Headers[2].Value != "became-secret" || e.Template.Params[0].Value != httpclient.Mask {
		t.Errorf("entry changed: %+v", e.Template)
	}
}

// An entry without a template is restored as sent, without the fields the
// client and the auth wrote.
func TestRestoreAsSent(t *testing.T) {
	t.Parallel()
	e := entry(1)
	e.Method = "POST"
	e.Request = RequestSnapshot{
		Method: "POST", URL: "https://api.test/items?page=2",
		Headers: []Header{
			{Name: ":authority", Value: "api.test"}, {Name: "Host", Value: "api.test"}, {Name: "User-Agent", Value: "PostSonde/1.0"},
			{Name: "Content-Type", Value: "application/json"}, {Name: "Authorization", Value: "Bearer " + httpclient.Mask},
			{Name: "Content-Length", Value: "20"}, {Name: "Cookie", Value: "a=b"}, {Name: "X-Custom", Value: "v " + httpclient.Mask},
			{Name: "Accept-Encoding", Value: "gzip"},
		},
		Body: Body{Text: `{"pw":"` + httpclient.Mask + `"}`, Size: 20},
	}
	r := Restore(e, nil)
	noMask(t, r)
	if r.Templated || r.URL != "https://api.test/items?page=2" || r.Method != "POST" || len(r.Params) != 0 {
		t.Errorf("restored = %+v", r)
	}
	var names []string
	for _, h := range r.Headers {
		names = append(names, h.Name)
	}
	if !slices.Equal(names, []string{"Content-Type", "X-Custom"}) || r.Headers[1].Value != "v " {
		t.Errorf("headers = %+v", r.Headers)
	}
	if r.Body == nil || r.Body.Mode != "raw" || r.Body.Type != "json" || r.Body.Text != `{"pw":""}` {
		t.Errorf("body = %+v", r.Body)
	}
	if !slices.Equal(r.Hidden, []Hidden{{Section: SectionHeaders, Name: "X-Custom", Reason: HiddenMasked}, {Section: SectionBody, Reason: HiddenMasked}}) {
		t.Errorf("hidden = %+v", r.Hidden)
	}

	bin := entry(2)
	bin.Kind = KindReplay
	bin.Request.Body = Body{Text: "AAEC", Base64: true, Size: 3}
	if r := Restore(bin, nil); r.Kind != KindHTTP || r.Body.Mode != "none" || !slices.Equal(r.Hidden, []Hidden{{Section: SectionBody, Reason: HiddenBinary}}) {
		t.Errorf("binary = %+v", r)
	}
}

func TestRestoreGraphQLAsSent(t *testing.T) {
	t.Parallel()
	post := entry(1)
	post.Kind = KindGraphQL
	post.Request = RequestSnapshot{Method: "POST", URL: "https://g.test/graphql",
		Body: Body{Text: `{"query":"query Me { me }","variables":{"id":1},"operationName":"Me"}`}}
	r := Restore(post, nil)
	if r.Kind != KindGraphQL || r.Method != "" || r.Query != "query Me { me }" || r.Variables != `{"id":1}` || r.OperationName != "Me" || r.Body != nil {
		t.Errorf("post = %+v", r)
	}
	get := entry(2)
	get.Kind = KindGraphQL
	get.Request = RequestSnapshot{Method: "GET", URL: "https://g.test/graphql?query=%7B+me+%7D&operationName=Me"}
	r = Restore(get, nil)
	if r.Method != "GET" || r.URL != "https://g.test/graphql" || r.Query != "{ me }" || r.OperationName != "Me" {
		t.Errorf("get = %+v", r)
	}
}
