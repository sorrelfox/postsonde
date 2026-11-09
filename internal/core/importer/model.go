package importer

import (
	"github.com/sorrelfox/postsonde/internal/core/environments"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// Severity is how bad a diagnostic is.
type Severity string

// Severities.
const (
	// SeverityError: the item (or the file) could not be imported.
	SeverityError Severity = "error"
	// SeverityWarning: something was lost or changed on the way.
	SeverityWarning Severity = "warning"
	// SeverityInfo: imported as it was, with a note.
	SeverityInfo Severity = "info"
)

// Location says where something is in the source file. Every member is
// optional.
type Location struct {
	// Line and Column are 1-based, Column in characters; 0 when unknown.
	Line   int
	Column int
	// Path locates the item in the source's structure, in the format's
	// own terms: `item[2].item[0]` for Postman, `paths./users.get` for
	// OpenAPI, "command 3" for a cURL file.
	Path string
}

// IsZero reports whether l says nothing.
func (l Location) IsZero() bool { return l.Line == 0 && l.Column == 0 && l.Path == "" }

// Diagnostic is a problem or a note about one item or the whole file.
// Messages never contain header, body or credential values.
type Diagnostic struct {
	// Code is stable: "import.…" for the pipeline's own, "<format>.…" for
	// a format's ("curl.unsupported_option", "postman.script_skipped").
	Code     string
	Severity Severity
	Message  string
	Source   Location
}

// Model is what an Importer reads from a file: the collection as a folder
// tree, the environments, and diagnostics about the file as a whole.
type Model struct {
	// Name is the suggested name of the folder the import creates: the
	// collection's name, else the file's name without its extension.
	Name string
	// Root is the collection. Its settings (description, variables,
	// headers, auth) become those of the folder the import creates; its
	// items that folder's content.
	Root Folder
	// Environments are created next to the project's own.
	Environments []Environment
	// Diagnostics concern the whole file.
	Diagnostics []Diagnostic
}

// Folder is a folder with its settings and items.
type Folder struct {
	Name        string
	Description string
	// Variables, Headers and Auth are inherited by everything inside, as
	// in a _folder.json. A secret variable's value and a typed auth secret
	// go to the folder's local side file, never to the project.
	Variables []projectfile.Variable
	Headers   []projectfile.KeyValue
	Auth      projectfile.Auth
	// GRPC, Server and Unknown are the other members of a native
	// _folder.json (the PostSonde folder zip, docs/import.md#folder-zips):
	// the proto setup gRPC requests inherit, the folder's saved server,
	// and the members this version does not know. Other formats leave
	// them empty.
	GRPC    projectfile.FolderGRPC
	Server  *projectfile.FolderServer
	Unknown projectfile.Unknown
	Items   []Item
	Source  Location
	// Diagnostics concern the folder itself.
	Diagnostics []Diagnostic
}

// Item is one entry of a folder: exactly one of Folder, Request and
// Skipped is set.
type Item struct {
	Folder  *Folder
	Request *Request
	Skipped *Skipped
}

// Request is a request to create.
type Request struct {
	Name string
	// File is a request file of a kind this version writes
	// (*projectfile.HTTPRequest, GraphQLRequest, WebSocketRequest,
	// GRPCRequest, TCPRequest, SSERequest or SocketIORequest). Its ID and
	// name are ignored: Write gives it a new ID and Name. Typed auth
	// secrets in it go to the local side files.
	File        projectfile.File
	Source      Location
	Diagnostics []Diagnostic
}

// Skipped is an item of the source that is not imported, and why.
type Skipped struct {
	// Kind says what it was in the source's terms: "request", "folder",
	// "websocket request", "script"…
	Kind string
	Name string
	// Code and Reason are the report's diagnostic for it (a warning).
	Code   string
	Reason string
	Source Location
}

// Environment is an environment to create. Secret variables' values go to
// the local side files, as the environments editor keeps them.
type Environment struct {
	Name        string
	Variables   []environments.Variable
	Source      Location
	Diagnostics []Diagnostic
}

// FolderItem, RequestItem and SkippedItem wrap a value as an Item.
func FolderItem(f *Folder) Item   { return Item{Folder: f} }
func RequestItem(r *Request) Item { return Item{Request: r} }
func SkippedItem(s *Skipped) Item { return Item{Skipped: s} }
