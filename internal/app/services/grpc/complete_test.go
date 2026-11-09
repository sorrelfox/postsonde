package grpc

import (
	"errors"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

func completeInput(text string, offset int) MessageCompleteInputDTO {
	return MessageCompleteInputDTO{Files: []string{"proto/shop.proto"}, Service: "shop.v1.Orders", Method: "Place", Message: text, Offset: offset}
}

func TestCompleteMessage(t *testing.T) {
	t.Parallel()
	s := newShopService(t)
	// Offsets count UTF-16 units: the emoji is 4 bytes and 2 units.
	text := "{\"id\": \"\U0001F600\", \"ki"
	got, err := s.CompleteMessage(t.Context(), completeInput(text, 16))
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != "shop.v1.Order" || got.Context != "key" || got.Owner != "shop.v1.Order" || got.From != 13 || got.To != 16 || got.Prefix != "ki" {
		t.Fatalf("got %+v", got)
	}
	if len(got.Items) != 1 || got.Count != 1 {
		t.Fatalf("items %+v", got.Items)
	}
	it := got.Items[0]
	if it.Label != "kind" || it.Kind != "field" || it.Detail != "Kind" || it.Type != "shop.v1.Kind" || it.Number != 3 ||
		it.InsertText != `"kind"` || it.Suffix != `: ""` || it.SuffixCursor != 3 || !it.Retrigger {
		t.Errorf("item %+v", it)
	}

	// Enum values; a set oneof member hides the other.
	got, err = s.CompleteMessage(t.Context(), completeInput(`{"card": "x", "kind": "`, 23))
	if err != nil {
		t.Fatal(err)
	}
	if got.Context != "value" || got.Owner != "shop.v1.Order.kind" || len(got.Items) != 2 || got.Items[1].Label != "KIND_BOOK" || got.Items[1].InsertText != `"KIND_BOOK"` {
		t.Errorf("enum: %+v", got)
	}
	got, err = s.CompleteMessage(t.Context(), completeInput(`{"card": "x", "category": {"children": [{`, 41))
	if err != nil {
		t.Fatal(err)
	}
	var labels []string
	for _, it := range got.Items {
		labels = append(labels, it.Label)
	}
	if got.Owner != "shop.v1.Category" || strings.Join(labels, ",") != "name,children" {
		t.Errorf("nested: %s %v", got.Owner, labels)
	}
	got, err = s.CompleteMessage(t.Context(), completeInput(`{"card": "x", "`, 15))
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range got.Items {
		if it.Label == "cash" || it.Label == "card" {
			t.Errorf("oneof member %s suggested", it.Label)
		}
	}

	// The linked files are cached: completing links nothing again.
	first, _, _, err := s.link(t.Context(), []string{"proto/shop.proto"}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteMessage(t.Context(), completeInput(`{`, 1)); err != nil {
		t.Fatal(err)
	}
	again, _, _, _ := s.link(t.Context(), []string{"proto/shop.proto"}, nil, false)
	if again != first {
		t.Error("the setup was linked again")
	}

	// Method errors are as for ExampleMessage.
	in := completeInput("{", 1)
	in.Method = "Nope"
	_, err = s.CompleteMessage(t.Context(), in)
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != CodeMethodNotFound {
		t.Errorf("unknown method: %v", err)
	}
}

func TestByteOffset(t *testing.T) {
	t.Parallel()
	s := "a\U0001F600b"
	for off16, want := range []int{0, 1, 5, 5, 6, 6} {
		if got := byteOffset(s, off16); got != want {
			t.Errorf("byteOffset(%d) = %d, want %d", off16, got, want)
		}
	}
}
