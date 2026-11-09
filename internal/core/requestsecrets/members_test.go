package requestsecrets

import (
	"reflect"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// An oauth1 auth has two secret members, each split, merged and reported
// missing on its own.
func TestSplitMergeTwoMembers(t *testing.T) {
	a := projectfile.Auth{Type: projectfile.AuthOAuth1, ConsumerKey: "ck", ConsumerSecret: "typed", TokenSecret: "{{ts}}"}
	out, values := Split(a)
	if out.ConsumerSecret != "" || out.TokenSecret != "{{ts}}" || !reflect.DeepEqual(out.LocalSecrets, []string{"consumerSecret"}) ||
		!reflect.DeepEqual(values, map[string]string{"auth.consumerSecret": "typed"}) {
		t.Fatalf("Split = %+v, %v", out, values)
	}
	if back := Merge(out, values); back.ConsumerSecret != "typed" || back.TokenSecret != "{{ts}}" || Missing(back) {
		t.Errorf("Merge = %+v", back)
	}

	a = projectfile.Auth{Type: projectfile.AuthOAuth1, ConsumerSecret: "c", TokenSecret: "t"}
	out, values = Split(a)
	if !reflect.DeepEqual(out.LocalSecrets, []string{"consumerSecret", "tokenSecret"}) || len(values) != 2 {
		t.Fatalf("Split = %+v, %v", out, values)
	}
	half := Merge(out, map[string]string{"auth.consumerSecret": "c"})
	if half.ConsumerSecret != "c" || MissingMember(half) != "tokenSecret" {
		t.Errorf("half merged = %+v, missing %q", half, MissingMember(half))
	}
	// An empty member stays marked; a mark of another type's member goes.
	out, _ = Split(projectfile.Auth{Type: projectfile.AuthOAuth1, LocalSecrets: []string{"tokenSecret", "token"}})
	if !reflect.DeepEqual(out.LocalSecrets, []string{"tokenSecret"}) {
		t.Errorf("marks = %v", out.LocalSecrets)
	}
}
