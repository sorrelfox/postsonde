package incremental

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// compactJSON returns src without whitespace, for comparisons.
func compactJSON(t *testing.T, src string) string {
	t.Helper()
	var b bytes.Buffer
	if err := json.Compact(&b, []byte(src)); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, src)
	}
	return b.String()
}

// merge applies the payloads in order and returns the merged result and
// every payload's entries.
func merge(t *testing.T, payloads ...string) (string, [][]Entry, *Result) {
	t.Helper()
	var r Result
	var all [][]Entry
	for _, p := range payloads {
		e, err := r.Apply([]byte(p))
		if err != nil {
			t.Fatalf("Apply(%s): %v", p, err)
		}
		all = append(all, e)
	}
	return compactJSON(t, r.JSON()), all, &r
}

func TestMergeOlderFormat(t *testing.T) {
	got, entries, r := merge(t,
		`{"data":{"user":{"id":"1","friends":[{"name":"Ada"}]}},"hasNext":true}`,
		`{"incremental":[{"data":{"email":"u@example.com","profile":{"bio":"hi"}},"path":["user"],"label":"Details"}],"hasNext":true}`,
		`{"incremental":[{"items":[{"name":"Bo"},{"name":"Cy"}],"path":["user","friends",1]}],"hasNext":true}`,
		`{"incremental":[{"data":{"age":3},"path":["user","friends",0]}],"hasNext":false}`,
	)
	want := `{"data":{"user":{"id":"1","friends":[{"name":"Ada","age":3},{"name":"Bo"},{"name":"Cy"}],"email":"u@example.com","profile":{"bio":"hi"}}}}`
	if got != want {
		t.Errorf("merged\n got %s\nwant %s", got, want)
	}
	wantEntries := [][]Entry{
		{{Kind: KindInitial}},
		{{Kind: KindDefer, Path: "user", Label: "Details"}},
		{{Kind: KindStream, Path: "user.friends[1]", Items: 2}},
		{{Kind: KindDefer, Path: "user.friends[0]"}},
	}
	if !reflect.DeepEqual(entries, wantEntries) {
		t.Errorf("entries\n got %+v\nwant %+v", entries, wantEntries)
	}
	if r.HasNext() || !r.Completed() || r.Payloads() != 4 {
		t.Errorf("hasNext %v, completed %v, payloads %d", r.HasNext(), r.Completed(), r.Payloads())
	}
}

// The first experiments sent each subsequent payload on its own, with
// path and data or items at the top level.
func TestMergeEarliestFormat(t *testing.T) {
	got, entries, _ := merge(t,
		`{"data":{"list":[1]},"hasNext":true}`,
		`{"data":{"extra":true},"path":[],"label":"L","hasNext":true}`,
		`{"items":[2,3],"path":["list",1],"hasNext":true}`,
		`{"data":4,"path":["list",3],"hasNext":false}`,
	)
	if want := `{"data":{"list":[1,2,3,4],"extra":true}}`; got != want {
		t.Errorf("merged\n got %s\nwant %s", got, want)
	}
	if e := entries[1][0]; e.Kind != KindDefer || e.Label != "L" || e.Path != "" {
		t.Errorf("defer entry %+v", e)
	}
	if e := entries[2][0]; e.Kind != KindStream || e.Items != 2 {
		t.Errorf("stream entry %+v", e)
	}
}

func TestMergeNewerFormat(t *testing.T) {
	got, entries, r := merge(t,
		`{"data":{"hero":{"name":"Luke","friends":[{"id":"1"}]}},"pending":[{"id":"0","path":["hero"],"label":"D"},{"id":"1","path":["hero","friends"]}],"hasNext":true}`,
		`{"incremental":[{"id":"0","data":{"homeWorld":{"name":"Tatooine"}}},{"id":"0","subPath":["homeWorld"],"data":{"climate":"arid"}}],"hasNext":true}`,
		`{"incremental":[{"id":"1","items":[{"id":"2"},{"id":"3"}]}],"completed":[{"id":"0"}],"hasNext":true}`,
		`{"completed":[{"id":"1","errors":[{"message":"stream failed","path":["hero","friends",3]}]}],"hasNext":false}`,
	)
	want := `{"data":{"hero":{"name":"Luke","friends":[{"id":"1"},{"id":"2"},{"id":"3"}],"homeWorld":{"name":"Tatooine","climate":"arid"}}},"errors":[{"message":"stream failed","path":["hero","friends",3]}]}`
	if got != want {
		t.Errorf("merged\n got %s\nwant %s", got, want)
	}
	wantEntries := [][]Entry{
		{{Kind: KindInitial}, {Kind: KindPending, ID: "0", Path: "hero", Label: "D"}, {Kind: KindPending, ID: "1", Path: "hero.friends"}},
		{{Kind: KindDefer, ID: "0", Path: "hero", Label: "D"}, {Kind: KindDefer, ID: "0", Path: "hero.homeWorld", Label: "D"}},
		{{Kind: KindStream, ID: "1", Path: "hero.friends", Items: 2}, {Kind: KindCompleted, ID: "0", Path: "hero", Label: "D"}},
		{{Kind: KindCompleted, ID: "1", Path: "hero.friends", Errors: 1}},
	}
	if !reflect.DeepEqual(entries, wantEntries) {
		t.Errorf("entries\n got %+v\nwant %+v", entries, wantEntries)
	}
	if r.Pending() != 0 || r.Errors() != 1 || !r.Completed() {
		t.Errorf("pending %d, errors %d, completed %v", r.Pending(), r.Errors(), r.Completed())
	}
}

func TestMergeErrorsAndExtensions(t *testing.T) {
	got, entries, _ := merge(t,
		`{"data":{"a":{"b":null}},"errors":[{"message":"first"}],"extensions":{"trace":1,"cost":5},"hasNext":true}`,
		`{"incremental":[{"data":{"x":1},"path":["a"],"errors":[{"message":"in defer"}],"extensions":{"cost":7}}],"hasNext":true}`,
		`{"incremental":[{"data":{"y":1},"path":["a","b"]}],"hasNext":false}`,
	)
	want := `{"data":{"a":{"b":null,"x":1}},"errors":[{"message":"first"},{"message":"in defer"}],"extensions":{"trace":1,"cost":7}}`
	if got != want {
		t.Errorf("merged\n got %s\nwant %s", got, want)
	}
	if entries[0][0].Errors != 1 || entries[1][0].Errors != 1 {
		t.Errorf("error counts %+v %+v", entries[0], entries[1])
	}
	// Data under a null (an error nulled it) is not merged.
	if p := entries[2][0].Problem; !strings.Contains(p, "a.b") {
		t.Errorf("problem %q", p)
	}
}

func TestMergeKeepsLiteralsAndOrder(t *testing.T) {
	var r Result
	if _, err := r.Apply([]byte("{\"data\":{\"z\":12345678901234567890123,\"a\":\"caf\\u00e9\",\"m\":1.50e+3},\"hasNext\":true}")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Apply([]byte("{\"incremental\":[{\"data\":{\"b\":\"\\ud83d\\ude00\"},\"path\":[]}],\"hasNext\":false}")); err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"data\": {\n    \"z\": 12345678901234567890123,\n    \"a\": \"caf\\u00e9\",\n    \"m\": 1.50e+3,\n    \"b\": \"\\ud83d\\ude00\"\n  }\n}"
	if got := r.JSON(); got != want {
		t.Errorf("JSON\n got %s\nwant %s", got, want)
	}
}

func TestMergeProblems(t *testing.T) {
	var r Result
	if _, err := r.Apply([]byte(`[1]`)); err == nil {
		t.Error("an array payload was accepted")
	}
	if _, err := r.Apply([]byte(`{"data":`)); err == nil {
		t.Error("broken JSON was accepted")
	}
	if r.JSON() != "" {
		t.Errorf("JSON before a payload: %q", r.JSON())
	}
	_, entries, _ := merge(t,
		`{"data":{"a":{}},"hasNext":true}`,
		`{"incremental":[{"id":"9","data":{"x":1}}],"completed":[{"id":"8"}],"hasNext":true}`,
		`{"incremental":[{"items":[1],"path":["nope",0]},{"path":["a"]}],"hasNext":false}`,
	)
	for _, e := range []Entry{entries[1][0], entries[1][1], entries[2][0], entries[2][1]} {
		if e.Problem == "" {
			t.Errorf("no problem for %+v", e)
		}
	}
}

// The initial payload may carry errors only (data null), and later
// payloads then have nothing to merge into.
func TestMergeNullData(t *testing.T) {
	got, entries, _ := merge(t,
		`{"data":null,"errors":[{"message":"boom"}],"hasNext":true}`,
		`{"incremental":[{"data":{"x":1},"path":["user"]}],"hasNext":false}`,
	)
	if want := `{"data":null,"errors":[{"message":"boom"}]}`; got != want {
		t.Errorf("merged %s", got)
	}
	if entries[1][0].Problem == "" {
		t.Errorf("no problem: %+v", entries[1][0])
	}
}
