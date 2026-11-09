package insomnia

import (
	"context"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/importer"
	"github.com/sorrelfox/postsonde/internal/core/yaml"
)

// maxNesting is the deepest folder nesting read from a v5 file.
const maxNesting = 256

// parseV5 reads a v5 file: a collection (or design document) with its
// folder tree nested under `collection`, its environments, cookie jar and
// spec; or an environment file.
func parseV5(ctx context.Context, root *yaml.Node) (*importer.Model, error) {
	typ := sget(root, "type")
	kind, version, _ := strings.Cut(typ, ".insomnia.rest/")
	m := &importer.Model{Name: sget(root, "name")}
	m.Root.Description = str(path(root, "meta", "description"))
	m.Root.Source = importer.Location{Line: root.Line, Column: root.Column}
	if !strings.HasPrefix(version, "5.") {
		m.Diagnostics = append(m.Diagnostics, importer.Diagnostic{
			Code: CodeVersion, Severity: importer.SeverityInfo,
			Source:  locOf(get(root, "type"), "type"),
			Message: "The Insomnia file version " + version + " is read as version 5.",
		})
	}

	var top []*res
	switch kind {
	case "mock":
		top = append(top, &res{kind: kindSkipped, skipped: &importer.Skipped{
			Kind: "mock server", Name: m.Name, Code: CodeUnsupported, Source: m.Root.Source,
			Reason: "PostSonde does not import Insomnia mock servers.",
		}})
	default:
		top = v5Items(get(root, "collection"), "collection", 0)
		if spec := get(root, "spec"); spec != nil && hasContent(get(spec, "contents")) {
			top = append(top, &res{kind: kindSkipped, skipped: &importer.Skipped{
				Kind: "API spec", Name: m.Name, Code: CodeUnsupported, Source: locOf(spec, "spec"),
				Reason: "The API spec is not imported with the collection: save it as a file and import it with the OpenAPI format.",
			}})
		}
		for i, s := range items(get(root, "testSuites")) {
			top = append(top, &res{kind: kindSkipped, skipped: &importer.Skipped{
				Kind: "test suite", Name: sget(s, "name"), Code: CodeUnsupported,
				Source: locOf(s, "testSuites["+strconv.Itoa(i)+"]"),
				Reason: "PostSonde does not import Insomnia test suites.",
			}})
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, errs.Wrap(err, errs.KindOf(err), "Reading the file was interrupted.")
	}
	m.Root.Items = mapChildren(top)

	if env := get(root, "environments"); isMap(env) {
		base := v5Env(env, "environments")
		var subs []*envSource
		for i, s := range items(get(env, "subEnvironments")) {
			subs = append(subs, v5Env(s, "environments.subEnvironments["+strconv.Itoa(i)+"]"))
		}
		m.Environments = environmentsOf(base, subs, "")
	}
	if jar := get(root, "cookieJar"); jar != nil {
		if c := len(items(get(jar, "cookies"))); c > 0 {
			m.Diagnostics = append(m.Diagnostics, cookieDiag(c, locOf(jar, "cookieJar")))
		}
	}
	return m, nil
}

func v5Env(n *yaml.Node, p string) *envSource {
	return &envSource{
		n: n, name: sget(n, "name"), loc: locOf(n, p),
		private: isTrue(path(n, "meta", "isPrivate")) || isTrue(get(n, "isPrivate")),
	}
}

// v5Items reads the items of a collection or folder, in file order.
func v5Items(seq *yaml.Node, p string, depth int) []*res {
	var out []*res
	for i, n := range items(seq) {
		n = n.Resolve()
		if !isMap(n) {
			continue
		}
		ip := p + "[" + strconv.Itoa(i) + "]"
		r := v5Resource(n, ip)
		if r.kind == kindFolder {
			if depth >= maxNesting {
				r = &res{kind: kindSkipped, skipped: &importer.Skipped{
					Kind: "folder", Name: r.name, Code: CodeUnsupported, Source: r.loc,
					Reason: "The folder is nested too deeply.",
				}}
			} else {
				r.children = v5Items(get(n, "children"), ip+".children", depth+1)
			}
		}
		out = append(out, r)
	}
	return out
}

// v5Resource reads one item of a v5 collection. Its kind comes from its
// members and its ID's prefix (fld_, req_, greq_, ws-req_).
func v5Resource(n *yaml.Node, p string) *res {
	id := str(path(n, "meta", "id"))
	r := &res{id: id, name: sget(n, "name"), desc: str(path(n, "meta", "description")), loc: locOf(n, p)}
	switch {
	case get(n, "children") != nil || strings.HasPrefix(id, "fld_"):
		r.kind = kindFolder
	case strings.HasPrefix(id, "greq_") || get(n, "protoMethodName") != nil:
		r.kind = kindGRPC
		if pf := sget(n, "protoFileId"); pf != "" {
			r.protoFile = pf
		}
	case strings.HasPrefix(id, "ws-req_"):
		r.kind = kindWebSocket
	case strings.HasPrefix(id, "socketio"):
		r.kind = kindSkipped
		r.skipped = &importer.Skipped{Kind: "socket.io request", Name: r.name, Code: CodeUnsupported, Source: r.loc,
			Reason: "PostSonde does not import Insomnia socket.io requests."}
		return r
	case get(n, "url") != nil:
		r.kind = kindRequest
	default:
		r.kind = kindSkipped
		r.skipped = &importer.Skipped{Kind: "item", Name: r.name, Code: CodeUnsupported, Source: r.loc,
			Reason: "The item is neither a folder nor a request PostSonde knows."}
		return r
	}
	fillCommon(r, n)
	r.preScript, r.postScript = str(path(n, "scripts", "preRequest")), str(path(n, "scripts", "afterResponse"))
	s := get(n, "settings")
	r.followRedirects = sget(s, "followRedirects")
	for _, o := range []struct {
		n    *yaml.Node
		name string
	}{
		{path(s, "cookies", "send"), offSendCookies}, {path(s, "cookies", "store"), offStoreCookies},
		{get(s, "encodeUrl"), offEncodeURL}, {get(s, "rebuildPath"), offRebuildPath},
	} {
		if isFalse(o.n) {
			r.off = append(r.off, o.name)
		}
	}
	return r
}

// hasContent reports whether a spec's contents are not empty.
func hasContent(n *yaml.Node) bool {
	n = n.Resolve()
	if n == nil {
		return false
	}
	switch n.Kind {
	case yaml.MappingNode:
		return len(n.Pairs) > 0
	case yaml.SequenceNode:
		return len(n.Items) > 0
	}
	return strings.TrimSpace(str(n)) != ""
}

func locOf(n *yaml.Node, p string) importer.Location {
	if n == nil {
		return importer.Location{Path: p}
	}
	return importer.Location{Line: n.Line, Column: n.Column, Path: p}
}
