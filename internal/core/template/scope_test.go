package template

import (
	"slices"
	"testing"
)

func TestChainNames(t *testing.T) {
	c := Chain{
		Vars("Global", map[string]string{"host": "a", "token": "b"}),
		dynamicScope{},
		Vars("Environment: dev", map[string]string{"host": "c", "id": "d"}),
	}
	got := c.Names()
	want := []string{"host", "id", "token"}
	if !slices.Equal(got, want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
	if got := (Chain{}).Names(); len(got) != 0 {
		t.Fatalf("empty chain Names() = %v", got)
	}
}

// dynamicScope is a Scope that cannot list its names.
type dynamicScope struct{}

func (dynamicScope) Name() string                   { return "Dynamic" }
func (dynamicScope) Lookup(string) (Variable, bool) { return Variable{}, false }
