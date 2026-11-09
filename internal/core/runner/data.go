package runner

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// Data-driven runs (RUN-05): a run over the rows of a CSV or JSON file
// makes one iteration per row, each with the row's values as variables of
// the run (Vars.SetRow). See docs/runner.md#data-driven-runs.

// Error codes of data files.
const (
	// CodeDataInvalid: the data file cannot be read as CSV or JSON rows
	// (kind invalid_argument). Details: name, and line and column where
	// the file says where.
	CodeDataInvalid = "runner.data_invalid"
	// CodeDataTooLarge: the data file is over MaxDataBytes, or has more
	// than MaxDataRows rows or MaxDataColumns columns (kind
	// invalid_argument). Details: name, limit.
	CodeDataTooLarge = "runner.data_too_large"
	// CodeDataEmpty: the data file has no rows (kind invalid_argument).
	// Details: name.
	CodeDataEmpty = "runner.data_empty"
	// CodeDataUnreadable: the data file cannot be opened or read (kind
	// not_found, permission_denied or io). Details: name.
	CodeDataUnreadable = "runner.data_unreadable"
)

// Limits of a data file.
const (
	// MaxDataBytes is the largest data file read.
	MaxDataBytes = 16 << 20
	// MaxDataRows is the most rows: one iteration each, so MaxIterations.
	MaxDataRows = MaxIterations
	// MaxDataColumns is the most columns (variables) of a row.
	MaxDataColumns = 1000
)

// DataFormat is the format of a data file.
type DataFormat string

const (
	// DataCSV: a header row naming the columns, then one row per line.
	DataCSV DataFormat = "csv"
	// DataJSON: an array of objects, one per row.
	DataJSON DataFormat = "json"
)

// Data is the rows of a data file.
type Data struct {
	Format DataFormat
	// Delimiter is the field delimiter of a CSV file (the one given, or
	// the one detected); 0 for JSON.
	Delimiter rune
	// Columns are the variable names, in the order the file has them: the
	// header of a CSV file, the members of a JSON file's objects in the
	// order first met.
	Columns []string
	// Rows are the values of each row by column. A JSON object without a
	// member has no value for it: the variable is not set by that row.
	Rows []map[string]string
}

// Delimiters are the CSV field delimiters a data file may use.
var Delimiters = []rune{',', ';', '\t', '|'}

// ValidDelimiter reports whether d is one of Delimiters, or 0 (detect).
func ValidDelimiter(d rune) bool {
	return d == 0 || strings.ContainsRune(string(Delimiters), d)
}

// ReadDataFile reads and parses the data file at path (ParseData). It
// fails with CodeDataUnreadable when the file cannot be read and
// CodeDataTooLarge when it is over MaxDataBytes, before reading it all.
func ReadDataFile(path string, delimiter rune) (Data, error) {
	name := filepath.Base(path)
	f, err := os.Open(path)
	if err != nil {
		return Data{}, unreadable(name, err)
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() > MaxDataBytes {
		return Data{}, tooLarge(name, "bytes", "is larger than 16 MiB")
	} else if err == nil && st.IsDir() {
		return Data{}, errs.New(errs.InvalidArgument, fmt.Sprintf("%q is a folder, not a data file.", name)).
			WithCode(CodeDataUnreadable).WithDetail("name", name)
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxDataBytes+1))
	if err != nil {
		return Data{}, unreadable(name, err)
	}
	if len(b) > MaxDataBytes {
		return Data{}, tooLarge(name, "bytes", "is larger than 16 MiB")
	}
	return ParseData(name, b, delimiter)
}

func unreadable(name string, err error) error {
	kind := errs.IO
	switch {
	case errors.Is(err, os.ErrNotExist):
		kind = errs.NotFound
	case errors.Is(err, os.ErrPermission):
		kind = errs.PermissionDenied
	}
	return errs.Wrap(err, kind, fmt.Sprintf("The data file %q could not be read.", name)).
		WithCode(CodeDataUnreadable).
		WithHint("Choose the file again.").
		WithDetail("name", name)
}

// ParseData parses the data file name (its base name, for the format and
// messages) with content b. A name ending in .json is JSON; .csv, .tsv
// and .txt are CSV; another is JSON when its first character is "[" and
// CSV otherwise. delimiter is the CSV field delimiter, 0 to detect it
// (a tab for .tsv).
func ParseData(name string, b []byte, delimiter rune) (Data, error) {
	if len(b) > MaxDataBytes {
		return Data{}, tooLarge(name, "bytes", "is larger than 16 MiB")
	}
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	if !utf8.Valid(b) {
		return Data{}, invalidData(name, "is not UTF-8 text", 0, 0)
	}
	ext := strings.ToLower(filepath.Ext(name))
	switch {
	case ext == ".json":
		return parseJSON(name, b)
	case ext == ".tsv" && delimiter == 0:
		return parseCSV(name, b, '\t')
	case ext == ".csv" || ext == ".tsv" || ext == ".txt":
		return parseCSV(name, b, delimiter)
	case bytes.HasPrefix(bytes.TrimLeft(b, " \t\r\n"), []byte("[")):
		return parseJSON(name, b)
	}
	return parseCSV(name, b, delimiter)
}

func invalidData(name, what string, line, column int) error {
	msg := fmt.Sprintf("The data file %q %s.", name, what)
	if line > 0 {
		msg = fmt.Sprintf("The data file %q %s (line %d, column %d).", name, what, line, column)
	}
	e := errs.New(errs.InvalidArgument, msg).WithCode(CodeDataInvalid).WithDetail("name", name)
	if line > 0 {
		e = e.WithDetail("line", strconv.Itoa(line)).WithDetail("column", strconv.Itoa(column))
	}
	return e.WithHint("A data file is a CSV file with a header row, or a JSON array of objects.")
}

func tooLarge(name, limit, what string) error {
	return errs.New(errs.InvalidArgument, fmt.Sprintf("The data file %q %s.", name, what)).
		WithCode(CodeDataTooLarge).
		WithHint("A data file has at most 10,000 rows and 1,000 columns, in 16 MiB. Split it into several runs.").
		WithDetail("name", name).
		WithDetail("limit", limit)
}

func emptyData(name string) error {
	return errs.New(errs.InvalidArgument, fmt.Sprintf("The data file %q has no rows.", name)).
		WithCode(CodeDataEmpty).
		WithHint("A run makes one iteration per row: add at least one row under the header.").
		WithDetail("name", name)
}

// parseCSV reads a header row and the rows under it (encoding/csv:
// quoted fields, "" in a quoted field, CRLF line ends, blank lines
// skipped). Every row has as many fields as the header.
func parseCSV(name string, b []byte, delimiter rune) (Data, error) {
	if delimiter == 0 {
		delimiter = sniffDelimiter(b)
	}
	r := csv.NewReader(bytes.NewReader(b))
	r.Comma = delimiter
	header, err := r.Read()
	if errors.Is(err, io.EOF) {
		return Data{}, emptyData(name)
	}
	if err != nil {
		return Data{}, csvError(name, err)
	}
	if len(header) > MaxDataColumns {
		return Data{}, tooLarge(name, "columns", "has more than 1,000 columns")
	}
	columns := make([]string, len(header))
	seen := make(map[string]bool, len(header))
	for i, h := range header {
		h = strings.TrimSpace(h)
		switch {
		case h == "":
			return Data{}, invalidData(name, fmt.Sprintf("has no name for column %d of its header row", i+1), 0, 0)
		case seen[h]:
			return Data{}, invalidData(name, fmt.Sprintf("names the column %q twice in its header row", h), 0, 0)
		}
		seen[h] = true
		columns[i] = h
	}
	d := Data{Format: DataCSV, Delimiter: delimiter, Columns: columns}
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Data{}, csvError(name, err)
		}
		if len(d.Rows) == MaxDataRows {
			return Data{}, tooLarge(name, "rows", "has more than 10,000 rows")
		}
		row := make(map[string]string, len(columns))
		for i, c := range columns {
			row[c] = rec[i]
		}
		d.Rows = append(d.Rows, row)
	}
	if len(d.Rows) == 0 {
		return Data{}, emptyData(name)
	}
	return d, nil
}

func csvError(name string, err error) error {
	var pe *csv.ParseError
	if !errors.As(err, &pe) {
		return invalidData(name, "cannot be read as CSV", 0, 0)
	}
	switch {
	case errors.Is(pe.Err, csv.ErrFieldCount):
		return invalidData(name, "has a row with a different number of fields than its header row", pe.Line, pe.Column)
	case errors.Is(pe.Err, csv.ErrQuote), errors.Is(pe.Err, csv.ErrBareQuote):
		return invalidData(name, `has a quote (") where a field cannot have one; put the field in quotes and double the quotes inside it`, pe.Line, pe.Column)
	}
	return invalidData(name, "cannot be read as CSV", pe.Line, pe.Column)
}

// sniffDelimiter is the delimiter of Delimiters found most often outside
// quotes in the first line of b; a comma when there is none.
func sniffDelimiter(b []byte) rune {
	counts := map[rune]int{}
	quoted := false
	for _, c := range string(b) {
		if c == '"' {
			quoted = !quoted
			continue
		}
		if !quoted && (c == '\n' || c == '\r') {
			break
		}
		if !quoted && strings.ContainsRune(string(Delimiters), c) {
			counts[c]++
		}
	}
	best, n := ',', 0
	for _, d := range Delimiters {
		if counts[d] > n {
			best, n = d, counts[d]
		}
	}
	return best
}

// parseJSON reads an array of objects. String values are taken as they
// are; numbers, booleans, objects and arrays as their JSON text (compact
// for objects and arrays), so 1.50 stays "1.50"; null is "".
func parseJSON(name string, b []byte) (Data, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	fail := func(err error) (Data, error) {
		var se *json.SyntaxError
		if errors.As(err, &se) && se.Offset < int64(len(bytes.TrimRight(b, " \t\r\n"))) {
			line, col := lineCol(b, se.Offset)
			return Data{}, invalidData(name, "is not valid JSON", line, col)
		}
		if se != nil || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return Data{}, invalidData(name, "ends before its JSON does", 0, 0)
		}
		line, col := lineCol(b, dec.InputOffset())
		return Data{}, invalidData(name, "is not valid JSON", line, col)
	}
	tok, err := dec.Token()
	if errors.Is(err, io.EOF) {
		return Data{}, emptyData(name)
	}
	if err != nil {
		return fail(err)
	}
	if tok != json.Delim('[') {
		return Data{}, invalidData(name, "is not a JSON array of objects", 0, 0)
	}
	d := Data{Format: DataJSON}
	known := map[string]bool{}
	for dec.More() {
		if len(d.Rows) == MaxDataRows {
			return Data{}, tooLarge(name, "rows", "has more than 10,000 rows")
		}
		at := dec.InputOffset()
		tok, err := dec.Token()
		if err != nil {
			return fail(err)
		}
		if tok != json.Delim('{') {
			line, col := lineCol(b, at)
			return Data{}, invalidData(name, fmt.Sprintf("has an item that is not an object (item %d)", len(d.Rows)+1), line, col)
		}
		row := map[string]string{}
		for dec.More() {
			tok, err := dec.Token()
			if err != nil {
				return fail(err)
			}
			key, _ := tok.(string)
			var raw json.RawMessage
			if err := dec.Decode(&raw); err != nil {
				return fail(err)
			}
			if !known[key] {
				if len(d.Columns) == MaxDataColumns {
					return Data{}, tooLarge(name, "columns", "has more than 1,000 columns")
				}
				known[key] = true
				d.Columns = append(d.Columns, key)
			}
			row[key] = jsonText(raw)
		}
		if _, err := dec.Token(); err != nil { // }
			return fail(err)
		}
		d.Rows = append(d.Rows, row)
	}
	if _, err := dec.Token(); err != nil { // ]
		return fail(err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		line, col := lineCol(b, dec.InputOffset())
		return Data{}, invalidData(name, "has more after its JSON array", line, col)
	}
	if len(d.Rows) == 0 {
		return Data{}, emptyData(name)
	}
	return d, nil
}

// jsonText is the text of a JSON value as a variable's value.
func jsonText(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	switch {
	case len(raw) == 0 || string(raw) == "null":
		return ""
	case raw[0] == '"':
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
	case raw[0] == '{' || raw[0] == '[':
		var buf bytes.Buffer
		if json.Compact(&buf, raw) == nil {
			return buf.String()
		}
	}
	return string(raw)
}

// lineCol is the 1-based line and column (in characters) of byte offset
// off of b.
func lineCol(b []byte, off int64) (int, int) {
	if off < 0 {
		off = 0
	}
	if off > int64(len(b)) {
		off = int64(len(b))
	}
	before := b[:off]
	line := bytes.Count(before, []byte("\n")) + 1
	start := bytes.LastIndexByte(before, '\n') + 1
	return line, utf8.RuneCount(before[start:]) + 1
}
