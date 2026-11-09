package rpcstatus

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"testing/fstest"

	"github.com/sorrelfox/postsonde/internal/core/grpc/dynamic"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/desc"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/linker"
	"github.com/sorrelfox/postsonde/internal/core/grpc/protojson"
)

// fromJSON builds a message of type name in p from JSON.
func fromJSON(t *testing.T, p *desc.Pool, name, js string) *dynamic.Message {
	t.Helper()
	md := p.FindMessage(name)
	if md == nil {
		t.Fatalf("no type %s", name)
	}
	m := dynamic.New(md)
	if err := (protojson.UnmarshalOptions{Resolver: p}).Unmarshal([]byte(js), m); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return m
}

func userPool(t *testing.T) *desc.Pool {
	t.Helper()
	res, err := linker.Load(context.Background(), linker.Options{
		Files: []string{"shop.proto"},
		FS: fstest.MapFS{"shop.proto": {Data: []byte(`syntax = "proto3";
package shop.v1;
message OutOfStock { string sku = 1; int32 left = 2; }
`)}},
	})
	if err != nil || res.HasErrors() {
		t.Fatalf("Load: %v %v", err, res.Diagnostics)
	}
	return res.Pool
}

func TestBuiltin(t *testing.T) {
	p, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"google.rpc.Status", "google.rpc.ErrorInfo", "google.rpc.RetryInfo", "google.rpc.DebugInfo",
		"google.rpc.QuotaFailure", "google.rpc.QuotaFailure.Violation", "google.rpc.PreconditionFailure",
		"google.rpc.BadRequest", "google.rpc.BadRequest.FieldViolation", "google.rpc.RequestInfo",
		"google.rpc.ResourceInfo", "google.rpc.Help", "google.rpc.LocalizedMessage", "google.protobuf.Duration",
	} {
		if p.FindMessage(name) == nil {
			t.Errorf("%s missing", name)
		}
	}
}

func TestRoundTrip(t *testing.T) {
	p, _ := Builtin()
	user := userPool(t)
	in := &Status{
		Code:    3,
		Message: "bad order",
		Details: []Detail{
			{Message: fromJSON(t, p, "google.rpc.ErrorInfo", `{"reason": "STOCK", "domain": "shop.example", "metadata": {"sku": "A1"}}`)},
			{Message: fromJSON(t, p, "google.rpc.RetryInfo", `{"retryDelay": "1.500s"}`)},
			{Message: fromJSON(t, p, "google.rpc.BadRequest", `{"fieldViolations": [{"field": "items[0].qty", "description": "must be positive"}]}`)},
			{Message: fromJSON(t, user, "shop.v1.OutOfStock", `{"sku": "A1", "left": 2}`), TypeURL: "example.com/types/shop.v1.OutOfStock"},
			{TypeURL: "type.googleapis.com/other.v1.Unknown", Value: []byte{0x08, 0x01}},
			{TypeURL: "type.googleapis.com/google.rpc.DebugInfo", Value: []byte{0x0a, 0x05, 'x'}},
		},
	}
	b, err := Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	st, err := Decode(b, user)
	if err != nil {
		t.Fatal(err)
	}
	if st.Code != 3 || st.Message != "bad order" || len(st.Details) != 6 {
		t.Fatalf("status %+v", st)
	}
	want := []struct {
		url, typ string
		json     string
		known    bool
		err      bool
	}{
		{"type.googleapis.com/google.rpc.ErrorInfo", "google.rpc.ErrorInfo", `{"reason":"STOCK","domain":"shop.example","metadata":{"sku":"A1"}}`, true, false},
		{"type.googleapis.com/google.rpc.RetryInfo", "google.rpc.RetryInfo", `{"retryDelay":"1.500s"}`, true, false},
		{"type.googleapis.com/google.rpc.BadRequest", "google.rpc.BadRequest", `{"fieldViolations":[{"field":"items[0].qty","description":"must be positive"}]}`, true, false},
		{"example.com/types/shop.v1.OutOfStock", "shop.v1.OutOfStock", `{"sku":"A1","left":2}`, true, false},
		{"type.googleapis.com/other.v1.Unknown", "other.v1.Unknown", "", false, false},
		{"type.googleapis.com/google.rpc.DebugInfo", "google.rpc.DebugInfo", "", true, true},
	}
	for i, w := range want {
		d := st.Details[i]
		js, err := st.JSON(d)
		if err != nil {
			t.Fatalf("detail %d JSON: %v", i, err)
		}
		if js != "" {
			var buf bytes.Buffer
			if err := json.Compact(&buf, []byte(js)); err != nil {
				t.Fatal(err)
			}
			js = buf.String()
		}
		if d.TypeURL != w.url || d.Type != w.typ || d.Known() != w.known || (d.Err != nil) != w.err || js != w.json {
			t.Errorf("detail %d = %+v json %s, want %+v", i, d, js, w)
		}
		if !w.known && string(d.Value) != "\x08\x01" {
			t.Errorf("unknown detail value %q", d.Value)
		}
	}

	// Without the caller's types, OutOfStock is unknown and kept as bytes.
	st, err = Decode(b, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d := st.Details[3]; d.Known() || len(d.Value) == 0 {
		t.Errorf("OutOfStock without types: %+v", d)
	}
}

func TestDecodeErrors(t *testing.T) {
	for _, b := range [][]byte{{1, 2, 3}, {0x0a, 0x05, 'a'}} {
		if _, err := Decode(b, nil); !errors.Is(err, ErrNotStatus) {
			t.Errorf("Decode(%x) = %v", b, err)
		}
	}
	st, err := Decode(nil, nil)
	if err != nil || st.Code != 0 || len(st.Details) != 0 {
		t.Errorf("empty: %+v %v", st, err)
	}
}
