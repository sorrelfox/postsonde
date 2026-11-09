package postman

import (
	"context"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/importer"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/yaml"
)

// Collection schema versions.
const (
	v20 = "2.0"
	v21 = "2.1"
)

// maxItemDepth is the deepest nesting of item groups this reader follows.
const maxItemDepth = 64

// parseCollection reads a Collection v2.0 or v2.1: the collection becomes
// the model's root folder, item groups folders, items requests.
func parseCollection(ctx context.Context, root *yaml.Node) (*importer.Model, error) {
	m := &importer.Model{}
	info := get(root, "info")
	version, diag := schemaVersion(root, info)
	if version == "1" {
		return nil, v1(root)
	}
	if diag != nil {
		m.Diagnostics = append(m.Diagnostics, *diag)
	}
	if items := get(root, "item"); items != nil && items.Kind != yaml.SequenceNode {
		return nil, importer.InvalidFile(format, "The collection's item member is not a list.", importer.Location{Line: items.Line, Path: "item"})
	}
	m.Name = strings.TrimSpace(sget(info, "name"))
	c := &collector{ctx: ctx, version: version}
	loc := importer.Location{Line: root.Line, Column: root.Column}
	m.Root = *c.folder(root, "", loc, behavior{}, 0)
	m.Root.Name = m.Name
	if d := description(get(info, "description")); d != "" && m.Root.Description == "" {
		m.Root.Description = d
	} else if d != "" {
		m.Root.Description = d + "\n\n" + m.Root.Description
	}
	if c.err != nil {
		return nil, c.err
	}
	return m, nil
}

// schemaVersion returns "2.0", "2.1" or "1" from info.schema, and an info
// when the file names another version or none.
func schemaVersion(root, info *yaml.Node) (string, *importer.Diagnostic) {
	schema := sget(info, "schema")
	at := importer.Location{Path: "info.schema"}
	if n := get(info, "schema"); n != nil {
		at.Line, at.Column = n.Line, n.Column
	} else {
		at = importer.Location{Line: root.Line, Path: "info"}
	}
	switch {
	case strings.Contains(schema, "/collection/v2.1"):
		return v21, nil
	case strings.Contains(schema, "/collection/v2.0"):
		return v20, nil
	case strings.Contains(schema, "/collection/v1"):
		return "1", nil
	case schema == "":
		return v21, &importer.Diagnostic{Code: CodeVersion, Severity: importer.SeverityInfo, Source: at,
			Message: "The collection names no schema version; it is read as Collection v2.1."}
	}
	return v21, &importer.Diagnostic{Code: CodeVersion, Severity: importer.SeverityInfo, Source: at,
		Message: "The collection's schema is not v2.0 or v2.1; it is read as v2.1."}
}

// collector walks the item tree.
type collector struct {
	ctx     context.Context
	version string
	err     error
}

// folder maps the collection (p "") or an item group, with everything
// inside. inherited is the protocolProfileBehavior of the folders above.
func (c *collector) folder(n *yaml.Node, p string, loc importer.Location, inherited behavior, depth int) *importer.Folder {
	t := &texts{}
	f := &importer.Folder{Name: strings.TrimSpace(sget(n, "name")), Source: loc}
	if f.Name == "" {
		f.Name = "Folder"
	}
	what := "folder"
	if p == "" {
		what = "collection"
	}
	f.Description = description(get(n, "description"))
	f.Description, f.Diagnostics = withScripts(f.Description, get(n, "event"), what, loc)
	f.Variables = variables(get(n, "variable"), t)
	var d []importer.Diagnostic
	f.Auth, d = c.auth(get(n, "auth"), t, loc)
	f.Diagnostics = append(f.Diagnostics, d...)
	f.Diagnostics = append(f.Diagnostics, t.diagnostics(loc)...)
	own := inherited.with(get(n, "protocolProfileBehavior"))
	for i, it := range items(get(n, "item")) {
		if c.err != nil {
			return f
		}
		if err := c.ctx.Err(); err != nil {
			c.err = errs.Wrap(err, errs.KindOf(err), "Reading the file was interrupted.")
			return f
		}
		ip := join(p, "item["+strconv.Itoa(i)+"]")
		il := importer.Location{Line: it.Line, Column: it.Column, Path: ip}
		name := strings.TrimSpace(sget(it, "name"))
		switch {
		case !isMap(it):
			f.Items = append(f.Items, importer.SkippedItem(&importer.Skipped{
				Kind: "item", Code: CodeUnsupported, Source: il,
				Reason: "The item is not a JSON object, so it is not a request or a folder.",
			}))
		case get(it, "item") != nil:
			if depth >= maxItemDepth {
				f.Items = append(f.Items, importer.SkippedItem(&importer.Skipped{
					Kind: "folder", Name: name, Code: CodeUnsupported, Source: il,
					Reason: "The folder is nested more than " + strconv.Itoa(maxItemDepth) + " levels deep.",
				}))
				continue
			}
			f.Items = append(f.Items, importer.FolderItem(c.folder(it, ip, il, own, depth+1)))
		case get(it, "request") != nil:
			f.Items = append(f.Items, importer.RequestItem(c.request(it, il, own)))
		default:
			f.Items = append(f.Items, importer.SkippedItem(&importer.Skipped{
				Kind: "item", Name: name, Code: CodeUnsupported, Source: il,
				Reason: "The item has neither a request nor items.",
			}))
		}
	}
	return f
}

func join(p, s string) string {
	if p == "" {
		return s
	}
	return p + "." + s
}

// variables maps collection or folder variables. A "secret" variable is a
// secret one: its value goes to the local side file.
func variables(n *yaml.Node, t *texts) []projectfile.Variable {
	var out []projectfile.Variable
	for _, v := range items(n) {
		name := sget(v, "key")
		if name == "" {
			name = sget(v, "name")
		}
		if name == "" {
			name = sget(v, "id")
		}
		if name == "" {
			continue
		}
		out = append(out, projectfile.Variable{
			Name:        name,
			Value:       t.conv(str(get(v, "value")), "a variable"),
			Secret:      sget(v, "type") == "secret",
			Description: description(get(v, "description")),
			Disabled:    isTrue(get(v, "disabled")),
		})
	}
	return out
}
