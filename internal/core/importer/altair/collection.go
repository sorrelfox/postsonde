package altair

import (
	"context"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/importer"
	"github.com/sorrelfox/postsonde/internal/core/yaml"
)

// maxCollectionDepth is the deepest nesting of sub-collections this
// reader follows.
const maxCollectionDepth = 64

// collectionNode is a collection of the file: the exported one (index 0)
// and every sub-collection, nested (collections) or listed flat with a
// parentPath.
type collectionNode struct {
	folder *importer.Folder
	loc    importer.Location
	id     string
	// parentID is the last ID of parentPath; "" when it has none.
	parentID string
	// container is the index of the collection whose collections list
	// holds this one; -1 for the exported collection.
	container int
	// parent is the index of the collection it goes into.
	parent int
}

// parseCollection reads a collection export (.agc): the collection
// becomes the import's folder, its queries requests and its
// sub-collections folders.
func parseCollection(ctx context.Context, root *yaml.Node) (*importer.Model, error) {
	var nodes []*collectionNode
	var walk func(n *yaml.Node, p string, container, depth int) error
	walk = func(n *yaml.Node, p string, container, depth int) error {
		if err := ctx.Err(); err != nil {
			return errs.Wrap(err, errs.KindOf(err), "Reading the file was interrupted.")
		}
		loc := importer.Location{Line: n.Line, Column: n.Column, Path: p}
		c := &collectionNode{loc: loc, id: sget(n, "id"), parentID: lastID(sget(n, "parentPath")), container: container, parent: container}
		c.folder = collectionFolder(n, p, loc)
		idx := len(nodes)
		nodes = append(nodes, c)
		for i, sub := range items(get(n, "collections")) {
			sp := join(p, "collections["+strconv.Itoa(i)+"]")
			sl := importer.Location{Line: sub.Line, Column: sub.Column, Path: sp}
			switch {
			case !isMap(sub):
				c.folder.Items = append(c.folder.Items, importer.SkippedItem(&importer.Skipped{
					Kind: "collection", Code: CodeUnsupported, Source: sl,
					Reason: "The sub-collection is not a JSON object.",
				}))
			case depth >= maxCollectionDepth:
				c.folder.Items = append(c.folder.Items, importer.SkippedItem(&importer.Skipped{
					Kind: "collection", Name: strings.TrimSpace(sget(sub, "title")), Code: CodeUnsupported, Source: sl,
					Reason: "The sub-collection is nested more than " + strconv.Itoa(maxCollectionDepth) + " levels deep.",
				}))
			default:
				if err := walk(sub, sp, idx, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(root, "", -1, 0); err != nil {
		return nil, err
	}
	place(nodes)

	// Sub-collections go before the queries, in file order.
	children := make([][]*collectionNode, len(nodes))
	for _, c := range nodes[1:] {
		children[c.parent] = append(children[c.parent], c)
	}
	for i, c := range nodes {
		var subs []importer.Item
		for _, ch := range children[i] {
			subs = append(subs, importer.FolderItem(ch.folder))
		}
		c.folder.Items = append(subs, c.folder.Items...)
	}
	m := &importer.Model{Name: nodes[0].folder.Name, Root: *nodes[0].folder}
	return m, nil
}

// place sets each sub-collection's parent: the collection its parentPath
// names when that is in the file and not a loop, else the one whose list
// holds it. Nested exports name their container anyway; flat lists need
// the parentPath.
func place(nodes []*collectionNode) {
	byID := map[string]int{}
	for i, c := range nodes {
		if c.id != "" {
			if _, dup := byID[c.id]; !dup {
				byID[c.id] = i
			}
		}
	}
	for i, c := range nodes[1:] {
		i++
		if c.parentID == "" || c.parentID == nodes[c.container].id {
			continue
		}
		p, ok := byID[c.parentID]
		if p == i {
			continue
		}
		if !ok {
			c.folder.Diagnostics = append(c.folder.Diagnostics, importer.Diagnostic{
				Code: CodeOrphan, Severity: importer.SeverityWarning, Source: c.loc,
				Message: "The sub-collection's parent (parentPath) is not in the file; it is placed in the collection that lists it.",
			})
			continue
		}
		c.parent = p
	}
	// Break loops of parents: a collection that does not reach the
	// exported one goes back into the collection that lists it, which
	// always does.
	for i, c := range nodes[1:] {
		i++
		seen := map[int]bool{i: true}
		for j := c.parent; j > 0; j = nodes[j].parent {
			if seen[j] {
				c.parent = c.container
				c.folder.Diagnostics = append(c.folder.Diagnostics, importer.Diagnostic{
					Code: CodeOrphan, Severity: importer.SeverityWarning, Source: c.loc,
					Message: "The sub-collection's parentPath makes a loop of parents; it is placed in the collection that lists it.",
				})
				break
			}
			seen[j] = true
		}
	}
}

// lastID returns the last ID of an Altair parentPath ("/1/4" is 4).
func lastID(p string) string {
	parts := strings.FieldsFunc(p, func(r rune) bool { return r == '/' })
	if len(parts) == 0 {
		return ""
	}
	return strings.TrimSpace(parts[len(parts)-1])
}

// collectionFolder maps a collection's own members and queries; its
// sub-collections are added by parseCollection.
func collectionFolder(n *yaml.Node, p string, loc importer.Location) *importer.Folder {
	f := &importer.Folder{Name: strings.TrimSpace(sget(n, "title")), Source: loc}
	if f.Name == "" {
		f.Name = strings.TrimSpace(sget(n, "name"))
	}
	if f.Name == "" {
		f.Name = "Collection"
	}
	f.Description, f.Diagnostics = scripts(strings.TrimSpace(sget(n, "description")), "collection", loc,
		script{"pre-request", get(get(n, "preRequest"), "script"), get(get(n, "preRequest"), "enabled")},
		script{"post-request", get(get(n, "postRequest"), "script"), get(get(n, "postRequest"), "enabled")})
	for i, q := range items(get(n, "queries")) {
		qp := join(p, "queries["+strconv.Itoa(i)+"]")
		ql := importer.Location{Line: q.Line, Column: q.Column, Path: qp}
		if !isMap(q) {
			f.Items = append(f.Items, importer.SkippedItem(&importer.Skipped{
				Kind: "query", Code: CodeUnsupported, Source: ql,
				Reason: "The query is not a JSON object.",
			}))
			continue
		}
		f.Items = append(f.Items, importer.RequestItem(window(q, ql)))
	}
	return f
}

func join(p, s string) string {
	if p == "" {
		return s
	}
	return p + "." + s
}
