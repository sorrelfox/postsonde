package reflectwire

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

func golden(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name+".bin"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

var listReq = Request{Host: "localhost:50051", Kind: RequestListServices, Name: "*"}

// TestGoldenRequests decodes grpc-go's encodings and encodes the same bytes.
func TestGoldenRequests(t *testing.T) {
	for name, want := range map[string]Request{
		"req_list_services":                 listReq,
		"req_file_containing_symbol":        {Kind: RequestFileContainingSymbol, Name: "shop.v1.OrderService"},
		"req_file_by_filename":              {Kind: RequestFileByFilename, Name: "shop/v1/common.proto"},
		"req_file_containing_extension":     {Kind: RequestFileContainingExtension, Name: "google.protobuf.FieldOptions", ExtensionNumber: 50001},
		"req_all_extension_numbers_of_type": {Kind: RequestAllExtensionNumbersOfType, Name: "legacy.Item"},
	} {
		b := golden(t, name)
		got, err := UnmarshalRequest(b)
		if err != nil || got != want {
			t.Errorf("%s: UnmarshalRequest = %+v, %v; want %+v", name, got, err, want)
		}
		if enc := MarshalRequest(want); !bytes.Equal(enc, b) {
			t.Errorf("%s: MarshalRequest = %x, want %x", name, enc, b)
		}
	}
}

func TestGoldenResponses(t *testing.T) {
	for name, want := range map[string]Response{
		"resp_list_services": {ValidHost: "localhost:50051", Original: &listReq, Kind: ResponseListServices,
			Services: []string{"shop.v1.OrderService", "grpc.reflection.v1.ServerReflection"}},
		"resp_file_descriptor": {Kind: ResponseFileDescriptor,
			Files: [][]byte{[]byte("\x0a\x0ba/one.proto"), []byte("\x0a\x0bb/two.proto\x1a\x0ba/one.proto")}},
		"resp_all_extension_numbers": {Kind: ResponseAllExtensionNumbers, BaseTypeName: "legacy.Item", ExtensionNumbers: []int32{100, 101, 150}},
		"resp_error":                 {ValidHost: "h", Kind: ResponseError, ErrorCode: 5, ErrorMessage: "symbol not found"},
	} {
		b := golden(t, name)
		got, err := UnmarshalResponse(b)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%s: UnmarshalResponse = %+v, %v; want %+v", name, got, err, want)
		}
		if enc := MarshalResponse(want); !bytes.Equal(enc, b) {
			t.Errorf("%s: MarshalResponse = %x, want %x", name, enc, b)
		}
	}
}

func TestDecodingRules(t *testing.T) {
	// Unknown fields (varint 9, fixed32 10, fixed64 11, group 12) are
	// skipped; a later oneof member replaces an earlier one.
	b := []byte{0x48, 0x01, 0x55, 1, 2, 3, 4, 0x59, 1, 2, 3, 4, 5, 6, 7, 8, 0x63, 0x08, 0x01, 0x64}
	b = append(b, MarshalRequest(Request{Kind: RequestFileByFilename, Name: "a.proto"})...)
	b = append(b, MarshalRequest(Request{Kind: RequestFileContainingSymbol, Name: "pkg.S"})...)
	r, err := UnmarshalRequest(b)
	if err != nil || r != (Request{Kind: RequestFileContainingSymbol, Name: "pkg.S"}) {
		t.Errorf("UnmarshalRequest = %+v, %v", r, err)
	}

	// Unpacked extension numbers, repeated file descriptor responses merged,
	// a later member of another kind replacing them.
	resp := []byte{0x2a, 0x04, 0x10, 0x01, 0x10, 0x02}
	got, err := UnmarshalResponse(resp)
	if err != nil || !slices.Equal(got.ExtensionNumbers, []int32{1, 2}) {
		t.Errorf("unpacked: %+v, %v", got, err)
	}
	files := append(MarshalResponse(Response{Kind: ResponseFileDescriptor, Files: [][]byte{[]byte("a")}}),
		MarshalResponse(Response{Kind: ResponseFileDescriptor, Files: [][]byte{[]byte("b")}})...)
	if got, _ := UnmarshalResponse(files); len(got.Files) != 2 {
		t.Errorf("merged files: %+v", got)
	}
	files = append(files, MarshalResponse(Response{Kind: ResponseError, ErrorCode: 12})...)
	if got, _ := UnmarshalResponse(files); got.Kind != ResponseError || got.Files != nil || got.ErrorCode != 12 {
		t.Errorf("replaced: %+v", got)
	}
	// A string field with a varint wire type is skipped.
	if got, err := UnmarshalResponse([]byte{0x08, 0x01}); err != nil || got.ValidHost != "" {
		t.Errorf("wrong wire type: %+v, %v", got, err)
	}

	for _, bad := range [][]byte{{0x0a, 0x05, 'a'}, {0x80}, {0x0c}, {0x3a, 0x02, 0x0a, 0x09}} {
		if _, err := UnmarshalResponse(bad); !errors.Is(err, ErrMalformed) {
			t.Errorf("UnmarshalResponse(%x) error = %v", bad, err)
		}
		if _, err := UnmarshalRequest(bad); bad[0] != 0x3a && !errors.Is(err, ErrMalformed) {
			t.Errorf("UnmarshalRequest(%x) error = %v", bad, err)
		}
	}
}

func TestFileInfo(t *testing.T) {
	name, deps, err := FileInfo([]byte("\x0a\x0bb/two.proto\x12\x01p\x1a\x0ba/one.proto\x1a\x01c"))
	if err != nil || name != "b/two.proto" || !slices.Equal(deps, []string{"a/one.proto", "c"}) {
		t.Errorf("FileInfo = %q %q %v", name, deps, err)
	}
	if _, _, err := FileInfo([]byte{0x0a, 0x09}); !errors.Is(err, ErrMalformed) {
		t.Errorf("truncated: %v", err)
	}
}

func FuzzUnmarshal(f *testing.F) {
	entries, _ := os.ReadDir("testdata")
	for _, e := range entries {
		b, _ := os.ReadFile(filepath.Join("testdata", e.Name()))
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		if r, err := UnmarshalResponse(b); err == nil {
			again, err := UnmarshalResponse(MarshalResponse(r))
			if err != nil || again.Kind != r.Kind || len(again.Files) != len(r.Files) || len(again.Services) != len(r.Services) {
				t.Fatalf("response round trip: %+v -> %+v, %v", r, again, err)
			}
		}
		if r, err := UnmarshalRequest(b); err == nil {
			again, err := UnmarshalRequest(MarshalRequest(r))
			if err != nil || again != r && r.Kind != RequestNone {
				t.Fatalf("request round trip: %+v -> %+v, %v", r, again, err)
			}
		}
		FileInfo(b)
	})
}
