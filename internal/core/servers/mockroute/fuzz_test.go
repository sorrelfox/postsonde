package mockroute

import (
	"reflect"
	"testing"
)

// FuzzMatch matches random paths against a fixed route set. Match must
// not panic, and a matched route must match the path on its own too, with
// the same parameters.
func FuzzMatch(f *testing.F) {
	for _, p := range overlapRequests {
		f.Add("GET", p, false, false)
	}
	f.Add("HEAD", "/users/%zz/avatar", true, true)
	f.Add("DELETE", "//users//", false, true)
	f.Add("", "?", true, false)

	type key struct{ strict, fold bool }
	matchers := map[key]*Matcher{}
	for _, k := range []key{{false, false}, {false, true}, {true, false}, {true, true}} {
		matchers[k] = compile(f, Options{StrictTrailingSlash: k.strict, CaseInsensitive: k.fold}, overlapRoutes...)
	}

	f.Fuzz(func(t *testing.T, method, path string, strict, fold bool) {
		opts := Options{StrictTrailingSlash: strict, CaseInsensitive: fold}
		res := matchers[key{strict, fold}].Match(method, path)
		if !res.Matched {
			if res.RouteIndex != -1 || res.Params != nil || res.Wildcard != "" {
				t.Fatalf("no match but %+v", res)
			}
			if res.MethodNotAllowed == (len(res.Allowed) == 0) {
				t.Fatalf("MethodNotAllowed %v with Allowed %v", res.MethodNotAllowed, res.Allowed)
			}
			return
		}
		if res.MethodNotAllowed || res.Allowed != nil {
			t.Fatalf("match with 405 fields: %+v", res)
		}
		if overlapRoutes[res.RouteIndex] != res.RouteID {
			t.Fatalf("RouteIndex %d is %q, RouteID %q", res.RouteIndex, overlapRoutes[res.RouteIndex], res.RouteID)
		}
		alone := compile(t, opts, res.RouteID)
		again := alone.Match(method, path)
		again.RouteIndex = res.RouteIndex
		if !reflect.DeepEqual(again, res) {
			t.Fatalf("%s %q: matched %+v, but the route alone gives %+v", method, path, res, again)
		}
	})
}
