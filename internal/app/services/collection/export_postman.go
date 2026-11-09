package collection

import (
	"context"
	"strings"

	core "github.com/sorrelfox/postsonde/internal/core/collection"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/importer"
	"github.com/sorrelfox/postsonde/internal/core/importer/postman"
	"github.com/sorrelfox/postsonde/internal/core/storage/atomicfile"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// Postman export (EXP-03, docs/import.md#postman-export): a folder, or the
// whole project, is written as a Postman Collection v2.1 file the user
// picks in the native save dialog, with a report of what was left out or
// changed. The files are read as the project has them (pending edits
// written first): secrets live in the local side files, which the export
// never reads, so a secret is a {{variable}} reference or empty.

// Error codes of ExportPostman.
const (
	// CodeExportFailed: the file could not be written (kind io, not_found
	// or permission_denied). Details: path.
	CodeExportFailed = "collection.export_failed"
	// CodeExportUnavailable: there is no save dialog (tests, headless;
	// kind unsupported).
	CodeExportUnavailable = "collection.export_unavailable"
)

// SaveDialog describes the native save dialog an export shows.
type SaveDialog struct {
	Title      string
	Message    string
	ButtonText string
	// Filename is the suggested file name.
	Filename string
}

// SavePicker shows the native save dialog and returns the chosen path, or
// "" when the user cancels. The app layer supplies it; tests fake it.
type SavePicker func(ctx context.Context, d SaveDialog) (string, error)

// WithSaveDialog sets the dialog ExportPostman asks for the file with.
func WithSaveDialog(pick SavePicker) Option {
	return func(o *options) { o.pickSave = pick }
}

// ExportPostmanInputDTO is the input of ExportPostman.
type ExportPostmanInputDTO struct {
	// ID is the folder to export; "" exports the whole project.
	ID string `json:"id"`
}

// ExportNoteDTO is a line of the export's report.
type ExportNoteDTO struct {
	// Path names the item from the exported folder down, " / " between
	// the names; "" for the collection itself.
	Path string `json:"path"`
	// Kind is "collection", "folder", "request", or what a skipped item
	// was ("WebSocket request").
	Kind string `json:"kind"`
	Code string `json:"code"`
	// Severity is "warning" or "info".
	Severity string `json:"severity"`
	Message  string `json:"message"`
	// Skipped is set for an item that is not in the file.
	Skipped bool `json:"skipped"`
}

// ExportPostmanResultDTO is what ExportPostman returns.
type ExportPostmanResultDTO struct {
	// Saved is false when the user cancelled the dialog.
	Saved bool   `json:"saved"`
	Path  string `json:"path"`
	// Name is the collection's name.
	Name string `json:"name"`
	// Folders and Requests count what the file holds; Skipped the items
	// left out.
	Folders  int `json:"folders"`
	Requests int `json:"requests"`
	Skipped  int `json:"skipped"`
	// Warnings counts the warning notes.
	Warnings int             `json:"warnings"`
	Notes    []ExportNoteDTO `json:"notes"`
}

// ExportPostman writes folder in.ID (the whole project for "") as a Postman
// Collection v2.1 file the user picks in the native save dialog (EXP-03).
// Pending edits are written first. The folder's settings become the
// collection's, with what it inherits from the folders above: their
// variables and headers, and their auth when its own is inherit. HTTP and
// GraphQL requests are exported; other kinds, and files that did not
// load, are left out and listed in the notes, with what was exported with
// a change. Nothing is written when the user cancels (saved false, the
// notes still filled).
//
// Errors: workspace.none_open without a project, collection.not_found for
// an ID that is not a folder, collection.export_failed when the file
// cannot be written, collection.export_unavailable without a save dialog.
func (s *CollectionService) ExportPostman(ctx context.Context, in ExportPostmanInputDTO) (ExportPostmanResultDTO, error) {
	if s.pickSave == nil {
		return ExportPostmanResultDTO{}, errs.New(errs.Unsupported, "Exporting is not available.").WithCode(CodeExportUnavailable)
	}
	if err := interrupted(ctx, "Exporting"); err != nil {
		return ExportPostmanResultDTO{}, err
	}
	name, res, err := s.buildPostman(in.ID)
	if err != nil {
		return ExportPostmanResultDTO{}, err
	}
	out := toExportPostmanDTO(name, res)
	path, err := s.pickSave(ctx, SaveDialog{
		Title:      "Export as Postman collection",
		Message:    "Export as a Postman Collection v2.1. Secrets are not exported.",
		ButtonText: "Export",
		Filename:   postman.ExportFileName(name),
	})
	if err != nil {
		return ExportPostmanResultDTO{}, errs.Wrap(err, errs.Internal, "The save dialog could not be shown.")
	}
	if path == "" {
		return out, nil
	}
	if err := atomicfile.WriteFile(path, res.Data, 0o644); err != nil {
		kind := errs.KindOf(err)
		if kind == errs.Internal {
			kind = errs.IO
		}
		return ExportPostmanResultDTO{}, errs.Wrap(err, kind, "The collection could not be saved to "+path+".").WithCode(CodeExportFailed).
			WithDetail("path", path).WithHint("Check that the folder exists and can be written to, and that the disk is not full.")
	}
	s.logger.Info("exported as Postman collection", "folders", res.Folders, "requests", res.Requests, "skipped", res.Skipped)
	out.Saved, out.Path = true, path
	return out, nil
}

// buildPostman makes the collection of folder id ("" for the project).
func (s *CollectionService) buildPostman(id string) (string, *postman.ExportResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ws, tree, err := s.currentLocked(false)
	if err != nil {
		return "", nil, err
	}
	// Pending edits go to disk first: the export reads the files as the
	// project has them, with their secrets in the local side files.
	s.flushDirLocked(ws.Root)
	n := tree.Root()
	if id != "" {
		n = tree.Node(id)
	}
	if n == nil || !n.IsContainer() {
		return "", nil, errs.New(errs.NotFound, "The folder is no longer in the project.").
			WithCode(core.CodeNotFound).
			WithHint("It may have been moved or deleted; reload the project.").
			WithDetail("id", id)
	}
	var root importer.Folder
	exportID := n.ID
	if n.Kind == core.KindRoot {
		root = projectFolder(n, ws.Name())
		if exportID == "" {
			exportID = ws.ID()
		}
	} else {
		root = exportFolder(n)
		inheritAbove(&root, n)
	}
	res, err := postman.Export(root, postman.ExportOptions{ID: exportID})
	if err != nil {
		return "", nil, errs.Wrap(err, errs.Internal, "The collection could not be made.")
	}
	return root.Name, res, nil
}

// projectFolder is the whole project as the export's root: the project's
// name, description and variables, and its items.
func projectFolder(n *core.Node, name string) importer.Folder {
	f := importer.Folder{Name: name}
	if w, ok := n.File.(*projectfile.Workspace); ok {
		f.Description, f.Variables = w.Description, w.Variables
	}
	f.Items = exportItems(n)
	return f
}

// exportFolder is folder n with everything inside.
func exportFolder(n *core.Node) importer.Folder {
	f := importer.Folder{Name: n.DisplayName(), Auth: projectfile.Auth{Type: projectfile.AuthInherit}}
	switch file := n.File.(type) {
	case *projectfile.Folder:
		f.Description, f.Variables, f.Headers, f.Auth = file.Description, file.Variables, file.Headers, file.Auth
		if file.Server != nil {
			f.Diagnostics = append(f.Diagnostics, importer.Diagnostic{Code: postman.CodeExportChanged, Severity: importer.SeverityInfo,
				Message: "The folder's saved server is not exported: Postman collections have no servers."})
		}
	default:
		f.Diagnostics = append(f.Diagnostics, importer.Diagnostic{Code: postman.CodeExportChanged, Severity: importer.SeverityWarning,
			Message: "The folder's settings (_folder.json) could not be read: its variables, headers and auth are not exported."})
	}
	f.Items = exportItems(n)
	return f
}

// exportItems are the children of n, in tree order.
func exportItems(n *core.Node) []importer.Item {
	var items []importer.Item
	for _, c := range n.Children {
		switch c.Kind {
		case core.KindFolder:
			sub := exportFolder(c)
			items = append(items, importer.FolderItem(&sub))
		case core.KindRequest:
			if c.File != nil {
				items = append(items, importer.RequestItem(&importer.Request{Name: c.DisplayName(), File: c.File}))
			}
		case core.KindUnknown:
			items = append(items, importer.SkippedItem(&importer.Skipped{Kind: "request of a newer PostSonde", Name: c.DisplayName(),
				Reason: "The request is of a kind this version of PostSonde does not know; it is not exported."}))
		case core.KindInvalid:
			items = append(items, importer.SkippedItem(&importer.Skipped{Kind: "file", Name: c.Entry,
				Reason: "The file could not be loaded; fix it and export again."}))
		}
	}
	return items
}

// inheritAbove adds to root, the exported folder n, what its requests
// inherit from the folders above n and the project: their variables (a
// nearer one wins), their headers (a nearer one wins, by name), and the
// nearest auth when n's own is inherit. A note says so.
func inheritAbove(root *importer.Folder, n *core.Node) {
	var chain []*core.Node // the project, then the folders down to n's parent
	for p := n.Parent; p != nil; p = p.Parent {
		chain = append([]*core.Node{p}, chain...)
	}
	var vars []projectfile.Variable
	var levels [][]projectfile.KeyValue
	auth := projectfile.Auth{Type: projectfile.AuthInherit}
	for _, p := range chain {
		switch f := p.File.(type) {
		case *projectfile.Workspace:
			vars = mergeVariables(vars, f.Variables)
		case *projectfile.Folder:
			vars = mergeVariables(vars, f.Variables)
			levels = append(levels, f.Headers)
			if f.Auth.Type != "" && f.Auth.Type != projectfile.AuthInherit {
				auth = f.Auth
			}
		}
	}
	var took []string
	if len(vars) > 0 {
		root.Variables = mergeVariables(vars, root.Variables)
		took = append(took, "variables")
	}
	if above := flattenHeaders(levels); len(above) > 0 {
		root.Headers = flattenHeaders([][]projectfile.KeyValue{above, root.Headers})
		took = append(took, "headers")
	}
	if (root.Auth.Type == "" || root.Auth.Type == projectfile.AuthInherit) && auth.Type != projectfile.AuthInherit {
		root.Auth = auth
		took = append(took, "auth")
	}
	if len(took) > 0 {
		root.Diagnostics = append(root.Diagnostics, importer.Diagnostic{Code: postman.CodeExportChanged, Severity: importer.SeverityInfo,
			Message: "The " + joinAnd(took) + " the folder inherits from the folders above it and the project are the collection's."})
	}
}

// mergeVariables returns base with over's variables: one of the same name
// replaced in place, the others added.
func mergeVariables(base, over []projectfile.Variable) []projectfile.Variable {
	out := append([]projectfile.Variable(nil), base...)
	for _, v := range over {
		i := -1
		for j, b := range out {
			if b.Name == v.Name {
				i = j
				break
			}
		}
		if i >= 0 {
			out[i] = v
		} else {
			out = append(out, v)
		}
	}
	return out
}

// flattenHeaders returns the header rows sent from levels, top first: the
// enabled rows with a name, those a nearer level has by name left out.
func flattenHeaders(levels [][]projectfile.KeyValue) []projectfile.KeyValue {
	taken := map[string]bool{}
	kept := make([][]projectfile.KeyValue, len(levels))
	for i := len(levels) - 1; i >= 0; i-- {
		var level []projectfile.KeyValue
		for _, h := range levels[i] {
			key := strings.ToLower(strings.TrimSpace(h.Name))
			if !h.Disabled && key != "" && !taken[key] {
				level = append(level, h)
			}
		}
		for _, h := range level {
			taken[strings.ToLower(strings.TrimSpace(h.Name))] = true
		}
		kept[i] = level
	}
	var out []projectfile.KeyValue
	for _, level := range kept {
		out = append(out, level...)
	}
	return out
}

func joinAnd(list []string) string {
	switch len(list) {
	case 0:
		return ""
	case 1:
		return list[0]
	}
	return strings.Join(list[:len(list)-1], ", ") + " and " + list[len(list)-1]
}

func toExportPostmanDTO(name string, r *postman.ExportResult) ExportPostmanResultDTO {
	out := ExportPostmanResultDTO{Name: name, Folders: r.Folders, Requests: r.Requests, Skipped: r.Skipped, Notes: make([]ExportNoteDTO, 0, len(r.Notes))}
	for _, n := range r.Notes {
		if n.Severity == importer.SeverityWarning {
			out.Warnings++
		}
		out.Notes = append(out.Notes, ExportNoteDTO{
			Path: strings.Join(n.Path, " / "), Kind: n.Kind, Code: n.Code, Severity: string(n.Severity), Message: n.Message, Skipped: n.Skipped,
		})
	}
	return out
}
