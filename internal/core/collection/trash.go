package collection

import (
	"cmp"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/storage/atomicfile"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// Error codes of the trash.
const (
	// CodeTrashNotFound: no complete trash entry has the ID (kind
	// not_found). Details: trashId.
	CodeTrashNotFound = "collection.trash_not_found"
	// CodeTrashFailed: the trash could not be read or written. The kind
	// comes from the cause. Details: path.
	CodeTrashFailed = "collection.trash_failed"
)

const (
	// TrashManifestName is the file describing a trash entry.
	TrashManifestName = "entry.json"
	// TrashDataDir is the directory of a trash entry holding the deleted
	// file or directory under its original name.
	TrashDataDir = "data"
	// TrashEntryPrefix starts every trash entry ID.
	TrashEntryPrefix = "trash_"
	// trashManifestVersion is the version of the manifest format.
	trashManifestVersion = 1
)

// Trash is the local trash of a project: a directory outside the project
// (in the workspace's local side files) with one subdirectory per deleted
// item:
//
//	<dir>/trash_<16 hex>/entry.json        the TrashEntry
//	<dir>/trash_<16 hex>/data/<entry name> the deleted file or directory
//
// Entries stay until Purge removes them. See docs/collection.md.
type Trash struct {
	dir string
	now func() time.Time
}

// NewTrash returns the trash kept in dir (absolute; created on the first
// delete).
func NewTrash(dir string) *Trash {
	return &Trash{dir: dir, now: time.Now}
}

// Dir returns the trash directory.
func (tr *Trash) Dir() string { return tr.dir }

// TrashEntry describes a deleted item. It is also the manifest's format.
type TrashEntry struct {
	// ID names the entry: TrashEntryPrefix and 16 hex digits.
	ID string `json:"id"`
	// NodeID is the item's ID when it was deleted.
	NodeID string `json:"nodeId"`
	// Kind and FileKind are the node's kind and its file's kind member.
	Kind     NodeKind         `json:"kind"`
	FileKind projectfile.Kind `json:"fileKind"`
	// Name is the display name.
	Name string `json:"name"`
	// Entry is the file or directory name; Path the slash path relative to
	// the project root.
	Entry string `json:"entry"`
	Path  string `json:"path"`
	// ParentID is the folder it was in, "" for the top level; Index its
	// position there.
	ParentID string `json:"parentId"`
	Index    int    `json:"index"`
	// DeletedAt is when it was deleted, in UTC.
	DeletedAt time.Time `json:"deletedAt"`
}

type trashManifest struct {
	Version int `json:"version"`
	TrashEntry
}

// Restored is the result of Restore.
type Restored struct {
	// NodeID is the restored item's ID: the old one unless another item
	// has it now. It is "" if the restored file is no longer an item.
	NodeID string
	// Path is the slash path it was restored to.
	Path string
	// ToRoot is set when its folder no longer exists, so it was restored to
	// the top level.
	ToRoot bool
}

var trashIDPattern = regexp.MustCompile(`^` + TrashEntryPrefix + `[0-9a-f]{16}$`)

// Delete moves the item id (a file or a whole directory, with any other
// files in it) into trash and removes it from its folder's order. Any item
// can be deleted, read-only ones included, as long as its folder is
// writable. It returns the trash entry to pass to Restore.
func (t *Tree) Delete(id string, trash *Trash) (TrashEntry, error) {
	n, err := t.opNode(id)
	if err != nil {
		return TrashEntry{}, err
	}
	parent := n.Parent
	if err := checkContainer(parent); err != nil {
		return TrashEntry{}, err
	}
	if rel, err := filepath.Rel(t.dir, trash.dir); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return TrashEntry{}, errs.New(errs.InvalidArgument, "The trash cannot be inside the project folder.").
			WithCode(CodeTrashFailed).
			WithHint("This is a bug in PostSonde.").
			WithDetail("path", trash.dir)
	}
	if err := t.materializeIDs(); err != nil {
		return TrashEntry{}, err
	}
	e := TrashEntry{
		ID:        TrashEntryPrefix + randomHex(),
		NodeID:    n.ID,
		Kind:      n.Kind,
		FileKind:  n.FileKind,
		Name:      n.DisplayName(),
		Entry:     n.Entry,
		Path:      n.Path,
		Index:     slices.Index(parent.Children, n),
		DeletedAt: trash.now().UTC(),
	}
	if parent.Kind != KindRoot {
		e.ParentID = parent.ID
	}
	entryDir := filepath.Join(trash.dir, e.ID)
	dataDir := filepath.Join(entryDir, TrashDataDir)
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return TrashEntry{}, trashFailed(err, "The trash could not be created.", dataDir)
	}
	manifest := filepath.Join(entryDir, TrashManifestName)
	if err := atomicfile.WriteJSON(manifest, trashManifest{Version: trashManifestVersion, TrashEntry: e}, 0o600); err != nil {
		_ = os.RemoveAll(entryDir)
		return TrashEntry{}, trashFailed(err, "The trash could not be written.", manifest)
	}
	if err := moveEntry(t.Abs(n.Path), filepath.Join(dataDir, n.Entry)); err != nil {
		_ = os.RemoveAll(entryDir)
		return TrashEntry{}, err
	}
	// From here the item is in the trash; a stale order name is ignored
	// by Load.
	if err := t.writeOrder(parent, entries(parent.Children, n)); err != nil {
		return TrashEntry{}, err
	}
	t.reload()
	return e, nil
}

// Restore moves the trash entry trashID back into the project: into its
// folder at its old position, or at the end of the top level when that
// folder no longer exists. The file or directory keeps its name unless it
// is taken meanwhile, in which case it gets a collision suffix. Restored
// files whose id another item has now get new IDs first. The entry is
// removed from the trash.
func (t *Tree) Restore(trash *Trash, trashID string) (Restored, error) {
	if t.root.ReadOnly != Writable {
		return Restored{}, readOnly(t.root.ID, t.root.ReadOnly)
	}
	e, err := trash.read(trashID)
	if err != nil {
		return Restored{}, err
	}
	parent, toRoot := t.root, false
	if e.ParentID != "" {
		if p := t.byID[e.ParentID]; p != nil && p.Kind == KindFolder {
			parent = p
		} else {
			toRoot = true
		}
	}
	if err := checkContainer(parent); err != nil {
		return Restored{}, err
	}
	if err := t.materializeIDs(); err != nil {
		return Restored{}, err
	}
	entryDir := filepath.Join(trash.dir, e.ID)
	dataDir := filepath.Join(entryDir, TrashDataDir)
	src := filepath.Join(dataDir, e.Entry)
	fi, err := os.Lstat(src)
	if err != nil {
		return Restored{}, trashNotFound(trashID)
	}
	if err := t.renewTrashIDs(dataDir, e.Entry, fi); err != nil {
		return Restored{}, err
	}
	ext := extFor(fi.IsDir())
	dir := t.Abs(parent.Path)
	entry, err := UniqueName(dir, strings.TrimSuffix(e.Entry, ext), ext, parent.Kind == KindRoot)
	if err != nil {
		return Restored{}, err
	}
	if err := moveEntry(src, filepath.Join(dir, entry)); err != nil {
		return Restored{}, err
	}
	index := e.Index
	if toRoot {
		index = End
	}
	rel := path.Join(parent.Path, entry)
	if err := t.writeOrder(parent, insertAt(entries(parent.Children, nil), index, entry)); err != nil {
		return Restored{}, err
	}
	// The item is back; an entry left without data is skipped by List and
	// removed by Purge.
	_ = os.RemoveAll(entryDir)
	t.reload()
	r := Restored{Path: rel, ToRoot: toRoot}
	if n := t.NodeAt(rel); n != nil {
		r.NodeID = n.ID
	}
	return r, nil
}

// renewTrashIDs gives a new ID to every writable folder or request file in
// the trashed item dataDir/entry whose id is already used by the tree or
// by an earlier file of the item, so restoring it cannot take an ID away
// from an item (Load keeps an id for the smallest path). Read-only files
// are left as they are.
func (t *Tree) renewTrashIDs(dataDir, entry string, fi fs.FileInfo) error {
	l := &loader{t: &Tree{dir: dataDir, byID: map[string]*Node{}, byPath: map[string]*Node{}}}
	top := &Node{Kind: KindFolder}
	switch mode := fi.Mode(); {
	case mode.IsDir():
		l.loadFolder(top, entry, entry, 1)
	case mode.IsRegular():
		l.loadFile(top, entry, entry, false)
	case mode&fs.ModeSymlink != 0:
		if st, err := os.Stat(filepath.Join(dataDir, entry)); err == nil && st.Mode().IsRegular() {
			l.loadFile(top, entry, entry, true)
		}
	}
	seen := map[string]bool{}
	for i, n := range l.nodes {
		id := l.fileIDs[i]
		if strings.TrimSpace(id) == "" {
			continue
		}
		if t.byID[id] == nil && !seen[id] {
			seen[id] = true
			continue
		}
		if n.File == nil || n.ReadOnly != Writable {
			continue
		}
		newID := projectfile.NewID(n.File.FileKind())
		if err := t.writeNew(l.t.Abs(n.FilePath()), newID, withID(n.File, newID)); err != nil {
			return err
		}
	}
	return nil
}

// List returns the complete entries of the trash, most recently deleted
// first. Entries without a readable manifest or without their data (left
// by an interrupted delete or restore) are skipped. A missing trash is
// empty.
func (tr *Trash) List() ([]TrashEntry, error) {
	dirs, err := os.ReadDir(tr.dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, trashFailed(err, "The trash could not be read.", tr.dir)
	}
	out := make([]TrashEntry, 0, len(dirs))
	for _, d := range dirs {
		if !d.IsDir() || !trashIDPattern.MatchString(d.Name()) {
			continue
		}
		if e, err := tr.read(d.Name()); err == nil {
			out = append(out, e)
		}
	}
	slices.SortFunc(out, func(a, b TrashEntry) int {
		return cmp.Or(b.DeletedAt.Compare(a.DeletedAt), cmp.Compare(a.ID, b.ID))
	})
	return out, nil
}

// Purge permanently removes the trash entries deleted before before
// (entries without a readable manifest count from their directory's
// modification time) and returns how many it removed. It is only ever
// called explicitly. One failing entry does not stop the others.
func (tr *Trash) Purge(before time.Time) (int, error) {
	dirs, err := os.ReadDir(tr.dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, trashFailed(err, "The trash could not be read.", tr.dir)
	}
	removed := 0
	var all []error
	for _, d := range dirs {
		if !d.IsDir() || !trashIDPattern.MatchString(d.Name()) {
			continue
		}
		entryDir := filepath.Join(tr.dir, d.Name())
		var when time.Time
		if m, err := readManifest(entryDir); err == nil {
			when = m.DeletedAt
		} else if fi, err := d.Info(); err == nil {
			when = fi.ModTime()
		} else {
			continue
		}
		if !when.Before(before) {
			continue
		}
		if err := os.RemoveAll(entryDir); err != nil {
			all = append(all, trashFailed(err, "A trash entry could not be removed.", entryDir))
			continue
		}
		removed++
	}
	return removed, errors.Join(all...)
}

// read returns the complete entry trashID.
func (tr *Trash) read(trashID string) (TrashEntry, error) {
	if !trashIDPattern.MatchString(trashID) {
		return TrashEntry{}, trashNotFound(trashID)
	}
	entryDir := filepath.Join(tr.dir, trashID)
	m, err := readManifest(entryDir)
	if err != nil || m.Entry == "" || m.Entry != filepath.Base(m.Entry) || strings.ContainsAny(m.Entry, `/\`) || m.Entry == ".." {
		return TrashEntry{}, trashNotFound(trashID)
	}
	if _, err := os.Lstat(filepath.Join(entryDir, TrashDataDir, m.Entry)); err != nil {
		return TrashEntry{}, trashNotFound(trashID)
	}
	m.ID = trashID
	return m.TrashEntry, nil
}

func readManifest(entryDir string) (trashManifest, error) {
	var m trashManifest
	data, err := os.ReadFile(filepath.Join(entryDir, TrashManifestName))
	if err != nil {
		return m, err
	}
	err = json.Unmarshal(data, &m)
	return m, err
}

// moveEntry renames from to to, which must not exist. Across file systems
// (the trash is in the app data directory, which may be on another disk
// than the project) it copies and then removes the original.
func moveEntry(from, to string) error {
	if _, err := os.Lstat(to); err == nil {
		return fileOpFailed(fs.ErrExist, "An item with the same file name already exists.", from, to).
			WithHint("Reload the project and try again.")
	}
	err := renameFunc(from, to)
	if err == nil {
		return nil
	}
	if !isCrossDevice(err) {
		return fileOpFailed(err, "The item could not be moved.", from, to)
	}
	if err := copyAll(from, to); err != nil {
		_ = os.RemoveAll(to)
		return fileOpFailed(err, "The item could not be copied to another disk.", from, to)
	}
	if err := os.RemoveAll(from); err != nil {
		return fileOpFailed(err, "The item was copied but the original could not be removed.", from, to)
	}
	return nil
}

func isCrossDevice(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	// ERROR_NOT_SAME_DEVICE is 17 on Windows.
	return errno == syscall.EXDEV || runtime.GOOS == "windows" && errno == 17
}

// copyAll copies the file, symbolic link or directory tree from to the new
// path to, keeping permissions and file modification times.
func copyAll(from, to string) error {
	return filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(from, p)
		if err != nil {
			return err
		}
		dst := filepath.Join(to, rel)
		fi, err := d.Info()
		if err != nil {
			return err
		}
		switch mode := fi.Mode(); {
		case mode.IsDir():
			return os.Mkdir(dst, mode.Perm()|0o700)
		case mode&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(target, dst)
		case mode.IsRegular():
			return copyFile(p, dst, fi)
		}
		return nil // sockets, devices: not project content
	})
}

func copyFile(from, to string, fi fs.FileInfo) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fi.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chtimes(to, fi.ModTime(), fi.ModTime())
}

func trashNotFound(trashID string) *errs.Error {
	return errs.New(errs.NotFound, "The deleted item is no longer in the trash.").
		WithCode(CodeTrashNotFound).
		WithHint("It may have been restored or emptied from the trash already.").
		WithDetail("trashId", trashID)
}

func trashFailed(err error, msg, p string) *errs.Error {
	return errs.Wrap(err, errs.KindOf(err), msg).
		WithCode(CodeTrashFailed).
		WithHint("Check that PostSonde's data folder is writable and the disk is not full.").
		WithDetail("path", p)
}
