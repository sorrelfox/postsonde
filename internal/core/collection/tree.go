package collection

import (
	"crypto/sha256"
	"path"
	"path/filepath"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/requestsecrets"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// Error codes, in addition to the errs kinds and the projectfile codes that
// loading and saving files return.
const (
	// CodeReadDirFailed: a directory of the project could not be listed.
	// The kind comes from the cause. Details: path.
	CodeReadDirFailed = "collection.read_dir_failed"
	// CodeDuplicateID: two files of the project have the same id, usually
	// because one was copied by hand (kind invalid_argument). Details: id,
	// path (the file that got a new ID), keptBy (the file that kept it),
	// newId.
	CodeDuplicateID = "collection.duplicate_id"
	// CodeMissingID: a file has no id (kind invalid_argument). Details:
	// path, newId.
	CodeMissingID = "collection.missing_id"
	// CodeTooDeep: folders are nested deeper than MaxDepth; the deeper ones
	// are not loaded (kind unsupported). Details: path.
	CodeTooDeep = "collection.too_deep"
	// CodeNotFound: no node has the ID a save names (kind not_found).
	// Details: id.
	CodeNotFound = "collection.not_found"
	// CodeWrongNode: a save does not fit the node: a folder saved as a
	// request, a file whose id is not the node's (kind invalid_argument).
	// Details: id.
	CodeWrongNode = "collection.wrong_node"
	// CodeReadOnly: the node or the whole project must not be written: an
	// unknown kind, a file that did not load, a symlink, or a project from
	// a newer PostSonde (kind unsupported). Details: id, reason.
	CodeReadOnly = "collection.read_only"
	// CodeChangedOnDisk: a file was changed or removed outside PostSonde
	// since the tree read it, so it is not overwritten (STO-05, kind io).
	// Refresh reads it again. Details: id, path.
	CodeChangedOnDisk = "collection.changed_on_disk"
)

// MaxDepth is the deepest folder nesting Load follows. Top-level folders
// are at depth 1.
const MaxDepth = 64

// NodeKind says what a node of the tree is.
type NodeKind string

const (
	// KindRoot is the project root. Its file is postsonde.json.
	KindRoot NodeKind = "root"
	// KindFolder is a directory with a _folder.json.
	KindFolder NodeKind = "folder"
	// KindRequest is a request file of a kind this version knows.
	KindRequest NodeKind = "request"
	// KindUnknown is a project file whose kind this version does not know,
	// typically a request type added by a newer PostSonde. It is shown and
	// kept exactly as it is.
	KindUnknown NodeKind = "unknown"
	// KindInvalid is a project file that could not be loaded (not valid
	// JSON, a member of the wrong type, a kind that does not belong there).
	// It keeps its place in the tree so it can be found and fixed by hand.
	KindInvalid NodeKind = "invalid"
)

// ReadOnlyReason says why a node's file must not be written; "" means it
// may be.
type ReadOnlyReason string

const (
	Writable ReadOnlyReason = ""
	// ReadOnlyNewerVersion: the file comes from a newer schema version.
	ReadOnlyNewerVersion ReadOnlyReason = "newer_version"
	// ReadOnlyUnknownKind: a KindUnknown node.
	ReadOnlyUnknownKind ReadOnlyReason = "unknown_kind"
	// ReadOnlyInvalid: the file did not load (a KindInvalid node, or a
	// folder or root whose own file is broken).
	ReadOnlyInvalid ReadOnlyReason = "invalid"
	// ReadOnlySymlink: the file is a symbolic link; an atomic save would
	// replace the link with a copy.
	ReadOnlySymlink ReadOnlyReason = "symlink"
)

// Node is a folder, request or other item of the tree. Nodes belong to
// their Tree: change them only through its methods.
type Node struct {
	// ID identifies the node: the file's id, or a derived one when the
	// file has none or shares it with another file (IDChanged).
	ID   string
	Kind NodeKind
	// Name is the name member of the file, as written; see DisplayName.
	Name string
	// Entry is the file or directory name in the parent directory
	// ("create-user.json", "users"); "" for the root.
	Entry string
	// Path is the slash-separated path of the entry relative to the
	// project root ("users/create-user.json"); "" for the root.
	Path string
	// Parent is nil for the root.
	Parent *Node
	// Children are a root's or folder's items in display order (see
	// docs/collection.md); nil for the other kinds.
	Children []*Node
	// File is the decoded file: *projectfile.Workspace for the root,
	// *projectfile.Folder for a folder, *projectfile.HTTPRequest or
	// *projectfile.GraphQLRequest for a request. It is nil for KindUnknown,
	// KindInvalid, and a root or folder whose own file did not load. Its id
	// is always ID.
	File projectfile.File
	// FileKind is the kind member as written ("http", "grpc"); "" when it
	// could not be read.
	FileKind projectfile.Kind
	// ReadOnly says why the node's file must not be written.
	ReadOnly ReadOnlyReason
	// IDChanged is set when ID is not the id in the file (missing or
	// duplicate). The next save of the file writes ID into it.
	IDChanged bool
	// order is the order member of a root or folder whose file did not
	// load, read best-effort, so the children keep their manual order.
	order []string
	// sum is the SHA-256 of the node's file as it was last read or written
	// (hasSum), so a write can tell that the file changed on disk since
	// (STO-05, CodeChangedOnDisk) and Refresh which files really changed.
	sum    [sha256.Size]byte
	hasSum bool
}

// setSum records data as the bytes of n's file on disk.
func (n *Node) setSum(data []byte) {
	n.sum, n.hasSum = sha256.Sum256(data), true
}

// IsContainer reports whether the node has children: the root or a folder.
func (n *Node) IsContainer() bool { return n.Kind == KindRoot || n.Kind == KindFolder }

// DisplayName returns Name, or when it is blank the entry name without
// ".json" (for the root, the project directory's name is up to the caller).
func (n *Node) DisplayName() string {
	if strings.TrimSpace(n.Name) != "" {
		return n.Name
	}
	return strings.TrimSuffix(n.Entry, projectfile.Ext)
}

// FilePath returns the slash path, relative to the project root, of the
// file that holds the node: postsonde.json for the root, <dir>/_folder.json
// for a folder, and Path for the others.
func (n *Node) FilePath() string {
	switch n.Kind {
	case KindRoot:
		return projectfile.WorkspaceFileName
	case KindFolder:
		return path.Join(n.Path, projectfile.FolderFileName)
	}
	return n.Path
}

// Order returns the manual order of a root's or folder's children as its
// file has it; nil for other nodes.
func (n *Node) Order() []string {
	switch f := n.File.(type) {
	case *projectfile.Workspace:
		return f.Order
	case *projectfile.Folder:
		return f.Order
	}
	return n.order
}

// Problem is something wrong found while loading or saving the tree. The
// rest of the tree is unaffected.
type Problem struct {
	// Path is the slash path, relative to the project root, of the file or
	// directory concerned.
	Path string
	// NodeID is the node concerned, or "" (an unreadable directory).
	NodeID string
	// Err says what is wrong. Its details include "path" (absolute).
	Err *errs.Error
}

// Tree is the request tree of a project: the root, its folders and
// requests. It is a snapshot taken by Load and updated by the save methods.
// It is not safe for concurrent use; its owner serialises access.
type Tree struct {
	dir  string
	root *Node
	// problems are the load problems.
	problems []Problem
	byID     map[string]*Node
	byPath   map[string]*Node
	// secrets, when set (KeepSecretsLocal), receives the typed secrets of
	// every file the tree writes.
	secrets *requestsecrets.Store
}

// KeepSecretsLocal makes every project file the tree writes from now on
// (saves, renames, reorders, creates, duplicates, restores) go through
// store.MoveOut first (PRV-09): a typed auth secret or a secret
// variable's value found in the file goes to the item's side file in
// store, and the project gets the file without it. A file whose secrets
// cannot be moved is not written (the error of MoveOut). Without a store,
// files are written as they are. The setting survives the reloads of tree
// operations.
func (t *Tree) KeepSecretsLocal(store *requestsecrets.Store) { t.secrets = store }

// localize returns f, the file of item id, as it may be written to the
// project (KeepSecretsLocal).
func (t *Tree) localize(id string, f projectfile.File) (projectfile.File, error) {
	if t.secrets == nil {
		return f, nil
	}
	return t.secrets.MoveOut(id, f)
}

// writeNew writes f, the file of item id, to the new file abs, after
// moving its secrets out (localize).
func (t *Tree) writeNew(abs, id string, f projectfile.File) error {
	f, err := t.localize(id, f)
	if err != nil {
		return err
	}
	return projectfile.WriteFile(abs, f)
}

// Dir returns the absolute path of the project root.
func (t *Tree) Dir() string { return t.dir }

// Root returns the root node.
func (t *Tree) Root() *Node { return t.root }

// Problems returns the problems found by Load, sorted by path.
func (t *Tree) Problems() []Problem { return append([]Problem(nil), t.problems...) }

// ReadOnly reports why no file of the project may be written: the
// workspace file is from a newer PostSonde or did not load. "" means
// files may be written, subject to each node's own ReadOnly.
func (t *Tree) ReadOnly() ReadOnlyReason { return t.root.ReadOnly }

// Node returns the node with the given ID, or nil.
func (t *Tree) Node(id string) *Node { return t.byID[id] }

// NodeAt returns the node at the slash path relative to the project root
// ("" for the root, "users/create-user.json"), or nil. The path must be
// spelled as Node.Path is.
func (t *Tree) NodeAt(rel string) *Node { return t.byPath[rel] }

// Abs returns the absolute path of a slash path relative to the root.
func (t *Tree) Abs(rel string) string {
	return filepath.Join(t.dir, filepath.FromSlash(rel))
}

// Len returns the number of nodes, the root included.
func (t *Tree) Len() int { return len(t.byID) }

// Walk calls fn for every node, depth first in display order, starting
// with the root. If fn returns false for a root or folder its children
// are skipped.
func (t *Tree) Walk(fn func(*Node) bool) {
	var visit func(n *Node)
	visit = func(n *Node) {
		if fn(n) {
			for _, c := range n.Children {
				visit(c)
			}
		}
	}
	visit(t.root)
}
