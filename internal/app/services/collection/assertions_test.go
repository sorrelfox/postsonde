package collection

import (
	"reflect"
	"strings"
	"testing"
)

// TestRequestAssertions saves the assertions of an http and a graphql
// request (TEST-01) through the auto-save pipeline and reads them back:
// order, disabled rows, rendered-later {{variables}} and unknown members
// are kept; none is left out of the file.
func TestRequestAssertions(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"http", "graphql"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			svc, _, dir, _, _ := newAutosaveService(t)
			ctx := t.Context()
			res, err := svc.CreateRequest(ctx, CreateRequestInputDTO{Name: "Checked", Kind: kind, Index: -1})
			if err != nil {
				t.Fatal(err)
			}
			req := getRequest(t, svc, res.NodeID)
			if req.Assertions != nil {
				t.Fatalf("new request has assertions %+v", req.Assertions)
			}
			want := []AssertionDTO{
				{Target: "status", Op: "equals", Expected: "200", Enabled: true},
				{Target: "jsonpath", Expr: "$.id", Op: "equals", Expected: "{{userId}}", Enabled: true, Extra: `{"x-note":"kept"}`},
				{Target: "time", Op: "lt", Expected: "500ms"},
			}
			req.Assertions = want
			update(t, svc, req, 1)
			if err := svc.Flush(ctx, req.ID); err != nil {
				t.Fatal(err)
			}
			file := readFile(t, dir, "checked.json")
			for _, s := range []string{`"assertions": [`, `"expected": "{{userId}}"`, `"x-note": "kept"`, `"disabled": true`} {
				if !strings.Contains(file, s) {
					t.Errorf("file lacks %s:\n%s", s, file)
				}
			}
			if got := getRequest(t, svc, req.ID).Assertions; !reflect.DeepEqual(got, want) {
				t.Errorf("assertions = %+v, want %+v", got, want)
			}

			// Deleting them all leaves the member out.
			req.Assertions = []AssertionDTO{}
			update(t, svc, req, 2)
			if err := svc.Flush(ctx, req.ID); err != nil {
				t.Fatal(err)
			}
			if file := readFile(t, dir, "checked.json"); strings.Contains(file, "assertions") {
				t.Errorf("empty assertions written:\n%s", file)
			}
		})
	}
}
