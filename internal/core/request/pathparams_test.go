package request

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

// pathParamsFixture is testdata/path_params.json, which
// frontend/src/lib/pathParams.test.ts runs too (the detect cases).
type pathParamsFixture struct {
	Detect []struct {
		Name  string   `json:"name"`
		URL   string   `json:"url"`
		Found []string `json:"found"`
		Names []string `json:"names"`
	} `json:"detect"`
	Substitute []struct {
		Name string `json:"name"`
		URL  string `json:"url"`
		Rows []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"rows"`
		Vars    map[string]string `json:"vars"`
		Want    string            `json:"want"`
		Missing []string          `json:"missing"`
	} `json:"substitute"`
}

func loadPathParamsFixture(t *testing.T) pathParamsFixture {
	t.Helper()
	data, err := os.ReadFile("testdata/path_params.json")
	if err != nil {
		t.Fatal(err)
	}
	var f pathParamsFixture
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Detect) == 0 || len(f.Substitute) == 0 {
		t.Fatal("no cases")
	}
	return f
}

func expandWith(vars map[string]string) Expand {
	resolver := template.Resolver{Scopes: template.Chain{template.Vars("env", vars)}}
	return func(s string) (string, error) {
		r := resolver.Render(s)
		return r.Text, r.Err()
	}
}

func TestPathParamsFixture(t *testing.T) {
	t.Parallel()

	for _, c := range loadPathParamsFixture(t).Detect {
		t.Run(c.Name, func(t *testing.T) {
			t.Parallel()
			found := []string{}
			for _, p := range PathParams(c.URL) {
				found = append(found, c.URL[p.Start:p.End])
				want := c.URL[p.Start+1 : p.End]
				if c.URL[p.Start] == '{' {
					want = c.URL[p.Start+1 : p.End-1]
				}
				if p.Name != want {
					t.Errorf("name %q does not match its placeholder %q", p.Name, c.URL[p.Start:p.End])
				}
			}
			if !reflect.DeepEqual(found, c.Found) {
				t.Errorf("PathParams(%q) = %q, want %q", c.URL, found, c.Found)
			}
			names := PathParamNames(c.URL)
			if names == nil {
				names = []string{}
			}
			if !reflect.DeepEqual(names, c.Names) {
				t.Errorf("PathParamNames(%q) = %q, want %q", c.URL, names, c.Names)
			}
		})
	}
}

func TestSubstitutePathFixture(t *testing.T) {
	t.Parallel()

	for _, c := range loadPathParamsFixture(t).Substitute {
		t.Run(c.Name, func(t *testing.T) {
			t.Parallel()
			rows := make([]projectfile.KeyValue, 0, len(c.Rows))
			for _, r := range c.Rows {
				rows = append(rows, kv(r.Name, r.Value))
			}
			got, err := SubstitutePath(c.URL, rows, expandWith(c.Vars))
			if got != c.Want {
				t.Errorf("SubstitutePath = %q, want %q", got, c.Want)
			}
			if len(c.Missing) == 0 {
				if err != nil {
					t.Fatalf("error: %v", err)
				}
				return
			}
			var e *errs.Error
			if !errors.As(err, &e) {
				t.Fatalf("error = %v, want an *errs.Error", err)
			}
			if e.Kind != errs.InvalidArgument || e.Code != CodePathParamMissing || e.Hint == "" {
				t.Errorf("error = %s / %s / hint %q", e.Kind, e.Code, e.Hint)
			}
			want := ""
			for i, n := range c.Missing {
				if i > 0 {
					want += ","
				}
				want += n
			}
			if e.Details[DetailPathParams] != want {
				t.Errorf("details = %v, want %s=%s", e.Details, DetailPathParams, want)
			}
		})
	}
}

func TestSubstitutePathErrors(t *testing.T) {
	t.Parallel()

	t.Run("message names every missing variable", func(t *testing.T) {
		_, err := SubstitutePath("http://h/:a/:b/:c", nil, nil)
		if got, want := err.Error(), "The path variables a, b and c have no value."; got != want {
			t.Errorf("message = %q, want %q", got, want)
		}
		_, err = SubstitutePath("http://h/{a}", nil, nil)
		if got, want := err.Error(), "The path variable a has no value."; got != want {
			t.Errorf("message = %q, want %q", got, want)
		}
	})

	t.Run("an undefined variable keeps its code", func(t *testing.T) {
		got, err := SubstitutePath("http://h/:id", []projectfile.KeyValue{kv("id", "{{nope}}")}, expandWith(nil))
		if got != "" {
			t.Errorf("url = %q, want none", got)
		}
		var e *errs.Error
		if !errors.As(err, &e) {
			t.Fatalf("error = %v", err)
		}
		if e.Code != template.CodeUnresolved || e.Details[DetailPathParam] != "id" || e.Details[DetailField] != "value" {
			t.Errorf("error = %s %v", e.Code, e.Details)
		}
	})

	t.Run("other expand errors are wrapped", func(t *testing.T) {
		_, err := SubstitutePath("http://h/:id", []projectfile.KeyValue{kv("id", "x")}, func(string) (string, error) {
			return "", errors.New("boom")
		})
		var e *errs.Error
		if !errors.As(err, &e) || e.Kind != errs.InvalidArgument || e.Err == nil || e.Err.Error() != "boom" || e.Details[DetailPathParam] != "id" {
			t.Errorf("error = %v (%s)", err, errs.KindOf(err))
		}
	})

	t.Run("disabled rows still give their value", func(t *testing.T) {
		got, err := SubstitutePath("http://h/:id", []projectfile.KeyValue{off("id", "1")}, nil)
		if err != nil || got != "http://h/1" {
			t.Errorf("SubstitutePath = %q, %v", got, err)
		}
	})

	t.Run("values are not rendered without expand", func(t *testing.T) {
		got, err := SubstitutePath("http://h/:id", []projectfile.KeyValue{kv("id", "{{x}}")}, nil)
		if err != nil || got != "http://h/%7B%7Bx%7D%7D" {
			t.Errorf("SubstitutePath = %q, %v", got, err)
		}
	})
}

func TestPathParamsOffsetsAreBytes(t *testing.T) {
	t.Parallel()

	url := "http://h/é/:id"
	params := PathParams(url)
	if len(params) != 1 || url[params[0].Start:params[0].End] != ":id" || params[0].Name != "id" {
		t.Errorf("PathParams = %+v", params)
	}
}

func FuzzPathParams(f *testing.F) {
	for _, s := range []string{"http://h/:id/{x}", "{{a}}/:b?c#d", "http://[::1]:80/{{x}}{y}", `\{{a}}/{b}}`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, url string) {
		last := 0
		for _, p := range PathParams(url) {
			if p.Start < last || p.End <= p.Start || p.End > len(url) || p.Name == "" {
				t.Fatalf("bad placeholder %+v in %q", p, url)
			}
			last = p.End
		}
		rows := []projectfile.KeyValue{}
		for _, n := range PathParamNames(url) {
			rows = append(rows, kv(n, "v"))
		}
		out, err := SubstitutePath(url, rows, nil)
		if err != nil {
			t.Fatalf("SubstitutePath with every value: %v", err)
		}
		if len(PathParams(url)) == 0 && out != url {
			t.Fatalf("changed a URL without placeholders: %q -> %q", url, out)
		}
	})
}
