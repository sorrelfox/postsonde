package mockexample

import (
	"slices"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/servers/mockhttp"
	"github.com/sorrelfox/postsonde/internal/core/servers/mockroute"
)

// Mode says how Merge puts the route into a configuration.
type Mode string

// Modes.
const (
	// ModeRoute adds a new route. When a route already answers the same
	// method and path, nothing is added (StatusExisting).
	ModeRoute Mode = "route"
	// ModeResponse adds the response as a named response of the route that
	// answers the same method and path (MOCK-07: picked with the
	// X-PostSonde-Response header); without one it adds a new route.
	ModeResponse Mode = "response"
	// ModeReplace puts the response in place of the default response of
	// the route that answers the same method and path, keeping its other
	// responses, delay and ID; without one it adds a new route.
	ModeReplace Mode = "replace"
)

// Statuses of a merge.
const (
	StatusAdded         = "added"
	StatusExisting      = "existing"
	StatusResponseAdded = "response_added"
	StatusReplaced      = "replaced"
	// StatusOverLimit: the server has mockhttp.MaxRoutes routes, or the
	// route mockhttp.MaxResponses responses.
	StatusOverLimit = "over_limit"
)

// Existing is the route of a configuration that answers the same method
// and path as the new one.
type Existing struct {
	Index  int
	ID     string
	Method string
	Path   string
}

// MergeResult is the configuration a merge gives.
type MergeResult struct {
	// Config is the configuration with the route or response in; the one
	// given when nothing was added.
	Config mockhttp.Config
	// Status is a Status constant.
	Status string
	// Existing is the route that answers the same method and path, nil
	// when there is none.
	Existing *Existing
	// RouteID and RouteIndex are the route added or changed; "" and -1
	// when nothing changed.
	RouteID    string
	RouteIndex int
	// ResponseName is the name of the response added (ModeResponse): the
	// name asked for, made unique within the route.
	ResponseName string
}

// Merge puts r into cfg by mode. name is the name of a response added
// with ModeResponse; blank uses the example's name. Routes are compared
// with cfg's own trailing-slash and case options, disabled ones too.
func Merge(cfg mockhttp.Config, r Result, mode Mode, name string) MergeResult {
	out := MergeResult{Config: cloneConfig(cfg), RouteIndex: -1}
	if i := findExisting(cfg, r.Method, r.Path); i >= 0 {
		ex := cfg.Routes[i]
		out.Existing = &Existing{Index: i, ID: ex.ID, Method: ex.Method, Path: ex.Path}
	}
	switch {
	case out.Existing == nil:
		if len(cfg.Routes) >= mockhttp.MaxRoutes {
			out.Status = StatusOverLimit
			return out
		}
		route := mockhttp.Route{
			ID:      uniqueID(cfg, routeID(r.Method, r.Path)),
			Enabled: true,
			Method:  r.Method,
			Path:    r.Path,
			Status:  r.Reply.Status,
			Headers: slices.Clone(r.Reply.Headers),
			Body:    r.Reply.Body,
			Delay:   mockhttp.Delay{Mode: mockhttp.DelayNone},
		}
		out.Config.Routes = append(out.Config.Routes, route)
		out.Status, out.RouteID, out.RouteIndex = StatusAdded, route.ID, len(out.Config.Routes)-1
	case mode == ModeResponse:
		i := out.Existing.Index
		route := &out.Config.Routes[i]
		if len(route.Responses) >= mockhttp.MaxResponses {
			out.Status = StatusOverLimit
			return out
		}
		resp := r.Reply
		resp.Headers = slices.Clone(resp.Headers)
		resp.Rules = nil
		resp.Templated = false
		resp.Name = responseName(route.Responses, name, r.Reply.Name)
		route.Responses = append(slices.Clone(route.Responses), resp)
		out.Status, out.RouteID, out.RouteIndex, out.ResponseName = StatusResponseAdded, route.ID, i, resp.Name
	case mode == ModeReplace:
		i := out.Existing.Index
		route := &out.Config.Routes[i]
		route.Status = r.Reply.Status
		route.Headers = slices.Clone(r.Reply.Headers)
		route.Body = r.Reply.Body
		route.Templated = false
		out.Status, out.RouteID, out.RouteIndex = StatusReplaced, route.ID, i
	default:
		out.Status = StatusExisting
	}
	return out
}

// findExisting returns the index of the first route of cfg that answers
// method and path as a route with them would (mockroute's duplicate
// rule), or -1.
func findExisting(cfg mockhttp.Config, method, path string) int {
	opts := mockroute.Options{StrictTrailingSlash: cfg.StrictTrailingSlash, CaseInsensitive: cfg.CaseInsensitive}
	for i, r := range cfg.Routes {
		_, diags := mockroute.Compile([]mockroute.Route{
			{ID: "a", Method: r.Method, Pattern: r.Path},
			{ID: "b", Method: method, Pattern: path},
		}, opts)
		for _, d := range diags {
			if d.Code == mockroute.CodeDuplicateRoute && d.RouteIndex == 1 {
				return i
			}
		}
	}
	return -1
}

// routeID is rt_ and the method and path in lower case, letters, digits
// and _ (as routes from OpenAPI).
func routeID(method, path string) string {
	var b strings.Builder
	b.WriteString("rt_")
	for _, r := range strings.ToLower(method + "_" + path) {
		switch {
		case 'a' <= r && r <= 'z', '0' <= r && r <= '9':
			b.WriteRune(r)
		default:
			if s := b.String(); s[len(s)-1] != '_' {
				b.WriteByte('_')
			}
		}
	}
	id := strings.TrimRight(b.String(), "_")
	if len(id) > 64 {
		id = id[:64]
	}
	return id
}

// uniqueID is id, or id_2, id_3…: the first no route of cfg has.
func uniqueID(cfg mockhttp.Config, id string) string {
	taken := map[string]bool{}
	for _, r := range cfg.Routes {
		taken[r.ID] = true
	}
	next := id
	for n := 2; taken[next]; n++ {
		next = id + "_" + strconv.Itoa(n)
	}
	return next
}

// responseName is the name of a response added to a route: name, else
// fallback, else "example", without control characters, not "default",
// and made unique within the route ("200 OK (2)"), letter case ignored.
func responseName(existing []mockhttp.Response, name, fallback string) string {
	clean := func(s string) string {
		return strings.TrimSpace(strings.Map(func(r rune) rune {
			if r < 0x20 || r == 0x7f {
				return ' '
			}
			return r
		}, s))
	}
	base := clean(name)
	if base == "" {
		base = clean(fallback)
	}
	if base == "" || strings.EqualFold(base, mockhttp.DefaultResponseName) {
		base = "example"
	}
	taken := map[string]bool{strings.ToLower(mockhttp.DefaultResponseName): true}
	for _, r := range existing {
		taken[strings.ToLower(strings.TrimSpace(r.Name))] = true
	}
	next := base
	for n := 2; taken[strings.ToLower(next)]; n++ {
		next = base + " (" + strconv.Itoa(n) + ")"
	}
	return next
}

func cloneConfig(c mockhttp.Config) mockhttp.Config {
	c.Routes = slices.Clone(c.Routes)
	if c.Routes == nil {
		c.Routes = []mockhttp.Route{}
	}
	return c
}
