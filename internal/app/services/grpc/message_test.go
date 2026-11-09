package grpc

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	coreworkspace "github.com/sorrelfox/postsonde/internal/core/workspace"
)

const shopProto = `syntax = "proto3";

package shop.v1;

import "google/protobuf/timestamp.proto";

enum Kind {
  KIND_UNSPECIFIED = 0;
  KIND_BOOK = 1;
}

message Category {
  string name = 1;
  repeated Category children = 2;
}

message Order {
  string id = 1;
  int64 total = 2;
  Kind kind = 3;
  repeated string tags = 4;
  map<string, int32> counts = 5;
  Category category = 6;
  google.protobuf.Timestamp created = 7;
  oneof pay {
    string card = 8;
    string cash = 9;
  }
}

message Ack {}

service Orders {
  rpc Place(Order) returns (Ack);
  rpc Upload(stream Order) returns (Ack);
}
`

func newShopService(t *testing.T) *GRPCService {
	t.Helper()
	dir := filepath.Join(tempDir(t), "shop")
	writeFile(t, dir, "postsonde.json", `{"schemaVersion": 1, "kind": "workspace", "id": "ws_0123456789abcdef", "name": "Shop"}`)
	writeFile(t, dir, "proto/shop.proto", shopProto)
	manager := coreworkspace.New(tempDir(t))
	if _, err := manager.Open(dir); err != nil {
		t.Fatal(err)
	}
	return New(manager, nil, nil, nil, nil)
}

func shopInput(method, message string) MessageInputDTO {
	return MessageInputDTO{Files: []string{"proto/shop.proto"}, Service: "shop.v1.Orders", Method: method, Message: message}
}

func TestExampleMessage(t *testing.T) {
	t.Parallel()
	s := newShopService(t)
	got, err := s.ExampleMessage(t.Context(), shopInput("Place", ""))
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "id": "",
  "total": "0",
  "kind": "KIND_UNSPECIFIED",
  "tags": [
    ""
  ],
  "counts": {
    "": 0
  },
  "category": {
    "name": "",
    "children": [
      {
        "name": "",
        "children": []
      }
    ]
  },
  "created": "1970-01-01T00:00:00Z",
  "card": ""
}`
	if got.Type != "shop.v1.Order" || got.JSON != want {
		t.Errorf("ExampleMessage = %s\n%s\nwant\n%s", got.Type, got.JSON, want)
	}
	// The example is a valid message.
	check, err := s.ValidateMessage(t.Context(), shopInput("Place", got.JSON))
	if err != nil || !check.Valid {
		t.Errorf("example does not validate: %+v %v", check, err)
	}
	// Client-streaming methods have examples too.
	if got, err := s.ExampleMessage(t.Context(), shopInput("Upload", "")); err != nil || got.Type != "shop.v1.Order" {
		t.Errorf("Upload: %+v %v", got, err)
	}
}

// TestOutputMessage: with output, the response type is used (the gRPC
// mock server's canned responses).
func TestOutputMessage(t *testing.T) {
	t.Parallel()
	s := newShopService(t)
	in := shopInput("Place", "")
	in.Output = true
	got, err := s.ExampleMessage(t.Context(), in)
	if err != nil || got.Type != "shop.v1.Ack" || got.JSON != "{}" {
		t.Errorf("ExampleMessage = %+v, %v", got, err)
	}
	in.Message = `{"id": "a"}`
	check, err := s.ValidateMessage(t.Context(), in)
	if err != nil || check.Valid || check.Type != "shop.v1.Ack" || check.Error.Message != `unknown field "id"` {
		t.Errorf("ValidateMessage = %+v, %v", check, err)
	}
	c, err := s.CompleteMessage(t.Context(), MessageCompleteInputDTO{Files: in.Files, Service: in.Service, Method: "Place", Message: "{}", Offset: 1, Output: true})
	if err != nil || c.Type != "shop.v1.Ack" || c.Count != 0 {
		t.Errorf("CompleteMessage = %+v, %v", c, err)
	}
}

func TestValidateMessage(t *testing.T) {
	t.Parallel()
	s := newShopService(t)
	cases := []struct {
		name, text string
		want       *MessageErrorDTO
	}{
		{"valid", `{"id": "a", "total": 3, "kind": "KIND_BOOK"}`, nil},
		{"blank", "  \n", nil},
		{"proto names", `{"id": "a", "counts": {"x": 1}}`, nil},
		{"unknown field", "{\n  \"id\": \"a\",\n  \"nope\": 1\n}",
			&MessageErrorDTO{Offset: 17, ByteOffset: 17, Line: 3, Column: 3, Message: `unknown field "nope"`}},
		{"syntax", `{"id": "a",}`,
			&MessageErrorDTO{Offset: 11, ByteOffset: 11, Line: 1, Column: 12}},
		{"wrong type", `{"total": true}`,
			&MessageErrorDTO{Offset: 10, ByteOffset: 10, Line: 1, Column: 11}},
		{"ends early", `{"id": "a"`,
			&MessageErrorDTO{Offset: 10, ByteOffset: 10, Line: 1, Column: 11}},
		// Offsets count UTF-16 units: the emoji is 4 bytes and 2 units.
		{"utf16", "{\"id\": \"\U0001F600\", \"nope\": 1}",
			&MessageErrorDTO{Offset: 13, ByteOffset: 15, Line: 1, Column: 14, Message: `unknown field "nope"`}},
		{"two oneof members", `{"card": "a", "cash": "b"}`,
			&MessageErrorDTO{Offset: 14, ByteOffset: 14, Line: 1, Column: 15}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := s.ValidateMessage(t.Context(), shopInput("Place", c.text))
			if err != nil {
				t.Fatal(err)
			}
			if got.Type != "shop.v1.Order" {
				t.Errorf("type %q", got.Type)
			}
			if c.want == nil {
				if !got.Valid || got.Error != nil {
					t.Errorf("want valid, got %+v", got.Error)
				}
				return
			}
			if got.Valid || got.Error == nil {
				t.Fatalf("want invalid, got %+v", got)
			}
			e := *got.Error
			if c.want.Message == "" {
				e.Message = ""
			}
			if e != *c.want {
				t.Errorf("error %+v, want %+v", *got.Error, *c.want)
			}
		})
	}
}

func TestMessageMethodErrors(t *testing.T) {
	t.Parallel()
	s := newShopService(t)
	for _, c := range []struct {
		in   MessageInputDTO
		code string
	}{
		{shopInput("", "{}"), CodeNoMethod},
		{shopInput("Nope", "{}"), CodeMethodNotFound},
		{MessageInputDTO{Service: "shop.v1.Orders", Method: "Place"}, CodeNoProto},
	} {
		_, err := s.ValidateMessage(t.Context(), c.in)
		var e *errs.Error
		if !errors.As(err, &e) || e.Code != c.code {
			t.Errorf("ValidateMessage(%+v) = %v, want %s", c.in, err, c.code)
		}
		_, err = s.ExampleMessage(t.Context(), c.in)
		if !errors.As(err, &e) || e.Code != c.code {
			t.Errorf("ExampleMessage(%+v) = %v, want %s", c.in, err, c.code)
		}
	}
}
