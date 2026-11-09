package collection

import (
	"bytes"
	"cmp"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/storage/atomicfile"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// CodeScratchFailed: the scratch directory or a scratch request file could
// not be read or written. The kind comes from the cause. Details: path.
const CodeScratchFailed = "collection.scratch_failed"

// ScratchNamePrefix starts the name a scratch request gets when none is
// given: "Scratch 1", "Scratch 2", …
const ScratchNamePrefix = "Scratch"

const (
	scratchDirPerm  fs.FileMode = 0o700
	scratchFilePerm fs.FileMode = 0o600
)

// scratchIDPattern is the ID of a scratch request, which is also its file
// name: a generated request ID, so it cannot leave the directory.
var scratchIDPattern = regexp.MustCompile(`^req_[0-9a-f]{16}$`)

// Scratch holds the scratch requests of a project (COL-06): quick requests
// that are not part of the project, kept in the workspace's local side
// files as one request file each:
//
//	<dir>/<id>.json   a request file, in the format of the project's
//
// The file name is the request's ID, a generated one ("req_" and 16 hex
// digits), so a scratch request keeps its ID when it is saved into the
// project (Tree.AddRequest). Scratch is not safe for concurrent use; its
// owner serialises access. See docs/collection.md (Scratch requests).
type Scratch struct {
	dir string
}

// NewScratch returns the scratch requests kept in dir (absolute; created on
// the first write).
func NewScratch(dir string) *Scratch { return &Scratch{dir: dir} }

// Dir returns the directory the scratch requests are kept in.
func (s *Scratch) Dir() string { return s.dir }

// IsScratchID reports whether id has the form of a scratch request's ID.
func IsScratchID(id string) bool { return scratchIDPattern.MatchString(id) }

// ScratchItem is one scratch request.
type ScratchItem struct {
	ID string
	// File is the request, its id set to ID.
	File projectfile.File
	// ReadOnly is ReadOnlyNewerVersion for a file from a newer PostSonde,
	// which can be opened but not changed; else Writable.
	ReadOnly ReadOnlyReason
}

// Name returns the item's display name: its name member, or "Untitled".
func (it ScratchItem) Name() string { return ScratchName(it.File) }

// ScratchName returns the display name of a scratch request file.
func ScratchName(f projectfile.File) string {
	if n := strings.TrimSpace(requestName(f)); n != "" {
		return requestName(f)
	}
	return "Untitled"
}

func (s *Scratch) path(id string) string { return filepath.Join(s.dir, id+".json") }

// List returns the scratch requests sorted by name (ignoring case, see
// FoldName), then ID. Files that do not load are reported as problems
// (Path is the file's absolute path) and left out; files whose name is
// not a scratch ID are ignored. A missing directory has none.
func (s *Scratch) List() ([]ScratchItem, []Problem) {
	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	var problems []Problem
	if err != nil {
		problems = append(problems, Problem{Path: s.dir, Err: scratchFailed(err, "The scratch requests could not be listed.", s.dir)})
	}
	var items []ScratchItem
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !IsScratchID(id) || !e.Type().IsRegular() {
			continue
		}
		it, err := s.read(id)
		if err != nil {
			var e *errs.Error
			if !errors.As(err, &e) {
				e = scratchFailed(err, "A scratch request could not be loaded.", s.path(id))
			}
			problems = append(problems, Problem{Path: s.path(id), NodeID: id, Err: e})
			continue
		}
		items = append(items, it)
	}
	slices.SortFunc(items, func(a, b ScratchItem) int {
		return cmp.Or(cmp.Compare(FoldName(a.Name()), FoldName(b.Name())), cmp.Compare(a.ID, b.ID))
	})
	return items, problems
}

// Get returns the scratch request id. It fails with CodeNotFound when
// there is none, and with the projectfile errors when its file does not
// load.
func (s *Scratch) Get(id string) (ScratchItem, error) {
	if !IsScratchID(id) {
		return ScratchItem{}, notFound(id)
	}
	return s.read(id)
}

// Has reports whether the scratch request id exists (whether or not its
// file loads).
func (s *Scratch) Has(id string) bool {
	if !IsScratchID(id) {
		return false
	}
	_, err := os.Lstat(s.path(id))
	return err == nil
}

func (s *Scratch) read(id string) (ScratchItem, error) {
	path := s.path(id)
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		return ScratchItem{}, notFound(id)
	}
	f, err := projectfile.ReadFile(path)
	if err != nil {
		return ScratchItem{}, err
	}
	if !f.FileKind().IsRequest() {
		return ScratchItem{}, errs.New(errs.InvalidArgument, "A scratch request file holds something else than a request.").
			WithCode(CodeWrongNode).
			WithDetail("id", id).
			WithDetail("path", path)
	}
	if RequestFileID(f) != id {
		f = withID(f, id)
	}
	it := ScratchItem{ID: id, File: f}
	if n, ok := f.(interface{ Newer() bool }); ok && n.Newer() {
		it.ReadOnly = ReadOnlyNewerVersion
	}
	return it, nil
}

// CheckWritable returns the error an edit of the scratch request id must
// be refused with: CodeNotFound, the projectfile errors of a file that
// does not load, or CodeReadOnly for a file from a newer PostSonde; nil
// when it may be saved.
func (s *Scratch) CheckWritable(id string) error {
	it, err := s.Get(id)
	if err != nil {
		return err
	}
	if it.ReadOnly != Writable {
		return readOnly(id, it.ReadOnly)
	}
	return nil
}

// Create writes a new, empty scratch request of kind (see NewRequestFile)
// called name, or "Scratch N" with the first N no other scratch request
// uses when name is blank, and returns it.
func (s *Scratch) Create(kind projectfile.Kind, name string) (projectfile.File, error) {
	if strings.TrimSpace(name) == "" {
		name = s.nextName()
	}
	f, err := NewRequestFile(kind, projectfile.NewID(kind), name)
	if err != nil {
		return nil, err
	}
	if err := s.write(RequestFileID(f), f); err != nil {
		return nil, err
	}
	return f, nil
}

// nextName returns "Scratch N" for the smallest N from 1 that no scratch
// request is called.
func (s *Scratch) nextName() string {
	items, _ := s.List()
	used := map[int]bool{}
	for _, it := range items {
		rest, ok := strings.CutPrefix(it.Name(), ScratchNamePrefix+" ")
		if !ok {
			continue
		}
		if n, err := strconv.Atoi(rest); err == nil {
			used[n] = true
		}
	}
	n := 1
	for used[n] {
		n++
	}
	return ScratchNamePrefix + " " + strconv.Itoa(n)
}

// Save replaces the scratch request id with f, writing it (atomically)
// only when its encoding differs from the file. The request must exist
// (CodeNotFound), so a late save cannot bring a deleted one back; f must
// be a request with id id (CodeWrongNode).
func (s *Scratch) Save(id string, f projectfile.File) error {
	if !s.Has(id) {
		return notFound(id)
	}
	if RequestFileID(f) != id {
		return wrongNode(id, "The scratch request does not match the file it is saved to.")
	}
	return s.write(id, f)
}

func (s *Scratch) write(id string, f projectfile.File) error {
	data, err := projectfile.Marshal(f)
	if err != nil {
		return err
	}
	path := s.path(id)
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		return nil
	}
	if err := os.MkdirAll(s.dir, scratchDirPerm); err != nil {
		return scratchFailed(err, "The scratch requests could not be saved.", s.dir)
	}
	if err := atomicfile.WriteFile(path, data, scratchFilePerm); err != nil {
		return scratchFailed(err, "The scratch request could not be saved.", path)
	}
	return nil
}

// Delete removes the scratch request id for good. A request that is not
// there is not an error.
func (s *Scratch) Delete(id string) error {
	if !IsScratchID(id) {
		return nil
	}
	path := s.path(id)
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return scratchFailed(err, "The scratch request could not be deleted.", path)
	}
	return nil
}

func scratchFailed(err error, msg, path string) *errs.Error {
	return errs.Wrap(err, errs.KindOf(err), msg).
		WithCode(CodeScratchFailed).
		WithHint("Check that PostSonde's data folder is writable and the disk is not full.").
		WithDetail("path", path)
}

// requestName returns the name member of a request file; "" for another
// type.
func requestName(f projectfile.File) string {
	switch v := f.(type) {
	case *projectfile.HTTPRequest:
		return v.Name
	case *projectfile.GraphQLRequest:
		return v.Name
	case *projectfile.TCPRequest:
		return v.Name
	case *projectfile.UDPRequest:
		return v.Name
	case *projectfile.WebSocketRequest:
		return v.Name
	case *projectfile.GRPCRequest:
		return v.Name
	case *projectfile.SSERequest:
		return v.Name
	case *projectfile.SocketIORequest:
		return v.Name
	}
	return ""
}
