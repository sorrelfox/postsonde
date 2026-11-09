package collection

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// Load reads the request tree of the project whose root directory is dir
// (absolute). It never writes, and it never fails as a whole: a file or
// directory that cannot be loaded becomes a problem, and a node where it
// helps to see it (see docs/collection.md), while the rest loads. The
// problems are also kept in Tree.Problems.
func Load(dir string) (*Tree, []Problem) {
	l := &loader{t: &Tree{dir: dir, byID: map[string]*Node{}, byPath: map[string]*Node{}}}
	root := &Node{Kind: KindRoot}
	l.t.root = root
	l.loadRoot(root)
	l.loadChildren(root, 0)
	l.assignIDs()
	return l.t, l.t.Problems()
}

type loader struct {
	t *Tree
	// nodes in walk order, the root first.
	nodes []*Node
	// fileIDs[i] is the id written in nodes[i]'s file, or "".
	fileIDs []string
	// broken[i] is set when nodes[i]'s own file did not load, so a missing
	// id is not reported on top of that problem.
	broken []bool
	// problemNodes[i] is the node of t.problems[i], or nil; its ID is only
	// known after assignIDs.
	problemNodes []*Node
}

func (l *loader) problem(rel string, n *Node, err error) {
	var e *errs.Error
	if !errors.As(err, &e) {
		e = errs.Wrap(err, errs.KindOf(err), "A project file could not be loaded.")
	}
	if _, ok := e.Details["path"]; !ok {
		e = e.WithDetail("path", l.t.Abs(rel))
	}
	l.t.problems = append(l.t.problems, Problem{Path: rel, Err: e})
	l.problemNodes = append(l.problemNodes, n)
}

func (l *loader) add(n *Node, fileID string, broken bool) {
	l.nodes = append(l.nodes, n)
	l.fileIDs = append(l.fileIDs, fileID)
	l.broken = append(l.broken, broken)
}

func (l *loader) loadRoot(root *Node) {
	rel := projectfile.WorkspaceFileName
	data, err := os.ReadFile(l.t.Abs(rel))
	if err != nil {
		l.add(root, "", true)
		root.ReadOnly = ReadOnlyInvalid
		l.problem(rel, root, readFailed(err))
		return
	}
	root.setSum(data)
	w, err := projectfile.DecodeWorkspace(data)
	if err != nil {
		h := peek(data)
		root.Name, root.FileKind, root.order = h.name, h.kind, h.order
		root.ReadOnly = ReadOnlyInvalid
		l.add(root, h.id, true)
		l.problem(rel, root, err)
		return
	}
	root.File, root.Name, root.FileKind = w, w.Name, projectfile.KindWorkspace
	if w.Newer() {
		root.ReadOnly = ReadOnlyNewerVersion
	}
	l.add(root, w.ID, false)
}

// loadChildren lists the directory of a root or folder node and loads its
// items.
func (l *loader) loadChildren(parent *Node, depth int) {
	abs := l.t.Abs(parent.Path)
	entries, err := os.ReadDir(abs)
	if err != nil {
		// ReadDir returns the entries it read before the error.
		l.problem(parent.Path, parent, errs.Wrap(err, errs.KindOf(err), "A project folder could not be read.").
			WithCode(CodeReadDirFailed).
			WithHint("Check that the folder exists and is readable.").
			WithDetail("path", abs))
	}
	var children []*Node
	for _, e := range entries {
		name := e.Name()
		if skipEntry(name, parent.Kind == KindRoot) {
			continue
		}
		rel := path.Join(parent.Path, name)
		mode := e.Type()
		symlink := mode&fs.ModeSymlink != 0
		if symlink {
			fi, err := os.Stat(filepath.Join(abs, name))
			if err != nil {
				continue // a dangling link is not a project file
			}
			if fi.IsDir() {
				continue // links to directories are not followed
			}
			mode = fi.Mode().Type()
		}
		var n *Node
		switch {
		case mode.IsDir():
			n = l.loadFolder(parent, name, rel, depth+1)
		case mode.IsRegular() && strings.HasSuffix(name, projectfile.Ext):
			n = l.loadFile(parent, name, rel, symlink)
		}
		if n != nil {
			children = append(children, n)
		}
	}
	parent.Children = applyOrder(parent.Order(), children)
}

// skipEntry reports whether a directory entry is never part of the tree:
// names starting with "_" or "." (which covers _folder.json and the
// .<name>.tmp-* leftovers of atomic saves) and, at the root, the workspace
// file and the environments directory.
func skipEntry(name string, root bool) bool {
	if strings.HasPrefix(name, "_") || strings.HasPrefix(name, ".") {
		return true
	}
	return root && isReservedAtRoot(name)
}

// loadFolder loads a directory with a _folder.json, and returns nil for a
// directory without one.
func (l *loader) loadFolder(parent *Node, name, rel string, depth int) *Node {
	n := &Node{Kind: KindFolder, Entry: name, Path: rel, Parent: parent}
	fileRel := n.FilePath()
	data, err := os.ReadFile(l.t.Abs(fileRel))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		if fi, statErr := os.Stat(l.t.Abs(fileRel)); statErr == nil && fi.IsDir() {
			return nil // a directory named _folder.json is not a folder file
		}
		n.ReadOnly = ReadOnlyInvalid
		l.add(n, "", true)
		l.problem(fileRel, n, readFailed(err))
	default:
		n.setSum(data)
		f, err := projectfile.DecodeFolder(data)
		if err != nil {
			h := peek(data)
			n.Name, n.FileKind, n.order = h.name, h.kind, h.order
			n.ReadOnly = ReadOnlyInvalid
			l.add(n, h.id, true)
			l.problem(fileRel, n, err)
			break
		}
		n.File, n.Name, n.FileKind = f, f.Name, projectfile.KindFolder
		if f.Newer() {
			n.ReadOnly = ReadOnlyNewerVersion
		}
		l.add(n, f.ID, false)
	}
	if depth > MaxDepth {
		l.problem(rel, n, errs.New(errs.Unsupported, fmt.Sprintf("Folders are nested more than %d levels deep; the deeper ones are not shown.", MaxDepth)).
			WithCode(CodeTooDeep).
			WithHint("Move the inner folders up."))
		return n
	}
	l.loadChildren(n, depth)
	return n
}

// loadFile loads a .json file in a root or folder directory. It returns
// nil for a file that is not a project file: valid JSON that is not an
// object with a kind, or invalid JSON that does not even mention "kind"
// (a tsconfig.json with comments next to the requests).
func (l *loader) loadFile(parent *Node, name, rel string, symlink bool) *Node {
	n := &Node{Entry: name, Path: rel, Parent: parent}
	data, err := os.ReadFile(l.t.Abs(rel))
	if err != nil {
		n.Kind, n.ReadOnly = KindInvalid, ReadOnlyInvalid
		l.add(n, "", true)
		l.problem(rel, n, readFailed(err))
		return n
	}
	n.setSum(data)
	f, err := projectfile.Decode(data)
	if err != nil {
		h := peek(data)
		var e *errs.Error
		errors.As(err, &e)
		switch {
		case h.object && h.kind == "",
			!h.object && json.Valid(data),
			!h.object && !bytes.Contains(data, []byte(`"kind"`)):
			return nil
		case e != nil && e.Code == projectfile.CodeUnknownKind:
			n.Kind, n.ReadOnly = KindUnknown, ReadOnlyUnknownKind
			n.Name, n.FileKind = h.name, h.kind
			l.add(n, h.id, false)
		default:
			n.Kind, n.ReadOnly = KindInvalid, ReadOnlyInvalid
			n.Name, n.FileKind = h.name, h.kind
			l.add(n, h.id, true)
			l.problem(rel, n, err)
		}
		return n
	}
	if !f.FileKind().IsRequest() {
		h := peek(data)
		n.Kind, n.ReadOnly = KindInvalid, ReadOnlyInvalid
		n.Name, n.FileKind = h.name, f.FileKind()
		l.add(n, h.id, true)
		l.problem(rel, n, errs.New(errs.Parse, fmt.Sprintf("The file is of kind %q, which does not belong among requests.", f.FileKind())).
			WithCode(projectfile.CodeWrongKind).
			WithHint("Move the file where files of its kind belong, or give it a request kind.").
			WithDetail("kind", string(f.FileKind())).
			WithDetail("expected", "request"))
		return n
	}
	n.Kind, n.File, n.FileKind = KindRequest, f, f.FileKind()
	id := ""
	switch r := f.(type) {
	case *projectfile.HTTPRequest:
		n.Name, id = r.Name, r.ID
		if r.Newer() {
			n.ReadOnly = ReadOnlyNewerVersion
		}
	case *projectfile.GraphQLRequest:
		n.Name, id = r.Name, r.ID
		if r.Newer() {
			n.ReadOnly = ReadOnlyNewerVersion
		}
	case *projectfile.TCPRequest:
		n.Name, id = r.Name, r.ID
		if r.Newer() {
			n.ReadOnly = ReadOnlyNewerVersion
		}
	case *projectfile.UDPRequest:
		n.Name, id = r.Name, r.ID
		if r.Newer() {
			n.ReadOnly = ReadOnlyNewerVersion
		}
	case *projectfile.WebSocketRequest:
		n.Name, id = r.Name, r.ID
		if r.Newer() {
			n.ReadOnly = ReadOnlyNewerVersion
		}
	case *projectfile.GRPCRequest:
		n.Name, id = r.Name, r.ID
		if r.Newer() {
			n.ReadOnly = ReadOnlyNewerVersion
		}
	case *projectfile.SSERequest:
		n.Name, id = r.Name, r.ID
		if r.Newer() {
			n.ReadOnly = ReadOnlyNewerVersion
		}
	case *projectfile.SocketIORequest:
		n.Name, id = r.Name, r.ID
		if r.Newer() {
			n.ReadOnly = ReadOnlyNewerVersion
		}
	}
	if symlink && n.ReadOnly == Writable {
		n.ReadOnly = ReadOnlySymlink
	}
	l.add(n, id, false)
	return n
}

// assignIDs gives every node its ID and indexes the tree. A file keeps its
// id unless another file has the same one: then the file with the
// smallest path (the root first) keeps it and each other one gets an ID
// derived from its id and path. A file without an id gets one derived from
// its path. Derived IDs are stable across loads, so local data keyed by
// them survives a restart until the file is saved with the new ID.
func (l *loader) assignIDs() {
	byID := map[string][]int{}
	for i, id := range l.fileIDs {
		if strings.TrimSpace(id) != "" {
			byID[id] = append(byID[id], i)
		}
	}
	taken := map[string]bool{}
	for id := range byID {
		taken[id] = true
	}
	derive := func(n *Node, seed string) string {
		prefix := projectfile.RequestIDPrefix
		switch n.Kind {
		case KindRoot:
			prefix = projectfile.WorkspaceIDPrefix
		case KindFolder:
			prefix = projectfile.FolderIDPrefix
		}
		for i := 0; ; i++ {
			sum := sha256.Sum256([]byte(seed + "\x00" + strconv.Itoa(i)))
			id := prefix + hex.EncodeToString(sum[:8])
			if !taken[id] {
				taken[id] = true
				return id
			}
		}
	}

	ids := make([]string, len(l.nodes))
	for i, n := range l.nodes {
		id := l.fileIDs[i]
		switch group := byID[id]; {
		case strings.TrimSpace(id) == "":
			ids[i] = derive(n, "missing\x00"+n.Path)
			if !l.broken[i] {
				n.IDChanged = n.ReadOnly == Writable
				l.problem(n.FilePath(), n, errs.New(errs.InvalidArgument, "The file has no id, so it was given one.").
					WithCode(CodeMissingID).
					WithHint("The new id is written the next time the file is saved.").
					WithDetail("path", l.t.Abs(n.FilePath())).
					WithDetail("newId", ids[i]))
			}
		case len(group) == 1:
			ids[i] = id
		default:
			keeper := slices.MinFunc(group, func(a, b int) int {
				return cmp.Compare(l.nodes[a].Path, l.nodes[b].Path)
			})
			if keeper == i {
				ids[i] = id
				break
			}
			ids[i] = derive(n, "duplicate\x00"+id+"\x00"+n.Path)
			n.IDChanged = !l.broken[i] && n.ReadOnly == Writable
			l.problem(n.FilePath(), n,
				errs.New(errs.InvalidArgument, "Another file in the project has the same id, so this one was given a new id.").
					WithCode(CodeDuplicateID).
					WithHint("This happens when a file is copied by hand. The new id is written the next time the file is saved.").
					WithDetail("path", l.t.Abs(n.FilePath())).
					WithDetail("id", id).
					WithDetail("keptBy", l.t.Abs(l.nodes[keeper].FilePath())).
					WithDetail("newId", ids[i]))
		}
	}

	for i, n := range l.nodes {
		n.ID = ids[i]
		if n.IDChanged {
			setFileID(n.File, n.ID)
		}
		l.t.byID[n.ID] = n
		l.t.byPath[n.Path] = n
	}
	for i, n := range l.problemNodes {
		if n != nil {
			l.t.problems[i].NodeID = n.ID
		}
	}
	slices.SortStableFunc(l.t.problems, func(a, b Problem) int { return cmp.Compare(a.Path, b.Path) })
}

// setFileID sets the id member of a decoded file.
func setFileID(f projectfile.File, id string) {
	switch v := f.(type) {
	case *projectfile.Workspace:
		v.ID = id
	case *projectfile.Folder:
		v.ID = id
	case *projectfile.HTTPRequest:
		v.ID = id
	case *projectfile.GraphQLRequest:
		v.ID = id
	case *projectfile.TCPRequest:
		v.ID = id
	case *projectfile.UDPRequest:
		v.ID = id
	case *projectfile.WebSocketRequest:
		v.ID = id
	case *projectfile.GRPCRequest:
		v.ID = id
	case *projectfile.SSERequest:
		v.ID = id
	case *projectfile.SocketIORequest:
		v.ID = id
	}
}

// head is what peek reads from a file that did not decode.
type head struct {
	object   bool
	id, name string
	kind     projectfile.Kind
	order    []string
}

// peek reads the id, name, kind and order members of a file best-effort,
// ignoring members that are missing or of the wrong type.
func peek(data []byte) head {
	var members map[string]json.RawMessage
	if json.Unmarshal(data, &members) != nil || members == nil {
		return head{}
	}
	h := head{object: true}
	var kind string
	_ = json.Unmarshal(members["id"], &h.id)
	_ = json.Unmarshal(members["name"], &h.name)
	_ = json.Unmarshal(members["kind"], &kind)
	_ = json.Unmarshal(members["order"], &h.order)
	h.kind = projectfile.Kind(kind)
	return h
}

func readFailed(err error) error {
	return errs.Wrap(err, errs.KindOf(err), "A project file could not be read.").
		WithCode(projectfile.CodeReadFailed).
		WithHint("Check that the file exists and is readable.")
}

// applyOrder returns children in display order: the entries order names
// first, in that order, then the others sorted by name ignoring case (see
// FoldName), ties broken by the exact name. An order name matches the
// entry with exactly that name, or failing that the single remaining entry
// whose name folds to the same key (a hand-written "Café" for a directory
// the file system lists in NFD). Names that match nothing are ignored.
func applyOrder(order []string, children []*Node) []*Node {
	out := make([]*Node, 0, len(children))
	placed := make([]bool, len(children))
	for _, name := range order {
		if i := matchEntry(name, children, placed); i >= 0 {
			placed[i] = true
			out = append(out, children[i])
		}
	}
	rest := make([]*Node, 0, len(children)-len(out))
	for i, c := range children {
		if !placed[i] {
			rest = append(rest, c)
		}
	}
	slices.SortFunc(rest, func(a, b *Node) int {
		return cmp.Or(cmp.Compare(FoldName(a.Entry), FoldName(b.Entry)), cmp.Compare(a.Entry, b.Entry))
	})
	return append(out, rest...)
}

// matchEntry returns the index of the unplaced child an order name refers
// to, or -1.
func matchEntry(name string, children []*Node, placed []bool) int {
	for i, c := range children {
		if !placed[i] && c.Entry == name {
			return i
		}
	}
	key, found := FoldName(name), -1
	for i, c := range children {
		if !placed[i] && FoldName(c.Entry) == key {
			if found >= 0 {
				return -1 // ambiguous
			}
			found = i
		}
	}
	return found
}

// keptOrder returns the order names that match an entry of children, in
// the same way as applyOrder, dropping the others and repeated names. When
// every name matches it returns order itself, so an unchanged folder saves
// byte-identical.
func keptOrder(order []string, children []*Node) []string {
	placed := make([]bool, len(children))
	var kept []string
	dropped := false
	for _, name := range order {
		if i := matchEntry(name, children, placed); i >= 0 {
			placed[i] = true
			kept = append(kept, name)
		} else {
			dropped = true
		}
	}
	if !dropped {
		return order
	}
	return kept
}
