package incremental

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

const multipartBody = "\r\n---\r\nContent-Type: application/json; charset=utf-8\r\n\r\n" +
	`{"data":{"a":1},"hasNext":true}` +
	"\r\n---\r\nContent-Type: application/json; charset=utf-8\r\n\r\n" +
	`{"incremental":[{"data":{"b":2},"path":[]}],"hasNext":false}` +
	"\r\n-----\r\n"

func feedAll(p *multipartParser, body string, step int) []string {
	var out []string
	for i := 0; i < len(body); i += step {
		for _, b := range p.feed([]byte(body[i:min(i+step, len(body))])) {
			out = append(out, string(b))
		}
	}
	for _, b := range p.close() {
		out = append(out, string(b))
	}
	return out
}

func TestMultipartParts(t *testing.T) {
	want := []string{`{"data":{"a":1},"hasNext":true}`, `{"incremental":[{"data":{"b":2},"path":[]}],"hasNext":false}`}
	// Whatever the chunks, the same parts come out.
	for _, step := range []int{1, 2, 3, 7, 64, len(multipartBody)} {
		if got := feedAll(newMultipartParser("-"), multipartBody, step); !reflect.DeepEqual(got, want) {
			t.Errorf("step %d: %q", step, got)
		}
	}
	// A body starting with the delimiter, LF line ends, no headers, a
	// preamble, another boundary and no close delimiter.
	lf := "preamble\n--abc\n\n{\"x\":1}\n--abc\n{\"y\":2}\n--abc\ncontent-type: application/json\n\n{\"z\":3}"
	if got := feedAll(newMultipartParser("abc"), lf, 5); !reflect.DeepEqual(got, []string{`{"x":1}`, `{"y":2}`, `{"z":3}`}) {
		t.Errorf("LF body: %q", got)
	}
	// Empty parts are skipped, the epilogue ignored.
	if got := feedAll(newMultipartParser("-"), "---\r\n\r\n\r\n---\r\n\r\n{}\r\n-----\r\nignored {\"a\":1}", 4); !reflect.DeepEqual(got, []string{`{}`}) {
		t.Errorf("empty parts: %q", got)
	}
}

// A part is out as soon as the delimiter after it arrives, before the
// next part's bytes.
func TestMultipartPartBeforeNext(t *testing.T) {
	p := newMultipartParser("-")
	if got := p.feed([]byte("\r\n---\r\nContent-Type: application/json\r\n\r\n{\"a\":1}")); len(got) != 0 {
		t.Fatalf("part before its delimiter: %q", got)
	}
	if got := p.feed([]byte("\r\n---")); len(got) != 1 || string(got[0]) != `{"a":1}` {
		t.Fatalf("part not out after its delimiter: %q", got)
	}
}

func TestDetect(t *testing.T) {
	for ct, want := range map[string][2]string{
		`multipart/mixed; boundary="-"; deferSpec=20220824`: {FormatMultipart, "-"},
		`multipart/mixed;boundary=graphql`:                  {FormatMultipart, "graphql"},
		`Multipart/Mixed`:                                   {FormatMultipart, "-"},
		`text/event-stream; charset=utf-8`:                  {FormatEventStream, ""},
		`application/json`:                                  {"", ""},
		``:                                                  {"", ""},
	} {
		f, b := Detect(ct)
		if f != want[0] || b != want[1] {
			t.Errorf("Detect(%q) = %q, %q", ct, f, b)
		}
	}
	if NewCollector("application/json", time.Now(), nil) != nil {
		t.Error("a collector for JSON")
	}
}

func TestCollectorMultipart(t *testing.T) {
	start := time.Unix(100, 0)
	clock := start
	calls := 0
	c := NewCollector(`multipart/mixed; boundary="-"`, start, func() { calls++ })
	c.now = func() time.Time { return clock }
	half := strings.Index(multipartBody, `{"incremental"`)
	clock = start.Add(10 * time.Millisecond)
	if n, err := c.Write([]byte(multipartBody[:half])); n != half || err != nil {
		t.Fatalf("Write = %d, %v", n, err)
	}
	s := c.Snapshot()
	if s.Count != 1 || s.Completed || !s.HasNext || calls != 1 {
		t.Fatalf("after the first part: %+v, %d calls", s, calls)
	}
	if s.Merged != "{\n  \"data\": {\n    \"a\": 1\n  }\n}" {
		t.Errorf("merged so far %q", s.Merged)
	}
	clock = start.Add(250 * time.Millisecond)
	c.Write([]byte(multipartBody[half:]))
	c.Close()
	s = c.Snapshot()
	if s.Count != 2 || !s.Completed || s.HasNext || calls != 2 || s.Format != FormatMultipart {
		t.Fatalf("at the end: %+v, %d calls", s, calls)
	}
	if s.Parts[0].At != 10*time.Millisecond || s.Parts[1].At != 250*time.Millisecond {
		t.Errorf("times %v %v", s.Parts[0].At, s.Parts[1].At)
	}
	if s.Parts[0].Text != "{\n  \"data\": {\n    \"a\": 1\n  },\n  \"hasNext\": true\n}" || *s.Parts[1].HasNext {
		t.Errorf("parts %+v", s.Parts)
	}
	if compactJSON(t, s.Merged) != `{"data":{"a":1,"b":2}}` {
		t.Errorf("merged %s", s.Merged)
	}
}

func TestCollectorEventStream(t *testing.T) {
	c := NewCollector("text/event-stream", time.Now(), nil)
	c.Write([]byte("event: next\ndata: {\"data\":{\"a\":[1]},\"hasNext\":true}\n\n: keep-alive\n\n"))
	c.Write([]byte("data: {\"incremental\":[{\"items\":[2],\"path\":[\"a\",1]}],\"hasNext\":true}\n\n"))
	c.Write([]byte("event: next\ndata: not json\n\nevent: complete\ndata:\n\nevent: next\ndata: {\"x\":1}\n\n"))
	c.Close()
	s := c.Snapshot()
	if s.Count != 3 || !s.Completed || s.Format != FormatEventStream {
		t.Fatalf("snapshot %+v", s)
	}
	if s.Parts[2].Problem == "" || s.Parts[2].Text != "not json" {
		t.Errorf("bad part %+v", s.Parts[2])
	}
	if compactJSON(t, s.Merged) != `{"data":{"a":[1,2]}}` {
		t.Errorf("merged %s", s.Merged)
	}
}

func TestCollectorLimits(t *testing.T) {
	c := NewCollector("multipart/mixed", time.Now(), nil)
	big := `{"data":{"s":"` + strings.Repeat("é", MaxPartText) + `"},"hasNext":true}`
	c.Write([]byte("---\r\n\r\n" + big + "\r\n---"))
	for range MaxParts + 5 {
		c.Write([]byte("\r\n\r\n{\"hasNext\":true}\r\n---"))
	}
	s := c.Snapshot()
	if s.Count != MaxParts+6 || len(s.Parts) != MaxParts {
		t.Fatalf("count %d, listed %d", s.Count, len(s.Parts))
	}
	if p := s.Parts[0]; !p.Clipped || len(p.Text) > MaxPartText || !strings.HasPrefix(p.Text, "{") || p.Size != len(big) {
		t.Errorf("first part clipped %v, %d bytes", p.Clipped, len(p.Text))
	}
	total := 0
	for _, p := range s.Parts {
		total += len(p.Text)
	}
	if total > MaxPartsText {
		t.Errorf("%d bytes of text", total)
	}
}
