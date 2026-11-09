package projectfile

import (
	"crypto/rand"
	"encoding/hex"
	"io/fs"
)

// CurrentVersion is the schema version this build reads and writes, the
// same for every kind of file. Bump it only for a change old files must be
// migrated for (a renamed, moved or re-interpreted member), and add the
// step to migrations. Adding a member with a default needs no bump.
const CurrentVersion = 1

// File and directory names with a fixed meaning in a project.
const (
	// WorkspaceFileName is the workspace marker at the project root. Its
	// presence makes a directory a PostSonde project.
	WorkspaceFileName = "postsonde.json"
	// FolderFileName holds a folder's own settings, in the folder's
	// directory. The project root has none; its settings are in the
	// workspace file.
	FolderFileName = "_folder.json"
	// EnvironmentsDir is the directory at the project root that holds one
	// file per environment.
	EnvironmentsDir = "environments"
	// Ext is the extension of every project file.
	Ext = ".json"
)

// FilePerm is the permission of a new project file. Project files hold no
// secrets and are meant to be shared, so they are readable by everyone
// like any other source file. An existing file keeps its permission.
const FilePerm fs.FileMode = 0o644

// Kind says what a file holds. It is the "kind" member of every file.
type Kind string

// The kinds this version knows.
const (
	KindWorkspace   Kind = "workspace"
	KindFolder      Kind = "folder"
	KindHTTP        Kind = "http"
	KindGraphQL     Kind = "graphql"
	KindTCP         Kind = "tcp"
	KindWebSocket   Kind = "websocket"
	KindGRPC        Kind = "grpc"
	KindSSE         Kind = "sse"
	KindSocketIO    Kind = "socketio"
	KindEnvironment Kind = "environment"
)

// Kinds returns every kind this version knows, in the order
// docs/file-format.md lists them.
func Kinds() []Kind {
	return []Kind{KindWorkspace, KindFolder, KindHTTP, KindGraphQL, KindTCP, KindUDP, KindWebSocket, KindGRPC, KindSSE, KindSocketIO, KindEnvironment}
}

// IsRequest reports whether k is a kind of request file (http, graphql,
// tcp, udp, websocket, grpc, sse, socketio). Later protocols add request
// kinds.
func (k Kind) IsRequest() bool {
	return k == KindHTTP || k == KindGraphQL || k == KindTCP || k == KindUDP || k == KindWebSocket || k == KindGRPC || k == KindSSE ||
		k == KindSocketIO
}

// Error codes, in addition to the errs kinds.
const (
	// CodeParseFailed: the data is not a JSON object, or a member does not
	// have the type the schema gives it (kind parse). Details: line and
	// column for a syntax error, field for a bad schemaVersion or kind.
	CodeParseFailed = "projectfile.parse_failed"
	// CodeWrongKind: the file is valid but holds another kind than the one
	// asked for (kind parse). Details: kind, expected.
	CodeWrongKind = "projectfile.wrong_kind"
	// CodeUnknownKind: the kind is not one this version knows, typically a
	// file from a newer PostSonde (kind unsupported). Details: kind. Leave
	// such a file as it is.
	CodeUnknownKind = "projectfile.unknown_kind"
	// CodeNewerVersion: writing a file whose schema version is newer than
	// CurrentVersion (kind unsupported). Details: version, supported.
	CodeNewerVersion = "projectfile.newer_version"
	// CodeInvalidValue: a value cannot be written (kind invalid_argument).
	// Details: field.
	CodeInvalidValue = "projectfile.invalid_value"
	// CodeMigrationFailed: an older file could not be upgraded (kind
	// parse). Details: version.
	CodeMigrationFailed = "projectfile.migration_failed"
	// CodeReadFailed: the file could not be read. The kind comes from the
	// cause. Details: path.
	CodeReadFailed = "projectfile.read_failed"
	// CodeSaveFailed: the file could not be written. The kind comes from
	// the cause. Details: path.
	CodeSaveFailed = "projectfile.save_failed"
)

// Head holds the two members every file starts with. It is embedded first
// in each file type, so they are written first.
type Head struct {
	// SchemaVersion is the format version. After decoding it is
	// CurrentVersion, or the file's own version when that is newer (see
	// Newer). Marshal writes CurrentVersion.
	SchemaVersion int `json:"schemaVersion"`
	// Kind is what the file holds. Marshal sets it from the Go type.
	Kind Kind `json:"kind"`
}

// Newer reports whether the file came from a newer PostSonde. Such a value
// can be shown but not written back: Marshal refuses it, so members whose
// meaning changed in the newer version are not lost or corrupted.
func (h Head) Newer() bool { return h.SchemaVersion > CurrentVersion }

// File is one of *Workspace, *Folder, *HTTPRequest, *GraphQLRequest,
// *TCPRequest, *UDPRequest, *WebSocketRequest, *GRPCRequest, *SSERequest,
// *SocketIORequest and *Environment.
type File interface {
	// FileKind returns the kind of file the type is written as.
	FileKind() Kind
	head() Head
}

func (*Workspace) FileKind() Kind        { return KindWorkspace }
func (*Folder) FileKind() Kind           { return KindFolder }
func (*HTTPRequest) FileKind() Kind      { return KindHTTP }
func (*GraphQLRequest) FileKind() Kind   { return KindGraphQL }
func (*TCPRequest) FileKind() Kind       { return KindTCP }
func (*WebSocketRequest) FileKind() Kind { return KindWebSocket }
func (*GRPCRequest) FileKind() Kind      { return KindGRPC }
func (*SSERequest) FileKind() Kind       { return KindSSE }
func (*SocketIORequest) FileKind() Kind  { return KindSocketIO }
func (*Environment) FileKind() Kind      { return KindEnvironment }

func (w *Workspace) head() Head        { return w.Head }
func (f *Folder) head() Head           { return f.Head }
func (r *HTTPRequest) head() Head      { return r.Head }
func (r *GraphQLRequest) head() Head   { return r.Head }
func (r *TCPRequest) head() Head       { return r.Head }
func (r *WebSocketRequest) head() Head { return r.Head }
func (r *GRPCRequest) head() Head      { return r.Head }
func (r *SSERequest) head() Head       { return r.Head }
func (r *SocketIORequest) head() Head  { return r.Head }
func (e *Environment) head() Head      { return e.Head }

// ID prefixes. They only make IDs easier to tell apart when reading files;
// code must treat IDs as opaque strings.
const (
	WorkspaceIDPrefix   = "ws_"
	FolderIDPrefix      = "fld_"
	RequestIDPrefix     = "req_"
	EnvironmentIDPrefix = "env_"
)

// idRandomBytes is the number of random bytes in an ID: 16 hex digits.
const idRandomBytes = 8

// NewID returns a new random ID for a file of kind k: the kind's prefix
// followed by 16 lowercase hex digits from crypto/rand, e.g.
// "req_3f9a0c12d4e5b678". Request kinds share RequestIDPrefix.
func NewID(k Kind) string {
	prefix := RequestIDPrefix
	switch k {
	case KindWorkspace:
		prefix = WorkspaceIDPrefix
	case KindFolder:
		prefix = FolderIDPrefix
	case KindEnvironment:
		prefix = EnvironmentIDPrefix
	}
	var b [idRandomBytes]byte
	// crypto/rand.Read never returns an error; it crashes the program
	// irrecoverably if the OS random source fails.
	_, _ = rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}
