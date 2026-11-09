package collection

import (
	"reflect"
	"strings"
	"testing"
)

// TestRequestExtractions saves the extraction rules of an http, graphql
// and sse request (TEST-05) through the auto-save pipeline and reads them
// back: order, flags, disabled rules, unknown members; the runtime scope
// is left out of the file, and so are no rules at all. Only rules are
// stored: there is no member a value could go in.
func TestRequestExtractions(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"http", "graphql", "sse"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			svc, _, dir, _, _ := newAutosaveService(t)
			ctx := t.Context()
			res, err := svc.CreateRequest(ctx, CreateRequestInputDTO{Name: "Login", Kind: kind, Index: -1})
			if err != nil {
				t.Fatal(err)
			}
			req := getRequest(t, svc, res.NodeID)
			if req.Extractions != nil {
				t.Fatalf("new request has extractions %+v", req.Extractions)
			}
			want := []ExtractionDTO{
				{Source: "jsonpath", Expr: "$.token", Variable: "token", Scope: "runtime", Secret: true, Persist: true, Enabled: true},
				{Source: "header", Expr: "X-Id", Variable: "requestId", Scope: "run", Enabled: true, Extra: `{"x-note":"kept"}`},
				{Source: "status", Variable: "code", Scope: "runtime"},
			}
			req.Extractions = want
			update(t, svc, req, 1)
			if err := svc.Flush(ctx, req.ID); err != nil {
				t.Fatal(err)
			}
			file := readFile(t, dir, "login.json")
			for _, s := range []string{`"extractions": [`, `"variable": "token"`, `"scope": "run"`, `"x-note": "kept"`, `"disabled": true`, `"secret": true`, `"persist": true`} {
				if !strings.Contains(file, s) {
					t.Errorf("file lacks %s:\n%s", s, file)
				}
			}
			if strings.Contains(file, `"runtime"`) {
				t.Errorf("the runtime scope is written:\n%s", file)
			}
			if got := getRequest(t, svc, req.ID).Extractions; !reflect.DeepEqual(got, want) {
				t.Errorf("extractions = %+v, want %+v", got, want)
			}

			// Deleting them all leaves the member out.
			req.Extractions = []ExtractionDTO{}
			update(t, svc, req, 2)
			if err := svc.Flush(ctx, req.ID); err != nil {
				t.Fatal(err)
			}
			if file := readFile(t, dir, "login.json"); strings.Contains(file, "extractions") {
				t.Errorf("empty extractions written:\n%s", file)
			}
		})
	}
}
