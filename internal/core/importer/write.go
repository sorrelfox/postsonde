package importer

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/collection"
	"github.com/sorrelfox/postsonde/internal/core/environments"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/requestsecrets"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// Target is the open project an import writes into.
type Target struct {
	// Tree is the project's request tree; it is reloaded after the write.
	Tree *collection.Tree
	// Secrets keeps typed auth secrets of the new requests and folders in
	// the workspace's local side files.
	Secrets *requestsecrets.Store
	// Environments creates the environments; nil leaves them out (as
	// Options.SkipEnvironments does).
	Environments *environments.Store
}

// Options say where and how an import is written.
type Options struct {
	// ParentID is the folder the import goes into; "" for the top level.
	ParentID string
	// Index is the position among the folder's items of the new folder
	// (or of the first item, with IntoParent); collection.End (-1) puts it
	// last. The zero value puts it first.
	Index int
	// FolderName names the new folder; blank uses the model's Name. When
	// the folder already has an item with that name (ignoring case), " 2",
	// " 3"… is added (CodeRenamed).
	FolderName string
	// IntoParent writes the collection's items straight into ParentID
	// instead of into a new folder. The collection's own settings are then
	// not written (CodeSettingsSkipped when it has any).
	IntoParent bool
	// SkipEnvironments leaves the environments out
	// (CodeEnvironmentsSkipped).
	SkipEnvironments bool
}

// maxEnvironmentName is environments.MaxNameLength.
const maxEnvironmentName = environments.MaxNameLength

// Write creates the model's folders, requests and environments in the
// project and returns the report. It never overwrites: the new folder,
// every file and every environment get free names. It fails, writing
// nothing, when the import cannot start (the parent is not a writable
// folder, the new folder cannot be created); items that fail after that
// are StatusFailed in the report, with Err set, and the rest is still
// written. The tree is reloaded before Write returns. A model without a
// folder or request (only environments or skipped items) makes no folder
// (the report is then IntoParent).
func Write(t Target, m *Model, opts Options) (*Report, error) {
	if !hasWritable(&m.Root) && !hasSettings(&m.Root) {
		// No folder or request (only environments, or only skipped items):
		// no folder to make.
		opts.IntoParent = true
	}
	r := Preview(m)
	r.IntoParent = opts.IntoParent
	w := &writer{t: t, r: r}
	if opts.IntoParent {
		if err := w.intoParent(m, opts); err != nil {
			return nil, err
		}
	} else if err := w.newFolder(m, opts); err != nil {
		return nil, err
	}
	if w.direct {
		t.Tree.Reload()
	}
	w.environments(m, opts)
	r.recount()
	return r, nil
}

type writer struct {
	t Target
	r *Report
	// direct is set once files were written without the tree's operations.
	direct bool
}

// newFolder creates the collection's folder in opts.ParentID and writes
// everything into it.
func (w *writer) newFolder(m *Model, opts Options) error {
	name := strings.TrimSpace(opts.FolderName)
	if name == "" {
		name = m.Name
	}
	free := freeName(w.siblingNames(opts.ParentID), name)
	if free != name {
		w.r.note(0, Diagnostic{
			Code: CodeRenamed, Severity: SeverityInfo,
			Message: "The folder already has an item called " + quote(name) + ", so the import is called " + quote(free) + ".",
		})
	}
	id, err := w.t.Tree.CreateFolder(opts.ParentID, free, opts.Index)
	if err != nil {
		return err
	}
	root := &w.r.Items[0]
	root.Name, root.NodeID, root.Status = free, id, StatusImported
	n := w.t.Tree.Node(id)
	if n == nil {
		return errs.New(errs.Internal, "The new folder is not in the project.").WithHint("This is a bug in PostSonde.")
	}
	w.direct = true
	w.fillFolder(w.t.Tree.Abs(n.Path), id, free, &m.Root, 0)
	return nil
}

// intoParent writes the collection's items into the existing folder
// opts.ParentID.
func (w *writer) intoParent(m *Model, opts Options) error {
	parent := w.t.Tree.Root()
	if opts.ParentID != "" {
		parent = w.t.Tree.Node(opts.ParentID)
	}
	if parent == nil || !parent.IsContainer() {
		return errs.New(errs.NotFound, "The folder to import into is no longer in the project.").
			WithCode(collection.CodeNotFound).
			WithHint("Choose another folder.").
			WithDetail("id", opts.ParentID)
	}
	if parent.ReadOnly != collection.Writable {
		return errs.New(errs.Unsupported, "The folder to import into cannot be changed.").
			WithCode(collection.CodeReadOnly).
			WithDetail("id", parent.ID)
	}
	root := &w.r.Items[0]
	root.Name, root.NodeID, root.Status = parent.DisplayName(), parent.ID, StatusImported
	if parent.Kind == collection.KindRoot {
		root.Name, root.NodeID = "", ""
	}
	if hasSettings(&m.Root) {
		w.r.note(0, Diagnostic{
			Code: CodeSettingsSkipped, Severity: SeverityWarning,
			Message: "The collection's own variables, headers, auth and description were not imported, because its items went into an existing folder.",
			Source:  m.Root.Source,
		})
	}
	index := opts.Index
	next := func() int {
		i := index
		if index >= 0 {
			index++
		}
		return i
	}
	for _, it := range m.Root.Items {
		switch {
		case it.Request != nil:
			at := w.r.index[it.Request]
			f, secrets, err := requestFile(it.Request)
			if err != nil {
				w.r.fail(at, err, CodeInvalidItem)
				continue
			}
			id := collection.RequestFileID(f)
			if err := w.writeSecrets(id, secrets); err != nil {
				w.r.fail(at, err, CodeWriteFailed)
				continue
			}
			got, err := w.t.Tree.AddRequest(opts.ParentID, f, it.Request.Name, next())
			if err != nil {
				if len(secrets) > 0 {
					_ = w.t.Secrets.Remove(id)
				}
				w.r.fail(at, err, CodeWriteFailed)
				continue
			}
			if got != id && len(secrets) > 0 {
				// The tree already had the ID: the secrets follow the new one.
				_ = w.t.Secrets.Write(got, secrets)
				_ = w.t.Secrets.Remove(id)
			}
			w.imported(at, got, it.Request.Name)
		case it.Folder != nil:
			at := w.r.index[it.Folder]
			id, err := w.t.Tree.CreateFolder(opts.ParentID, it.Folder.Name, next())
			if err != nil {
				w.failSubtree(at, err)
				continue
			}
			n := w.t.Tree.Node(id)
			if n == nil {
				w.failSubtree(at, errs.New(errs.Internal, "The new folder is not in the project."))
				continue
			}
			w.imported(at, id, it.Folder.Name)
			w.direct = true
			w.fillFolder(w.t.Tree.Abs(n.Path), id, it.Folder.Name, it.Folder, at)
		}
	}
	return nil
}

// fillFolder writes the settings and items of f into the directory dir of
// the existing folder id (created by the tree, with a _folder.json).
func (w *writer) fillFolder(dir, id, name string, f *Folder, at int) {
	file := w.folderFile(id, name, f, at)
	if err := w.writeFile(filepath.Join(dir, projectfile.FolderFileName), id, file); err != nil {
		w.r.note(at, Diagnostic{
			Code: CodeWriteFailed, Severity: SeverityError,
			Message: "The folder's settings could not be written: " + errorMessage(err),
		})
	}
	w.fillItems(dir, file, f, at)
}

// fillItems writes the items of f into dir, then the folder file with
// their order.
func (w *writer) fillItems(dir string, file *projectfile.Folder, f *Folder, at int) {
	var order []string
	for _, it := range f.Items {
		switch {
		case it.Request != nil:
			if entry, ok := w.writeRequest(dir, it.Request); ok {
				order = append(order, entry)
			}
		case it.Folder != nil:
			if entry, ok := w.writeFolder(dir, it.Folder); ok {
				order = append(order, entry)
			}
		}
	}
	if len(order) == 0 {
		return
	}
	file.Order = order
	if err := w.writeFile(filepath.Join(dir, projectfile.FolderFileName), file.ID, file); err != nil {
		w.r.note(at, Diagnostic{
			Code: CodeWriteFailed, Severity: SeverityWarning,
			Message: "The order of the folder's items could not be written, so they are sorted by name: " + errorMessage(err),
		})
	}
}

// writeRequest writes q as a new file in dir and returns its entry name.
func (w *writer) writeRequest(dir string, q *Request) (string, bool) {
	at := w.r.index[q]
	f, secrets, err := requestFile(q)
	if err != nil {
		w.r.fail(at, err, CodeInvalidItem)
		return "", false
	}
	id := collection.RequestFileID(f)
	entry, err := collection.UniqueName(dir, collection.Slug(q.Name), projectfile.Ext, false)
	if err != nil {
		w.r.fail(at, err, CodeWriteFailed)
		return "", false
	}
	if err := w.writeSecrets(id, secrets); err != nil {
		w.r.fail(at, err, CodeWriteFailed)
		return "", false
	}
	if err := w.writeFile(filepath.Join(dir, entry), id, f); err != nil {
		if len(secrets) > 0 {
			_ = w.t.Secrets.Remove(id)
		}
		w.r.fail(at, err, CodeWriteFailed)
		return "", false
	}
	w.imported(at, id, q.Name)
	return entry, true
}

// writeFolder creates f as a new directory in dir, with its settings and
// items, and returns its entry name. A directory counts as a folder once
// its _folder.json is written, so that is written first.
func (w *writer) writeFolder(dir string, f *Folder) (string, bool) {
	at := w.r.index[f]
	entry, err := collection.UniqueName(dir, collection.Slug(f.Name), "", false)
	if err != nil {
		w.failSubtree(at, err)
		return "", false
	}
	abs := filepath.Join(dir, entry)
	if err := os.Mkdir(abs, 0o755); err != nil {
		w.failSubtree(at, errs.Wrap(err, errs.KindOf(err), "The folder could not be created.").
			WithCode(collection.CodeFileOpFailed).WithDetail("path", abs))
		return "", false
	}
	id := projectfile.NewID(projectfile.KindFolder)
	file := w.folderFile(id, f.Name, f, at)
	if err := w.writeFile(filepath.Join(abs, projectfile.FolderFileName), id, file); err != nil {
		_ = os.Remove(abs)
		_ = w.t.Secrets.Remove(id)
		w.failSubtree(at, err)
		return "", false
	}
	w.imported(at, id, f.Name)
	w.fillItems(abs, file, f, at)
	return entry, true
}

func (w *writer) writeSecrets(id string, values map[string]string) error {
	if len(values) == 0 {
		return nil
	}
	return w.t.Secrets.Write(id, values)
}

// writeFile writes f, the file of item id, to the new file path, after
// moving any secret still in it to id's local side file
// (requestsecrets.Store.MoveOut), so an import can never put a secret into
// a project file (PRV-09).
func (w *writer) writeFile(path, id string, f projectfile.File) error {
	f, err := w.t.Secrets.MoveOut(id, f)
	if err != nil {
		return err
	}
	return projectfile.WriteFile(path, f)
}

// folderFile builds the _folder.json of a new folder from f's settings.
// A typed auth secret and the values of the secret variables go to the
// local side file of id (requestsecrets.SplitFile); the file keeps the
// variables with "value": "".
func (w *writer) folderFile(id, name string, f *Folder, at int) *projectfile.Folder {
	auth := f.Auth
	if auth.Type == "" {
		auth.Type = projectfile.AuthInherit
	}
	out := &projectfile.Folder{
		ID: id, Name: name, Description: f.Description,
		Headers:   append([]projectfile.KeyValue(nil), f.Headers...),
		Variables: append([]projectfile.Variable(nil), f.Variables...),
		Auth:      auth,
		GRPC:      f.GRPC,
		Server:    f.Server,
		Unknown:   f.Unknown,
	}
	file, secrets := requestsecrets.SplitFile(out)
	if err := w.writeSecrets(id, secrets); err != nil {
		// Keep nothing typed in the project file: the secrets are lost.
		w.r.note(at, Diagnostic{
			Code: CodeWriteFailed, Severity: SeverityWarning,
			Message: "The folder's auth secret and secret variables could not be kept: " + errorMessage(err),
		})
	}
	return file.(*projectfile.Folder)
}

func (w *writer) imported(at int, id, name string) {
	it := &w.r.Items[at]
	it.Status, it.NodeID, it.Name = StatusImported, id, name
}

// failSubtree marks item at failed with err, and everything below it
// failed because of it.
func (w *writer) failSubtree(at int, err error) {
	w.r.fail(at, err, CodeWriteFailed)
	for _, i := range w.r.subtree(at) {
		if w.r.Items[i].Status == StatusSkipped {
			continue
		}
		it := &w.r.Items[i]
		it.Status, it.NodeID = StatusFailed, ""
		it.Diagnostics = append(it.Diagnostics, Diagnostic{
			Code: CodeWriteFailed, Severity: SeverityError,
			Message: "Not imported: the folder it is in could not be created.",
		})
	}
}

// environments creates the model's environments with free names.
func (w *writer) environments(m *Model, opts Options) {
	if len(m.Environments) == 0 {
		return
	}
	if opts.SkipEnvironments || w.t.Environments == nil {
		for i := range m.Environments {
			at := w.r.index[&m.Environments[i]]
			w.r.Items[at].Status = StatusSkipped
			w.r.note(at, Diagnostic{
				Code: CodeEnvironmentsSkipped, Severity: SeverityInfo,
				Message: "Environments were not imported.",
			})
		}
		return
	}
	snap, err := w.t.Environments.List()
	var taken []string
	if err == nil {
		for _, e := range snap.Environments {
			taken = append(taken, e.Name)
		}
		for _, p := range snap.Problems {
			taken = append(taken, strings.TrimSuffix(p.FileName, filepath.Ext(p.FileName)))
		}
	}
	for i := range m.Environments {
		e := &m.Environments[i]
		at := w.r.index[e]
		if err != nil {
			w.r.fail(at, err, CodeWriteFailed)
			continue
		}
		name := strings.TrimSpace(e.Name)
		if name == "" {
			name = "Environment"
		}
		name = truncateRunes(name, maxEnvironmentName-4)
		free := freeName(taken, name)
		if free != name {
			w.r.note(at, Diagnostic{
				Code: CodeRenamed, Severity: SeverityInfo,
				Message: "An environment called " + quote(name) + " exists, so this one is called " + quote(free) + ".",
				Source:  e.Source,
			})
		}
		env, cerr := w.t.Environments.Create(free)
		if cerr != nil {
			w.r.fail(at, cerr, CodeWriteFailed)
			continue
		}
		taken = append(taken, free)
		if len(e.Variables) > 0 {
			if _, serr := w.t.Environments.SaveVariables(env.ID, e.Variables); serr != nil {
				_ = w.t.Environments.Delete(env.ID)
				w.r.fail(at, serr, CodeWriteFailed)
				continue
			}
		}
		w.imported(at, env.ID, free)
	}
}

// siblingNames returns the display names of the items of folder parentID.
func (w *writer) siblingNames(parentID string) []string {
	n := w.t.Tree.Root()
	if parentID != "" {
		n = w.t.Tree.Node(parentID)
	}
	if n == nil {
		return nil
	}
	out := make([]string, 0, len(n.Children))
	for _, c := range n.Children {
		out = append(out, c.DisplayName())
	}
	return out
}

// requestFile validates q's file and returns a copy with a new ID and q's
// name, as it may be written to the project, and its typed auth secrets.
func requestFile(q *Request) (projectfile.File, map[string]string, error) {
	invalid := func() (projectfile.File, map[string]string, error) {
		kind := ""
		if q.File != nil {
			kind = string(q.File.FileKind())
		}
		return nil, nil, errs.New(errs.InvalidArgument, "The importer produced a request PostSonde cannot write.").
			WithCode(CodeInvalidItem).
			WithHint("This is a bug in PostSonde.").
			WithDetail("kind", kind)
	}
	if q.File == nil {
		return invalid()
	}
	id := projectfile.NewID(q.File.FileKind())
	switch v := q.File.(type) {
	case *projectfile.HTTPRequest:
		c := *v
		c.ID, c.Name = id, q.Name
		file, secrets := requestsecrets.Split(authOrInherit(c.Auth))
		c.Auth = file
		return &c, secrets, nil
	case *projectfile.GraphQLRequest:
		c := *v
		c.ID, c.Name = id, q.Name
		file, secrets := requestsecrets.Split(authOrInherit(c.Auth))
		c.Auth = file
		return &c, secrets, nil
	case *projectfile.WebSocketRequest:
		c := *v
		c.ID, c.Name = id, q.Name
		return &c, nil, nil
	case *projectfile.TCPRequest:
		c := *v
		c.ID, c.Name = id, q.Name
		return &c, nil, nil
	case *projectfile.UDPRequest:
		c := *v
		c.ID, c.Name = id, q.Name
		return &c, nil, nil
	case *projectfile.GRPCRequest:
		c := *v
		c.ID, c.Name = id, q.Name
		return &c, nil, nil
	case *projectfile.SSERequest:
		c := *v
		c.ID, c.Name = id, q.Name
		file, secrets := requestsecrets.Split(authOrInherit(c.Auth))
		c.Auth = file
		return &c, secrets, nil
	case *projectfile.SocketIORequest:
		c := *v
		c.ID, c.Name = id, q.Name
		return &c, nil, nil
	}
	return invalid()
}

func authOrInherit(a projectfile.Auth) projectfile.Auth {
	if a.Type == "" {
		a.Type = projectfile.AuthInherit
	}
	return a
}

// hasWritable reports whether f has a folder or request to write.
func hasWritable(f *Folder) bool {
	for _, it := range f.Items {
		if it.Folder != nil || it.Request != nil {
			return true
		}
	}
	return false
}

// hasSettings reports whether f has variables, headers, a description,
// an auth other than inherit, or the other members of a native folder
// file (a proto setup, a saved server, unknown members).
func hasSettings(f *Folder) bool {
	return len(f.Variables) > 0 || len(f.Headers) > 0 || strings.TrimSpace(f.Description) != "" ||
		(f.Auth.Type != "" && f.Auth.Type != projectfile.AuthInherit) ||
		!reflect.ValueOf(f.GRPC).IsZero() || f.Server != nil || len(f.Unknown) > 0
}

// freeName returns name, or "name 2", "name 3"… when taken has it,
// ignoring case.
func freeName(taken []string, name string) string {
	used := make(map[string]bool, len(taken))
	for _, t := range taken {
		used[strings.ToLower(t)] = true
	}
	if !used[strings.ToLower(name)] {
		return name
	}
	for n := 2; ; n++ {
		c := name + " " + strconv.Itoa(n)
		if !used[strings.ToLower(c)] {
			return c
		}
	}
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n]))
}

func quote(s string) string { return "“" + s + "”" }
