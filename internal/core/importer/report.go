package importer

import (
	"errors"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// ItemKind says what a report item is.
type ItemKind string

// Item kinds.
const (
	KindFolder      ItemKind = "folder"
	KindRequest     ItemKind = "request"
	KindEnvironment ItemKind = "environment"
	// KindSkipped is an item of the source that is not imported.
	KindSkipped ItemKind = "skipped"
)

// Status is what happened, or will happen, to an item.
type Status string

// Statuses.
const (
	// StatusNew: Preview's status of an item that would be created.
	StatusNew Status = "new"
	// StatusImported: the item was created.
	StatusImported Status = "imported"
	// StatusSkipped: the item is not imported (see its diagnostics).
	StatusSkipped Status = "skipped"
	// StatusFailed: writing the item failed (see its diagnostics).
	StatusFailed Status = "failed"
)

// ReportItem is one line of a preview or report.
type ReportItem struct {
	Kind ItemKind
	// Name is the item's name; after Write, the name it was created with.
	Name string
	// Parent is the index in Report.Items of the folder the item is in;
	// -1 for the collection's own folder (always Items[0]) and the
	// environments.
	Parent int
	// FileKind is a request's kind ("http", "graphql"…); SkippedKind the
	// source's kind of a skipped item ("script", "request"…).
	FileKind    projectfile.Kind
	SkippedKind string
	// Method is an http request's method; Detail its URL (a GraphQL or
	// WebSocket request's URL, a TCP or gRPC request's address).
	Method string
	Detail string
	Status Status
	// NodeID is the created folder's or request's ID in the tree, or the
	// created environment's ID; for Items[0] with Options.IntoParent, the
	// folder the items went into ("" for the top level).
	NodeID string
	Source Location
	// Diagnostics are the importer's and the writer's, in that order.
	Diagnostics []Diagnostic
	// Err is why the item failed, for callers that report it as an error.
	Err error
}

// Counts sum up a report.
type Counts struct {
	// Folders, Requests and Environments count the items created (after
	// Write) or to be created (Preview); Folders leaves out Items[0] when
	// the items went into an existing folder.
	Folders      int
	Requests     int
	Environments int
	// Skipped counts the items not imported, Failed those whose write
	// failed.
	Skipped int
	Failed  int
	// Lossy counts the imported items with a warning.
	Lossy int
	// Errors, Warnings and Infos count every diagnostic, the file's
	// included.
	Errors   int
	Warnings int
	Infos    int
}

// Report is what Preview and Write return.
type Report struct {
	// Items are the collection's folder (Items[0]), then its content in
	// order, depth first, then the environments.
	Items []ReportItem
	// Diagnostics concern the whole file (and, after Write, the options).
	Diagnostics []Diagnostic
	Counts      Counts
	// IntoParent is set when the collection's items went straight into an
	// existing folder (Options.IntoParent): Items[0] is that folder, not a
	// new one.
	IntoParent bool

	// index maps the model's folders, requests and environments to their
	// item.
	index map[any]int
}

// Preview describes what Write would create from m, without writing
// anything: every folder, request and environment has StatusNew, every
// skipped item StatusSkipped with its reason. Items[0] is the collection's
// folder, named m.Name.
func Preview(m *Model) *Report {
	r := &Report{index: map[any]int{}}
	r.Diagnostics = append(r.Diagnostics, m.Diagnostics...)
	r.addFolder(&m.Root, m.Name, -1)
	for i := range m.Environments {
		e := &m.Environments[i]
		r.index[e] = len(r.Items)
		r.Items = append(r.Items, ReportItem{
			Kind: KindEnvironment, Name: e.Name, Parent: -1, Status: StatusNew,
			Source: e.Source, Diagnostics: append([]Diagnostic(nil), e.Diagnostics...),
		})
	}
	r.recount()
	return r
}

func (r *Report) addFolder(f *Folder, name string, parent int) {
	at := len(r.Items)
	r.index[f] = at
	r.Items = append(r.Items, ReportItem{
		Kind: KindFolder, Name: name, Parent: parent, Status: StatusNew,
		Source: f.Source, Diagnostics: append([]Diagnostic(nil), f.Diagnostics...),
	})
	for _, it := range f.Items {
		switch {
		case it.Folder != nil:
			r.addFolder(it.Folder, it.Folder.Name, at)
		case it.Request != nil:
			q := it.Request
			r.index[q] = len(r.Items)
			item := ReportItem{
				Kind: KindRequest, Name: q.Name, Parent: at, Status: StatusNew,
				Source: q.Source, Diagnostics: append([]Diagnostic(nil), q.Diagnostics...),
			}
			describeRequest(&item, q.File)
			r.Items = append(r.Items, item)
		case it.Skipped != nil:
			s := it.Skipped
			r.Items = append(r.Items, ReportItem{
				Kind: KindSkipped, Name: s.Name, Parent: at, SkippedKind: s.Kind, Status: StatusSkipped,
				Source: s.Source,
				Diagnostics: []Diagnostic{{
					Code: s.Code, Severity: SeverityWarning, Message: s.Reason, Source: s.Source,
				}},
			})
		}
	}
}

// describeRequest fills the kind, method and detail of a request item.
func describeRequest(item *ReportItem, f projectfile.File) {
	if f == nil {
		return
	}
	item.FileKind = f.FileKind()
	switch v := f.(type) {
	case *projectfile.HTTPRequest:
		item.Method, item.Detail = v.Method, v.URL
		if item.Method == "" {
			item.Method = "GET"
		}
	case *projectfile.GraphQLRequest:
		item.Detail = v.URL
	case *projectfile.WebSocketRequest:
		item.Detail = v.URL
	case *projectfile.TCPRequest:
		item.Detail = v.Address
	case *projectfile.UDPRequest:
		item.Detail = v.Address
	case *projectfile.SSERequest:
		item.Method, item.Detail = v.Method, v.URL
		if item.Method == "" {
			item.Method = "GET"
		}
	case *projectfile.SocketIORequest:
		item.Detail = v.URL
	case *projectfile.GRPCRequest:
		item.Detail = v.Address
		if v.Service != "" || v.Method != "" {
			item.Method = v.Service + "/" + v.Method
		}
	}
}

// subtree returns the indices of the items below item at (not at itself).
func (r *Report) subtree(at int) []int {
	var out []int
	below := map[int]bool{at: true}
	for i := at + 1; i < len(r.Items); i++ {
		if p := r.Items[i].Parent; p >= 0 && below[p] {
			below[i] = true
			out = append(out, i)
		}
	}
	return out
}

func (r *Report) note(at int, d Diagnostic) {
	r.Items[at].Diagnostics = append(r.Items[at].Diagnostics, d)
}

func (r *Report) fail(at int, err error, code string) {
	it := &r.Items[at]
	it.Status, it.Err, it.NodeID = StatusFailed, err, ""
	it.Diagnostics = append(it.Diagnostics, Diagnostic{
		Code: code, Severity: SeverityError, Message: errorMessage(err), Source: it.Source,
	})
}

// recount computes Counts from the items.
func (r *Report) recount() {
	c := Counts{}
	count := func(ds []Diagnostic) {
		for _, d := range ds {
			switch d.Severity {
			case SeverityError:
				c.Errors++
			case SeverityWarning:
				c.Warnings++
			default:
				c.Infos++
			}
		}
	}
	count(r.Diagnostics)
	for i, it := range r.Items {
		count(it.Diagnostics)
		switch it.Status {
		case StatusSkipped:
			c.Skipped++
			continue
		case StatusFailed:
			c.Failed++
			continue
		}
		if i == 0 && r.IntoParent {
			continue
		}
		switch it.Kind {
		case KindFolder:
			c.Folders++
		case KindRequest:
			c.Requests++
		case KindEnvironment:
			c.Environments++
		}
		for _, d := range it.Diagnostics {
			if d.Severity == SeverityWarning || d.Severity == SeverityError {
				c.Lossy++
				break
			}
		}
	}
	r.Counts = c
}

// errorMessage is the user-facing message of err: an errs.Error's message
// and hint, without the causes (which may name paths the user did not
// choose); another error's text.
func errorMessage(err error) string {
	var e *errs.Error
	if errors.As(err, &e) {
		if e.Hint != "" {
			return e.Message + " " + e.Hint
		}
		return e.Message
	}
	if err == nil {
		return "The item could not be written."
	}
	return err.Error()
}
