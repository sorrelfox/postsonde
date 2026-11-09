package linker

import (
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/desc"
)

func TestGroupsAndExtensionGroups(t *testing.T) {
	res := load(t, map[string]string{
		"g.proto": `syntax = "proto2";
package g;
message M {
  extensions 10 to 20;
  oneof choice {
    group Picked = 1 { optional int32 v = 1; }
  }
  extend M {
    optional group Nested = 11 { optional Picked p = 1; }
  }
}
extend M {
  repeated group Top = 12 { optional M.Nested n = 1; }
}
`,
	}, "g.proto")
	expectDiags(t, res)
	p := res.Pool
	m := p.FindMessage("g.M")
	picked := m.FieldByName("picked")
	if picked == nil || picked.Kind != desc.KindGroup || picked.Oneof == nil || picked.Message != p.FindMessage("g.M.Picked") {
		t.Errorf("picked = %+v", picked)
	}
	nested := p.FindExtension("g.M.nested")
	if nested == nil || nested.Kind != desc.KindGroup || nested.Parent != m || nested.Message.Parent != m ||
		nested.Message.FieldByName("p").Message != p.FindMessage("g.M.Picked") {
		t.Fatalf("nested = %+v", nested)
	}
	top := p.FindExtension("g.top")
	if top == nil || top.Parent != nil || top.Cardinality != desc.Repeated || top.Message != p.FindMessage("g.Top") ||
		res.Files[0].Messages[1] != top.Message || top.Message.FieldByName("n").Message != nested.Message {
		t.Errorf("top = %+v", top)
	}
}
