package dynamic

import (
	"context"
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/desc"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/linker"
)

var (
	poolOnce sync.Once
	testPool *desc.Pool
	poolErr  error
)

// pool links the test .proto files in testdata once.
func pool(tb testing.TB) *desc.Pool {
	tb.Helper()
	poolOnce.Do(func() {
		res, err := linker.Load(context.Background(), linker.Options{
			Files:       []string{"proto3.proto", "proto2.proto", "editions.proto", "editions_file_none.proto"},
			ImportPaths: []string{"testdata"},
		})
		if err != nil {
			poolErr = err
			return
		}
		if len(res.Diagnostics) > 0 {
			poolErr = fmt.Errorf("diagnostics: %v", res.Diagnostics)
			return
		}
		testPool = res.Pool
	})
	if poolErr != nil {
		tb.Fatal(poolErr)
	}
	return testPool
}

func msgType(tb testing.TB, name string) *desc.Message {
	tb.Helper()
	md := pool(tb).FindMessage(name)
	if md == nil {
		tb.Fatalf("no message %s", name)
	}
	return md
}

// field returns the field of md named name, or the extension with full
// name "[name]".
func field(tb testing.TB, md *desc.Message, name string) *desc.Field {
	tb.Helper()
	var fd *desc.Field
	if strings.HasPrefix(name, "[") {
		fd = pool(tb).FindExtension(strings.Trim(name, "[]"))
	} else {
		fd = md.FieldByName(name)
	}
	if fd == nil {
		tb.Fatalf("no field %s in %s", name, md.FullName)
	}
	return fd
}

// build creates a message from name/value pairs; []any values fill lists,
// map[any]any values fill maps.
func build(tb testing.TB, typeName string, kv ...any) *Message {
	tb.Helper()
	m := New(msgType(tb, typeName))
	fill(tb, m, kv...)
	return m
}

func fill(tb testing.TB, m *Message, kv ...any) {
	tb.Helper()
	for i := 0; i < len(kv); i += 2 {
		fd := field(tb, m.md, kv[i].(string))
		switch v := kv[i+1].(type) {
		case []any:
			l := m.Mutable(fd).(*List)
			for _, x := range v {
				if err := l.Append(x); err != nil {
					tb.Fatal(err)
				}
			}
		case map[any]any:
			mp := m.Mutable(fd).(*Map)
			for k, x := range v {
				if err := mp.Set(k, x); err != nil {
					tb.Fatal(err)
				}
			}
		default:
			if err := m.Set(fd, v); err != nil {
				tb.Fatal(err)
			}
		}
	}
}

// dump writes a message as "path: value" lines in declaration order, maps
// sorted by key. The golden generator (see testdata/README.md) writes the
// same format from protobuf-go's view of the message.
func dump(m *Message) string {
	var sb strings.Builder
	dumpMessage(&sb, "", m)
	return sb.String()
}

func dumpMessage(sb *strings.Builder, prefix string, m *Message) {
	m.Range(func(fd *desc.Field, v any) bool {
		name := prefix + fd.Name
		if fd.IsExtension {
			name = prefix + "[" + fd.FullName + "]"
		}
		switch v := v.(type) {
		case *Map:
			for _, k := range v.Keys(true) {
				x, _ := v.Get(k)
				dumpSingular(sb, name+"["+formatKey(k)+"]", x)
			}
		case *List:
			for i := range v.Len() {
				dumpSingular(sb, name+"["+strconv.Itoa(i)+"]", v.Get(i))
			}
		default:
			dumpSingular(sb, name, v)
		}
		return true
	})
	if u := m.Unknown(); len(u) > 0 {
		fmt.Fprintf(sb, "%sunknown: %s\n", prefix, hex.EncodeToString(u))
	}
}

func dumpSingular(sb *strings.Builder, name string, v any) {
	var s string
	switch v := v.(type) {
	case *Message:
		fmt.Fprintf(sb, "%s: {}\n", name)
		dumpMessage(sb, name+".", v)
		return
	case float64:
		s = strconv.FormatFloat(v, 'g', -1, 64)
	case float32:
		s = strconv.FormatFloat(float64(v), 'g', -1, 32)
	case string:
		s = strconv.Quote(v)
	case []byte:
		s = "0x" + hex.EncodeToString(v)
	default:
		s = fmt.Sprint(v)
	}
	fmt.Fprintf(sb, "%s: %s\n", name, s)
}

// goldenNames lists the golden cases.
func goldenNames(tb testing.TB) []string {
	tb.Helper()
	entries, err := goldenFS.ReadDir("testdata/golden")
	if err != nil {
		tb.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if n, ok := strings.CutSuffix(e.Name(), ".bin"); ok {
			names = append(names, n)
		}
	}
	slices.Sort(names)
	return names
}
