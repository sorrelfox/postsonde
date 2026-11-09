package insomnia

import (
	"context"
	"math"
	"slices"
	"strconv"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/importer"
	"github.com/sorrelfox/postsonde/internal/core/yaml"
)

// entry is one resource of a v4 export.
type entry struct {
	n      *yaml.Node
	typ    string
	id     string
	parent string
	loc    importer.Location
}

// skippedKinds names the v4 resource types that are items PostSonde does
// not import, for the report.
var skippedKinds = map[string]string{
	"api_spec":           "API spec",
	"unit_test_suite":    "test suite",
	"mock_server":        "mock server",
	"socketio_request":   "socket.io request",
	"client_certificate": "client certificate",
	"ca_certificate":     "CA certificate",
}

// ignored are v4 resource types read with another resource, or holding
// nothing to import (app state, responses, tokens).
var ignored = map[string]bool{
	"workspace": true, "environment": true, "cookie_jar": true, "proto_file": true,
	"proto_directory": true, "websocket_payload": true, "socketio_payload": true,
	"unit_test": true, "mock_route": true, "workspace_meta": true, "request_meta": true,
	"request_group_meta": true, "grpc_request_meta": true, "websocket_request_meta": true,
	"response": true, "websocket_response": true, "request_version": true, "oauth2_token": true,
	"plugin_data": true, "settings": true, "stats": true, "git_repository": true,
	"project": true, "organization": true,
}

// parseV4 reads a v4 export: resources linked by parentId.
func parseV4(ctx context.Context, root *yaml.Node) (*importer.Model, error) {
	m := &importer.Model{}
	resources := get(root, "resources")
	if resources == nil || resources.Kind != yaml.SequenceNode {
		return nil, importer.InvalidFile(format, "The Insomnia export has no resources list.", importer.Location{Line: root.Line, Path: "resources"})
	}
	if v := sget(root, "__export_format"); v != "" && v != "4" && v != "3" {
		m.Diagnostics = append(m.Diagnostics, importer.Diagnostic{
			Code: CodeVersion, Severity: importer.SeverityInfo,
			Source:  importer.Location{Line: get(root, "__export_format").Line, Path: "__export_format"},
			Message: "The export format " + v + " is read as version 4.",
		})
	}

	var all []*entry
	byID := map[string]*entry{}
	for i, n := range resources.Items {
		n = n.Resolve()
		if !isMap(n) {
			continue
		}
		e := &entry{n: n, typ: sget(n, "_type"), id: sget(n, "_id"), parent: sget(n, "parentId"),
			loc: importer.Location{Line: n.Line, Column: n.Column, Path: "resources[" + strconv.Itoa(i) + "]"}}
		all = append(all, e)
		if e.id != "" && byID[e.id] == nil {
			byID[e.id] = e
		}
	}

	protoFiles := map[string]string{}
	payloads := map[string][]*entry{}
	var workspaces, envs []*entry
	cookies := 0
	cookieLoc := importer.Location{}
	for _, e := range all {
		switch e.typ {
		case "workspace":
			workspaces = append(workspaces, e)
		case "environment":
			envs = append(envs, e)
		case "proto_file":
			protoFiles[e.id] = sget(e.n, "name")
		case "websocket_payload":
			payloads[e.parent] = append(payloads[e.parent], e)
		case "cookie_jar":
			if c := len(items(get(e.n, "cookies"))); c > 0 {
				if cookies == 0 {
					cookieLoc = e.loc
				}
				cookies += c
			}
		}
	}

	// The folder tree.
	var nodes []*res
	children := map[string][]*res{}
	for _, e := range all {
		r := v4Resource(e, protoFiles, payloads)
		if r == nil {
			continue
		}
		nodes = append(nodes, r)
		children[r.parent] = append(children[r.parent], r)
	}
	isWorkspace := func(id string) bool { e := byID[id]; return e != nil && e.typ == "workspace" }
	visited := map[*res]bool{}
	var build func(parent string) []*res
	build = func(parent string) []*res {
		kids := sorted(children[parent])
		out := kids[:0:0]
		for _, k := range kids {
			if visited[k] {
				continue
			}
			visited[k] = true
			if k.kind == kindFolder {
				k.children = build(k.id)
			}
			out = append(out, k)
		}
		return out
	}

	var top []*res
	switch len(workspaces) {
	case 1:
		ws := workspaces[0]
		m.Name = sget(ws.n, "name")
		m.Root.Description = sget(ws.n, "description")
		m.Root.Source = ws.loc
		top = build(ws.id)
	default:
		for _, ws := range workspaces {
			f := &res{kind: kindFolder, id: ws.id, name: sget(ws.n, "name"), desc: sget(ws.n, "description"), loc: ws.loc}
			f.children = build(ws.id)
			top = append(top, f)
		}
	}
	// Items whose folder is not in the file go to the top level.
	var orphans []*res
	for _, r := range nodes {
		if p := byID[r.parent]; !visited[r] && !isWorkspace(r.parent) && (p == nil || p.typ != "request_group") {
			orphans = append(orphans, r)
		}
	}
	for _, r := range sorted(orphans) {
		if visited[r] {
			continue
		}
		visited[r] = true
		if r.kind == kindFolder {
			r.children = build(r.id)
		}
		top = append(top, r)
	}
	if len(orphans) > 0 && len(workspaces) > 0 {
		m.Diagnostics = append(m.Diagnostics, importer.Diagnostic{
			Code: CodeOrphan, Severity: importer.SeverityWarning, Source: orphans[0].loc,
			Message: strconv.Itoa(len(orphans)) + " item(s) whose folder is not in the file were put at the top level.",
		})
	}
	// Items in a loop of parent IDs are never reached.
	for _, r := range nodes {
		if !visited[r] {
			top = append(top, &res{kind: kindSkipped, skipped: &importer.Skipped{
				Kind: r.kind, Name: r.name, Code: CodeOrphan, Source: r.loc,
				Reason: "Its folder is part of a loop of parent IDs, so it has no place in the tree.",
			}})
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, errs.Wrap(err, errs.KindOf(err), "Reading the file was interrupted.")
	}
	m.Root.Items = mapChildren(top)

	// Environments: each sub-environment with its base's variables; a base
	// without sub-environments on its own.
	isEnv := map[string]bool{}
	for _, e := range envs {
		isEnv[e.id] = true
	}
	for _, base := range envs {
		if isEnv[base.parent] {
			continue
		}
		prefix := ""
		if len(workspaces) > 1 {
			if ws := byID[base.parent]; ws != nil {
				prefix = sget(ws.n, "name") + " / "
			}
		}
		var subs []*envSource
		var subEntries []*entry
		for _, e := range envs {
			if e.parent == base.id && e != base {
				subEntries = append(subEntries, e)
			}
		}
		slices.SortStableFunc(subEntries, func(a, b *entry) int { return cmpNodes(a.n, b.n, "metaSortKey") })
		for _, e := range subEntries {
			subs = append(subs, &envSource{n: e.n, name: sget(e.n, "name"), loc: e.loc, private: isTrue(get(e.n, "isPrivate"))})
		}
		m.Environments = append(m.Environments, environmentsOf(
			&envSource{n: base.n, name: sget(base.n, "name"), loc: base.loc, private: isTrue(get(base.n, "isPrivate"))},
			subs, prefix)...)
	}

	if cookies > 0 {
		m.Diagnostics = append(m.Diagnostics, cookieDiag(cookies, cookieLoc))
	}
	return m, nil
}

// v4Resource reads a resource of the folder tree; nil for other types.
func v4Resource(e *entry, protoFiles map[string]string, payloads map[string][]*entry) *res {
	n := e.n
	r := &res{id: e.id, parent: e.parent, name: sget(n, "name"), desc: sget(n, "description"), loc: e.loc}
	r.sort, r.hasSort = sortKey(n, "metaSortKey")
	switch e.typ {
	case "request_group":
		r.kind = kindFolder
	case "request":
		r.kind = kindRequest
	case "grpc_request":
		r.kind = kindGRPC
		if id := sget(n, "protoFileId"); id != "" {
			r.protoFile = protoFiles[id]
			if r.protoFile == "" {
				r.protoFile = id
			}
		}
	case "websocket_request":
		r.kind = kindWebSocket
		ps := payloads[e.id]
		slices.SortStableFunc(ps, func(a, b *entry) int { return cmpNodes(a.n, b.n, "metaSortKey") })
		for _, p := range ps {
			r.payloads = append(r.payloads, p.n)
		}
	default:
		if ignored[e.typ] || e.typ == "" {
			return nil
		}
		if e.typ == "api_spec" && sget(n, "contents") == "" {
			return nil
		}
		kind := skippedKinds[e.typ]
		reason := "PostSonde does not import Insomnia " + kind + "s."
		switch e.typ {
		case "api_spec":
			if r.name == "" {
				r.name = sget(n, "fileName")
			}
			reason = "The API spec is not imported with the collection: save it as a file and import it with the OpenAPI format."
		}
		if kind == "" {
			kind = e.typ
			reason = "PostSonde does not import Insomnia “" + e.typ + "” items."
		}
		r.kind = kindSkipped
		r.skipped = &importer.Skipped{Kind: kind, Name: r.name, Code: CodeUnsupported, Reason: reason, Source: e.loc}
		return r
	}
	fillCommon(r, n)
	r.preScript, r.postScript = sget(n, "preRequestScript"), sget(n, "afterResponseScript")
	r.followRedirects = sget(n, "settingFollowRedirects")
	for _, s := range []struct{ key, name string }{
		{"settingSendCookies", offSendCookies}, {"settingStoreCookies", offStoreCookies},
		{"settingEncodeUrl", offEncodeURL}, {"settingRebuildPath", offRebuildPath},
	} {
		if isFalse(get(n, s.key)) {
			r.off = append(r.off, s.name)
		}
	}
	return r
}

// fillCommon reads the members both versions name alike.
func fillCommon(r *res, n *yaml.Node) {
	r.url = sget(n, "url")
	r.method = sget(n, "method")
	r.body = get(n, "body")
	r.params = get(n, "parameters")
	r.pathParams = get(n, "pathParameters")
	r.headers = get(n, "headers")
	r.auth = get(n, "authentication")
	r.env = get(n, "environment")
	r.metadata = get(n, "metadata")
	r.protoMethod = sget(n, "protoMethodName")
	r.reflection = isTrue(path(n, "reflectionApi", "enabled"))
}

func sortKey(n *yaml.Node, key string) (float64, bool) {
	f, ok := number(get(n, key))
	if !ok || math.IsNaN(f) {
		return 0, false
	}
	return f, true
}

func cmpNodes(a, b *yaml.Node, key string) int {
	ak, aok := sortKey(a, key)
	bk, bok := sortKey(b, key)
	return cmpSort(ak, aok, bk, bok)
}

func cmpSort(a float64, aok bool, b float64, bok bool) int {
	switch {
	case aok && bok:
		if a < b {
			return -1
		}
		if a > b {
			return 1
		}
		return 0
	case aok:
		return -1
	case bok:
		return 1
	}
	return 0
}

// sorted returns rs in Insomnia's order: by sort key, items without one
// last, ties in file order.
func sorted(rs []*res) []*res {
	out := slices.Clone(rs)
	slices.SortStableFunc(out, func(a, b *res) int { return cmpSort(a.sort, a.hasSort, b.sort, b.hasSort) })
	return out
}

func cookieDiag(n int, loc importer.Location) importer.Diagnostic {
	return importer.Diagnostic{
		Code: CodeCookies, Severity: importer.SeverityInfo, Source: loc,
		Message: "The cookie jar (" + strconv.Itoa(n) + " cookie(s)) is not imported: PostSonde keeps the cookies servers set as requests are sent.",
	}
}
