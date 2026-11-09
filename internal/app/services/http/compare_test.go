package http

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// HTTP-40: two stored responses compared through the service: the status,
// the headers by name whatever their case, the JSON body by path, a
// Base64 body decoded, and never a null list for the UI.
func TestCompareResponses(t *testing.T) {
	t.Parallel()
	s := &HTTPService{}
	left := StoredResponseInputDTO{StatusCode: 200, Status: "200 OK",
		Headers: []HeaderDTO{{Name: "Content-Type", Value: "application/json"}, {Name: "ETag", Value: `"a"`}},
		Body:    `{"id":1,"name":"Ada","tags":["x"]}`}
	right := StoredResponseInputDTO{StatusCode: 201, Status: "201 Created",
		Headers: []HeaderDTO{{Name: "content-type", Value: "application/json"}, {Name: "Etag", Value: `"b"`}, {Name: "Location", Value: "/u/1"}},
		Body:    base64.StdEncoding.EncodeToString([]byte(`{"id":1,"name":"Ada Lovelace","tags":["x","y"]}`)), BodyBase64: true}
	got, err := s.CompareResponses(t.Context(), CompareInputDTO{Left: left, Right: right})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != (StatusDiffDTO{Left: "200 OK", Right: "201 Created", Changed: true}) {
		t.Errorf("status %+v", got.Status)
	}
	var headers []string
	for _, h := range got.Headers {
		headers = append(headers, h.Kind+" "+h.Name)
	}
	if strings.Join(headers, ", ") != "same Content-Type, changed ETag, added Location" || got.HeadersAdded != 1 || got.HeadersChanged != 1 {
		t.Errorf("headers %q", headers)
	}
	b := got.Body
	if b.Mode != "json" || b.Identical || b.Changed != 1 || b.Added != 1 || b.Removed != 0 {
		t.Errorf("body %+v", b)
	}
	var paths []string
	for _, c := range b.Changes {
		paths = append(paths, c.Kind+" "+c.Path)
	}
	if strings.Join(paths, ", ") != "changed $.name, added $.tags[1]" {
		t.Errorf("changes %q", paths)
	}
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), ":null,\"right\":null") || strings.Contains(string(data), `"notices":null`) {
		t.Errorf("null lists: %s", data)
	}

	same, err := s.CompareResponses(t.Context(), CompareInputDTO{Left: left, Right: left})
	if err != nil || !same.Body.Identical || same.Status.Changed || len(same.Body.Notices) != 0 {
		t.Errorf("same = %+v, %v", same, err)
	}
	if _, err := s.CompareResponses(t.Context(), CompareInputDTO{Left: left, Right: StoredResponseInputDTO{Body: "!", BodyBase64: true}}); !errors.Is(err, &errs.Error{Code: CodeInvalidStored}) {
		t.Errorf("bad Base64 = %v", err)
	}
}
