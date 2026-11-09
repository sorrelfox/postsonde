package projectfile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"slices"
	"strconv"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// ReadFile reads and decodes the project file at path. Errors carry the
// path in their details: CodeReadFailed when it cannot be read, and the
// errors of Decode otherwise.
func ReadFile(path string) (File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errs.Wrap(err, errs.KindOf(err), "A project file could not be read.").
			WithCode(CodeReadFailed).
			WithHint("Check that the file exists and is readable.").
			WithDetail("path", path)
	}
	f, err := Decode(data)
	if err != nil {
		return nil, withPath(err, path)
	}
	return f, nil
}

// Decode decodes a project file of any kind this version knows, upgrading
// an older schema version in memory. The result is a *Workspace, *Folder,
// *HTTPRequest, *GraphQLRequest, *TCPRequest, *UDPRequest, *WebSocketRequest, *SSERequest,
// *SocketIORequest, *GRPCRequest or *Environment.
//
// Errors are *errs.Error values: CodeParseFailed for data that is not a
// JSON object or does not match the schema, CodeUnknownKind for a kind
// this version does not know (leave that file untouched), and
// CodeMigrationFailed when an upgrade step fails. A file from a newer
// version decodes the members this version knows; see Head.Newer.
func Decode(data []byte) (File, error) {
	return decode(data, nil, migrations, CurrentVersion)
}

// DecodeWorkspace is Decode for a workspace file; another kind is a
// CodeWrongKind error.
func DecodeWorkspace(data []byte) (*Workspace, error) {
	f, err := decode(data, []Kind{KindWorkspace}, migrations, CurrentVersion)
	if err != nil {
		return nil, err
	}
	return f.(*Workspace), nil
}

// DecodeFolder is Decode for a _folder.json file; another kind is a
// CodeWrongKind error.
func DecodeFolder(data []byte) (*Folder, error) {
	f, err := decode(data, []Kind{KindFolder}, migrations, CurrentVersion)
	if err != nil {
		return nil, err
	}
	return f.(*Folder), nil
}

// DecodeEnvironment is Decode for an environment file; another kind is a
// CodeWrongKind error.
func DecodeEnvironment(data []byte) (*Environment, error) {
	f, err := decode(data, []Kind{KindEnvironment}, migrations, CurrentVersion)
	if err != nil {
		return nil, err
	}
	return f.(*Environment), nil
}

// A migration upgrades the top-level members of a file of the given kind
// by one version. It receives the kind as the file declares it before the
// step and may change any member, "kind" included.
type migration func(kind Kind, members map[string]json.RawMessage) (map[string]json.RawMessage, error)

// migrations[v] upgrades a file from version v to v+1. There are none yet:
// version 1 is the first. When CurrentVersion goes to 2, add migrations[1],
// which receives the version-1 members of any kind of file and returns
// them in the version-2 layout (the "schemaVersion" member need not
// change). Keep every step forever: a file may skip many versions.
var migrations = map[int]migration{}

// maxVersion bounds the version so a huge number is reported instead of
// overflowing.
const maxVersion = 1 << 20

// decode is Decode with the accepted kinds (nil for any known kind), the
// migration steps and the current version as parameters, for tests.
func decode(data []byte, want []Kind, steps map[int]migration, current int) (File, error) {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &members); err != nil || members == nil {
		return nil, parseFailed(data, err)
	}

	version, err := parseVersion(members["schemaVersion"])
	if err != nil {
		return nil, err
	}
	if version < current {
		kind, _ := parseKind(members["kind"]) // checked after the upgrade
		for v := version; v < current; v++ {
			step, ok := steps[v]
			if !ok {
				err = fmt.Errorf("projectfile: no migration from version %d to %d", v, v+1)
			} else {
				members, err = step(kind, members)
			}
			if err != nil {
				return nil, errs.Wrap(err, errs.Parse,
					fmt.Sprintf("A project file from schema version %d could not be upgraded.", version)).
					WithCode(CodeMigrationFailed).
					WithHint("The file is left as it is. Open it with the PostSonde version that wrote it, or fix it by hand.").
					WithDetail("version", strconv.Itoa(version))
			}
			if members == nil {
				members = map[string]json.RawMessage{}
			}
			if k, err := parseKind(members["kind"]); err == nil {
				kind = k
			}
		}
		version = current
	}

	kind, err := parseKind(members["kind"])
	if err != nil {
		return nil, err
	}
	if want != nil && !slices.Contains(want, kind) {
		return nil, errs.New(errs.Parse, fmt.Sprintf("The file is of kind %q, not %q.", kind, want[0])).
			WithCode(CodeWrongKind).
			WithHint("Move or rename the file so it is where a file of its kind belongs.").
			WithDetail("kind", string(kind)).
			WithDetail("expected", string(want[0]))
	}

	var f File
	switch kind {
	case KindWorkspace:
		f = &Workspace{}
	case KindFolder:
		f = &Folder{}
	case KindHTTP:
		f = &HTTPRequest{}
	case KindGraphQL:
		f = &GraphQLRequest{}
	case KindTCP:
		f = &TCPRequest{}
	case KindUDP:
		f = &UDPRequest{}
	case KindWebSocket:
		f = &WebSocketRequest{}
	case KindGRPC:
		f = &GRPCRequest{}
	case KindSSE:
		f = &SSERequest{}
	case KindSocketIO:
		f = &SocketIORequest{}
	case KindEnvironment:
		f = &Environment{}
	default:
		return nil, errs.New(errs.Unsupported, fmt.Sprintf("PostSonde does not know files of kind %q.", kind)).
			WithCode(CodeUnknownKind).
			WithHint("The file may come from a newer PostSonde. It is left as it is.").
			WithDetail("kind", string(kind))
	}

	// The members are decoded again as a whole, with the version written
	// as a plain integer (a file may say 1.0) and after any upgrade.
	members["schemaVersion"] = json.RawMessage(strconv.Itoa(version))
	normalized, err := json.Marshal(members)
	if err != nil {
		return nil, parseFailed(nil, err)
	}
	if err := json.Unmarshal(normalized, f); err != nil {
		return nil, parseFailed(nil, err)
	}
	setHead(f, Head{SchemaVersion: version, Kind: kind})
	return f, nil
}

func setHead(f File, h Head) {
	switch v := f.(type) {
	case *Workspace:
		v.Head = h
	case *Folder:
		v.Head = h
	case *HTTPRequest:
		v.Head = h
	case *GraphQLRequest:
		v.Head = h
	case *TCPRequest:
		v.Head = h
	case *UDPRequest:
		v.Head = h
	case *WebSocketRequest:
		v.Head = h
	case *GRPCRequest:
		v.Head = h
	case *SSERequest:
		v.Head = h
	case *SocketIORequest:
		v.Head = h
	case *Environment:
		v.Head = h
	}
}

// parseVersion returns the file's schema version: 1 when the member is
// missing or null, which only a hand-written file can lack.
func parseVersion(raw json.RawMessage) (int, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return 1, nil
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err != nil || f != math.Trunc(f) || f < 1 || f > maxVersion {
		return 0, errs.New(errs.Parse, fmt.Sprintf("The file's schemaVersion, %s, is not a whole number from 1.", bytes.TrimSpace(raw))).
			WithCode(CodeParseFailed).
			WithHint(fmt.Sprintf(`Set "schemaVersion" to the version the file was written in (this PostSonde writes %d).`, CurrentVersion)).
			WithDetail("field", "schemaVersion")
	}
	return int(f), nil
}

// parseKind returns the file's kind, which must be a non-empty string.
func parseKind(raw json.RawMessage) (Kind, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil || s == "" {
		return "", errs.New(errs.Parse, `The file has no "kind", so it is not a PostSonde project file.`).
			WithCode(CodeParseFailed).
			WithHint(`Every project file starts with "schemaVersion" and "kind", e.g. "kind": "http".`).
			WithDetail("field", "kind")
	}
	return Kind(s), nil
}

func parseFailed(data []byte, cause error) *errs.Error {
	if cause == nil {
		cause = errors.New("projectfile: not a JSON object")
	}
	e := errs.Wrap(cause, errs.Parse, "A project file does not match the PostSonde file format.").
		WithCode(CodeParseFailed).
		WithHint("Fix the file by hand; docs/file-format.md describes the format. PostSonde does not overwrite it until then.")
	var syntax *json.SyntaxError
	if errors.As(cause, &syntax) && data != nil {
		line, col := position(data, syntax.Offset)
		e = e.WithDetail("line", strconv.Itoa(line)).WithDetail("column", strconv.Itoa(col))
	}
	return e
}

// position converts a byte offset into a 1-based line and column.
func position(data []byte, offset int64) (line, col int) {
	if offset > int64(len(data)) {
		offset = int64(len(data))
	}
	before := data[:max(offset-1, 0)]
	line = bytes.Count(before, []byte("\n")) + 1
	col = len(before) - bytes.LastIndexByte(before, '\n')
	return line, col
}

// withPath adds the path to an *errs.Error's details.
func withPath(err error, path string) error {
	var e *errs.Error
	if errors.As(err, &e) {
		return e.WithDetail("path", path)
	}
	return err
}
