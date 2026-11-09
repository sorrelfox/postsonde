package collection

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"io/fs"
	"os"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/storage/atomicfile"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// SaveRequest makes f the file of the request node id and writes it, if
// its encoding differs from the file on disk. It reports whether it wrote.
// f must be a request (*projectfile.HTTPRequest,
// *projectfile.GraphQLRequest, *projectfile.TCPRequest,
// *projectfile.WebSocketRequest, *projectfile.GRPCRequest or
// *projectfile.SSERequest or *projectfile.SocketIORequest; the kind may
// change) with the node's ID.
// The tree takes f over: do not change it afterwards.
//
// It fails with CodeNotFound, CodeWrongNode, CodeReadOnly, the
// projectfile errors of Marshal, or CodeSaveFailed; on failure the node
// is unchanged. The file name is not changed when the name is: renaming
// is a tree operation of its own.
func (t *Tree) SaveRequest(id string, f projectfile.File) (bool, error) {
	n, err := t.writableNode(id, KindRequest)
	if err != nil {
		return false, err
	}
	var name, fileID string
	ok := false
	switch r := f.(type) {
	case *projectfile.HTTPRequest:
		if r != nil {
			name, fileID, ok = r.Name, r.ID, true
		}
	case *projectfile.GraphQLRequest:
		if r != nil {
			name, fileID, ok = r.Name, r.ID, true
		}
	case *projectfile.TCPRequest:
		if r != nil {
			name, fileID, ok = r.Name, r.ID, true
		}
	case *projectfile.UDPRequest:
		if r != nil {
			name, fileID, ok = r.Name, r.ID, true
		}
	case *projectfile.WebSocketRequest:
		if r != nil {
			name, fileID, ok = r.Name, r.ID, true
		}
	case *projectfile.GRPCRequest:
		if r != nil {
			name, fileID, ok = r.Name, r.ID, true
		}
	case *projectfile.SSERequest:
		if r != nil {
			name, fileID, ok = r.Name, r.ID, true
		}
	case *projectfile.SocketIORequest:
		if r != nil {
			name, fileID, ok = r.Name, r.ID, true
		}
	}
	if !ok {
		return false, wrongNode(id, "Only a request can be saved as a request.")
	}
	if fileID != n.ID {
		return false, wrongNode(id, "The request's id is not the id of the item it is saved to.")
	}
	f, written, err := t.write(n, f)
	if err != nil {
		return false, err
	}
	n.File, n.Name, n.FileKind, n.IDChanged = f, name, f.FileKind(), false
	return written, nil
}

// SaveFolder makes f the _folder.json of the folder node id and writes it,
// if its encoding differs from the file on disk. f's ID must be the
// node's. Its Order is written without the names that match no item of
// the folder, and the folder's children are put in the new order. The
// tree takes f over. Errors are those of SaveRequest.
func (t *Tree) SaveFolder(id string, f *projectfile.Folder) (bool, error) {
	n, err := t.writableNode(id, KindFolder)
	if err != nil {
		return false, err
	}
	if f == nil {
		return false, wrongNode(id, "Only a folder can be saved as a folder.")
	}
	if f.ID != n.ID {
		return false, wrongNode(id, "The folder's id is not the id of the folder it is saved to.")
	}
	c := *f
	c.Order = keptOrder(f.Order, n.Children)
	written, err := t.writeContainer(n, &c)
	if err != nil {
		return false, err
	}
	n.Name, n.IDChanged = c.Name, false
	return written, nil
}

// SaveWorkspace is SaveFolder for the root: it makes w the workspace file,
// postsonde.json, and writes it if it changed. The open workspace in
// package workspace keeps the marker it read; reopen it to see the change.
func (t *Tree) SaveWorkspace(w *projectfile.Workspace) (bool, error) {
	n, err := t.writableNode(t.root.ID, KindRoot)
	if err != nil {
		return false, err
	}
	if w == nil || w.ID != n.ID {
		return false, wrongNode(n.ID, "The workspace's id is not the id of the project it is saved to.")
	}
	c := *w
	c.Order = keptOrder(w.Order, n.Children)
	written, err := t.writeContainer(n, &c)
	if err != nil {
		return false, err
	}
	n.Name, n.IDChanged = c.Name, false
	return written, nil
}

// SaveAll writes the file of every node that may be written (the root,
// folders and requests with a file and no ReadOnly reason) whose encoding
// differs from the file on disk, depth first. It returns how many files it
// wrote and the errors joined; one failing file does not stop the others.
// It writes nothing in a read-only project.
func (t *Tree) SaveAll() (int, error) {
	if t.root.ReadOnly != Writable {
		return 0, readOnly(t.root.ID, t.root.ReadOnly)
	}
	var nodes []*Node
	t.Walk(func(n *Node) bool {
		if n.File != nil && n.ReadOnly == Writable {
			nodes = append(nodes, n)
		}
		return true
	})
	written := 0
	var all []error
	for _, n := range nodes {
		var ok bool
		var err error
		switch f := n.File.(type) {
		case *projectfile.Workspace:
			ok, err = t.SaveWorkspace(f)
		case *projectfile.Folder:
			ok, err = t.SaveFolder(n.ID, f)
		default:
			ok, err = t.SaveRequest(n.ID, f)
		}
		if ok {
			written++
		}
		if err != nil {
			all = append(all, err)
		}
	}
	return written, errors.Join(all...)
}

// writableNode returns the node id of kind k when it may be written.
func (t *Tree) writableNode(id string, k NodeKind) (*Node, error) {
	n := t.byID[id]
	if n == nil {
		return nil, errs.New(errs.NotFound, "The item to save is no longer in the project.").
			WithCode(CodeNotFound).
			WithHint("It may have been moved or deleted; reload the project.").
			WithDetail("id", id)
	}
	if t.root.ReadOnly != Writable {
		return nil, readOnly(t.root.ID, t.root.ReadOnly)
	}
	if n.ReadOnly != Writable {
		return nil, readOnly(id, n.ReadOnly)
	}
	if n.Kind != k {
		return nil, wrongNode(id, "The item to save is not of the kind being saved.")
	}
	return n, nil
}

// write moves the typed secrets of f out (localize), encodes it and writes
// it to n's file unless the file already holds exactly those bytes. It
// returns the file as written.
func (t *Tree) write(n *Node, f projectfile.File) (projectfile.File, bool, error) {
	abs := t.Abs(n.FilePath())
	f, err := t.localize(n.ID, f)
	if err != nil {
		return nil, false, err
	}
	data, err := projectfile.Marshal(f)
	if err != nil {
		var e *errs.Error
		if errors.As(err, &e) {
			return nil, false, e.WithDetail("path", abs)
		}
		return nil, false, err
	}
	old, readErr := os.ReadFile(abs)
	if n.hasSum && (errors.Is(readErr, fs.ErrNotExist) || (readErr == nil && sha256.Sum256(old) != n.sum)) {
		return nil, false, changedOnDisk(n.ID, abs)
	}
	if readErr == nil && bytes.Equal(old, data) {
		n.setSum(data)
		return f, false, nil
	}
	if err := atomicfile.WriteFile(abs, data, projectfile.FilePerm); err != nil {
		return nil, false, errs.Wrap(err, errs.KindOf(err), "A project file could not be saved.").
			WithCode(projectfile.CodeSaveFailed).
			WithHint("Check that the project folder is writable and the disk is not full.").
			WithDetail("path", abs)
	}
	n.setSum(data)
	return f, true, nil
}

// changedOnDisk is the error of a write whose file is no longer what the
// tree read: it was edited or removed outside PostSonde (STO-05).
func changedOnDisk(id, abs string) *errs.Error {
	return errs.New(errs.IO, "The file was changed outside PostSonde, so it was not overwritten.").
		WithCode(CodeChangedOnDisk).
		WithHint("PostSonde reads it again; choose which version to keep.").
		WithDetail("id", id).
		WithDetail("path", abs)
}

// writeContainer writes f, the workspace or folder file of n, and makes
// the file as written n's file, its children in f's order.
func (t *Tree) writeContainer(n *Node, f projectfile.File) (bool, error) {
	written, ok, err := t.write(n, f)
	if err != nil {
		return false, err
	}
	n.File = written
	n.Children = applyOrder(n.Order(), n.Children)
	return ok, nil
}

func wrongNode(id, msg string) *errs.Error {
	return errs.New(errs.InvalidArgument, msg).
		WithCode(CodeWrongNode).
		WithHint("This is a bug in PostSonde; reload the project.").
		WithDetail("id", id)
}

func readOnly(id string, reason ReadOnlyReason) *errs.Error {
	msg := "This file cannot be changed by PostSonde."
	hint := "Fix or update the file by hand."
	switch reason {
	case ReadOnlyNewerVersion:
		msg = "The file was written by a newer version of PostSonde, so it is not overwritten."
		hint = "Update PostSonde to change it."
	case ReadOnlyUnknownKind:
		msg = "This version of PostSonde does not know this kind of file, so it is kept as it is."
		hint = "Update PostSonde to change it."
	case ReadOnlyInvalid:
		msg = "The file could not be loaded, so it is not overwritten."
		hint = "Fix the file by hand; the project's problems say what is wrong."
	case ReadOnlySymlink:
		msg = "The file is a symbolic link, which saving would replace with a copy."
		hint = "Edit the file the link points to by hand, or replace the link with the file."
	}
	return errs.New(errs.Unsupported, msg).
		WithCode(CodeReadOnly).
		WithHint(hint).
		WithDetail("id", id).
		WithDetail("reason", string(reason))
}
