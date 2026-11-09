package graphqlmock

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/graphql/schema"
)

// Error codes of loading a schema.
const (
	// CodeSchemaFailed: the schema could not be loaded: the file is
	// missing or unreadable, or the SDL or introspection JSON does not
	// parse (kind invalid_argument, or the file error's). Details:
	// source ("file" or "text"), and path for a file.
	CodeSchemaFailed = "graphqlmock.schema_failed"
	// CodeRelativePath: the schema file's path is relative and nothing
	// resolves it (no project is open). Details: path.
	CodeRelativePath = "graphqlmock.relative_path"
)

// Schema sources (Loaded.Source).
const (
	SourceNone = ""
	SourceFile = "file"
	SourceText = "text"
)

// Schema formats (Loaded.Format).
const (
	FormatSDL           = "sdl"
	FormatIntrospection = "introspection"
)

// Loaded is a schema as the server loaded it.
type Loaded struct {
	// Schema is nil when the configuration names no schema.
	Schema *schema.Schema
	// Source says where it came from, Format whether it was SDL or an
	// introspection result.
	Source string
	Format string
	// Problems are the type system problems of a usable schema (broken
	// references, SDL rule violations); the server serves it anyway.
	Problems schema.Problems
}

// LoadSchema loads the schema c names: its file (read with readFile, nil
// reads absolute paths from disk) or its text. No schema at all is a
// Loaded without Schema, not an error. A schema with problems is usable
// and loads with its Problems; one that does not parse is
// CodeSchemaFailed.
func LoadSchema(c Config, readFile func(stored string) ([]byte, error)) (*Loaded, error) {
	if c.SchemaFile != "" {
		data, err := readSchemaFile(c.SchemaFile, readFile)
		if err != nil {
			return nil, err
		}
		format := FormatSDL
		if strings.EqualFold(filepath.Ext(c.SchemaFile), ".json") || looksLikeJSON(string(data)) {
			format = FormatIntrospection
		}
		l, err := parseSchema(string(data), format, SourceFile)
		if err != nil {
			e := asErr(err).WithDetail("path", c.SchemaFile)
			e.Message = "The schema file " + strconv.Quote(filepath.Base(c.SchemaFile)) + ": " + lowerFirst(e.Message)
			return nil, e
		}
		return l, nil
	}
	if strings.TrimSpace(c.SchemaText) == "" {
		return &Loaded{}, nil
	}
	format := FormatSDL
	if looksLikeJSON(c.SchemaText) {
		format = FormatIntrospection
	}
	return parseSchema(c.SchemaText, format, SourceText)
}

func readSchemaFile(stored string, readFile func(string) ([]byte, error)) ([]byte, error) {
	if readFile != nil {
		data, err := readFile(stored)
		if err != nil {
			return nil, err
		}
		if len(data) > MaxSchemaText {
			return nil, schemaFailed(SourceFile, fmt.Sprintf("The schema file %s is larger than %d MB.", strconv.Quote(filepath.Base(stored)), MaxSchemaText>>20)).
				WithDetail("path", stored)
		}
		return data, nil
	}
	if !filepath.IsAbs(stored) {
		return nil, errs.New(errs.InvalidArgument, "The schema file "+strconv.Quote(stored)+" is relative to the project, but no project is open.").
			WithCode(CodeRelativePath).WithDetail("path", stored).
			WithHint("Open the project the server belongs to, or choose the file again.")
	}
	info, err := os.Stat(stored)
	if err == nil && info.Size() > MaxSchemaText {
		return nil, schemaFailed(SourceFile, fmt.Sprintf("The schema file %s is larger than %d MB.", strconv.Quote(filepath.Base(stored)), MaxSchemaText>>20)).
			WithDetail("path", stored)
	}
	data, err := os.ReadFile(stored)
	if err != nil {
		kind := errs.IO
		if errors.Is(err, os.ErrNotExist) {
			kind = errs.NotFound
		}
		return nil, errs.Wrap(err, kind, "The schema file "+strconv.Quote(filepath.Base(stored))+" could not be read.").
			WithCode(CodeSchemaFailed).WithDetail("source", SourceFile).WithDetail("path", stored)
	}
	return data, nil
}

// parseSchema parses SDL or an introspection result.
func parseSchema(text, format, source string) (*Loaded, error) {
	var sch *schema.Schema
	var err error
	if format == FormatIntrospection {
		sch, err = schema.ParseIntrospection([]byte(text))
	} else {
		sch, err = schema.ParseSDL(text)
	}
	l := &Loaded{Schema: sch, Source: source, Format: format}
	if err != nil {
		if sch == nil {
			e := asErr(err)
			out := schemaFailed(source, e.Message)
			out.Err = err
			return nil, out
		}
		errors.As(err, &l.Problems)
	}
	if sch.Query() == nil {
		return nil, schemaFailed(source, "The schema has no query root type.")
	}
	return l, nil
}

func looksLikeJSON(s string) bool {
	s = strings.TrimLeft(s, " \t\r\n\ufeff")
	return strings.HasPrefix(s, "{") && json.Valid([]byte(s))
}

func schemaFailed(source, msg string) *errs.Error {
	return errs.New(errs.InvalidArgument, msg).WithCode(CodeSchemaFailed).WithDetail("source", source)
}

func asErr(err error) *errs.Error {
	if e, ok := errors.AsType[*errs.Error](err); ok {
		c := *e
		return &c
	}
	return errs.Wrap(err, errs.InvalidArgument, err.Error()).WithCode(CodeSchemaFailed)
}

func lowerFirst(s string) string {
	if len(s) > 1 && s[0] >= 'A' && s[0] <= 'Z' && !(s[1] >= 'A' && s[1] <= 'Z') {
		return string(s[0]+'a'-'A') + s[1:]
	}
	return s
}
