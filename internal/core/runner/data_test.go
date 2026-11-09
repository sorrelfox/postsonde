package runner

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

func TestParseData(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		file      string
		content   string
		delimiter rune
		format    DataFormat
		delim     rune
		columns   []string
		rows      []map[string]string
	}{
		{
			name: "csv header and rows", file: "users.csv", content: "id,name\n1,Ada\n2,Grace\n",
			format: DataCSV, delim: ',', columns: []string{"id", "name"},
			rows: []map[string]string{{"id": "1", "name": "Ada"}, {"id": "2", "name": "Grace"}},
		},
		{
			name: "quoted fields: delimiter, doubled quotes and a line break inside", file: "q.csv",
			content: "id,note\n1,\"a, b\"\n2,\"say \"\"hi\"\"\"\n3,\"two\nlines\"\n",
			format:  DataCSV, delim: ',', columns: []string{"id", "note"},
			rows: []map[string]string{{"id": "1", "note": "a, b"}, {"id": "2", "note": `say "hi"`}, {"id": "3", "note": "two\nlines"}},
		},
		{
			name: "CRLF line ends, a CRLF in a quoted field is a line feed", file: "crlf.csv",
			content: "id,note\r\n1,x\r\n2,\"a\r\nb\"\r\n",
			format:  DataCSV, delim: ',', columns: []string{"id", "note"},
			rows: []map[string]string{{"id": "1", "note": "x"}, {"id": "2", "note": "a\nb"}},
		},
		{
			name: "BOM, spaces around header names, blank lines skipped, no final line end", file: "bom.csv",
			content: "\xef\xbb\xbf id , name \n\n1, Ada \n",
			format:  DataCSV, delim: ',', columns: []string{"id", "name"},
			rows: []map[string]string{{"id": "1", "name": " Ada "}},
		},
		{
			name: "semicolons detected", file: "semi.csv", content: "id;price\n1;1,50\n",
			format: DataCSV, delim: ';', columns: []string{"id", "price"},
			rows: []map[string]string{{"id": "1", "price": "1,50"}},
		},
		{
			name: "delimiter in a quoted header is not counted", file: "quoted-header.csv", content: "\"a;b\",c\n1,2\n",
			format: DataCSV, delim: ',', columns: []string{"a;b", "c"},
			rows: []map[string]string{{"a;b": "1", "c": "2"}},
		},
		{
			name: "tsv is tab separated", file: "t.tsv", content: "id\tname\n1\tA, B\n",
			format: DataCSV, delim: '\t', columns: []string{"id", "name"},
			rows: []map[string]string{{"id": "1", "name": "A, B"}},
		},
		{
			name: "pipe given", file: "p.csv", content: "id|name\n1|Ada\n", delimiter: '|',
			format: DataCSV, delim: '|', columns: []string{"id", "name"},
			rows: []map[string]string{{"id": "1", "name": "Ada"}},
		},
		{
			name: "one column", file: "one.csv", content: "id\n1\n2\n",
			format: DataCSV, delim: ',', columns: []string{"id"},
			rows: []map[string]string{{"id": "1"}, {"id": "2"}},
		},
		{
			name: "empty fields", file: "empty.csv", content: "a,b\n,\n",
			format: DataCSV, delim: ',', columns: []string{"a", "b"},
			rows: []map[string]string{{"a": "", "b": ""}},
		},
		{
			name: "json values keep their text", file: "rows.json",
			content: `[{"id": 1, "price": 1.50, "big": 1e3, "neg": -0, "ok": true, "no": false, "none": null, "s": "x \"y\" é", "obj": {"a": [1, 2]}, "list": [ "a" , 2 ]}]`,
			format:  DataJSON, columns: []string{"id", "price", "big", "neg", "ok", "no", "none", "s", "obj", "list"},
			rows: []map[string]string{{"id": "1", "price": "1.50", "big": "1e3", "neg": "-0", "ok": "true", "no": "false", "none": "",
				"s": `x "y" é`, "obj": `{"a":[1,2]}`, "list": `["a",2]`}},
		},
		{
			name: "json members differ by row, columns in the order met", file: "d.json",
			content: "[\n {\"b\": \"1\"},\n {\"a\": \"2\", \"b\": \"3\"}\n]\n",
			format:  DataJSON, columns: []string{"b", "a"},
			rows: []map[string]string{{"b": "1"}, {"a": "2", "b": "3"}},
		},
		{
			name: "json detected without an extension", file: "data", content: "  [{\"a\": \"1\"}]",
			format: DataJSON, columns: []string{"a"}, rows: []map[string]string{{"a": "1"}},
		},
		{
			name: "json with a BOM", file: "b.json", content: "\xef\xbb\xbf[{\"a\": \"1\"}]",
			format: DataJSON, columns: []string{"a"}, rows: []map[string]string{{"a": "1"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d, err := ParseData(tt.file, []byte(tt.content), tt.delimiter)
			if err != nil {
				t.Fatalf("ParseData = %v", err)
			}
			if d.Format != tt.format || d.Delimiter != tt.delim {
				t.Errorf("format, delimiter = %q, %q; want %q, %q", d.Format, d.Delimiter, tt.format, tt.delim)
			}
			if !reflect.DeepEqual(d.Columns, tt.columns) {
				t.Errorf("columns = %q, want %q", d.Columns, tt.columns)
			}
			if !reflect.DeepEqual(d.Rows, tt.rows) {
				t.Errorf("rows = %q, want %q", d.Rows, tt.rows)
			}
		})
	}
}

func TestParseDataErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		file    string
		content string
		code    string
		message string
		line    string
	}{
		{"csv without anything", "e.csv", "", CodeDataEmpty, `The data file "e.csv" has no rows.`, ""},
		{"csv header only", "h.csv", "id,name\r\n", CodeDataEmpty, `The data file "h.csv" has no rows.`, ""},
		{"a row with too few fields", "f.csv", "a,b\n1,2\n3\n", CodeDataInvalid,
			`The data file "f.csv" has a row with a different number of fields than its header row (line 3, column 1).`, "3"},
		{"a row with too many fields", "f.csv", "a,b\n1,2,3\n", CodeDataInvalid, "", "2"},
		{"a bare quote", "q.csv", "a,b\n1,x\"y\n", CodeDataInvalid, "", "2"},
		{"an unclosed quote", "q.csv", "a,b\n1,\"x\n", CodeDataInvalid, "", "2"},
		{"a column without a name", "n.csv", "a,,c\n1,2,3\n", CodeDataInvalid,
			`The data file "n.csv" has no name for column 2 of its header row.`, ""},
		{"a column named twice", "d.csv", "id,name,id\n1,2,3\n", CodeDataInvalid,
			`The data file "d.csv" names the column "id" twice in its header row.`, ""},
		{"not UTF-8", "l.csv", "id\n\xe9\n", CodeDataInvalid, `The data file "l.csv" is not UTF-8 text.`, ""},
		{"json empty file", "e.json", "  ", CodeDataEmpty, "", ""},
		{"json empty array", "e.json", "[]", CodeDataEmpty, `The data file "e.json" has no rows.`, ""},
		{"json object, not an array", "o.json", `{"a": 1}`, CodeDataInvalid, `The data file "o.json" is not a JSON array of objects.`, ""},
		{"json array of numbers", "n.json", "[\n  1\n]", CodeDataInvalid,
			`The data file "n.json" has an item that is not an object (item 1) (line 2, column 3).`, "2"},
		{"json syntax error", "s.json", "[\n{\"a\": 1,}\n]", CodeDataInvalid, "", "2"},
		{"json cut short", "c.json", `[{"a": 1}`, CodeDataInvalid, `The data file "c.json" ends before its JSON does.`, ""},
		{"json with more after the array", "m.json", `[{"a": 1}] {}`, CodeDataInvalid, "", "1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := ParseData(tt.file, []byte(tt.content), 0)
			var e *errs.Error
			if !errors.As(err, &e) {
				t.Fatalf("ParseData = %v, want an errs.Error", err)
			}
			if e.Code != tt.code || e.Kind != errs.InvalidArgument {
				t.Errorf("code, kind = %q, %q; want %q, invalid_argument (%v)", e.Code, e.Kind, tt.code, err)
			}
			if tt.message != "" && e.Message != tt.message {
				t.Errorf("message = %q\nwant      %q", e.Message, tt.message)
			}
			if e.Details["name"] != tt.file || e.Details["line"] != tt.line {
				t.Errorf("details = %v, want name %q, line %q", e.Details, tt.file, tt.line)
			}
			if e.Hint == "" {
				t.Error("no hint")
			}
		})
	}
}

func TestParseDataLimits(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	b.WriteString("n\n")
	for i := range MaxDataRows {
		fmt.Fprintf(&b, "%d\n", i)
	}
	d, err := ParseData("max.csv", []byte(b.String()), 0)
	if err != nil || len(d.Rows) != MaxDataRows {
		t.Fatalf("%d rows: %d, %v", MaxDataRows, len(d.Rows), err)
	}
	b.WriteString("one more\n")
	wantCode(t, "rows", CodeDataTooLarge, "rows")(ParseData("max.csv", []byte(b.String()), 0))

	var js strings.Builder
	js.WriteString("[")
	for i := range MaxDataRows + 1 {
		if i > 0 {
			js.WriteString(",")
		}
		js.WriteString("{}")
	}
	js.WriteString("]")
	wantCode(t, "json rows", CodeDataTooLarge, "rows")(ParseData("max.json", []byte(js.String()), 0))

	header := strings.Repeat("c,", MaxDataColumns) + "c\n"
	wantCode(t, "csv columns", CodeDataTooLarge, "columns")(ParseData("wide.csv", []byte(header), 0))

	var obj strings.Builder
	obj.WriteString("[{")
	for i := range MaxDataColumns + 1 {
		if i > 0 {
			obj.WriteString(",")
		}
		fmt.Fprintf(&obj, `"c%d": 1`, i)
	}
	obj.WriteString("}]")
	wantCode(t, "json columns", CodeDataTooLarge, "columns")(ParseData("wide.json", []byte(obj.String()), 0))

	wantCode(t, "bytes", CodeDataTooLarge, "bytes")(ParseData("big.csv", make([]byte, MaxDataBytes+1), 0))
}

func wantCode(t *testing.T, name, code, limit string) func(Data, error) {
	return func(_ Data, err error) {
		t.Helper()
		var e *errs.Error
		if !errors.As(err, &e) || e.Code != code || e.Details["limit"] != limit {
			t.Errorf("%s: err = %v, want %s (limit %s)", name, err, code, limit)
		}
	}
}

func TestReadDataFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "users.csv")
	if err := os.WriteFile(path, []byte("id\n7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := ReadDataFile(path, 0)
	if err != nil || len(d.Rows) != 1 || d.Rows[0]["id"] != "7" {
		t.Fatalf("ReadDataFile = %v, %v", d, err)
	}

	var e *errs.Error
	if _, err := ReadDataFile(filepath.Join(dir, "gone.csv"), 0); !errors.As(err, &e) || e.Code != CodeDataUnreadable || e.Kind != errs.NotFound {
		t.Errorf("missing file: %v", err)
	}
	if _, err := ReadDataFile(dir, 0); !errors.As(err, &e) || e.Code != CodeDataUnreadable {
		t.Errorf("folder: %v", err)
	}
	big := filepath.Join(dir, "big.csv")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(MaxDataBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := ReadDataFile(big, 0); !errors.As(err, &e) || e.Code != CodeDataTooLarge {
		t.Errorf("large file: %v", err)
	}
}

func TestValidDelimiter(t *testing.T) {
	t.Parallel()
	for _, d := range []rune{0, ',', ';', '\t', '|'} {
		if !ValidDelimiter(d) {
			t.Errorf("%q not valid", d)
		}
	}
	for _, d := range []rune{'"', 'a', '\n', ':'} {
		if ValidDelimiter(d) {
			t.Errorf("%q valid", d)
		}
	}
}

func TestVarsRow(t *testing.T) {
	t.Parallel()
	v := NewVars()
	v.SetRow(map[string]string{"id": "1", "name": "Ada"})
	v.Set("token", template.Variable{Value: "t1", Secret: true})
	lookup := func(name string) string {
		val, ok := v.Lookup(name)
		if !ok {
			return "<unset>"
		}
		return val.Value
	}
	if lookup("id") != "1" || lookup("token") != "t1" {
		t.Fatalf("first row: id %q, token %q", lookup("id"), lookup("token"))
	}
	// A value extracted during the iteration wins over the row.
	v.Set("id", template.Variable{Value: "from response"})
	if lookup("id") != "from response" {
		t.Errorf("id after Set = %q", lookup("id"))
	}
	if got := v.Names(); !reflect.DeepEqual(got, []string{"id", "name", "token"}) {
		t.Errorf("names = %q", got)
	}
	// The next row replaces the row and what was set under its names;
	// a name it does not have is not the previous row's any more.
	v.SetRow(map[string]string{"id": "2"})
	if lookup("id") != "2" || lookup("name") != "<unset>" || lookup("token") != "t1" {
		t.Errorf("second row: id %q, name %q, token %q", lookup("id"), lookup("name"), lookup("token"))
	}
	if got := v.Names(); !reflect.DeepEqual(got, []string{"id", "token"}) {
		t.Errorf("names = %q", got)
	}
	chain := template.Chain{template.Vars("Folder", map[string]string{"id": "folder", "name": "folder"}), v}
	if b, ok := chain.Lookup("id"); !ok || b.Value != "2" || b.Scope != VarsScopeName {
		t.Errorf("chain id = %+v", b)
	}
	if b, ok := chain.Lookup("name"); !ok || b.Value != "folder" {
		t.Errorf("chain name = %+v", b)
	}
}
