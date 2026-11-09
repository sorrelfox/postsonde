package harimport

import (
	"context"
	"mime"
	"net/url"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/importer"
	"github.com/sorrelfox/postsonde/internal/core/yaml"
)

// reader walks the entries.
type reader struct {
	ctx context.Context
	// seen holds the entries imported, to leave out repeats.
	seen map[string]bool
	// Entries left out, by reason.
	static, preflight, repeated int
}

// group is a folder being filled: a page, or a host.
type group struct {
	folder *importer.Folder
}

// staticTypes are Chrome's _resourceType values of page resources, not
// API calls.
var staticTypes = map[string]bool{
	"image": true, "font": true, "stylesheet": true, "script": true,
	"media": true, "manifest": true, "texttrack": true,
}

// read fills m from the entries: a folder per page (or per host without
// pages); a single folder's requests go straight into the root.
func (r *reader) read(log, entries *yaml.Node, m *importer.Model) error {
	type page struct {
		title string
		at    importer.Location
	}
	pages := map[string]page{}
	for i, p := range items(get(log, "pages")) {
		id := sget(p, "id")
		if id == "" {
			continue
		}
		title := strings.TrimSpace(sget(p, "title"))
		if title == "" {
			title = "Page " + strconv.Itoa(i+1)
		}
		pages[id] = page{title: folderName(title), at: importer.Location{Line: p.Line, Column: p.Column, Path: "log.pages[" + strconv.Itoa(i) + "]"}}
	}
	var order []*group
	groups := map[string]*group{}
	groupOf := func(key, name string, at importer.Location) *group {
		g := groups[key]
		if g == nil {
			g = &group{folder: &importer.Folder{Name: name, Source: at}}
			groups[key] = g
			order = append(order, g)
		}
		return g
	}

	for i, e := range entries.Items {
		if i%64 == 0 {
			if err := r.ctx.Err(); err != nil {
				return errs.Wrap(err, errs.KindOf(err), "Reading the file was interrupted.")
			}
		}
		at := importer.Location{Line: e.Line, Column: e.Column, Path: "log.entries[" + strconv.Itoa(i) + "]"}
		rq := get(e, "request")
		if !isMap(e) || !isMap(rq) {
			g := groupOf("", "Other", at)
			g.folder.Items = append(g.folder.Items, importer.SkippedItem(&importer.Skipped{
				Kind: "entry", Code: CodeUnsupported, Source: at,
				Reason: "The entry has no request object.",
			}))
			continue
		}
		method := strings.ToUpper(strings.TrimSpace(sget(rq, "method")))
		if method == "" {
			method = "GET"
		}
		switch {
		case isStatic(e, method):
			r.static++
			continue
		case isPreflight(e, rq, method):
			r.preflight++
			continue
		}
		raw := strings.TrimSpace(sget(rq, "url"))
		u, err := url.Parse(raw)
		if err != nil || u.Scheme == "" {
			u = &url.URL{Opaque: raw}
		}
		u.Fragment, u.RawFragment = "", ""

		var g *group
		if p, ok := pages[sget(e, "pageref")]; ok {
			g = groupOf("page:"+sget(e, "pageref"), p.title, p.at)
		} else {
			host := u.Host
			if host == "" {
				host = "Other"
			}
			g = groupOf("host:"+strings.ToLower(host), host, at)
		}

		scheme := strings.ToLower(u.Scheme)
		switch scheme {
		case "http", "https", "ws", "wss":
		default:
			g.folder.Items = append(g.folder.Items, importer.SkippedItem(unsupportedURL(scheme, method, at)))
			continue
		}
		key := method + " " + u.String() + "\n" + sget(get(rq, "postData"), "text")
		if r.seen[key] {
			r.repeated++
			continue
		}
		r.seen[key] = true
		g.folder.Items = append(g.folder.Items, importer.RequestItem(mapEntry(e, rq, method, u, at)))
	}

	switch len(order) {
	case 0:
	case 1:
		m.Root.Items = order[0].folder.Items
	default:
		for _, g := range order {
			m.Root.Items = append(m.Root.Items, importer.FolderItem(g.folder))
		}
	}
	if d := r.filtered(); d != nil {
		m.Diagnostics = append(m.Diagnostics, *d)
	}
	return nil
}

// filtered is the file-level note of the entries left out; nil for none.
func (r *reader) filtered() *importer.Diagnostic {
	var parts []string
	if r.static > 0 {
		parts = append(parts, plural(r.static, "static resource", "static resources")+" (images, scripts, styles, fonts, media)")
	}
	if r.preflight > 0 {
		parts = append(parts, plural(r.preflight, "CORS preflight request", "CORS preflight requests"))
	}
	if r.repeated > 0 {
		parts = append(parts, plural(r.repeated, "repeated request", "repeated requests")+" (the same method, URL and body as an earlier one)")
	}
	if len(parts) == 0 {
		return nil
	}
	msg := parts[0]
	switch len(parts) {
	case 2:
		msg += " and " + parts[1]
	case 3:
		msg += ", " + parts[1] + " and " + parts[2]
	}
	return &importer.Diagnostic{Code: CodeFiltered, Severity: importer.SeverityInfo, Source: importer.Location{Path: "log.entries"},
		Message: "Left out " + msg + "."}
}

// isStatic reports whether an entry loaded a page resource rather than
// calling an API: by Chrome's _resourceType, else (Firefox, Safari) a GET
// or HEAD whose response is an image, font, style sheet, script or media.
func isStatic(e *yaml.Node, method string) bool {
	if rt := strings.ToLower(sget(e, "_resourceType")); rt != "" {
		return staticTypes[rt]
	}
	if method != "GET" && method != "HEAD" {
		return false
	}
	mt := strings.ToLower(sget(path(e, "response", "content"), "mimeType"))
	if parsed, _, err := mime.ParseMediaType(mt); err == nil {
		mt = parsed
	}
	switch {
	case strings.HasPrefix(mt, "image/"), strings.HasPrefix(mt, "font/"), strings.HasPrefix(mt, "audio/"),
		strings.HasPrefix(mt, "video/"), strings.HasPrefix(mt, "application/font-"),
		strings.Contains(mt, "javascript"), strings.Contains(mt, "ecmascript"):
		return true
	}
	switch mt {
	case "text/css", "application/wasm", "application/x-font-woff", "application/vnd.ms-fontobject", "application/manifest+json":
		return true
	}
	return false
}

// isPreflight reports whether an entry is a CORS preflight the browser
// made on its own.
func isPreflight(e, rq *yaml.Node, method string) bool {
	if strings.EqualFold(sget(e, "_resourceType"), "preflight") {
		return true
	}
	return method == "OPTIONS" && headerValue(rq, "Access-Control-Request-Method") != ""
}

// unsupportedURL is the skipped item of an entry PostSonde cannot send.
func unsupportedURL(scheme, method string, at importer.Location) *importer.Skipped {
	name := method + " " + scheme + ": URL"
	reason := "A " + scheme + ": URL is not fetched over the network, so there is no request to import."
	if scheme == "" {
		name = method + " request"
		reason = "The entry's URL is not an absolute http, https, ws or wss URL."
	}
	return &importer.Skipped{Kind: "request", Name: name, Code: CodeUnsupported, Source: at, Reason: reason}
}

// folderName shortens a page title (often its URL) for a folder name.
func folderName(title string) string {
	if u, err := url.Parse(title); err == nil && u.Host != "" && (u.Scheme == "http" || u.Scheme == "https") {
		title = u.Host + strings.TrimSuffix(u.EscapedPath(), "/")
	}
	return clipName(title)
}

// clipName cuts a name to 80 characters.
func clipName(s string) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) > 80 {
		return string(r[:79]) + "…"
	}
	return string(r)
}
