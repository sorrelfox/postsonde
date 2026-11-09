package sse

import (
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

type parseCase struct {
	name      string
	in        string
	want      []ev
	lastID    string
	retry     time.Duration
	discarded bool
}

// specCases are modelled on the examples of the HTML standard's
// server-sent events section.
var specCases = []parseCase{
	{
		name: "stock ticker: multi-line data",
		in:   "data: YHOO\ndata: +2\ndata: 10\n\n",
		want: []ev{{Type: "message", Data: "YHOO\n+2\n10"}},
	},
	{
		name:      "comment, id, empty id, incomplete trailing event",
		in:        ": test stream\n\ndata: first event\nid: 1\n\ndata:second event\nid\n\ndata:  third event\n",
		want:      []ev{{Type: "message", Data: "first event", ID: "1"}, {Type: "message", Data: "second event", ID: ""}},
		lastID:    "",
		discarded: true,
	},
	{
		name:      "empty data lines",
		in:        "data\n\ndata\ndata\n\ndata:\n",
		want:      []ev{{Type: "message", Data: ""}, {Type: "message", Data: "\n"}},
		discarded: true,
	},
	{
		name: "space after colon is optional",
		in:   "data:test\n\ndata: test\n\n",
		want: []ev{{Type: "message", Data: "test"}, {Type: "message", Data: "test"}},
	},
	{
		name: "event types",
		in:   "event: add\ndata: 73857293\n\nevent: remove\ndata: 2153\n\nevent: add\ndata: 113411\n\n",
		want: []ev{{Type: "add", Data: "73857293"}, {Type: "remove", Data: "2153"}, {Type: "add", Data: "113411"}},
	},
	{
		name: "messages with two lines",
		in:   "data: This is the first message.\n\ndata: This is the second message, it\ndata: has two lines.\n\ndata: This is the third message.\n\n",
		want: []ev{
			{Type: "message", Data: "This is the first message."},
			{Type: "message", Data: "This is the second message, it\nhas two lines."},
			{Type: "message", Data: "This is the third message."},
		},
	},
	{
		name:   "id persists across events",
		in:     "id: 7\ndata: a\n\ndata: b\n\nid: 8\n\ndata: c\n\n",
		want:   []ev{{Type: "message", Data: "a", ID: "7"}, {Type: "message", Data: "b", ID: "7"}, {Type: "message", Data: "c", ID: "8"}},
		lastID: "8",
	},
	{
		name:  "retry",
		in:    "retry: 10000\ndata: x\n\ndata: y\n\n",
		want:  []ev{{Type: "message", Data: "x", Retry: dur(10 * time.Second)}, {Type: "message", Data: "y"}},
		retry: 10 * time.Second,
	},
}

var edgeCases = []parseCase{
	{name: "empty stream"},
	{name: "only blank lines", in: "\n\r\n\r\n\n"},
	{
		name: "CR line ends",
		in:   "data: a\rdata: b\r\rdata: c\r\r",
		want: []ev{{Type: "message", Data: "a\nb"}, {Type: "message", Data: "c"}},
	},
	{
		name: "CRLF line ends",
		in:   "data: a\r\ndata: b\r\n\r\n",
		want: []ev{{Type: "message", Data: "a\nb"}},
	},
	{
		name: "mixed line ends: LF CR is two breaks, CR LF is one",
		in:   "data: a\n\rdata: b\r\n\ndata: c\r\r\ndata: d\n\r",
		want: []ev{{Type: "message", Data: "a"}, {Type: "message", Data: "b"}, {Type: "message", Data: "c"}, {Type: "message", Data: "d"}},
	},
	{
		name:      "CR LF CR LF without data",
		in:        "data: a\r\n\r\ndata: b\r\n",
		want:      []ev{{Type: "message", Data: "a"}},
		discarded: true,
	},
	{
		name: "leading BOM stripped",
		in:   "\uFEFFdata: a\n\n",
		want: []ev{{Type: "message", Data: "a"}},
	},
	{
		name: "BOM alone on the first line is a blank line",
		in:   "\uFEFF\ndata: a\n\n",
		want: []ev{{Type: "message", Data: "a"}},
	},
	{
		name:      "second BOM is not stripped: unknown field",
		in:        "\uFEFF\uFEFFdata: a\n\n",
		discarded: false,
	},
	{
		name: "BOM later in the stream is kept in data",
		in:   "data: a\n\n\uFEFFdata: b\n\ndata: \uFEFFc\n\n",
		want: []ev{{Type: "message", Data: "a"}, {Type: "message", Data: "\uFEFFc"}},
	},
	{
		name: "invalid UTF-8 becomes U+FFFD per maximal subpart",
		in:   "data: \xE2\x82A\xFF\xED\xA0\x80\n\n",
		want: []ev{{Type: "message", Data: "\uFFFDA\uFFFD\uFFFD\uFFFD\uFFFD"}},
	},
	{
		name: "valid multi-byte data",
		in:   "data: h\u00e9llo \u20ac \U0001F600\n\n",
		want: []ev{{Type: "message", Data: "h\u00e9llo \u20ac \U0001F600"}},
	},
	{
		name:   "id with NULL is ignored",
		in:     "id: 1\ndata: a\n\nid: 2\x003\ndata: b\n\n",
		want:   []ev{{Type: "message", Data: "a", ID: "1"}, {Type: "message", Data: "b", ID: "1"}},
		lastID: "1",
	},
	{
		name:   "id without data is committed by the blank line",
		in:     "id: 5\n\n",
		lastID: "5",
	},
	{
		name:      "id is not committed without a blank line",
		in:        "id: 5\n\nid: 6\n",
		lastID:    "5",
		discarded: true,
	},
	{
		name: "retry with non-digits is ignored",
		in:   "retry: 1x\ndata: a\n\nretry: -5\ndata: b\n\nretry:  5\ndata: c\n\nretry: 1.5\nretry:\nretry\ndata: d\n\nretry: 99999999999999999999\ndata: e\n\n",
		want: []ev{{Type: "message", Data: "a"}, {Type: "message", Data: "b"}, {Type: "message", Data: "c"}, {Type: "message", Data: "d"}, {Type: "message", Data: "e"}},
	},
	{
		name:  "retry applies without dispatch and in a discarded block",
		in:    "retry: 250\n\nretry: 500\n",
		retry: 500 * time.Millisecond, discarded: true,
	},
	{
		name:  "retry of zero",
		in:    "retry: 0\ndata: a\n\n",
		want:  []ev{{Type: "message", Data: "a", Retry: dur(0)}},
		retry: 0,
	},
	{
		name: "event type resets after empty data dispatch",
		in:   "event: foo\n\ndata: a\n\n",
		want: []ev{{Type: "message", Data: "a"}},
	},
	{
		name: "empty event type is message",
		in:   "event: foo\nevent\ndata: a\n\n",
		want: []ev{{Type: "message", Data: "a"}},
	},
	{
		name: "only one space removed, colons kept in value",
		in:   "data:  two\ndata:a:b: c\n\n",
		want: []ev{{Type: "message", Data: " two\na:b: c"}},
	},
	{
		name: "field names are case-sensitive and exact",
		in:   "Data: a\ndata : b\n data: c\ndata: d\n\n",
		want: []ev{{Type: "message", Data: "d"}},
	},
	{
		name:      "incomplete last line is discarded",
		in:        "data: a\n\ndata: b",
		want:      []ev{{Type: "message", Data: "a"}},
		discarded: true,
	},
	{
		name:      "data without a blank line at the end",
		in:        "data: a\n",
		discarded: true,
	},
	{
		name: "tab is not stripped",
		in:   "data:\tx\n\n",
		want: []ev{{Type: "message", Data: "\tx"}},
	},
}

func TestParse(t *testing.T) {
	for i, tc := range append(append([]parseCase{}, specCases...), edgeCases...) {
		t.Run(tc.name, func(t *testing.T) {
			r := checkChunking(t, Options{}, tc.in, 300, uint64(i))
			if got := stripEvents(r.Events); !reflect.DeepEqual(got, orEmpty(tc.want)) {
				t.Fatalf("events\n got %+v\nwant %+v\n%s", got, tc.want, dump(r))
			}
			if r.LastID != tc.lastID {
				t.Errorf("LastEventID = %q, want %q", r.LastID, tc.lastID)
			}
			if r.Retry != tc.retry {
				t.Errorf("Retry = %v, want %v", r.Retry, tc.retry)
			}
			if r.Discarded != tc.discarded {
				t.Errorf("Close discarded = %v, want %v", r.Discarded, tc.discarded)
			}
			if r.ErrCode != "" {
				t.Errorf("error %s", r.ErrCode)
			}
			if r.Offset != int64(len(tc.in)) {
				t.Errorf("Offset = %d, want %d", r.Offset, len(tc.in))
			}
		})
	}
}

func orEmpty(v []ev) []ev {
	if v == nil {
		return []ev{}
	}
	return v
}

// TestSplitCRLF checks that a CR ending one chunk dispatches at once and
// that an LF starting the next chunk is not a second (blank) line.
func TestSplitCRLF(t *testing.T) {
	p := NewParser(Options{})
	evs, err := p.Feed([]byte("data: a\r\n\r"))
	if err != nil || len(evs) != 1 || evs[0].Data != "a" {
		t.Fatalf("first chunk: %+v, %v; want the event at once", evs, err)
	}
	evs, _ = p.Feed([]byte("\ndata: b\r"))
	if len(evs) != 0 {
		t.Fatalf("LF after split CR made a blank line: %+v", evs)
	}
	evs, _ = p.Feed([]byte("\n"))
	if len(evs) != 0 {
		t.Fatalf("LF after split CR made a blank line: %+v", evs)
	}
	evs, _ = p.Feed([]byte("\r"))
	if len(evs) != 1 || evs[0].Data != "b" {
		t.Fatalf("CR after CRLF should dispatch: %+v", evs)
	}
	evs, _ = p.Feed([]byte("\n\n"))
	if len(evs) != 0 {
		t.Fatalf("got %+v", evs)
	}
	if p.Close() {
		t.Error("nothing should be discarded")
	}
}

func TestPositions(t *testing.T) {
	in := "\uFEFF: hi\r\nid: 1\r\ndata: a\r\n\r\nfoo: bar\ndata\n\n"
	r := checkChunking(t, Options{}, in, 100, 1)
	wantComments := []Comment{{Text: " hi", Pos: Position{Offset: 0, Line: 1}}}
	if !reflect.DeepEqual(r.Comments, wantComments) {
		t.Errorf("comments %+v", r.Comments)
	}
	wantFields := []Field{
		{Name: "id", Value: "1", Pos: Position{Offset: 9, Line: 2}},
		{Name: "data", Value: "a", Pos: Position{Offset: 16, Line: 3}},
		{Name: "foo", Value: "bar", Pos: Position{Offset: 27, Line: 5}, Ignored: IgnoredUnknownField},
		{Name: "data", Value: "", Pos: Position{Offset: 36, Line: 6}},
	}
	if !reflect.DeepEqual(r.Fields, wantFields) {
		t.Errorf("fields\n got %+v\nwant %+v", r.Fields, wantFields)
	}
	wantEvents := []Event{
		{Type: "message", Data: "a", ID: "1", Start: Position{0, 1}, End: Position{25, 4}},
		{Type: "message", Data: "", ID: "1", Start: Position{27, 5}, End: Position{41, 7}},
	}
	if !reflect.DeepEqual(r.Events, wantEvents) {
		t.Errorf("events\n got %+v\nwant %+v", r.Events, wantEvents)
	}
}

func TestIgnoredFieldsReported(t *testing.T) {
	r := runWhole(Options{}, "id: a\x00b\nretry: x\nretry: 12\nwhat\n:c\n\n")
	want := []Field{
		{Name: "id", Value: "a\x00b", Pos: Position{0, 1}, Ignored: IgnoredIDContainsNull},
		{Name: "retry", Value: "x", Pos: Position{8, 2}, Ignored: IgnoredInvalidRetry},
		{Name: "retry", Value: "12", Pos: Position{17, 3}},
		{Name: "what", Value: "", Pos: Position{27, 4}, Ignored: IgnoredUnknownField},
	}
	if !reflect.DeepEqual(r.Fields, want) {
		t.Errorf("fields\n got %+v\nwant %+v", r.Fields, want)
	}
	if len(r.Comments) != 1 || r.Comments[0].Text != "c" {
		t.Errorf("comments %+v", r.Comments)
	}
	if r.Retry != 12*time.Millisecond || !r.HasRetry {
		t.Errorf("retry %v %v", r.Retry, r.HasRetry)
	}
}

func TestNoCallbacks(t *testing.T) {
	p := NewParser(Options{})
	evs, err := p.Feed([]byte(": c\nfoo\nid: 1\nretry: 5\nevent: e\ndata: d\n\n"))
	if err != nil || len(evs) != 1 || evs[0].Type != "e" || evs[0].ID != "1" {
		t.Fatalf("%+v %v", evs, err)
	}
}

func TestLimits(t *testing.T) {
	cases := []struct {
		name string
		opts Options
		in   string
		want []ev
		code string
	}{
		{"line at the limit", Options{MaxLineSize: 8}, "data: ab\n\n", []ev{{Type: "message", Data: "ab"}}, ""},
		{"line over the limit", Options{MaxLineSize: 8}, "data: a\n\ndata: abc\n\n", []ev{{Type: "message", Data: "a"}}, CodeLineTooLong},
		{"unfinished line over the limit", Options{MaxLineSize: 8}, "data: a\n\ndata: abc", []ev{{Type: "message", Data: "a"}}, CodeLineTooLong},
		{"comment over the limit", Options{MaxLineSize: 4}, ":abcd\n", nil, CodeLineTooLong},
		{"BOM counts toward the line", Options{MaxLineSize: 4}, "\uFEFFdata\n", nil, CodeLineTooLong},
		{"no line limit", Options{MaxLineSize: -1}, "data: " + strings.Repeat("x", 1000) + "\n\n", []ev{{Type: "message", Data: strings.Repeat("x", 1000)}}, ""},
		{"event at the limit", Options{MaxEventSize: 4}, "data: a\ndata: b\n\n", []ev{{Type: "message", Data: "a\nb"}}, ""},
		{"event over the limit", Options{MaxEventSize: 4}, "data: a\n\ndata: a\ndata: bc\n\n", []ev{{Type: "message", Data: "a"}}, CodeEventTooBig},
		{"limit resets per event", Options{MaxEventSize: 4}, "data: abc\n\ndata: abc\n\n", []ev{{Type: "message", Data: "abc"}, {Type: "message", Data: "abc"}}, ""},
		{"no event limit", Options{MaxEventSize: -1}, strings.Repeat("data: xx\n", 100) + "\n", []ev{{Type: "message", Data: strings.TrimSuffix(strings.Repeat("xx\n", 100), "\n")}}, ""},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := checkChunking(t, tc.opts, tc.in, 200, uint64(i))
			if got := stripEvents(r.Events); !reflect.DeepEqual(got, orEmpty(tc.want)) {
				t.Errorf("events %+v, want %+v", got, tc.want)
			}
			if r.ErrCode != tc.code {
				t.Errorf("error %q, want %q", r.ErrCode, tc.code)
			}
		})
	}
}

func TestFailedParserIsSticky(t *testing.T) {
	p := NewParser(Options{MaxLineSize: 3})
	evs, err := p.Feed([]byte("a:\n\nabcd\n"))
	if !errors.Is(err, &errs.Error{Kind: errs.Protocol, Code: CodeLineTooLong}) {
		t.Fatalf("err = %v", err)
	}
	if len(evs) != 0 {
		t.Fatalf("events %+v", evs)
	}
	var e *errs.Error
	if errors.As(err, &e) && (e.Details["line"] != "3" || e.Details["offset"] != "4" || e.Details["limit"] != "3") {
		t.Errorf("details %v", e.Details)
	}
	if _, err2 := p.Feed([]byte("\n")); err2 != err {
		t.Errorf("second Feed error %v", err2)
	}
	if p.Err() != err {
		t.Error("Err() not set")
	}
	if p.Close() {
		t.Error("a failed parser discards nothing")
	}
}

func TestFeedAfterClose(t *testing.T) {
	p := NewParser(Options{})
	p.Close()
	if p.Close() {
		t.Error("second Close reported a discard")
	}
	_, err := p.Feed([]byte("data: a\n\n"))
	if !errors.Is(err, &errs.Error{Code: CodeClosed}) {
		t.Fatalf("err = %v", err)
	}
}

func TestEventsIterator(t *testing.T) {
	in := "retry: 3000\n: keep-alive\nevent: tick\ndata: 1\nid: a\n\ndata: 2\n\ndata: 3"
	var got []ev
	for e, err := range Events(iotest.OneByteReader(strings.NewReader(in)), Options{}) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, ev{e.Type, e.Data, e.ID, e.Retry})
	}
	want := []ev{{Type: "tick", Data: "1", ID: "a", Retry: dur(3 * time.Second)}, {Type: "message", Data: "2", ID: "a"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestEventsIteratorStops(t *testing.T) {
	n := 0
	for range Events(strings.NewReader("data: 1\n\ndata: 2\n\n"), Options{}) {
		n++
		break
	}
	if n != 1 {
		t.Fatal(n)
	}
}

func TestEventsIteratorErrors(t *testing.T) {
	boom := errors.New("boom")
	r := io.MultiReader(strings.NewReader("data: 1\n\n"), iotest.ErrReader(boom))
	var events int
	var gotErr error
	for _, err := range Events(r, Options{}) {
		if err != nil {
			gotErr = err
			continue
		}
		events++
	}
	if events != 1 || !errors.Is(gotErr, boom) || !errors.Is(gotErr, &errs.Error{Code: CodeReadFailed}) {
		t.Fatalf("events %d, err %v", events, gotErr)
	}

	gotErr = nil
	for _, err := range Events(strings.NewReader("data: 12345\n\n"), Options{MaxLineSize: 5}) {
		gotErr = err
	}
	if !errors.Is(gotErr, &errs.Error{Code: CodeLineTooLong}) {
		t.Fatalf("err %v", gotErr)
	}
}

// TestChunkingProperty generates streams from line fragments and checks
// that random chunking never changes the result.
func TestChunkingProperty(t *testing.T) {
	fragments := []string{
		"data: x", "data:y", "data", "data: \u20ac", "event: e", "event", "id: 1", "id", "id: \x00",
		"retry: 10", "retry: z", ": c", ":", "foo: bar", "\uFEFF", "\xE2\x82", "\xF0\x9F", "data: \xED\xA0",
		"\n", "\r", "\r\n", "\n", "\r\n", "\n\n",
	}
	for seed := range uint64(300) {
		rng := newRand(seed)
		var sb strings.Builder
		for range rng.IntN(30) {
			sb.WriteString(fragments[rng.IntN(len(fragments))])
		}
		opts := Options{}
		if seed%3 == 0 {
			opts = Options{MaxLineSize: 12, MaxEventSize: 20}
		}
		checkChunking(t, opts, sb.String(), 20, seed)
	}
}

func TestParseRetry(t *testing.T) {
	cases := map[string]time.Duration{"0": 0, "1": time.Millisecond, "007": 7 * time.Millisecond, "9223372036854": 9223372036854 * time.Millisecond}
	for in, want := range cases {
		if got, ok := parseRetry([]byte(in)); !ok || got != want {
			t.Errorf("parseRetry(%q) = %v, %v", in, got, ok)
		}
	}
	for _, in := range []string{"", " 1", "1 ", "+1", "-1", "1e3", "\uff11", "9223372036855", "99999999999999999999999"} {
		if _, ok := parseRetry([]byte(in)); ok {
			t.Errorf("parseRetry(%q) accepted", in)
		}
	}
}

// TestInitialLastEventID: a parser started with the ID a reconnection sent
// (SSE-03) gives it to events until an id field changes it; an empty id
// field clears it; an ID with NULL is not taken.
func TestInitialLastEventID(t *testing.T) {
	t.Parallel()
	p := NewParser(Options{LastEventID: "41"})
	if p.LastEventID() != "41" {
		t.Fatalf("LastEventID = %q", p.LastEventID())
	}
	events, err := p.Feed([]byte("data: a\n\nid: 42\ndata: b\n\nid\ndata: c\n\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[0].ID != "41" || events[1].ID != "42" || events[2].ID != "" {
		t.Errorf("events = %+v", events)
	}
	if p := NewParser(Options{LastEventID: "a\x00b"}); p.LastEventID() != "" {
		t.Errorf("LastEventID with NULL = %q", p.LastEventID())
	}
}
