package template

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// responseCases is testdata/responses.json, which
// frontend/src/lib/templateResponse.test.ts runs too.
type responseCases struct {
	Cases []struct {
		Name      string `json:"name"`
		Response  bool   `json:"response"`
		Valid     bool   `json:"valid"`
		RequestID string `json:"requestId"`
		Source    string `json:"source"`
		Expr      string `json:"expr"`
		Send      string `json:"send"`
		MaxAgeMs  int64  `json:"maxAgeMs"`
		Secret    bool   `json:"secret"`
	} `json:"cases"`
}

func TestParseResponse(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("testdata/responses.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx responseCases
	if err := json.Unmarshal(data, &fx); err != nil {
		t.Fatal(err)
	}
	for _, c := range fx.Cases {
		got, ok, err := ParseResponse(c.Name)
		switch {
		case !c.Response:
			if ok {
				t.Errorf("ParseResponse(%q) ok, want not a response reference", c.Name)
			}
		case !c.Valid:
			if !ok || err == nil {
				t.Errorf("ParseResponse(%q) = ok %v err %v, want an error", c.Name, ok, err)
			}
			if IsDynamic(c.Name) {
				t.Errorf("IsDynamic(%q) = true", c.Name)
			}
		default:
			want := ResponseRef{RequestID: c.RequestID, Source: ResponseSource(c.Source), Expr: c.Expr, Send: SendWhen(c.Send),
				MaxAge: time.Duration(c.MaxAgeMs) * time.Millisecond, Secret: c.Secret}
			if !ok || err != nil || got != want {
				t.Errorf("ParseResponse(%q) = %+v, %v, %v; want %+v", c.Name, got, ok, err, want)
			}
			if !IsDynamic(c.Name) {
				t.Errorf("IsDynamic(%q) = false", c.Name)
			}
		}
	}
}

func TestFormatAge(t *testing.T) {
	t.Parallel()
	for d, want := range map[time.Duration]string{
		10 * time.Minute:       "10m",
		90 * time.Minute:       "1h30m",
		48 * time.Hour:         "2d",
		30 * time.Second:       "30s",
		0:                      "0s",
		25*time.Hour + 5e9:     "1d1h5s",
		500 * time.Millisecond: "0s",
	} {
		if got := FormatAge(d); got != want {
			t.Errorf("FormatAge(%v) = %q, want %q", d, got, want)
		}
		if d >= time.Second {
			if back, ok := parseAge(want); !ok || back != d.Truncate(time.Second) {
				t.Errorf("parseAge(%q) = %v, %v", want, back, ok)
			}
		}
	}
}

// fakeResponses answers response references from fixed values by
// request ID and expression.
type fakeResponses struct {
	values map[string]ResponseValue
	err    error
	got    []ResponseRef
}

func (f *fakeResponses) ReadResponse(ref ResponseRef) (ResponseValue, error) {
	f.got = append(f.got, ref)
	if f.err != nil {
		return ResponseValue{}, f.err
	}
	v, ok := f.values[ref.RequestID+" "+ref.Expr]
	if !ok {
		return ResponseValue{}, errs.New(errs.NotFound, "JSONPath "+ref.Expr+" selects nothing.").WithCode("http.response_value_not_found")
	}
	return v, nil
}

func TestRenderResponse(t *testing.T) {
	t.Parallel()
	f := &fakeResponses{values: map[string]ResponseValue{
		"req_login $.token": {Value: "t0k{{x}}", Request: "Login"},
		"req_login $.key":   {Value: "k3y", Secret: true},
	}}
	r := Resolver{Scopes: Chain{Vars("Global", map[string]string{"auth": "Bearer {{$response req_login jsonpath $.token}}"})}, Responses: f}

	res := r.Render("{{auth}} {{$response req_login jsonpath $.key}} {{$response req_login jsonpath $.token secret}}")
	if err := res.Err(); err != nil {
		t.Fatal(err)
	}
	// The value is used as is: braces in it are not variables.
	if want := "Bearer t0k{{x}} k3y t0k{{x}}"; res.Text != want {
		t.Errorf("Text = %q, want %q", res.Text, want)
	}
	nested := res.Refs[0].Nested[0]
	if !nested.Resolved || nested.Scope != "Response: Login" || nested.Secret {
		t.Errorf("nested = %+v", nested)
	}
	if res.Refs[1].Scope != ResponseScope || !res.Refs[1].Secret {
		t.Errorf("a secret value: %+v", res.Refs[1])
	}
	if !res.Refs[2].Secret || !res.Secret {
		t.Errorf("the secret flag: %+v", res.Refs[2])
	}
	if !IsResponseScope(nested.Scope) || !IsResponseScope(ResponseScope) || IsResponseScope("Responses") {
		t.Error("IsResponseScope")
	}

	// A pipe reads the reference as its input.
	res = r.Render("{{ $response req_login jsonpath $.key | base64 }}")
	if err := res.Err(); err != nil || res.Text != "azN5" || !res.Secret {
		t.Errorf("pipe: %q %v secret %v", res.Text, err, res.Secret)
	}
}

func TestRenderResponseProblems(t *testing.T) {
	t.Parallel()
	// Without a reader.
	res := Resolver{}.Render("x {{$response req_1 status}}")
	err := res.Err()
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != CodeResponseUnavailable || e.Details["reference"] != "$response req_1 status" {
		t.Fatalf("no reader: %v", err)
	}
	if res.Text != "x {{$response req_1 status}}" || len(res.Problems) != 1 || res.Problems[0].Kind != ProblemResponse {
		t.Errorf("left as written: %q %+v", res.Text, res.Problems)
	}

	// The reader's error is returned as it is, with the reference.
	f := &fakeResponses{values: map[string]ResponseValue{}}
	err = Resolver{Responses: f}.Render("{{$response req_1 jsonpath $.nope}}").Err()
	if !errors.As(err, &e) || e.Code != "http.response_value_not_found" || e.Details["reference"] != "$response req_1 jsonpath $.nope" {
		t.Errorf("reader error: %v", err)
	}

	// A plain error gets a message of its own.
	f.err = errors.New("boom")
	err = Resolver{Responses: f}.Render("{{$response req_1 status}}").Err()
	if !errors.As(err, &e) || e.Message != "The value of {{$response req_1 status}} cannot be read from the response." {
		t.Errorf("plain error: %v", err)
	}

	// Invalid arguments never reach the reader.
	f = &fakeResponses{}
	err = Resolver{Responses: f}.Render("{{$response req_1 nope}}").Err()
	if !errors.As(err, &e) || e.Code != CodeInvalidDynamic || len(f.got) != 0 {
		t.Errorf("invalid: %v, reader called %d times", err, len(f.got))
	}

	// Undefined names come first.
	f.err = errors.New("boom")
	err = Resolver{Responses: f}.Render("{{nope}} {{$response req_1 status}}").Err()
	if !errors.As(err, &e) || e.Code != CodeUnresolved {
		t.Errorf("unresolved first: %v", err)
	}
}

func TestResponseIsListed(t *testing.T) {
	t.Parallel()
	if DynamicDescription(ResponseName) == "" {
		t.Error("no description")
	}
	found := false
	for _, n := range DynamicNames() {
		found = found || n == ResponseName
	}
	if !found {
		t.Error("not listed")
	}
}
