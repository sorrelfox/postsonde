package report

import (
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// JUnit XML (RUN-08), in the common form test tools write and the test
// report parsers of CI servers read:
//
//	<testsuites name tests failures errors skipped time timestamp>
//	  <testsuite name tests failures errors skipped time timestamp>  one per iteration
//	    <properties><property name value/>…</properties>
//	    <testcase name classname time>                              one per request
//	      <failure message type>…</failure>   an assertion failed, or an SSE status
//	      <error message type>…</error>       the request could not be built or sent
//	      <skipped message/>                  a kind a run does not send
//	      <system-out>…</system-out>          the status, time and each assertion
//	    </testcase>
//	  </testsuite>
//	</testsuites>
//
// A suite per iteration, so a data-driven run's rows are apart. The
// classname is the run's folder and the folders down to the request,
// joined with "."; the name is the request's, with the iteration when
// the run has several ("Get user (iteration 2)"), and a number when the
// same classname and name come again, so every case is unique within the
// report (CI test report parsers key cases by them). Times are seconds;
// timestamps are UTC without a zone, as the Ant schema has them.
// Characters XML cannot hold become U+FFFD (encoding/xml).

type junitSuites struct {
	XMLName   xml.Name     `xml:"testsuites"`
	Name      string       `xml:"name,attr"`
	Tests     int          `xml:"tests,attr"`
	Failures  int          `xml:"failures,attr"`
	Errors    int          `xml:"errors,attr"`
	Skipped   int          `xml:"skipped,attr"`
	Time      string       `xml:"time,attr"`
	Timestamp string       `xml:"timestamp,attr,omitempty"`
	Suites    []junitSuite `xml:"testsuite"`
}

type junitSuite struct {
	Name       string          `xml:"name,attr"`
	ID         int             `xml:"id,attr"`
	Tests      int             `xml:"tests,attr"`
	Failures   int             `xml:"failures,attr"`
	Errors     int             `xml:"errors,attr"`
	Skipped    int             `xml:"skipped,attr"`
	Time       string          `xml:"time,attr"`
	Timestamp  string          `xml:"timestamp,attr,omitempty"`
	Properties []junitProperty `xml:"properties>property"`
	Cases      []junitCase     `xml:"testcase"`
}

type junitProperty struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

type junitCase struct {
	Name      string        `xml:"name,attr"`
	Classname string        `xml:"classname,attr"`
	Time      string        `xml:"time,attr"`
	Failure   *junitProblem `xml:"failure"`
	Error     *junitProblem `xml:"error"`
	Skipped   *junitSkipped `xml:"skipped"`
	SystemOut string        `xml:"system-out,omitempty"`
}

type junitProblem struct {
	Message string `xml:"message,attr"`
	Type    string `xml:"type,attr"`
	Text    string `xml:",chardata"`
}

type junitSkipped struct {
	Message string `xml:"message,attr"`
}

// WriteJUnit writes r as JUnit XML to w.
func WriteJUnit(w io.Writer, r Run) error {
	doc := junitSuites{
		Name: r.Title(), Time: seconds(r.ElapsedMs), Timestamp: timestamp(r.StartedAt),
		Suites: []junitSuite{},
	}
	multi := r.Iterations > 1
	seen := map[string]int{}
	var suite *junitSuite
	var suiteMs []int64
	iteration := 0
	for _, res := range r.Results {
		if suite == nil || res.Iteration != iteration {
			iteration = res.Iteration
			name := r.Title()
			if multi {
				name = fmt.Sprintf("%s, iteration %d", r.Title(), res.Iteration)
			}
			props := []junitProperty{{Name: "iteration", Value: strconv.Itoa(res.Iteration)}}
			if r.DataFile != "" {
				props = append(props, junitProperty{Name: "dataFile", Value: r.DataFile}, junitProperty{Name: "dataRow", Value: strconv.Itoa(res.Iteration)})
			}
			doc.Suites = append(doc.Suites, junitSuite{Name: name, ID: len(doc.Suites), Timestamp: timestamp(res.StartedAt), Properties: props})
			suiteMs = append(suiteMs, 0)
			suite = &doc.Suites[len(doc.Suites)-1]
		}
		suiteMs[len(suiteMs)-1] += max(0, res.DurationMs)
		c := junitCase{Classname: classname(r, res), Time: seconds(res.DurationMs)}
		c.Name = res.Name
		if multi {
			c.Name = fmt.Sprintf("%s (iteration %d)", res.Name, res.Iteration)
		}
		key := c.Classname + "\x00" + c.Name
		seen[key]++
		if n := seen[key]; n > 1 {
			c.Name = fmt.Sprintf("%s (%d)", c.Name, n)
		}
		suite.Tests++
		switch res.Outcome {
		case OutcomeFailed:
			suite.Failures++
			c.Failure = &junitProblem{Message: res.Why(), Type: failureType(res), Text: failureText(res)}
		case OutcomeError:
			suite.Errors++
			c.Error = &junitProblem{Message: res.Why(), Type: errorType(res), Text: errorText(res)}
		case OutcomeSkipped:
			suite.Skipped++
			c.Skipped = &junitSkipped{Message: res.Why()}
		}
		if res.Outcome != OutcomeSkipped {
			c.SystemOut = systemOut(res)
		}
		suite.Cases = append(suite.Cases, c)
		if suite.Timestamp == "" {
			suite.Timestamp = timestamp(res.StartedAt)
		}
	}
	for i := range doc.Suites {
		s := &doc.Suites[i]
		s.Time = seconds(suiteMs[i])
		doc.Tests += s.Tests
		doc.Failures += s.Failures
		doc.Errors += s.Errors
		doc.Skipped += s.Skipped
	}
	if _, err := io.WriteString(w, xml.Header); err != nil {
		return err
	}
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\n")
	return err
}

func classname(r Run, res Result) string {
	parts := append([]string{r.Title()}, res.Path...)
	return strings.Join(parts, ".")
}

// seconds is ms as seconds with three decimals.
func seconds(ms int64) string {
	if ms < 0 {
		ms = 0
	}
	return fmt.Sprintf("%d.%03d", ms/1000, ms%1000)
}

// timestamp is Unix milliseconds as an ISO 8601 date and time in UTC
// without a zone; "" for 0.
func timestamp(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05")
}

func failureType(res Result) string {
	for _, t := range res.FailedTests() {
		if t.Outcome == "invalid" {
			return "invalid assertion"
		}
	}
	if len(res.Tests) == 0 {
		return "status"
	}
	return "assertion"
}

// failureText is every failed assertion's message, a line each.
func failureText(res Result) string {
	var lines []string
	for _, t := range res.FailedTests() {
		lines = append(lines, t.Message)
	}
	if len(lines) == 0 {
		return res.Why()
	}
	return strings.Join(lines, "\n")
}

func errorType(res Result) string {
	if res.Error != nil && res.Error.Code != "" {
		return res.Error.Code
	}
	if res.Error != nil && res.Error.Kind != "" {
		return res.Error.Kind
	}
	return "error"
}

func errorText(res Result) string {
	if res.Error == nil {
		return res.Why()
	}
	if res.Error.Hint != "" {
		return res.Error.Message + "\n" + res.Error.Hint
	}
	return res.Error.Message
}

// systemOut is the request's line and each assertion's outcome.
func systemOut(res Result) string {
	var b strings.Builder
	head := strings.TrimSpace(res.Method + " " + res.Name)
	b.WriteString(head)
	if res.Status != "" {
		b.WriteString(": " + res.Status)
	} else if res.StatusCode != 0 {
		b.WriteString(": " + strconv.Itoa(res.StatusCode))
	}
	fmt.Fprintf(&b, ", %d ms", res.DurationMs)
	if res.Kind == "sse" {
		fmt.Fprintf(&b, ", %d events", res.Events)
	}
	for _, t := range res.Tests {
		fmt.Fprintf(&b, "\n%s: %s", strings.ToUpper(t.Outcome), t.Message)
	}
	return b.String()
}
