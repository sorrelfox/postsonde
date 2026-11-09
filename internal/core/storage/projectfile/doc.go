// Package projectfile defines the on-disk format of a PostSonde project
// (STO-07): the workspace marker, folder files, request files and
// environment files, with their schema version and migrations.
// docs/file-format.md describes the format for users; this package is its
// reference implementation.
//
// # Files
//
// A project is a directory with a [WorkspaceFileName] (postsonde.json) at
// its root. Every file is a JSON object that starts with "schemaVersion"
// and "kind" ([Head]):
//
//   - [Workspace], kind "workspace": postsonde.json.
//   - [Folder], kind "folder": _folder.json in each folder's directory.
//   - [HTTPRequest], [GraphQLRequest], [TCPRequest], [UDPRequest],
//     [WebSocketRequest], [GRPCRequest], [SSERequest] and
//     [SocketIORequest], kinds "http", "graphql", "tcp", "udp",
//     "websocket", "grpc", "sse" and "socketio": one *.json file per
//     request.
//   - [Environment], kind "environment": environments/*.json.
//
// Project files are meant to be shared. They never hold secrets or
// local-only state (active environment, open tabs, history, tokens); those
// live in local side files keyed by the IDs in the project files ([NewID]).
//
// # Reading
//
// [Decode] (and [ReadFile]) parse any kind; [DecodeWorkspace],
// [DecodeFolder] and [DecodeEnvironment] also check the kind. A missing
// schemaVersion counts as 1. An older version is upgraded in memory by the
// steps in migrations, one version at a time. A newer version decodes the
// members this version knows and [Head.Newer] reports it; such a value is
// never written. A kind this version does not know is a [CodeUnknownKind]
// error: callers leave that file untouched.
//
// Members this version does not know are kept at every level, in the
// Unknown field of each object type, and written back after the known
// members, sorted by key.
//
// # Writing
//
// [Marshal] (and [WriteFile]) encode with stablejson, so a file that is
// read and written again without changes is byte-identical. Known members
// are written in the order of the Go types; optional members that are
// empty are left out. Marshal refuses a value without an ID, an HTTP
// request without a method, a secret variable with a value, and a value
// from a newer schema version. WriteFile writes through atomicfile.
//
// All errors are *errs.Error values with a code from this package.
package projectfile
