package mockopenapi

import (
	"strconv"

	"github.com/sorrelfox/postsonde/internal/core/servers/mockhttp"
	"github.com/sorrelfox/postsonde/internal/core/servers/mockroute"
)

// Mode says what Merge does with a server's existing routes.
type Mode string

// Modes.
const (
	// ModeMerge keeps the existing routes and adds the generated ones
	// after them. A generated route with the same method and path as an
	// existing one is left out, so edits made by hand are kept.
	ModeMerge Mode = "merge"
	// ModeReplace drops the existing routes.
	ModeReplace Mode = "replace"
)

// Route statuses of a merge.
const (
	// StatusAdded: the generated route is in the merged configuration.
	StatusAdded = "added"
	// StatusExisting: an existing route has the same method and path,
	// and was kept instead (ModeMerge).
	StatusExisting = "existing"
	// StatusOverLimit: the server already had too many routes.
	StatusOverLimit = "over_limit"
)

// MergeResult is the configuration a merge gives.
type MergeResult struct {
	// Config is existing's configuration with the routes merged in.
	Config mockhttp.Config
	// Statuses[i] is what happened to generated[i].
	Statuses []string
	// IDs[i] is the ID of generated[i] in Config: its own, or a new one
	// when an existing route already had it; "" when it was not added.
	IDs []string
	// Added counts the generated routes added, Kept the existing routes
	// kept and Removed the existing routes dropped (ModeReplace).
	Added, Kept, Removed int
	// Diagnostics say which generated routes were not added and why;
	// RouteIndex is the index in generated.
	Diagnostics []Diagnostic
}

// Merge puts generated routes into a mock server's configuration. Routes
// are compared with the configuration's own matching options (trailing
// slash, case), so a route that could never win is not added. The result
// has at most mockhttp.MaxRoutes routes.
func Merge(existing mockhttp.Config, generated []mockhttp.Route, mode Mode) MergeResult {
	out := MergeResult{
		Config: mockhttp.Config{
			StrictTrailingSlash: existing.StrictTrailingSlash,
			CaseInsensitive:     existing.CaseInsensitive,
		},
		Statuses: make([]string, len(generated)),
		IDs:      make([]string, len(generated)),
	}
	var base []mockhttp.Route
	if mode == ModeReplace {
		out.Removed = len(existing.Routes)
	} else {
		base = existing.Routes
	}
	out.Kept = len(base)

	// Compile the kept routes, all of them as if enabled, then the
	// generated ones: a duplicate_route diagnostic on a generated route
	// means an earlier route already answers its method and paths.
	opts := mockroute.Options{StrictTrailingSlash: existing.StrictTrailingSlash, CaseInsensitive: existing.CaseInsensitive}
	all := make([]mockroute.Route, 0, len(base)+len(generated))
	for i, r := range base {
		all = append(all, mockroute.Route{ID: "k" + strconv.Itoa(i), Method: r.Method, Pattern: r.Path})
	}
	for i, r := range generated {
		all = append(all, mockroute.Route{ID: "g" + strconv.Itoa(i), Method: r.Method, Pattern: r.Path})
	}
	_, diags := mockroute.Compile(all, opts)
	dup := map[int]bool{}
	for _, d := range diags {
		if d.Code == mockroute.CodeDuplicateRoute && d.RouteIndex >= len(base) {
			dup[d.RouteIndex-len(base)] = true
		}
	}

	ids := map[string]bool{}
	routes := make([]mockhttp.Route, 0, len(base)+len(generated))
	for _, r := range base {
		ids[r.ID] = true
		routes = append(routes, cloneRoute(r))
	}
	for i, r := range generated {
		if dup[i] {
			out.Statuses[i] = StatusExisting
			out.Diagnostics = append(out.Diagnostics, Diagnostic{Severity: SeverityInfo, Code: CodeExistingRoute, RouteIndex: i,
				Message: "Another route already answers " + r.Method + " " + r.Path + "; it is kept and the generated one is not added."})
			continue
		}
		if len(routes) >= mockhttp.MaxRoutes {
			out.Statuses[i] = StatusOverLimit
			out.Diagnostics = append(out.Diagnostics, Diagnostic{Severity: SeverityError, Code: CodeTooManyRoutes, RouteIndex: i,
				Message: "A mock server has at most " + strconv.Itoa(mockhttp.MaxRoutes) + " routes; " + r.Method + " " + r.Path + " is not added."})
			continue
		}
		r = cloneRoute(r)
		if r.ID == "" || ids[r.ID] {
			base := r.ID
			if base == "" {
				base = "rt_route"
			}
			for n := 2; ; n++ {
				id := base + "_" + strconv.Itoa(n)
				if !ids[id] {
					r.ID = id
					break
				}
			}
		}
		ids[r.ID] = true
		out.Statuses[i] = StatusAdded
		out.IDs[i] = r.ID
		out.Added++
		routes = append(routes, r)
	}
	out.Config.Routes = routes
	return out
}

func cloneRoute(r mockhttp.Route) mockhttp.Route {
	r.Headers = append([]mockhttp.Header(nil), r.Headers...)
	if r.Headers == nil {
		r.Headers = []mockhttp.Header{}
	}
	return r
}
