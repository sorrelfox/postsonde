package collection

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// Error codes of the tree operations, in addition to CodeNotFound,
// CodeWrongNode, CodeReadOnly and the projectfile codes.
const (
	// CodeInvalidTarget: the parent of a create, move or restore is not a
	// folder or the root, or a folder would be moved into itself or one of
	// its folders (kind invalid_argument). Details: id, parentId.
	CodeInvalidTarget = "collection.invalid_target"
	// CodeInvalidKind: a request of a kind this version cannot create
	// (kind invalid_argument). Details: kind.
	CodeInvalidKind = "collection.invalid_kind"
	// CodeFileOpFailed: creating, renaming, moving or removing a file or
	// directory failed. The kind comes from the cause. Details: path, and
	// to for a rename.
	CodeFileOpFailed = "collection.file_op_failed"
)

// End is the index that puts an item after the last one.
const End = -1

// dirPerm is the permission of the folders the operations create.
const dirPerm fs.FileMode = 0o755

// CreateFolder creates a folder called name in the folder parentID ("" or
// the root's ID for the top level) at index among its items (End, or any
// index out of range, appends). The directory name comes from Slug and
// UniqueName. It returns the new folder's ID.
//
// Like every operation it fails with CodeNotFound, CodeReadOnly (a
// read-only project or parent), CodeInvalidTarget or CodeFileOpFailed and
// the projectfile errors, and reloads the tree from disk when it succeeds.
func (t *Tree) CreateFolder(parentID, name string, index int) (string, error) {
	f := &projectfile.Folder{
		ID:   projectfile.NewID(projectfile.KindFolder),
		Name: name,
		Auth: projectfile.Auth{Type: projectfile.AuthInherit},
	}
	return t.create(parentID, f, f.ID, name, index)
}

// CreateRequest creates an empty request of kind (projectfile.KindHTTP, a
// GET, projectfile.KindGraphQL, projectfile.KindTCP, projectfile.KindUDP,
// projectfile.KindWebSocket, projectfile.KindGRPC, projectfile.KindSSE or
// projectfile.KindSocketIO)
// called name, as
// CreateFolder does, and
// returns its ID. Other kinds fail with CodeInvalidKind.
func (t *Tree) CreateRequest(parentID, name string, kind projectfile.Kind, index int) (string, error) {
	id := projectfile.NewID(kind)
	f, err := NewRequestFile(kind, id, name)
	if err != nil {
		return "", err
	}
	return t.create(parentID, f, id, name, index)
}

// NewRequestFile returns the empty request of kind that CreateRequest
// writes, with id and name: an http GET without body, or an empty
// graphql, tcp, udp, websocket, grpc, sse or socketio request (an sse request
// connects with GET; every kind but tcp and udp inherits its auth). Other kinds fail with
// CodeInvalidKind. Scratch requests start from it too.
func NewRequestFile(kind projectfile.Kind, id, name string) (projectfile.File, error) {
	switch kind {
	case projectfile.KindHTTP:
		return &projectfile.HTTPRequest{
			ID: id, Name: name, Method: "GET",
			Body: projectfile.Body{Mode: projectfile.BodyNone},
			Auth: projectfile.Auth{Type: projectfile.AuthInherit},
		}, nil
	case projectfile.KindGraphQL:
		return &projectfile.GraphQLRequest{ID: id, Name: name, Auth: projectfile.Auth{Type: projectfile.AuthInherit}}, nil
	case projectfile.KindTCP:
		return &projectfile.TCPRequest{ID: id, Name: name}, nil
	case projectfile.KindUDP:
		return &projectfile.UDPRequest{ID: id, Name: name}, nil
	case projectfile.KindWebSocket:
		return &projectfile.WebSocketRequest{ID: id, Name: name, Auth: projectfile.Auth{Type: projectfile.AuthInherit}}, nil
	case projectfile.KindGRPC:
		return &projectfile.GRPCRequest{ID: id, Name: name, Auth: projectfile.Auth{Type: projectfile.AuthInherit}}, nil
	case projectfile.KindSSE:
		return &projectfile.SSERequest{ID: id, Name: name, Auth: projectfile.Auth{Type: projectfile.AuthInherit}}, nil
	case projectfile.KindSocketIO:
		return &projectfile.SocketIORequest{ID: id, Name: name, Auth: projectfile.Auth{Type: projectfile.AuthInherit}}, nil
	}
	return nil, errs.New(errs.InvalidArgument, "Requests of this kind cannot be created.").
		WithCode(CodeInvalidKind).
		WithHint("Create an HTTP, GraphQL, TCP, UDP, WebSocket, gRPC, SSE or Socket.IO request.").
		WithDetail("kind", string(kind))
}

// AddRequest writes the request file f, called name, into the folder
// parentID at index, as CreateRequest does for an empty one, and returns
// its ID: f's own ID, or a new one when an item of the tree already has
// it. Saving a scratch request into the project (COL-06) uses it. f must
// be a request of a kind this version knows (CodeInvalidKind); it is not
// modified.
func (t *Tree) AddRequest(parentID string, f projectfile.File, name string, index int) (string, error) {
	id := RequestFileID(f)
	if id == "" {
		kind := ""
		if f != nil {
			kind = string(f.FileKind())
		}
		return "", errs.New(errs.InvalidArgument, "Only requests can be added to the project.").
			WithCode(CodeInvalidKind).
			WithHint("This is a bug in PostSonde.").
			WithDetail("kind", kind)
	}
	if t.Node(id) != nil {
		id = projectfile.NewID(f.FileKind())
	}
	return t.create(parentID, withName(withID(f, id), name), id, name, index)
}

// RequestFileID returns the id member of a request file of a kind this
// version knows; "" for anything else.
func RequestFileID(f projectfile.File) string {
	switch v := f.(type) {
	case *projectfile.HTTPRequest:
		return v.ID
	case *projectfile.GraphQLRequest:
		return v.ID
	case *projectfile.TCPRequest:
		return v.ID
	case *projectfile.UDPRequest:
		return v.ID
	case *projectfile.WebSocketRequest:
		return v.ID
	case *projectfile.GRPCRequest:
		return v.ID
	case *projectfile.SSERequest:
		return v.ID
	case *projectfile.SocketIORequest:
		return v.ID
	}
	return ""
}

func (t *Tree) create(parentID string, f projectfile.File, id, name string, index int) (string, error) {
	parent, err := t.targetContainer(parentID, "")
	if err != nil {
		return "", err
	}
	folder := f.FileKind() == projectfile.KindFolder
	dir := t.Abs(parent.Path)
	entry, err := UniqueName(dir, Slug(name), extFor(folder), parent.Kind == KindRoot)
	if err != nil {
		return "", err
	}
	abs := filepath.Join(dir, entry)
	if folder {
		// A directory without _folder.json is not a folder, so a crash
		// before the file is written leaves nothing in the tree.
		if err := os.Mkdir(abs, dirPerm); err != nil {
			return "", fileOpFailed(err, "The folder could not be created.", abs, "")
		}
		if err := t.writeNew(filepath.Join(abs, projectfile.FolderFileName), id, f); err != nil {
			_ = os.Remove(abs)
			return "", err
		}
	} else if err := t.writeNew(abs, id, f); err != nil {
		return "", err
	}
	if err := t.writeOrder(parent, insertAt(entries(parent.Children, nil), index, entry)); err != nil {
		return "", err
	}
	t.reload()
	return id, nil
}

// Rename sets the name of the folder or request id and renames its file or
// directory to match (see docs/collection.md: the file name is kept when
// its slug already fits the name). The item keeps its place. Unknown,
// invalid, newer-version and symlinked items cannot be renamed
// (CodeReadOnly).
func (t *Tree) Rename(id, name string) error {
	n, err := t.opNode(id)
	if err != nil {
		return err
	}
	if n.ReadOnly != Writable {
		return readOnly(id, n.ReadOnly)
	}
	parent := n.Parent
	if err := checkContainer(parent); err != nil {
		return err
	}
	if err := t.materializeIDs(); err != nil {
		return err
	}
	// The name first: if renaming the file then fails, the item simply
	// keeps its old file name.
	if _, _, err := t.write(n, withName(n.File, name)); err != nil {
		return err
	}
	folder := n.Kind == KindFolder
	ext := extFor(folder)
	dir := t.Abs(parent.Path)
	names, err := listNames(dir)
	if err != nil {
		return err
	}
	others := slices.DeleteFunc(names, func(s string) bool { return s == n.Entry })
	entry := renamedEntry(strings.TrimSuffix(n.Entry, ext), Slug(name), ext, others, parent.Kind == KindRoot)
	if entry != n.Entry {
		if err := renameEntry(t.Abs(n.Path), filepath.Join(dir, entry)); err != nil {
			return err
		}
		order := entries(parent.Children, nil)
		order[slices.Index(order, n.Entry)] = entry
		if err := t.writeOrder(parent, order); err != nil {
			return err
		}
	}
	t.reload()
	return nil
}

// renamedEntry returns the entry name for an item whose entry base is base
// after a rename that gives slug: base itself when it is slug, or slug with
// a collision suffix while slug is taken by another entry; else a unique
// name among others.
func renamedEntry(base, slug, ext string, others []string, root bool) string {
	if base == slug {
		return base + ext
	}
	if rest, ok := strings.CutPrefix(base, slug+"-"); ok && isDigits(rest) {
		taken := slices.ContainsFunc(others, func(s string) bool { return FoldName(s) == FoldName(slug+ext) })
		if taken {
			return base + ext
		}
	}
	return uniqueName(others, slug, ext, root)
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// Duplicate copies the folder or request id, with a new ID for the copy and
// for every folder and request inside a copied folder, names it "<name>
// copy" (or "copy 2", …) and puts it right after the original. It returns
// the copy's ID. Only the items of the tree are copied, not other files in
// a folder's directory. A folder holding unknown, invalid or newer-version
// items, or nested too deep to be loaded, is not copied (CodeReadOnly).
func (t *Tree) Duplicate(id string) (string, error) {
	n, err := t.opNode(id)
	if err != nil {
		return "", err
	}
	if n.Kind != KindFolder && n.Kind != KindRequest {
		return "", readOnly(id, n.ReadOnly)
	}
	parent := n.Parent
	if err := checkContainer(parent); err != nil {
		return "", err
	}
	var bad *Node
	walkNode(n, func(c *Node) {
		if bad == nil && c.ReadOnly != Writable && c.ReadOnly != ReadOnlySymlink {
			bad = c
		}
	})
	if bad != nil {
		return "", readOnly(bad.ID, bad.ReadOnly)
	}
	for _, p := range t.problems {
		if p.Err.Code == CodeTooDeep && (p.Path == n.Path || strings.HasPrefix(p.Path, n.Path+"/")) {
			return "", readOnly(id, ReadOnlyInvalid).WithHint("Move the inner folders up before duplicating the folder.")
		}
	}

	siblings := make([]string, 0, len(parent.Children))
	for _, c := range parent.Children {
		siblings = append(siblings, c.DisplayName())
	}
	name := copyName(n.DisplayName(), siblings)
	folder := n.Kind == KindFolder
	dir := t.Abs(parent.Path)
	entry, err := UniqueName(dir, Slug(name), extFor(folder), parent.Kind == KindRoot)
	if err != nil {
		return "", err
	}
	dst := filepath.Join(dir, entry)
	newID := projectfile.NewID(n.File.FileKind())
	if folder {
		// Build the copy under a hidden name Load skips, then rename it into
		// place, so a crash never leaves half a folder in the tree.
		tmp := filepath.Join(dir, "."+entry+".copy-"+randomHex())
		if err := t.copyFolder(n, tmp, newID, name); err != nil {
			_ = os.RemoveAll(tmp)
			return "", err
		}
		if err := renameEntry(tmp, dst); err != nil {
			_ = os.RemoveAll(tmp)
			return "", err
		}
	} else if err := t.writeNew(dst, newID, withName(withID(n.File, newID), name)); err != nil {
		return "", err
	}
	if err := t.writeOrder(parent, insertAt(entries(parent.Children, nil), slices.Index(parent.Children, n)+1, entry)); err != nil {
		return "", err
	}
	t.reload()
	return newID, nil
}

// copyFolder writes a copy of the folder n into the new directory dir: its
// _folder.json with id and name, and its items with new IDs.
func (t *Tree) copyFolder(n *Node, dir, id, name string) error {
	if err := os.Mkdir(dir, dirPerm); err != nil {
		return fileOpFailed(err, "The folder could not be created.", dir, "")
	}
	f := withName(withID(n.File, id), name).(*projectfile.Folder)
	f.Order = entries(n.Children, nil)
	if err := t.writeNew(filepath.Join(dir, projectfile.FolderFileName), id, f); err != nil {
		return err
	}
	for _, c := range n.Children {
		dst := filepath.Join(dir, c.Entry)
		cid := projectfile.NewID(c.File.FileKind())
		if c.Kind == KindFolder {
			if err := t.copyFolder(c, dst, cid, c.Name); err != nil {
				return err
			}
			continue
		}
		if err := t.writeNew(dst, cid, withID(c.File, cid)); err != nil {
			return err
		}
	}
	return nil
}

var copySuffix = regexp.MustCompile(`^(.*\S) copy(?: ([0-9]+))?$`)

// copyName returns the name of a copy of an item called name among items
// called siblings: "<name> copy", or "<name> copy 2", "copy 3", … when that
// is taken (ignoring case). The copy of "X copy" is "X copy 2".
func copyName(name string, siblings []string) string {
	base := name
	if m := copySuffix.FindStringSubmatch(name); m != nil {
		base = m[1]
	}
	taken := func(s string) bool {
		return slices.ContainsFunc(siblings, func(x string) bool { return strings.EqualFold(x, s) })
	}
	candidate := base + " copy"
	for i := 2; taken(candidate); i++ {
		candidate = base + " copy " + strconv.Itoa(i)
	}
	return candidate
}

// Move moves the item id into the folder parentID ("" or the root's ID for
// the top level) at index among the items there, counted without the item
// itself (End, or any index out of range, appends). Moving within the same
// folder only reorders. Moving to another folder renames the file or
// directory, keeping its name unless it is taken there, in which case it
// gets a collision suffix (the display name is unchanged). Any item can be
// moved, since its file is not rewritten, except a symlink to another
// folder (CodeReadOnly) and a folder into itself or one of its folders
// (CodeInvalidTarget).
func (t *Tree) Move(id, parentID string, index int) error {
	n, err := t.opNode(id)
	if err != nil {
		return err
	}
	target, err := t.targetContainer(parentID, id)
	if err != nil {
		return err
	}
	for p := target; p != nil; p = p.Parent {
		if p == n {
			return invalidTarget(id, parentID, "A folder cannot be moved into itself or into one of its folders.")
		}
	}
	source := n.Parent
	if err := checkContainer(source); err != nil {
		return err
	}
	if target == source {
		if err := t.writeOrder(source, insertAt(entries(source.Children, n), index, n.Entry)); err != nil {
			return err
		}
		t.reload()
		return nil
	}
	if n.ReadOnly == ReadOnlySymlink {
		return readOnly(id, n.ReadOnly).WithHint("A symbolic link can only be reordered in its folder; move it by hand.")
	}
	if err := t.materializeIDs(); err != nil {
		return err
	}
	ext := extFor(n.Kind == KindFolder)
	dir := t.Abs(target.Path)
	entry, err := UniqueName(dir, strings.TrimSuffix(n.Entry, ext), ext, target.Kind == KindRoot)
	if err != nil {
		return err
	}
	if err := renameEntry(t.Abs(n.Path), filepath.Join(dir, entry)); err != nil {
		return err
	}
	if err := t.writeOrder(target, insertAt(entries(target.Children, nil), index, entry)); err != nil {
		return err
	}
	if err := t.writeOrder(source, entries(source.Children, n)); err != nil {
		return err
	}
	t.reload()
	return nil
}

// Reload reads the tree from its directory again, after files were
// written below a folder without the tree's operations (the importer
// writes a new folder's content directly, then reloads once).
func (t *Tree) Reload() { t.reload() }

// reload replaces the tree with a fresh Load of its directory, so the
// nodes, paths, order and problems are exactly what the files say.
func (t *Tree) reload() {
	fresh, _ := Load(t.dir)
	fresh.secrets = t.secrets
	*t = *fresh
}

// opNode returns the node id for an operation: it must exist, not be the
// root, and the project must be writable.
func (t *Tree) opNode(id string) (*Node, error) {
	n := t.byID[id]
	if n == nil {
		return nil, notFound(id)
	}
	if t.root.ReadOnly != Writable {
		return nil, readOnly(t.root.ID, t.root.ReadOnly)
	}
	if n.Kind == KindRoot {
		return nil, wrongNode(id, "The project itself cannot be renamed, moved, copied or deleted here.")
	}
	return n, nil
}

// targetContainer returns the writable root or folder parentID ("" for
// the root) that an item is created, moved or restored into. id is the
// item moved, for the error details.
func (t *Tree) targetContainer(parentID, id string) (*Node, error) {
	if t.root.ReadOnly != Writable {
		return nil, readOnly(t.root.ID, t.root.ReadOnly)
	}
	n := t.root
	if parentID != "" {
		n = t.byID[parentID]
	}
	if n == nil {
		return nil, notFound(parentID)
	}
	if !n.IsContainer() {
		return nil, invalidTarget(id, parentID, "Items can only be put into a folder.")
	}
	return n, checkContainer(n)
}

// checkContainer fails with CodeReadOnly when the root or folder c cannot
// have its order written.
func checkContainer(c *Node) error {
	if c.ReadOnly != Writable {
		return readOnly(c.ID, c.ReadOnly)
	}
	return nil
}

// writeOrder writes the order member of the root or folder c.
func (t *Tree) writeOrder(c *Node, order []string) error {
	var f projectfile.File
	switch v := c.File.(type) {
	case *projectfile.Workspace:
		cp := *v
		cp.Order = order
		f = &cp
	case *projectfile.Folder:
		cp := *v
		cp.Order = order
		f = &cp
	default:
		return readOnly(c.ID, ReadOnlyInvalid)
	}
	_, _, err := t.write(c, f)
	return err
}

// materializeIDs writes the derived ID of every writable node whose file
// lacks it (IDChanged) before an operation changes paths, since derived IDs
// depend on paths.
func (t *Tree) materializeIDs() error {
	var nodes []*Node
	t.Walk(func(n *Node) bool {
		if n.IDChanged && n.File != nil && n.ReadOnly == Writable {
			nodes = append(nodes, n)
		}
		return true
	})
	for _, n := range nodes {
		f, _, err := t.write(n, n.File)
		if err != nil {
			return err
		}
		n.File = f
		n.IDChanged = false
	}
	return nil
}

// entries returns the entry names of nodes, leaving out skip.
func entries(nodes []*Node, skip *Node) []string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		if n != skip {
			out = append(out, n.Entry)
		}
	}
	return out
}

// insertAt inserts s at index, or appends it when index is out of range.
func insertAt(list []string, index int, s string) []string {
	if index < 0 || index > len(list) {
		return append(list, s)
	}
	return slices.Insert(list, index, s)
}

func extFor(folder bool) string {
	if folder {
		return ""
	}
	return projectfile.Ext
}

// walkNode calls fn for n and every node below it.
func walkNode(n *Node, fn func(*Node)) {
	fn(n)
	for _, c := range n.Children {
		walkNode(c, fn)
	}
}

// withName returns a shallow copy of the folder or request f with name.
func withName(f projectfile.File, name string) projectfile.File {
	switch v := f.(type) {
	case *projectfile.Folder:
		c := *v
		c.Name = name
		return &c
	case *projectfile.HTTPRequest:
		c := *v
		c.Name = name
		return &c
	case *projectfile.GraphQLRequest:
		c := *v
		c.Name = name
		return &c
	case *projectfile.TCPRequest:
		c := *v
		c.Name = name
		return &c
	case *projectfile.UDPRequest:
		c := *v
		c.Name = name
		return &c
	case *projectfile.WebSocketRequest:
		c := *v
		c.Name = name
		return &c
	case *projectfile.GRPCRequest:
		c := *v
		c.Name = name
		return &c
	case *projectfile.SSERequest:
		c := *v
		c.Name = name
		return &c
	case *projectfile.SocketIORequest:
		c := *v
		c.Name = name
		return &c
	}
	return f
}

// withID returns a shallow copy of the folder or request f with id.
func withID(f projectfile.File, id string) projectfile.File {
	switch v := f.(type) {
	case *projectfile.Folder:
		c := *v
		c.ID = id
		return &c
	case *projectfile.HTTPRequest:
		c := *v
		c.ID = id
		return &c
	case *projectfile.GraphQLRequest:
		c := *v
		c.ID = id
		return &c
	case *projectfile.TCPRequest:
		c := *v
		c.ID = id
		return &c
	case *projectfile.UDPRequest:
		c := *v
		c.ID = id
		return &c
	case *projectfile.WebSocketRequest:
		c := *v
		c.ID = id
		return &c
	case *projectfile.GRPCRequest:
		c := *v
		c.ID = id
		return &c
	case *projectfile.SSERequest:
		c := *v
		c.ID = id
		return &c
	case *projectfile.SocketIORequest:
		c := *v
		c.ID = id
		return &c
	}
	return f
}

// listNames returns the entry names of dir; a missing dir has none.
func listNames(dir string) ([]string, error) {
	list, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, errs.Wrap(err, errs.KindOf(err), "The folder could not be read to choose a file name.").
			WithCode(CodeReadDirFailed).
			WithHint("Check that the project folder is readable.").
			WithDetail("path", dir)
	}
	names := make([]string, 0, len(list))
	for _, e := range list {
		names = append(names, e.Name())
	}
	return names, nil
}

// renameFunc is os.Rename; tests replace it to simulate failures.
var renameFunc = os.Rename

// renameEntry renames the file or directory from to the free name to in
// the project. When the two names differ only in case or normalization
// (a case-insensitive file system may treat them as one), it renames
// through a temporary name next to them; that name is visible, so an
// interrupted rename leaves the item in the tree under it.
func renameEntry(from, to string) error {
	if from == to {
		return nil
	}
	if filepath.Dir(from) == filepath.Dir(to) && FoldName(filepath.Base(from)) == FoldName(filepath.Base(to)) {
		ext := ""
		if fi, err := os.Lstat(from); err == nil && !fi.IsDir() {
			ext = filepath.Ext(from)
		}
		tmp := strings.TrimSuffix(to, ext) + "-" + randomHex() + ext
		if err := renameFunc(from, tmp); err != nil {
			return fileOpFailed(err, "The item could not be renamed.", from, tmp)
		}
		if err := renameFunc(tmp, to); err != nil {
			_ = renameFunc(tmp, from)
			return fileOpFailed(err, "The item could not be renamed.", tmp, to)
		}
		return nil
	}
	if _, err := os.Lstat(to); err == nil {
		return fileOpFailed(fs.ErrExist, "An item with the new file name already exists.", from, to).
			WithHint("Reload the project and try again.")
	}
	if err := renameFunc(from, to); err != nil {
		return fileOpFailed(err, "The item could not be renamed or moved.", from, to)
	}
	return nil
}

// randomHex returns 16 random lowercase hex digits.
func randomHex() string {
	id := projectfile.NewID(projectfile.KindHTTP)
	return id[len(projectfile.RequestIDPrefix):]
}

func notFound(id string) *errs.Error {
	return errs.New(errs.NotFound, "The item is no longer in the project.").
		WithCode(CodeNotFound).
		WithHint("It may have been moved or deleted; reload the project.").
		WithDetail("id", id)
}

func invalidTarget(id, parentID, msg string) *errs.Error {
	return errs.New(errs.InvalidArgument, msg).
		WithCode(CodeInvalidTarget).
		WithHint("Choose another folder.").
		WithDetail("id", id).
		WithDetail("parentId", parentID)
}

func fileOpFailed(err error, msg, from, to string) *errs.Error {
	e := errs.Wrap(err, errs.KindOf(err), msg).
		WithCode(CodeFileOpFailed).
		WithHint("Check that the project folder is writable and no other program uses the file.").
		WithDetail("path", from)
	if to != "" {
		e = e.WithDetail("to", to)
	}
	return e
}
