package report

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/testutil/golden"
)

const secret = "s3cr3t-t0ken"

// sample is a run of two iterations of a folder with one of each outcome,
// a secret in an assertion's value and an error, a name that repeats and
// a character XML cannot hold.
func sample() Run {
	b := NewBuilder(Summary{
		ID: "op_0123456789abcdef", FolderID: "fld_api", Folder: "API", Project: "Shop",
		StartedAt: 1_790_000_000_000, Iterations: 2, Requests: 5, Total: 10, DelayMs: 250,
		DataFile: "users.csv", DataRows: 2,
	}, 0)
	start := int64(1_790_000_000_000)
	for it := 1; it <= 2; it++ {
		base := (it - 1) * 5
		at := func(n int) int64 { return start + int64(base+n)*100 }
		b.Add(Result{Iteration: it, Index: base + 1, Position: 1, RequestID: "req_create", Name: "Create", Kind: "http", Method: "POST",
			Path: []string{}, Outcome: OutcomePassed, StartedAt: at(1), DurationMs: 12, StatusCode: 201, Status: "201 Created", Size: 20,
			TestsPassed: 1, Tests: []Test{{Index: 0, Target: "status", Op: "equals", Expected: "201", Outcome: "passed", Actual: "201", Exists: true,
				Message: "Expected status code to equal 201, and it was 201."}}})
		b.Add(Result{Iteration: it, Index: base + 2, Position: 2, RequestID: "req_get", Name: "Get <user> & \"co\"", Kind: "http", Method: "GET",
			Path: []string{"Users"}, Outcome: OutcomeFailed, StartedAt: at(2), DurationMs: 1500, StatusCode: 200, Status: "200 OK",
			TestsPassed: 0, TestsFailed: 2, Tests: []Test{
				{Index: 0, Target: "header", Expr: "Authorization", Op: "equals", Expected: "Bearer x", Outcome: "failed", Actual: "Bearer " + secret, Exists: true,
					Message: "Expected header Authorization to equal \"Bearer x\", but it was \"Bearer " + secret + "\"."},
				{Index: 1, Target: "jsonpath", Expr: "$[", Op: "exists", Outcome: "invalid", Message: "The JSONPath query \"$[\" is not valid.", Code: "jsonpath.invalid_query"},
			}})
		b.Add(Result{Iteration: it, Index: base + 3, Position: 3, RequestID: "req_undefined", Name: "Undefined", Kind: "http", Method: "GET",
			Path: []string{}, Outcome: OutcomeError, StartedAt: at(3), DurationMs: 0, Tests: []Test{},
			Error: &Error{Kind: "invalid_argument", Code: "template.unresolved", Message: "The variable {{nope}} is not defined.", Hint: "Define it in an environment."}})
		b.Add(Result{Iteration: it, Index: base + 4, Position: 4, RequestID: "req_socket", Name: "Socket", Kind: "websocket",
			Path: []string{}, Skip: "kind", Outcome: OutcomeSkipped, Tests: []Test{}})
		b.Add(Result{Iteration: it, Index: base + 5, Position: 5, RequestID: "req_dup", Name: "Create", Kind: "sse", Method: "GET",
			Path: []string{}, Outcome: OutcomeFailed, StartedAt: at(5), DurationMs: 30, StatusCode: 404, Status: "404 Not \x01Found", Events: 0, Tests: []Test{}})
	}
	return b.Finish(StatusStopped, start+2500)
}

func mask(s string) string { return strings.ReplaceAll(s, secret, "••••••") }

func TestBuilder(t *testing.T) {
	t.Parallel()
	r := sample()
	want := Counts{Results: 10, Passed: 2, Failed: 4, Errors: 2, Skipped: 2}
	if r.Counts != want || r.TotalMs != 2*(12+1500+0+30) || r.ElapsedMs != 2500 || r.Status != StatusStopped || r.Schema != Schema || r.Omitted != 0 {
		t.Fatalf("summary = %+v", r.Summary)
	}

	b := NewBuilder(Summary{ID: "op_1"}, 3)
	long := strings.Repeat("é", MaxText)
	for i := range 5 {
		b.Add(Result{Index: i + 1, Outcome: OutcomePassed, Name: long, Tests: []Test{{Message: long}}})
	}
	run := b.Finish(StatusOK, 0)
	if len(run.Results) != 3 || run.Omitted != 2 || run.Counts.Results != 5 || b.Len() != 5 || run.ElapsedMs != 0 {
		t.Fatalf("kept %d, omitted %d, counts %+v", len(run.Results), run.Omitted, run.Counts)
	}
	name := run.Results[0].Name
	if len(name) > MaxText || !strings.HasSuffix(name, "…") || !strings.HasPrefix(name, "éé") || !utf8Valid(name) || len(run.Results[0].Tests[0].Message) > MaxText {
		t.Errorf("clipped name: %d bytes, valid %v", len(name), utf8Valid(name))
	}
}

func utf8Valid(s string) bool { return strings.ToValidUTF8(s, "�") == s }

func TestMasked(t *testing.T) {
	t.Parallel()
	r := sample()
	r.Folder = "API " + secret
	m := r.Masked(mask)
	var buf bytes.Buffer
	if err := WriteJSON(&buf, m); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), secret) {
		t.Fatalf("secret in the masked record:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "••••••") {
		t.Error("nothing masked")
	}
	// The original is unchanged.
	if !strings.Contains(r.Results[1].Tests[0].Actual, secret) || !strings.Contains(r.Folder, secret) {
		t.Error("Masked changed the run it was called on")
	}
}

func TestWriteJUnitGolden(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if err := WriteJUnit(&buf, sample().Masked(mask)); err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, "junit", buf.Bytes())
	if strings.Contains(buf.String(), secret) {
		t.Error("secret in the report")
	}
}

func TestWriteJUnitOneIteration(t *testing.T) {
	t.Parallel()
	b := NewBuilder(Summary{ID: "op_1", Project: "Shop", Iterations: 1, StartedAt: 1}, 0)
	b.Add(Result{Iteration: 1, Index: 1, Name: "Ping", Outcome: OutcomePassed, DurationMs: 2})
	b.Add(Result{Iteration: 1, Index: 2, Name: "Ping", Outcome: OutcomePassed, DurationMs: 2})
	var buf bytes.Buffer
	if err := WriteJUnit(&buf, b.Finish(StatusOK, 5)); err != nil {
		t.Fatal(err)
	}
	doc := checkJUnit(t, buf.Bytes())
	if len(doc.Suites) != 1 || doc.Suites[0].Name != "Shop" {
		t.Fatalf("suites = %+v", doc.Suites)
	}
	names := []string{doc.Suites[0].Cases[0].Name, doc.Suites[0].Cases[1].Name}
	if names[0] != "Ping" || names[1] != "Ping (2)" || doc.Suites[0].Cases[0].Classname != "Shop" {
		t.Errorf("cases = %q", names)
	}

	// No results: an empty report that still parses.
	buf.Reset()
	if err := WriteJUnit(&buf, NewBuilder(Summary{ID: "op_2"}, 0).Finish(StatusCancelled, 0)); err != nil {
		t.Fatal(err)
	}
	if doc := checkJUnit(t, buf.Bytes()); len(doc.Suites) != 0 || doc.Name != "PostSonde run" {
		t.Errorf("empty = %+v", doc)
	}
}

// The JUnit structure CI test report parsers read: a testsuites root (or
// testsuite), testsuite elements holding testcase elements with name,
// classname and time in seconds, each with at most one of failure, error
// and skipped, and system-out; other elements are not expected. The
// counts of the Ant/Maven schema (tests, failures, errors, skipped, time)
// must add up.
type xSuites struct {
	XMLName  xml.Name   `xml:"testsuites"`
	Name     string     `xml:"name,attr"`
	Tests    string     `xml:"tests,attr"`
	Failures string     `xml:"failures,attr"`
	Errors   string     `xml:"errors,attr"`
	Skipped  string     `xml:"skipped,attr"`
	Time     string     `xml:"time,attr"`
	Suites   []xSuite   `xml:"testsuite"`
	Other    []xElement `xml:",any"`
}

type xSuite struct {
	Name       string      `xml:"name,attr"`
	ID         string      `xml:"id,attr"`
	Tests      string      `xml:"tests,attr"`
	Failures   string      `xml:"failures,attr"`
	Errors     string      `xml:"errors,attr"`
	Skipped    string      `xml:"skipped,attr"`
	Time       string      `xml:"time,attr"`
	Timestamp  string      `xml:"timestamp,attr"`
	Properties []xProperty `xml:"properties>property"`
	Cases      []xCase     `xml:"testcase"`
	Other      []xElement  `xml:",any"`
}

type xProperty struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

type xCase struct {
	Name      string     `xml:"name,attr"`
	Classname string     `xml:"classname,attr"`
	Time      string     `xml:"time,attr"`
	Failure   *xProblem  `xml:"failure"`
	Error     *xProblem  `xml:"error"`
	Skipped   *xProblem  `xml:"skipped"`
	SystemOut *string    `xml:"system-out"`
	Other     []xElement `xml:",any"`
}

type xProblem struct {
	Message string `xml:"message,attr"`
	Type    string `xml:"type,attr"`
	Text    string `xml:",chardata"`
}

type xElement struct {
	XMLName xml.Name
}

var (
	isTime      = regexp.MustCompile(`^\d+\.\d{3}$`)
	isTimestamp = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}$`)
)

// checkJUnit parses b strictly and checks it against the structure above.
func checkJUnit(t *testing.T, b []byte) xSuites {
	t.Helper()
	if !bytes.HasPrefix(b, []byte(xml.Header)) {
		t.Errorf("no XML declaration")
	}
	// Well-formed, with no character XML 1.0 cannot hold.
	dec := xml.NewDecoder(bytes.NewReader(b))
	dec.Strict = true
	for {
		_, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("not well-formed XML: %v", err)
		}
	}
	var doc xSuites
	if err := xml.Unmarshal(b, &doc); err != nil {
		t.Fatalf("not JUnit XML: %v", err)
	}
	if len(doc.Other) > 0 {
		t.Errorf("unexpected elements in testsuites: %v", doc.Other)
	}
	atoi := func(what, s string) int {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			t.Errorf("%s = %q, want a count", what, s)
		}
		return n
	}
	var tests, failures, errors, skipped int
	keys := map[string]bool{}
	for i, s := range doc.Suites {
		if s.Name == "" || s.ID != strconv.Itoa(i) || !isTime.MatchString(s.Time) || (s.Timestamp != "" && !isTimestamp.MatchString(s.Timestamp)) {
			t.Errorf("suite %d = %+v", i, s)
		}
		if len(s.Other) > 0 {
			t.Errorf("unexpected elements in suite %q: %v", s.Name, s.Other)
		}
		var f, e, sk int
		for _, c := range s.Cases {
			if c.Name == "" || c.Classname == "" || !isTime.MatchString(c.Time) {
				t.Errorf("case %+v", c)
			}
			if _, err := strconv.ParseFloat(c.Time, 64); err != nil {
				t.Errorf("case time %q", c.Time)
			}
			if len(c.Other) > 0 {
				t.Errorf("unexpected elements in case %q: %v", c.Name, c.Other)
			}
			n := 0
			if c.Failure != nil {
				n, f = n+1, f+1
				if c.Failure.Message == "" || c.Failure.Text == "" {
					t.Errorf("failure of %q without a message", c.Name)
				}
			}
			if c.Error != nil {
				n, e = n+1, e+1
				if c.Error.Message == "" || c.Error.Type == "" {
					t.Errorf("error of %q without a message or type", c.Name)
				}
			}
			if c.Skipped != nil {
				n, sk = n+1, sk+1
			}
			if n > 1 {
				t.Errorf("case %q has %d outcomes", c.Name, n)
			}
			key := c.Classname + "\x00" + c.Name
			if keys[key] {
				t.Errorf("case %q of %q twice", c.Name, c.Classname)
			}
			keys[key] = true
		}
		if atoi("tests", s.Tests) != len(s.Cases) || atoi("failures", s.Failures) != f || atoi("errors", s.Errors) != e || atoi("skipped", s.Skipped) != sk {
			t.Errorf("suite %q counts %s/%s/%s/%s, cases %d/%d/%d/%d", s.Name, s.Tests, s.Failures, s.Errors, s.Skipped, len(s.Cases), f, e, sk)
		}
		tests, failures, errors, skipped = tests+len(s.Cases), failures+f, errors+e, skipped+sk
	}
	if atoi("tests", doc.Tests) != tests || atoi("failures", doc.Failures) != failures || atoi("errors", doc.Errors) != errors || atoi("skipped", doc.Skipped) != skipped || !isTime.MatchString(doc.Time) {
		t.Errorf("testsuites counts = %s/%s/%s/%s %s, want %d/%d/%d/%d", doc.Tests, doc.Failures, doc.Errors, doc.Skipped, doc.Time, tests, failures, errors, skipped)
	}
	return doc
}

func TestWriteJUnitStructure(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if err := WriteJUnit(&buf, sample()); err != nil {
		t.Fatal(err)
	}
	doc := checkJUnit(t, buf.Bytes())
	if doc.Name != "API" || len(doc.Suites) != 2 || doc.Tests != "10" || doc.Failures != "4" || doc.Errors != "2" || doc.Skipped != "2" || doc.Time != "2.500" {
		t.Fatalf("testsuites = %+v", doc)
	}
	s := doc.Suites[1]
	if s.Name != "API, iteration 2" || s.Time != "1.542" || s.Timestamp != "2026-09-21T14:13:20" {
		t.Errorf("suite = %+v", s)
	}
	props := fmt.Sprint(s.Properties)
	if props != "[{iteration 2} {dataFile users.csv} {dataRow 2}]" {
		t.Errorf("properties = %s", props)
	}
	get := s.Cases[1]
	if get.Name != `Get <user> & "co" (iteration 2)` || get.Classname != "API.Users" || get.Time != "1.500" {
		t.Errorf("get = %+v", get)
	}
	if get.Failure == nil || get.Failure.Type != "invalid assertion" || !strings.HasPrefix(get.Failure.Message, "Expected header Authorization") ||
		strings.Count(get.Failure.Text, "\n") != 1 {
		t.Errorf("get failure = %+v", get.Failure)
	}
	if u := s.Cases[2]; u.Error == nil || u.Error.Type != "template.unresolved" || u.Error.Text != "The variable {{nope}} is not defined.\nDefine it in an environment." {
		t.Errorf("error = %+v", u.Error)
	}
	if sk := s.Cases[3]; sk.Skipped == nil || sk.Skipped.Message != "Not run: a run sends HTTP, GraphQL and SSE requests only." || sk.SystemOut != nil {
		t.Errorf("skipped = %+v", sk)
	}
	// The second "Create" of the iteration, and the SSE status failure
	// with the character XML cannot hold replaced.
	if dup := s.Cases[4]; dup.Name != "Create (iteration 2) (2)" || dup.Failure == nil || dup.Failure.Type != "status" ||
		dup.Failure.Message != "The response's status was 404 Not �Found." {
		t.Errorf("dup = %+v", dup)
	}
	if out := s.Cases[0].SystemOut; out == nil || *out != "POST Create: 201 Created, 12 ms\nPASSED: Expected status code to equal 201, and it was 201." {
		t.Errorf("system-out = %v", out)
	}
}

func TestWriteHTML(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if err := WriteHTML(&buf, sample().Masked(mask)); err != nil {
		t.Fatal(err)
	}
	html := buf.String()
	for _, want := range []string{
		"<title>API: run report</title>", "Stopped at a failure", "data file users.csv, 2 rows",
		"Get &lt;user&gt; &amp; &#34;co&#34;", "Expected header Authorization", "••••••", "The variable {{nope}} is not defined.",
		"Not run: a run sends HTTP, GraphQL and SSE requests only.", "1.500 s", "12 ms",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("HTML has no %q", want)
		}
	}
	for _, bad := range []string{secret, "<script", "http://", "https://", "src=", "href="} {
		if strings.Contains(html, bad) {
			t.Errorf("HTML has %q", bad)
		}
	}
}

func TestWriteJSONRoundTrip(t *testing.T) {
	t.Parallel()
	r := sample()
	var buf bytes.Buffer
	if err := WriteJSON(&buf, r); err != nil {
		t.Fatal(err)
	}
	var back Run
	if err := json.Unmarshal(buf.Bytes(), &back); err != nil {
		t.Fatal(err)
	}
	if back.Counts != r.Counts || len(back.Results) != 10 || back.Results[2].Error.Code != "template.unresolved" || back.Schema != Schema || back.DataFile != "users.csv" {
		t.Errorf("round trip = %+v", back.Summary)
	}
	if !strings.Contains(buf.String(), `"schema": "postsonde.run/1"`) || !strings.Contains(buf.String(), "{{nope}}") {
		t.Errorf("JSON:\n%s", buf.String()[:200])
	}
}

func TestWhy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		res  Result
		want string
	}{
		{Result{Outcome: OutcomePassed}, ""},
		{Result{Outcome: OutcomeError}, "The request could not be sent."},
		{Result{Outcome: OutcomeFailed}, "The request failed."},
		{Result{Outcome: OutcomeSkipped, Skip: "subscription"}, "Not run: GraphQL subscriptions and operations over WebSocket are not part of a run."},
		{Result{Outcome: OutcomeSkipped, Skip: "unknown"}, "Not run: this version does not know this kind of request."},
		{Result{Outcome: OutcomeSkipped, Skip: "invalid"}, "Not run: the file could not be loaded."},
	}
	for _, tt := range tests {
		if got := tt.res.Why(); got != tt.want {
			t.Errorf("Why(%+v) = %q, want %q", tt.res, got, tt.want)
		}
	}
	if (Summary{}).Title() != "PostSonde run" || (Summary{Project: "P"}).Title() != "P" {
		t.Error("Title")
	}
}
