package wellknown

import (
	"io/fs"
	"strings"
	"testing"
)

func TestFiles(t *testing.T) {
	want := []string{
		"google/protobuf/any.proto",
		"google/protobuf/api.proto",
		"google/protobuf/descriptor.proto",
		"google/protobuf/duration.proto",
		"google/protobuf/empty.proto",
		"google/protobuf/field_mask.proto",
		"google/protobuf/source_context.proto",
		"google/protobuf/struct.proto",
		"google/protobuf/timestamp.proto",
		"google/protobuf/type.proto",
		"google/protobuf/wrappers.proto",
		"google/rpc/error_details.proto",
		"google/rpc/status.proto",
	}
	got := Paths()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("Paths() = %q, want %q", got, want)
	}
	for _, p := range got {
		src, ok := Source(p)
		if !ok {
			t.Fatalf("Source(%q) missing", p)
		}
		// The license notice must ship with every file.
		if strings.HasPrefix(p, "google/rpc/") {
			if !strings.Contains(src, "Copyright 2025 Google LLC") || !strings.Contains(src, "Licensed under the Apache License, Version 2.0") {
				t.Errorf("%s: license header missing", p)
			}
			continue
		}
		if !strings.Contains(src, "Copyright 2008 Google Inc.") || !strings.Contains(src, "Redistribution and use in source and binary forms") {
			t.Errorf("%s: license header missing", p)
		}
	}
	// The Apache License 2.0 text ships with google/rpc/status.proto.
	lic, err := fs.ReadFile(FS(), "google/rpc/LICENSE")
	if err != nil || !strings.Contains(string(lic), "Apache License") ||
		!strings.Contains(string(lic), "TERMS AND CONDITIONS FOR USE, REPRODUCTION, AND DISTRIBUTION") {
		t.Errorf("google/rpc/LICENSE missing or wrong: %v", err)
	}
	for _, p := range []string{"", "google/protobuf/nope.proto", "/google/protobuf/any.proto", "google/protobuf/../protobuf/any.proto", "google/rpc/LICENSE"} {
		if _, ok := Source(p); ok {
			t.Errorf("Source(%q) found", p)
		}
	}
}
