package collection

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/fswatch"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// External changes (STO-05): files edited, added, removed or renamed
// outside PostSonde (an editor, git pull). Scan lists the files a project's
// tree and environments are read from, for an fswatch.Watcher; when it
// reports a change the owner calls Tree.Refresh, which reads the tree again
// in place and says which items really changed, by comparing each file's
// hash with the one the tree read or wrote. The app's own writes therefore
// never count as changes. See docs/collection.md#external-changes.

// Scan returns the modification metadata of the files Load reads from the
// project folder dir, and of the environment files: postsonde.json, every
// folder's _folder.json, every .json file in the root and in folders, and
// environments/*.json. Like Load it skips names starting with "_" or ".",
// does not look into directories without a _folder.json or follow links to
// directories, and stops at MaxDepth. It fails only when dir cannot be
// listed.
func Scan(dir string) (fswatch.Snapshot, error) {
	snap := fswatch.Snapshot{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	statInto(snap, dir, projectfile.WorkspaceFileName)
	scanEnvironments(snap, dir)
	scanDir(snap, dir, "", entries, 0)
	return snap, nil
}

// statInto records the file rel (slash path under dir), following a
// symbolic link to a file; missing files and directories are left out.
func statInto(snap fswatch.Snapshot, dir, rel string) bool {
	fi, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil || fi.IsDir() {
		return false
	}
	snap[rel] = fswatch.StatOf(fi)
	return true
}

func scanEnvironments(snap fswatch.Snapshot, dir string) {
	entries, err := os.ReadDir(filepath.Join(dir, projectfile.EnvironmentsDir))
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || !strings.HasSuffix(name, projectfile.Ext) {
			continue
		}
		statInto(snap, dir, path.Join(projectfile.EnvironmentsDir, name))
	}
}

func scanDir(snap fswatch.Snapshot, dir, rel string, entries []os.DirEntry, depth int) {
	for _, e := range entries {
		name := e.Name()
		if skipEntry(name, rel == "") {
			continue
		}
		child := path.Join(rel, name)
		mode := e.Type()
		if mode&fs.ModeSymlink != 0 {
			// Links to files are loaded (read-only), links to directories not.
			if strings.HasSuffix(name, projectfile.Ext) {
				statInto(snap, dir, child)
			}
			continue
		}
		switch {
		case mode.IsDir():
			if depth+1 > MaxDepth {
				continue
			}
			if !statInto(snap, dir, path.Join(child, projectfile.FolderFileName)) {
				continue // not a folder of the tree
			}
			// ReadDir returns what it read before an error, as Load uses it.
			sub, _ := os.ReadDir(filepath.Join(dir, filepath.FromSlash(child)))
			scanDir(snap, dir, child, sub, depth+1)
		case mode.IsRegular() && strings.HasSuffix(name, projectfile.Ext):
			statInto(snap, dir, child)
		}
	}
}

// NodeChange is an item that Refresh found changed.
type NodeChange struct {
	ID   string
	Kind NodeKind
	// Path is the item's path now; for a removed item, where it was.
	Path string
	// ParentID is the item's folder ("" for the top level); for a removed
	// item, the folder it was in.
	ParentID string
	// Old is the item's file before the refresh (nil when it had none):
	// for a modified folder, to tell which of its members changed.
	Old projectfile.File
}

// Changes is what Refresh found. Each list is in tree order (removed items
// in the old tree's order).
type Changes struct {
	// Added are items that were not in the tree.
	Added []NodeChange
	// Removed are items that are no longer in the tree.
	Removed []NodeChange
	// Modified are items whose file changed (its bytes, or whether it
	// loads), wherever it is now.
	Modified []NodeChange
	// Moved are items whose file is unchanged but at another path (renamed
	// or moved).
	Moved []NodeChange
	// Root is set when postsonde.json changed; OldRoot is its file before.
	Root    bool
	OldRoot projectfile.File
}

// Empty reports whether nothing changed.
func (c Changes) Empty() bool {
	return len(c.Added) == 0 && len(c.Removed) == 0 && len(c.Modified) == 0 && len(c.Moved) == 0 && !c.Root
}

type nodeState struct {
	path, parentID string
	kind           NodeKind
	readOnly       ReadOnlyReason
	sum            [32]byte
	hasSum         bool
	file           projectfile.File
}

func stateOf(n *Node) nodeState {
	parentID := ""
	if n.Parent != nil && n.Parent.Kind != KindRoot {
		parentID = n.Parent.ID
	}
	return nodeState{path: n.Path, parentID: parentID, kind: n.Kind, readOnly: n.ReadOnly, sum: n.sum, hasSum: n.hasSum, file: n.File}
}

// Refresh reads the tree from its directory again, in place (the *Tree
// stays the same, as after an operation), and returns which items changed
// on disk. Items are matched by ID. Files the tree wrote itself hash the
// same as when written, so they do not count as changes; neither do files
// that were touched but not changed.
func (t *Tree) Refresh() Changes {
	before := map[string]nodeState{}
	var order []string
	t.Walk(func(n *Node) bool {
		before[n.ID] = stateOf(n)
		order = append(order, n.ID)
		return true
	})
	oldRootID := t.root.ID
	oldRoot := before[oldRootID]
	t.reload()

	var c Changes
	seen := map[string]bool{}
	t.Walk(func(n *Node) bool {
		seen[n.ID] = true
		now := stateOf(n)
		if n.Kind == KindRoot {
			if now.hasSum != oldRoot.hasSum || now.sum != oldRoot.sum || now.readOnly != oldRoot.readOnly {
				c.Root, c.OldRoot = true, oldRoot.file
			}
			return true
		}
		old, ok := before[n.ID]
		change := NodeChange{ID: n.ID, Kind: n.Kind, Path: n.Path, ParentID: now.parentID}
		switch {
		case !ok || old.kind == KindRoot:
			c.Added = append(c.Added, change)
		case old.hasSum != now.hasSum || old.sum != now.sum || old.kind != now.kind || old.readOnly != now.readOnly:
			change.Old = old.file
			c.Modified = append(c.Modified, change)
		case old.path != now.path:
			c.Moved = append(c.Moved, change)
		}
		return true
	})
	for _, id := range order {
		old := before[id]
		if seen[id] || old.kind == KindRoot {
			continue
		}
		c.Removed = append(c.Removed, NodeChange{ID: id, Kind: old.kind, Path: old.path, ParentID: old.parentID, Old: old.file})
	}
	return c
}
