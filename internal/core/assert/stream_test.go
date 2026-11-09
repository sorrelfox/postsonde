package assert

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func msgs(bodies ...string) []Message {
	out := make([]Message, len(bodies))
	for i, b := range bodies {
		out[i] = Message{Body: []byte(b), Time: time.Duration(i+1) * 10 * time.Millisecond}
	}
	return out
}

func TestEvaluateStream(t *testing.T) {
	three := Stream{
		Response: Response{Status: 0, Header: http.Header{"Grpc-Status": {"0"}}, Time: 120 * time.Millisecond},
		Messages: msgs(`{"n":1,"ok":true}`, `{"n":2,"ok":true}`, `{"n":3,"ok":false,"done":true}`),
	}
	empty := Stream{}
	cases := []struct {
		name    string
		s       Stream
		a       Assertion
		outcome Outcome
		actual  string
		message string
		code    string
	}{
		{"count equals", three, Assertion{Target: TargetMessageCount, Op: OpEquals, Expected: "3"}, Passed, "3", "Message count equals 3: it was 3.", ""},
		{"count gte fails", three, Assertion{Target: TargetMessageCount, Op: OpGreaterOrEqual, Expected: "5"}, Failed, "3", "Expected message count to be at least 5, but it was 3.", ""},
		{"count of none", empty, Assertion{Target: TargetMessageCount, Op: OpEquals, Expected: "0"}, Passed, "0", "Message count equals 0: it was 0.", ""},
		{"whole stream header", three, Assertion{Target: TargetHeader, Expr: "grpc-status", Op: OpEquals, Expected: "0"}, Passed, `"0"`, `Header grpc-status equals 0: it was "0".`, ""},

		{"any passes", three, Assertion{Messages: MessagesAny, Target: TargetJSONPath, Expr: "$.n", Op: OpEquals, Expected: "2"}, Passed, "2", "Message 2's JSONPath $.n equals 2: it was 2.", ""},
		{"any fails", three, Assertion{Messages: MessagesAny, Target: TargetJSONPath, Expr: "$.n", Op: OpEquals, Expected: "9"}, Failed, "", "Expected some message's JSONPath $.n to equal 9, but none of the 3 messages did.", ""},
		{"any of none", empty, Assertion{Messages: MessagesAny, Target: TargetBody, Op: OpContains, Expected: "x"}, Failed, "", `Expected some message's body to contain "x", but there were no messages.`, ""},
		{"any body contains", three, Assertion{Messages: MessagesAny, Target: TargetBody, Op: OpContains, Expected: "done"}, Passed, `"{\"n\":3,\"ok\":false,\"done\":true}"`, `Message 3's body contains "done": it was "{\"n\":3,\"ok\":false,\"done\":true}".`, ""},

		{"every passes", three, Assertion{Messages: MessagesEvery, Target: TargetJSONPath, Expr: "$.n", Op: OpIsType, Expected: "number"}, Passed, "", "Every message's JSONPath $.n is of type number (3 messages).", ""},
		{"every fails", three, Assertion{Messages: MessagesEvery, Target: TargetJSONPath, Expr: "$.ok", Op: OpEquals, Expected: "true"}, Failed, "false", "Expected every message's JSONPath $.ok to equal true, but in message 3 it was false.", ""},
		{"every missing", three, Assertion{Messages: MessagesEvery, Target: TargetJSONPath, Expr: "$.done", Op: OpExists}, Failed, "", "Expected every message's JSONPath $.done to exist, but in message 1 it does not exist.", ""},
		{"every of none fails", empty, Assertion{Messages: MessagesEvery, Target: TargetJSONPath, Expr: "$.n", Op: OpExists}, Failed, "", "Expected every message's JSONPath $.n to exist, but there were no messages.", ""},

		{"none passes", three, Assertion{Messages: MessagesNone, Target: TargetJSONPath, Expr: "$.error", Op: OpExists}, Passed, "", "No message's JSONPath $.error exists (3 messages).", ""},
		{"none of none", empty, Assertion{Messages: MessagesNone, Target: TargetJSONPath, Expr: "$.error", Op: OpExists}, Passed, "", "No message's JSONPath $.error exists: there were no messages.", ""},
		{"none fails", three, Assertion{Messages: MessagesNone, Target: TargetJSONPath, Expr: "$.ok", Op: OpEquals, Expected: "false"}, Failed, "false", "Expected no message's JSONPath $.ok to equal false, but in message 3 it was false.", ""},

		{"first", three, Assertion{Messages: MessagesFirst, Target: TargetJSONPath, Expr: "$.n", Op: OpEquals, Expected: "1"}, Passed, "1", "The first message's JSONPath $.n equals 1: it was 1.", ""},
		{"last", three, Assertion{Messages: MessagesLast, Target: TargetJSONPath, Expr: "$.done", Op: OpEquals, Expected: "true"}, Passed, "true", "The last message's JSONPath $.done equals true: it was true.", ""},
		{"last fails", three, Assertion{Messages: MessagesLast, Target: TargetJSONPath, Expr: "$.n", Op: OpEquals, Expected: "1"}, Failed, "3", "Expected the last message's JSONPath $.n to equal 1, but it was 3.", ""},
		{"last of none", empty, Assertion{Messages: MessagesLast, Target: TargetBody, Op: OpExists}, Failed, "", "Expected the last message's body to exist, but there were no messages.", ""},
		{"nth", three, Assertion{Messages: MessagesNth, Index: 2, Target: TargetJSONPath, Expr: "$.n", Op: OpEquals, Expected: "2"}, Passed, "2", "Message 2's JSONPath $.n equals 2: it was 2.", ""},
		{"nth beyond", three, Assertion{Messages: MessagesNth, Index: 5, Target: TargetJSONPath, Expr: "$.n", Op: OpExists}, Failed, "", "Expected message 5's JSONPath $.n to exist, but there were only 3 messages.", ""},
		{"nth time", three, Assertion{Messages: MessagesNth, Index: 2, Target: TargetTime, Op: OpLess, Expected: "1s"}, Passed, "20 ms", `Message 2's response time is less than "1s": it was 20 ms.`, ""},
		{"nth size", three, Assertion{Messages: MessagesFirst, Target: TargetSize, Op: OpEquals, Expected: "17"}, Passed, "17 bytes", "The first message's body size equals 17 bytes: it was 17 bytes.", ""},

		{"not json", Stream{Messages: msgs("plain")}, Assertion{Messages: MessagesEvery, Target: TargetJSONPath, Expr: "$.a", Op: OpExists}, Failed, "", "Expected every message's JSONPath $.a to exist, but in message 1 the body is not valid JSON.", ""},
		{"bad index", three, Assertion{Messages: MessagesNth, Target: TargetBody, Op: OpExists}, Invalid, "", "The message number 0 is not valid: messages are counted from 1.", CodeInvalidIndex},
		{"bad scope", three, Assertion{Messages: "most", Target: TargetBody, Op: OpExists}, Invalid, "", `The message scope "most" is not known.`, CodeUnknownScope},
		{"bad regex in scope", three, Assertion{Messages: MessagesAny, Target: TargetBody, Op: OpMatches, Expected: "("}, Invalid, "", "The regular expression \"(\" is not valid: missing closing ): `(`.", CodeInvalidRegex},
		{"count with scope", three, Assertion{Messages: MessagesAny, Target: TargetMessageCount, Op: OpEquals, Expected: "1"}, Invalid, "", "The message count is a value of the whole stream: check it without a message scope.", CodeUnknownTarget},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EvaluateStream(context.Background(), tc.s, []Assertion{tc.a}, Options{})[0]
			if got.Outcome != tc.outcome || got.Actual != tc.actual || got.Message != tc.message || got.Code != tc.code {
				t.Errorf("got %s %q %q %q, want %s %q %q %q", got.Outcome, got.Actual, got.Message, got.Code, tc.outcome, tc.actual, tc.message, tc.code)
			}
		})
	}
}

func TestEvaluateStreamMessageHeaders(t *testing.T) {
	s := Stream{Messages: []Message{
		{Body: []byte("hello"), Header: http.Header{"Event": {"greeting"}}},
		{Body: []byte("bye"), Header: http.Header{"Event": {"farewell"}, "Id": {"7"}}},
	}}
	got := EvaluateStream(context.Background(), s, []Assertion{
		{Messages: MessagesAny, Target: TargetHeader, Expr: "event", Op: OpEquals, Expected: "farewell"},
		{Messages: MessagesLast, Target: TargetHeader, Expr: "id", Op: OpEquals, Expected: "7"},
		{Messages: MessagesFirst, Target: TargetHeader, Expr: "id", Op: OpNotExists},
		{Messages: MessagesEvery, Target: TargetRegex, Expr: "^(\\w+)$", Op: OpExists},
	}, Options{})
	for i, r := range got {
		if r.Outcome != Passed {
			t.Errorf("%d: %s %s", i, r.Outcome, r.Message)
		}
	}
}

func TestEvaluateStreamTruncated(t *testing.T) {
	// 10 messages received, the first 2 kept and the last.
	last := Message{Body: []byte(`{"n":10}`)}
	s := Stream{Messages: msgs(`{"n":1}`, `{"n":2}`), Total: 10, Last: &last}
	got := EvaluateStream(context.Background(), s, []Assertion{
		{Target: TargetMessageCount, Op: OpEquals, Expected: "10"},
		{Messages: MessagesLast, Target: TargetJSONPath, Expr: "$.n", Op: OpEquals, Expected: "10"},
		{Messages: MessagesNth, Index: 10, Target: TargetJSONPath, Expr: "$.n", Op: OpEquals, Expected: "10"},
		{Messages: MessagesNth, Index: 5, Target: TargetJSONPath, Expr: "$.n", Op: OpExists},
		{Messages: MessagesAny, Target: TargetJSONPath, Expr: "$.n", Op: OpEquals, Expected: "10"},
		{Messages: MessagesEvery, Target: TargetJSONPath, Expr: "$.n", Op: OpExists},
	}, Options{})
	want := []Outcome{Passed, Passed, Passed, Invalid, Passed, Passed}
	for i, r := range got {
		if r.Outcome != want[i] {
			t.Errorf("%d: %s %s, want %s", i, r.Outcome, r.Message, want[i])
		}
	}
	if got[1].Message != "The last message's JSONPath $.n equals 10: it was 10." {
		t.Errorf("last: %q", got[1].Message)
	}
	if got[3].Code != CodeMessageNotKept {
		t.Errorf("not kept: %q", got[3].Code)
	}
	if got[5].Message != "Every message's JSONPath $.n exists (10 messages). Only the first 2 of the 10 messages were kept for checks." {
		t.Errorf("every: %q", got[5].Message)
	}
}

func TestEvaluateStreamSchema(t *testing.T) {
	s := Stream{Messages: msgs(`{"id":1}`, `{"id":"x"}`)}
	schema := `{"type":"object","properties":{"id":{"type":"integer"}}}`
	got := EvaluateStream(context.Background(), s, []Assertion{
		{Messages: MessagesEvery, Target: TargetBody, Op: OpMatchesSchema, Expected: schema},
		{Messages: MessagesFirst, Target: TargetBody, Op: OpMatchesSchema, Expected: schema},
	}, Options{})
	if got[0].Outcome != Failed || len(got[0].SchemaErrors) != 1 || got[0].Message != "Expected every message's body to match the inline JSON schema, but in message 2 $.id must be integer, not string." {
		t.Errorf("every: %s %q %v", got[0].Outcome, got[0].Message, got[0].SchemaErrors)
	}
	if got[1].Outcome != Passed || got[1].Message != "The first message's body matches the inline JSON schema." {
		t.Errorf("first: %s %q", got[1].Outcome, got[1].Message)
	}
}

func TestMessageAssertionsNeedAStream(t *testing.T) {
	got := Evaluate(context.Background(), Response{Status: 200}, []Assertion{
		{Target: TargetMessageCount, Op: OpEquals, Expected: "1"},
		{Messages: MessagesAny, Target: TargetBody, Op: OpExists},
	})
	for i, r := range got {
		if r.Outcome != Invalid || r.Code != CodeNotStream {
			t.Errorf("%d: %s %s", i, r.Outcome, r.Code)
		}
	}
}
