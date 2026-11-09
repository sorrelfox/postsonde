package protojson

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/desc"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/linker"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/parser"
	"github.com/sorrelfox/postsonde/internal/testutil/testenv"
)

const completeProto = `syntax = "proto2";
package shop;

import "google/protobuf/any.proto";
import "google/protobuf/struct.proto";
import "google/protobuf/timestamp.proto";
import "google/protobuf/wrappers.proto";
import "google/protobuf/empty.proto";

// How the order is paid.
enum Pay {
  PAY_UNSPECIFIED = 0;
  // By card.
  PAY_CARD = 1;
  PAY_CASH = 2 [deprecated = true];
}

message Line {
  optional string sku = 1;
  optional int32 qty = 2;
  optional Pay pay = 3;
}

message Order {
  // The order id.
  optional string order_id = 1;
  optional Pay pay = 2;
  repeated Pay pays = 3;
  repeated Line lines = 4;
  map<string, Line> by_sku = 5;
  map<bool, string> flags = 6;
  map<string, Pay> pay_by_name = 7;
  optional Line main = 8;
  oneof target {
    string email = 9;
    string phone = 10;
  }
  optional bool gift = 11;
  optional google.protobuf.Timestamp created = 12;
  optional google.protobuf.BoolValue paid = 13;
  optional google.protobuf.Struct extra = 14;
  optional google.protobuf.Value any_json = 15;
  optional google.protobuf.Any detail = 16;
  optional google.protobuf.Empty nothing = 17;
  optional google.protobuf.NullValue nil = 18;
  optional int64 total = 19 [deprecated = true];
  optional string custom = 20 [json_name = "myCustom"];
  extensions 100 to 200;
}

extend Order {
  optional string note = 100;
}
`

var (
	completeOnce sync.Once
	completePool *desc.Pool
	completeErr  error
)

func completeSetup(tb testing.TB) (*desc.Pool, *desc.Message) {
	tb.Helper()
	completeOnce.Do(func() {
		res, err := linker.Load(context.Background(), linker.Options{
			Files: []string{"shop.proto"},
			FS:    fstest.MapFS{"shop.proto": {Data: []byte(completeProto)}},
		})
		if err != nil {
			completeErr = err
			return
		}
		for _, d := range res.Diagnostics {
			if d.Severity == parser.SeverityError {
				completeErr = fmt.Errorf("diagnostics: %v", res.Diagnostics)
				return
			}
		}
		completePool = res.Pool
	})
	if completeErr != nil {
		tb.Fatal(completeErr)
	}
	return completePool, completePool.FindMessage("shop.Order")
}

// completeAt completes text at the "|" in it, which is removed.
func completeAt(t *testing.T, text string) Completion {
	t.Helper()
	p, md := completeSetup(t)
	pos := strings.Index(text, "|")
	if pos < 0 {
		t.Fatalf("no cursor in %q", text)
	}
	return Complete(p, md, text[:pos]+text[pos+1:], pos)
}

func labels(c Completion) []string {
	out := make([]string, len(c.Items))
	for i, it := range c.Items {
		out[i] = it.Label
	}
	return out
}

func item(t *testing.T, c Completion, label string) CompletionItem {
	t.Helper()
	for _, it := range c.Items {
		if it.Label == label {
			return it
		}
	}
	t.Fatalf("no item %q in %v", label, labels(c))
	return CompletionItem{}
}

var allOrderFields = []string{"orderId", "pay", "pays", "lines", "bySku", "flags", "payByName", "main", "email", "phone",
	"gift", "created", "paid", "extra", "anyJson", "detail", "nothing", "nil", "total", "myCustom", "[shop.note]"}

func TestCompletePositions(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		text    string
		context CompletionContext
		owner   string
		want    []string // labels, in order; nil to skip
	}{
		{"empty object", `{|}`, CompleteKey, "shop.Order", allOrderFields},
		{"after comma, set fields left out", `{"orderId": "1", "pay": "PAY_CARD", |}`, CompleteKey, "shop.Order", nil},
		{"typed prefix in quotes", `{"pa|`, CompleteKey, "shop.Order", []string{"pay", "pays", "payByName", "paid"}},
		{"bare prefix", `{ gi| }`, CompleteKey, "shop.Order", []string{"gift"}},
		{"proto name when typed", `{"order_|`, CompleteKey, "shop.Order", []string{"order_id"}},
		{"substring after prefix", `{"sku|`, CompleteKey, "shop.Order", []string{"bySku"}},
		{"nested message", `{"main": {|}}`, CompleteKey, "shop.Line", []string{"sku", "qty", "pay"}},
		{"list element message", `{"lines": [{"sku": "a"}, {"q|`, CompleteKey, "shop.Line", []string{"qty"}},
		{"map value message", `{"bySku": {"a": {|}}}`, CompleteKey, "shop.Line", []string{"sku", "qty", "pay"}},
		{"map keys are free", `{"bySku": {|}}`, CompleteNone, "", nil},
		{"bool map keys", `{"flags": {"true": "x", |}}`, CompleteKey, "shop.Order.flags", []string{"false"}},
		{"enum value", `{"pay": |}`, CompleteValue, "shop.Order.pay", []string{"PAY_UNSPECIFIED", "PAY_CARD", "PAY_CASH", "null"}},
		{"enum value in quotes", `{"pay": "PAY_C|"}`, CompleteValue, "shop.Order.pay", []string{"PAY_CARD", "PAY_CASH"}},
		{"enum value bare", `{"pay": pay_ca|}`, CompleteValue, "shop.Order.pay", []string{"PAY_CARD", "PAY_CASH"}},
		{"enum list element", `{"pays": ["PAY_CARD", |]}`, CompleteValue, "shop.Order.pays", []string{"PAY_UNSPECIFIED", "PAY_CARD", "PAY_CASH"}},
		{"enum map value", `{"payByName": {"a": |}}`, CompleteValue, "shop.Order.pay_by_name", []string{"PAY_UNSPECIFIED", "PAY_CARD", "PAY_CASH"}},
		{"enum in nested list", `{"lines": [{"pay": "|"}]}`, CompleteValue, "shop.Line.pay", []string{"PAY_UNSPECIFIED", "PAY_CARD", "PAY_CASH"}},
		{"bool", `{"gift": |}`, CompleteValue, "shop.Order.gift", []string{"true", "false", "null"}},
		{"bool typed", `{"gift": f|}`, CompleteValue, "shop.Order.gift", []string{"false"}},
		{"bool wrapper", `{"paid": |}`, CompleteValue, "shop.Order.paid", []string{"true", "false", "null"}},
		{"null value enum", `{"nil": |}`, CompleteValue, "shop.Order.nil", []string{"null"}},
		{"string value has none", `{"orderId": "|"}`, CompleteNone, "", nil},
		{"number has none", `{"total": 1|}`, CompleteNone, "", nil},
		{"after a value", `{"gift": true|}`, CompleteNone, "", nil},
		{"after a key", `{"gift"| }`, CompleteNone, "", nil},
		{"unknown field", `{"nope": {|}}`, CompleteNone, "", nil},
		{"struct keys are free", `{"extra": {|}}`, CompleteNone, "", nil},
		{"struct values", `{"extra": {"a": |}}`, CompleteValue, "", []string{"true", "false", "null"}},
		{"value in list", `{"anyJson": [1, |]}`, CompleteValue, "", []string{"true", "false", "null"}},
		{"timestamp", `{"created": |}`, CompleteValue, "shop.Order.created", []string{"null"}},
		{"empty", `{"nothing": {|}}`, CompleteNone, "", nil},
		{"any keys", `{"detail": {|}}`, CompleteKey, "google.protobuf.Any", []string{"@type"}},
		{"any type url", `{"detail": {"@type": "type.googleapis.com/shop.L|"}}`, CompleteValue, "google.protobuf.Any", []string{"type.googleapis.com/shop.Line"}},
		{"any fields", `{"detail": {"@type": "type.googleapis.com/shop.Line", |}}`, CompleteKey, "shop.Line", []string{"sku", "qty", "pay"}},
		{"any fields before type", `{"detail": {|, "@type": "type.googleapis.com/shop.Line"}}`, CompleteKey, "shop.Line", []string{"sku", "qty", "pay"}},
		{"any of a well-known type", `{"detail": {"@type": "type.googleapis.com/google.protobuf.Timestamp", |}}`, CompleteKey, "google.protobuf.Any", []string{"value"}},
		{"any nested enum", `{"detail": {"@type": "type.googleapis.com/shop.Line", "pay": |}}`, CompleteValue, "shop.Line.pay", nil},
		{"oneof member set", `{"email": "a@b", |}`, CompleteKey, "shop.Order", nil},
		{"editing a key keeps the colon", `{"pa|y": "PAY_CARD"}`, CompleteKey, "shop.Order", []string{"pay", "pays", "payByName", "paid"}},
		{"missing comma", `{"gift": true "or|`, CompleteKey, "shop.Order", []string{"orderId"}},
		{"extension", `{"[shop.n|`, CompleteKey, "shop.Order", []string{"[shop.note]"}},
		{"custom json name", `{"myC|`, CompleteKey, "shop.Order", []string{"myCustom"}},
		{"root", `|`, CompleteNone, "", nil},
		{"deep unclosed", "{\n  \"lines\": [\n    {\n      \"pay\": \"PAY_|\n", CompleteValue, "shop.Line.pay", []string{"PAY_UNSPECIFIED", "PAY_CARD", "PAY_CASH"}},
		{"keys after the cursor count", `{|, "gift": true, "lines": []}`, CompleteKey, "shop.Order", nil},
		{"string escape", `{"orderId": "a\"b", "gi|`, CompleteKey, "shop.Order", []string{"gift"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := completeAt(t, tc.text)
			if got.Context != tc.context {
				t.Fatalf("context = %q, want %q (items %v)", got.Context, tc.context, labels(got))
			}
			if tc.owner != "" && got.Owner != tc.owner {
				t.Errorf("owner = %q, want %q", got.Owner, tc.owner)
			}
			if tc.want != nil && !slices.Equal(labels(got), tc.want) {
				t.Errorf("labels = %v, want %v", labels(got), tc.want)
			}
		})
	}
}

func TestCompleteLeavesOutSetFields(t *testing.T) {
	t.Parallel()
	got := labels(completeAt(t, `{"orderId": "1", "pay": "PAY_CARD", |, "gift": true, "by_sku": {}}`))
	for _, name := range []string{"orderId", "pay", "gift", "bySku"} {
		if slices.Contains(got, name) {
			t.Errorf("%s is set but suggested: %v", name, got)
		}
	}
	if !slices.Contains(got, "pays") {
		t.Errorf("pays missing: %v", got)
	}
	// A set oneof member hides the others.
	got = labels(completeAt(t, `{"email": "a@b", |}`))
	if slices.Contains(got, "phone") || slices.Contains(got, "email") {
		t.Errorf("oneof members suggested: %v", got)
	}
	got = labels(completeAt(t, `{"[shop.note]": "x", |}`))
	if slices.Contains(got, "[shop.note]") {
		t.Errorf("set extension suggested: %v", got)
	}
}

func TestCompleteItems(t *testing.T) {
	t.Parallel()
	c := completeAt(t, `{|}`)
	id := item(t, c, "orderId")
	if id.Detail != "string" || id.Documentation != "The order id." || id.Number != 1 || id.InsertText != `"orderId"` ||
		id.Suffix != `: ""` || id.SuffixCursor != 3 || id.Retrigger {
		t.Errorf("orderId = %+v", id)
	}
	cases := []struct {
		label, detail, typ, suffix string
		cursor                     int
		retrigger                  bool
	}{
		{"pay", "Pay", "shop.Pay", `: ""`, 3, true},
		{"pays", "repeated Pay", "shop.Pay", ": []", 3, true},
		{"lines", "repeated Line", "shop.Line", ": []", 3, true},
		{"bySku", "map<string, Line>", "shop.Line", ": {}", 3, false},
		{"main", "Line", "shop.Line", ": {}", 3, true},
		{"gift", "bool", "", ": ", 2, true},
		{"created", "Timestamp", "google.protobuf.Timestamp", `: ""`, 3, false},
		{"paid", "BoolValue", "google.protobuf.BoolValue", ": ", 2, true},
		{"extra", "Struct", "google.protobuf.Struct", ": {}", 3, false},
		{"anyJson", "Value", "google.protobuf.Value", ": ", 2, true},
		{"detail", "Any", "google.protobuf.Any", ": {}", 3, true},
		{"nil", "NullValue", "google.protobuf.NullValue", ": ", 2, true},
		{"total", "int64", "", ": ", 2, false},
	}
	for _, tc := range cases {
		it := item(t, c, tc.label)
		if it.Detail != tc.detail || it.Type != tc.typ || it.Suffix != tc.suffix || it.SuffixCursor != tc.cursor || it.Retrigger != tc.retrigger {
			t.Errorf("%s = %+v", tc.label, it)
		}
	}
	if !item(t, c, "total").Deprecated {
		t.Error("total is deprecated")
	}
	if it := item(t, c, "email"); it.Oneof != "target" {
		t.Errorf("email oneof = %q", it.Oneof)
	}
	if it := item(t, c, "[shop.note]"); it.Kind != ItemExtension || it.InsertText != `"[shop.note]"` {
		t.Errorf("note = %+v", it)
	}

	// The range covers the whole key; a colon after it gets no suffix.
	c = completeAt(t, `{"pa|y": "PAY_CARD"}`)
	if c.From != 1 || c.To != 6 || c.Prefix != "pa" {
		t.Errorf("range = %d-%d %q", c.From, c.To, c.Prefix)
	}
	if it := item(t, c, "pays"); it.Suffix != "" || it.Retrigger {
		t.Errorf("pays = %+v", it)
	}

	// Enum values are quoted, with their number and comment.
	c = completeAt(t, `{"pay": "PAY_|"}`)
	if c.From != 8 || c.To != 14 {
		t.Errorf("range = %d-%d", c.From, c.To)
	}
	card := item(t, c, "PAY_CARD")
	if card.Kind != ItemEnumValue || card.InsertText != `"PAY_CARD"` || card.Detail != "1" || card.Documentation != "By card." || card.Type != "shop.Pay" {
		t.Errorf("PAY_CARD = %+v", card)
	}
	if !item(t, c, "PAY_CASH").Deprecated {
		t.Error("PAY_CASH is deprecated")
	}
	c = completeAt(t, `{"gift": |}`)
	if it := item(t, c, "true"); it.Kind != ItemKeyword || it.InsertText != "true" || c.From != c.To {
		t.Errorf("true = %+v, range %d-%d", it, c.From, c.To)
	}
}

func TestCompleteWithoutPool(t *testing.T) {
	t.Parallel()
	_, md := completeSetup(t)
	// Without a pool only "@type" of an Any is known, and it is set.
	text := `{"detail": {"@type": "type.googleapis.com/shop.Line", `
	if got := Complete(nil, md, text, len(text)); got.Context != CompleteNone || len(got.Items) != 0 {
		t.Errorf("got %v %v", got.Context, labels(got))
	}
	got := Complete(nil, md, `{}`, 1)
	if slices.Contains(labels(got), "[shop.note]") {
		t.Error("extension without a pool")
	}
	// Out-of-range offsets are clamped.
	if got := Complete(nil, md, `{}`, 99); got.Context != CompleteNone {
		t.Errorf("offset past the end: %v", got.Context)
	}
}

// TestCompleteSpeed completes at the end of a large message: the
// completion is linear in the text and needs no linking.
func TestCompleteSpeed(t *testing.T) {
	p, md := completeSetup(t)
	var b strings.Builder
	b.WriteString(`{"lines": [`)
	for i := range 5000 {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"sku": "s%d", "qty": %d, "pay": "PAY_CARD"}`, i, i)
	}
	b.WriteString(`], `)
	text := b.String()
	// The best of a few runs: a loaded machine or a GC pause slows any one
	// of them, a real regression all of them.
	const runs = 3
	var c Completion
	var best time.Duration
	for i := range runs {
		start := time.Now()
		c = Complete(p, md, text, len(text))
		if d := time.Since(start); i == 0 || d < best {
			best = d
		}
	}
	if limit := testenv.Budget(200 * time.Millisecond); best > limit {
		t.Errorf("completing %d bytes took %v at best of %d, want under %v", len(text), best, runs, limit)
	}
	if slices.Contains(labels(c), "lines") || !slices.Contains(labels(c), "pay") {
		t.Errorf("labels = %v", labels(c))
	}
}

func FuzzComplete(f *testing.F) {
	f.Add(`{"pay": "PAY_`, 12)
	f.Add(`{"detail": {"@type": "x/shop.Line", "`, 30)
	f.Add(`[[[{{{"a\`, 5)
	f.Fuzz(func(t *testing.T, text string, off int) {
		p, md := completeSetup(t)
		c := Complete(p, md, text, off)
		if c.From < 0 || c.To < c.From || c.To > len(text) {
			t.Fatalf("range %d-%d in %d bytes", c.From, c.To, len(text))
		}
	})
}
