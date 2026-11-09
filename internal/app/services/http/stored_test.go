package http

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// HTTP-38, HTTP-39: a stored response (a history entry, a response
// example) is shown like a received one.
func TestStoredResponse(t *testing.T) {
	t.Parallel()
	s := &HTTPService{}
	tests := []struct {
		name  string
		in    StoredResponseInputDTO
		check func(t *testing.T, got ResponseDTO)
	}{
		{"json", StoredResponseInputDTO{
			Method: "POST", URL: "https://api.test/users?x=1", Proto: "HTTP/1.1",
			RequestHeaders: []HeaderDTO{{Name: "Host", Value: "api.test"}, {Name: "Content-Type", Value: "application/json"}},
			RequestBody:    `{"a":1}`,
			StatusCode:     201, Status: "201 Created",
			Headers:    []HeaderDTO{{Name: "Content-Type", Value: "application/json"}, {Name: "Set-Cookie", Value: "sid=1; Path=/"}},
			Body:       `{"id":"42"}`,
			BodySize:   11,
			DurationMs: 12,
		}, func(t *testing.T, got ResponseDTO) {
			if got.Status != "201 Created" || got.StatusCode != 201 || got.FinalURL != "https://api.test/users?x=1" || got.Protocol != "HTTP/1.1" {
				t.Errorf("status line = %q %d %q %q", got.Status, got.StatusCode, got.FinalURL, got.Protocol)
			}
			if got.BodyLanguage != "json" || got.PrettyState != "formatted" || !strings.Contains(got.BodyPretty, "\"id\": \"42\"") || got.BodyClipped {
				t.Errorf("body = %+v", got)
			}
			if len(got.Cookies) != 1 || got.Cookies[0].Name != "sid" || got.ContentType != "application/json" {
				t.Errorf("cookies %+v, content type %q", got.Cookies, got.ContentType)
			}
			hop := got.Timeline.Hops[0]
			if len(got.Timeline.Hops) != 1 || hop.Kind != HopFinal || hop.Request.Target != "/users?x=1" || hop.Request.Body != `{"a":1}` ||
				!strings.HasPrefix(hop.ResponseHead, "HTTP/1.1 201 Created\r\n") || !strings.Contains(hop.Request.Head, "POST /users?x=1 HTTP/1.1\r\n") {
				t.Errorf("timeline = %+v", got.Timeline)
			}
			if got.BodyID != "" || got.HistoryID != "" || got.Tests == nil || got.Warnings == nil || got.Timeline.Events == nil {
				t.Errorf("kept %q %q, lists %v %v %v", got.BodyID, got.HistoryID, got.Tests, got.Warnings, got.Timeline.Events)
			}
		}},
		{"reason phrase only, clipped", StoredResponseInputDTO{
			Method: "GET", URL: "https://api.test/big", StatusCode: 200, Status: "OK",
			Headers: []HeaderDTO{{Name: "Content-Type", Value: "application/json"}},
			Body:    `{"items":[1,2`, BodySize: 90000, BodyClipped: true,
		}, func(t *testing.T, got ResponseDTO) {
			if got.Status != "200 OK" || !got.BodyClipped || got.Size != 90000 || got.PrettyState != "incomplete" || got.BodyShown != 13 {
				t.Errorf("got status %q clipped %v size %d pretty %q shown %d", got.Status, got.BodyClipped, got.Size, got.PrettyState, got.BodyShown)
			}
		}},
		{"binary", StoredResponseInputDTO{
			Method: "GET", URL: "https://api.test/img", StatusCode: 200, Status: "200 OK",
			Headers: []HeaderDTO{{Name: "Content-Type", Value: "image/png"}},
			Body:    base64.StdEncoding.EncodeToString([]byte{0x89, 'P', 'N', 'G', 0}), BodyBase64: true, BodySize: 5,
		}, func(t *testing.T, got ResponseDTO) {
			if !got.BodyHex || got.BodySize != 5 || !strings.HasSuffix(got.FileName, ".png") {
				t.Errorf("got hex %v size %d file %q", got.BodyHex, got.BodySize, got.FileName)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := s.StoredResponse(t.Context(), tt.in)
			if err != nil {
				t.Fatal(err)
			}
			tt.check(t, got)
		})
	}

	if _, err := s.StoredResponse(t.Context(), StoredResponseInputDTO{Body: "not base64!", BodyBase64: true}); !errors.Is(err, &errs.Error{Code: CodeInvalidStored}) {
		t.Errorf("bad Base64 = %v, want %s", err, CodeInvalidStored)
	}
}
