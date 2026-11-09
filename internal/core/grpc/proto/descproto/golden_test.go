package descproto

import (
	"bytes"
	"slices"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/grpc/dynamic"
)

// TestGoldenLink links the descriptors protocompile produced for the test
// schema and requires the same model as linking the sources.
func TestGoldenLink(t *testing.T) {
	src := linkSchema(t)
	var files [][]byte
	for _, name := range schemaFiles {
		files = append(files, goldenDescriptor(t, name))
	}
	// Reverse order: Link must not depend on dependencies coming first.
	slices.Reverse(files)
	res := linkBytes(t, files)
	for _, name := range schemaFiles {
		got := res.Pool.FindFile(name)
		if got == nil {
			t.Fatalf("%s missing", name)
		}
		compareFiles(t, src.Pool.FindFile(name), got)
	}
}

// TestGoldenMarshal compares Marshal's output for the linked sources with
// protocompile's descriptors.
func TestGoldenMarshal(t *testing.T) {
	src := linkSchema(t)
	for _, name := range schemaFiles {
		got, err := Marshal(src.Pool.FindFile(name), src.Pool)
		if err != nil {
			t.Fatalf("Marshal %s: %v", name, err)
		}
		want := goldenDescriptor(t, name)
		if bytes.Equal(got, want) {
			continue
		}
		// Show where the descriptors differ, as decoded messages.
		gm, wm := dynamic.New(schema().FindMessage(typeFile)), dynamic.New(schema().FindMessage(typeFile))
		if err := dynamic.Unmarshal(got, gm); err != nil {
			t.Fatal(err)
		}
		if err := dynamic.Unmarshal(want, wm); err != nil {
			t.Fatal(err)
		}
		// protobuf-go writes extensions before the other fields of a
		// message, PostSonde in field number order; decoded without a
		// resolver, the extensions are unknown fields in the same order.
		if dynamic.Equal(gm, wm) {
			continue
		}
		t.Errorf("%s: Marshal differs from protocompile\n got %s\nwant %s", name, textOf(gm), textOf(wm))
	}
}

func textOf(m *dynamic.Message) string {
	p := &printer{syntax: "proto2"}
	return p.aggregate(m)
}
