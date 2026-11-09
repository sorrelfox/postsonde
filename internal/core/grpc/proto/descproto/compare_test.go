package descproto

import (
	"fmt"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/desc"
)

// dumpFile describes the parts of a linked file's model that must not
// depend on whether it was linked from source or from descriptors: names,
// kinds, types, presence, packing, JSON names, defaults, ranges, streaming
// flags and deprecation. Options are compared through Marshal.
func dumpFile(f *desc.File) string {
	var b strings.Builder
	fmt.Fprintf(&b, "file %s syntax=%v edition=%q package=%q deprecated=%v\n", f.Path, f.Syntax, f.Edition, f.Package, f.Deprecated)
	for _, imp := range f.Imports {
		fmt.Fprintf(&b, "import %s kind=%v resolved=%v\n", imp.Path, imp.Kind, imp.File != nil)
	}
	desc.Walk(f, func(d desc.Descriptor) bool {
		base := d.Desc()
		fmt.Fprintf(&b, "%T %s deprecated=%v", d, base.FullName, base.Deprecated)
		switch x := d.(type) {
		case *desc.Message:
			fmt.Fprintf(&b, " mapEntry=%v messageSet=%v ext=%v reserved=%v names=%v",
				x.MapEntry, x.MessageSetWireFormat, ranges(x.ExtensionRanges), ranges(x.ReservedRanges), x.ReservedNames)
			fmt.Fprintf(&b, " fields=%d oneofs=%d nested=%s", len(x.Fields), len(x.Oneofs), names(x.Messages))
		case *desc.Field:
			oneof := ""
			if x.Oneof != nil {
				oneof = fmt.Sprintf("%s(synthetic=%v)", x.Oneof.Name, x.Oneof.Synthetic)
			}
			ext := ""
			if x.IsExtension && x.Extendee != nil {
				ext = x.Extendee.FullName
			}
			def := "<nil>"
			if v := x.Default; v != nil {
				def = fmt.Sprintf("%v/%v/%v/%v/%v/%q", v.Kind, v.Bool, v.Int, v.Uint, v.Float, v.String)
				if v.Enum != nil {
					def += "/" + v.Enum.FullName
				}
			}
			fmt.Fprintf(&b, " number=%d kind=%v card=%v json=%q type=%q msg=%v enum=%v oneof=%s ext=%v extendee=%s p3opt=%v presence=%v packed=%v map=%v default=%s",
				x.Number, x.Kind, x.Cardinality, x.JSONName, x.TypeName, x.Message != nil, x.Enum != nil, oneof,
				x.IsExtension, ext, x.Proto3Optional, x.HasPresence, x.Packed, x.IsMap(), def)
		case *desc.Oneof:
			fmt.Fprintf(&b, " synthetic=%v fields=%d", x.Synthetic, len(x.Fields))
		case *desc.Enum:
			fmt.Fprintf(&b, " closed=%v reserved=%v names=%v", x.Closed, ranges(x.ReservedRanges), x.ReservedNames)
		case *desc.EnumValue:
			fmt.Fprintf(&b, " number=%d", x.Number)
		case *desc.Method:
			in, out := "<nil>", "<nil>"
			if x.Input != nil {
				in = x.Input.FullName
			}
			if x.Output != nil {
				out = x.Output.FullName
			}
			fmt.Fprintf(&b, " in=%s out=%s cs=%v ss=%v", in, out, x.ClientStreaming, x.ServerStreaming)
		}
		b.WriteByte('\n')
		return true
	})
	return b.String()
}

func ranges(rs []desc.Range) string {
	parts := make([]string, len(rs))
	for i, r := range rs {
		parts[i] = fmt.Sprintf("%d-%d", r.Start, r.End)
	}
	return strings.Join(parts, ",")
}

func names(ms []*desc.Message) string {
	parts := make([]string, len(ms))
	for i, m := range ms {
		parts[i] = m.Name
	}
	return strings.Join(parts, ",")
}

// compareFiles fails if the two models differ, showing the first
// differing lines.
func compareFiles(t *testing.T, want, got *desc.File) {
	t.Helper()
	w, g := strings.Split(dumpFile(want), "\n"), strings.Split(dumpFile(got), "\n")
	for i := 0; i < max(len(w), len(g)); i++ {
		var wl, gl string
		if i < len(w) {
			wl = w[i]
		}
		if i < len(g) {
			gl = g[i]
		}
		if wl != gl {
			t.Errorf("%s: model differs at line %d:\n want %s\n  got %s", want.Path, i+1, wl, gl)
			return
		}
	}
}
