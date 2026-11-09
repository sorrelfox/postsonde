package request

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// GraphQL file uploads (GQL-07): a GraphQL request with files is sent as
// multipart/form-data following the GraphQL multipart request spec
// (github.com/jaydenseric/graphql-multipart-request-spec): an
// "operations" field with the usual JSON body, the files' positions set
// to null; a "map" field naming, for each file part, the object paths it
// fills; then one part per file, named "0", "1", …. See docs/graphql.md.

// Error codes of the file uploads, besides those of the body files
// (CodeBodyFileUnset, CodeBodyFileMissing, CodeBodyFileUnreadable).
const (
	// CodeGraphQLInvalidUpload: a file row's path is blank or malformed,
	// runs into a value that is not an object or list, or clashes with
	// another row's; or its Content-Type contains a line break. Details:
	// field ("files"), row, part (the path as typed).
	CodeGraphQLInvalidUpload = "request.graphql_invalid_upload"
	// CodeGraphQLUploadOverGET: the request has files but is sent with
	// GET, which has no body.
	CodeGraphQLUploadOverGET = "request.graphql_upload_get"
)

// GraphQLPreflightHeader is the header a request with files sends, with
// the value "true", unless a header row sets it or settings.omitHeaders
// names it: Apollo Server's CSRF prevention refuses multipart requests
// without it (or another non-simple header), and other servers ignore it.
const GraphQLPreflightHeader = "Apollo-Require-Preflight"

// graphQLUpload is an enabled file row of a GraphQL request.
type graphQLUpload struct {
	// row is the row's index in the file's Files.
	row  int
	part projectfile.Part
	// path is the object path in the operations, "variables" first.
	path []string
}

// joined is the path as the map writes it: "variables.input.file".
func (u graphQLUpload) joined() string { return strings.Join(u.path, ".") }

// graphQLUploads returns the file rows that are sent: disabled rows and
// rows with neither a path nor a file (a row being filled in) are
// skipped. Each path is parsed (GraphQLUploadPath), each row must have a
// file, and no path may repeat or lie inside another.
func graphQLUploads(files []projectfile.Part) ([]graphQLUpload, error) {
	var out []graphQLUpload
	for i, f := range files {
		if f.Disabled || (strings.TrimSpace(f.Name) == "" && f.File == "") {
			continue
		}
		path, err := GraphQLUploadPath(f.Name)
		if err != nil {
			return nil, uploadError(err.Error(), "Write where the file goes in the variables, e.g. variables.file or variables.input.files.0.", i, f.Name)
		}
		if f.File == "" {
			return nil, withPart(errs.New(errs.InvalidArgument, "No file is chosen for "+strings.Join(path, ".")+".").
				WithCode(CodeBodyFileUnset).
				WithHint("Choose the file in the Files tab, or disable the row."), i, f.Name).
				WithDetail(DetailField, "files")
		}
		if hasHeaderBreak(f.ContentType) {
			return nil, uploadError("The Content-Type of the file for "+strings.Join(path, ".")+" contains a line break or control character.",
				"Remove line breaks and control characters from the Content-Type.", i, f.Name)
		}
		u := graphQLUpload{row: i, part: f, path: path}
		for _, prev := range out {
			if isPathPrefix(prev.path, path) || isPathPrefix(path, prev.path) {
				msg := "Two files are bound to " + u.joined() + "."
				if len(prev.path) != len(path) {
					msg = "The files for " + prev.joined() + " and " + u.joined() + " overlap: one lies inside the other."
				}
				return nil, uploadError(msg, "Bind each file to its own variable or list item.", i, f.Name)
			}
		}
		out = append(out, u)
	}
	return out, nil
}

// GraphQLUploadPath parses the object path a file row binds its file to:
// dot-separated names and list indexes, "variables" first as in the spec's
// map ("variables.input.files.0"). A path that does not start with
// "variables." is taken as inside the variables ("input.files.0"). The
// result starts with "variables" and names at least one variable.
func GraphQLUploadPath(text string) ([]string, error) {
	t := strings.TrimSpace(text)
	if t == "" {
		return nil, errs.New(errs.InvalidArgument, "A file has no variable path.")
	}
	segs := strings.Split(t, ".")
	for _, s := range segs {
		if s == "" {
			return nil, errs.New(errs.InvalidArgument, "The variable path "+strconv.Quote(t)+" has an empty part.")
		}
	}
	if segs[0] != "variables" || len(segs) == 1 {
		segs = append([]string{"variables"}, segs...)
	}
	return segs, nil
}

func isPathPrefix(prefix, path []string) bool {
	if len(prefix) > len(path) {
		return false
	}
	for i := range prefix {
		if prefix[i] != path[i] {
			return false
		}
	}
	return true
}

func uploadError(msg, hint string, row int, name string) *errs.Error {
	return withPart(errs.New(errs.InvalidArgument, msg).WithCode(CodeGraphQLInvalidUpload).WithHint(hint), row, name).
		WithDetail(DetailField, "files")
}

// graphQLMultipart builds the multipart body of a request with uploads:
// the operations (query, variables with every file's position set to
// null, operationName, extensions), the map and the files. Rows with the
// same file and Content-Type share one file part, which the map lists
// under each of their paths. root resolves relative file paths.
func graphQLMultipart(body graphQLBody, uploads []graphQLUpload, root string) (*MultipartBody, error) {
	var vars any = map[string]any{}
	if len(body.Variables) > 0 {
		dec := json.NewDecoder(bytes.NewReader(body.Variables))
		dec.UseNumber()
		if err := dec.Decode(&vars); err != nil {
			return nil, errs.Wrap(err, errs.Internal, "The GraphQL variables could not be read.")
		}
	}
	for _, u := range uploads {
		next, ok := setNull(vars, u.path[1:])
		if !ok {
			return nil, uploadError("The variables have a value on the way to "+u.joined()+" that is neither an object nor a list.",
				"Fix the path, or the variables so that the path leads to where the file goes.", u.row, u.part.Name)
		}
		vars = next
	}
	v, err := marshalJSON(vars)
	if err != nil {
		return nil, errs.Wrap(err, errs.Internal, "The GraphQL variables could not be written.")
	}
	body.Variables = v
	operations, err := marshalJSON(body)
	if err != nil {
		return nil, errs.Wrap(err, errs.Internal, "The GraphQL operations could not be written.")
	}

	// One file part per distinct file and Content-Type, in row order.
	type filePart struct {
		part  projectfile.Part
		row   int
		paths []string
	}
	var files []*filePart
	byFile := map[[2]string]*filePart{}
	for _, u := range uploads {
		key := [2]string{u.part.File, u.part.ContentType}
		fp := byFile[key]
		if fp == nil {
			fp = &filePart{part: u.part, row: u.row}
			byFile[key] = fp
			files = append(files, fp)
		}
		fp.paths = append(fp.paths, u.joined())
	}
	var m bytes.Buffer
	m.WriteByte('{')
	for i, fp := range files {
		if i > 0 {
			m.WriteByte(',')
		}
		paths, err := marshalJSON(fp.paths)
		if err != nil {
			return nil, errs.Wrap(err, errs.Internal, "The GraphQL upload map could not be written.")
		}
		m.WriteString(strconv.Quote(strconv.Itoa(i)) + ":")
		m.Write(paths)
	}
	m.WriteByte('}')

	parts := []projectfile.Part{
		{Name: "operations", Value: string(operations)},
		{Name: "map", Value: m.String()},
	}
	for i, fp := range files {
		parts = append(parts, projectfile.Part{Name: strconv.Itoa(i), File: fp.part.File, ContentType: fp.part.ContentType})
	}
	mb, err := multipartOf(parts, nil, root, func(i int, _ projectfile.Part) (int, string, string) {
		fp := files[i-2]
		return fp.row, fp.part.Name, "The file for " + strings.Join(fp.paths, " and ")
	})
	if err != nil {
		var e *errs.Error
		if errors.As(err, &e) {
			return nil, e.WithDetail(DetailField, "files")
		}
		return nil, err
	}
	return mb, nil
}

// setNull returns v with the value at path set to null, creating the
// objects and lists on the way (a list for a numeric step, an object
// otherwise; a list is padded with nulls up to the index). ok is false
// when a value on the way is neither an object, a list nor null, or a
// list's step is not an index.
func setNull(v any, path []string) (out any, ok bool) {
	if len(path) == 0 {
		return nil, true
	}
	step := path[0]
	if v == nil {
		if _, err := strconv.Atoi(step); err == nil {
			v = []any{}
		} else {
			v = map[string]any{}
		}
	}
	switch c := v.(type) {
	case map[string]any:
		child, ok := setNull(c[step], path[1:])
		if !ok {
			return v, false
		}
		c[step] = child
		return c, true
	case []any:
		idx, err := strconv.Atoi(step)
		if err != nil || idx < 0 || strings.HasPrefix(step, "+") {
			return v, false
		}
		for len(c) <= idx {
			c = append(c, nil)
		}
		child, ok := setNull(c[idx], path[1:])
		if !ok {
			return v, false
		}
		c[idx] = child
		return c, true
	}
	return v, false
}

// marshalJSON writes v as compact JSON without escaping <, > and &.
func marshalJSON(v any) (json.RawMessage, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// withPreflightHeader adds GraphQLPreflightHeader: true unless an enabled
// row sets it or omit names it.
func withPreflightHeader(headers []httpclient.Header, omit []string) []httpclient.Header {
	if hasHeader(headers, GraphQLPreflightHeader) || omitSet(omit)[strings.ToLower(GraphQLPreflightHeader)] {
		return headers
	}
	return append(headers, httpclient.Header{Name: GraphQLPreflightHeader, Value: "true", Enabled: true})
}
