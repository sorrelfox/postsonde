package template

import (
	"encoding/json"
	"os"
	"slices"
	"testing"

	"github.com/sorrelfox/postsonde/internal/testutil/golden"
)

// filterTableEntry is one filter as the editor knows it
// (frontend/src/lib/templateFilters.ts checks its table against the
// golden file filters.json.golden).
type filterTableEntry struct {
	Name        string `json:"name"`
	Keys        int    `json:"keys"`
	MaxArgs     int    `json:"maxArgs"`
	Description string `json:"description"`
}

// TestFilterTable writes the filter table the frontend's completion and
// hover use; go test -update rewrites it when a filter changes.
func TestFilterTable(t *testing.T) {
	t.Parallel()
	table := []filterTableEntry{}
	for _, n := range FilterNames() {
		f := filters[n]
		table = append(table, filterTableEntry{Name: n, Keys: f.keys, MaxArgs: f.maxArgs, Description: f.desc})
	}
	out, err := json.MarshalIndent(table, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, "filters.json", append(out, '\n'))
}

// pipeCases is testdata/pipes.json, which
// frontend/src/lib/templateFilters.test.ts runs too.
type pipeCases struct {
	Cases []struct {
		Name    string   `json:"name"`
		Pipe    bool     `json:"pipe"`
		Input   string   `json:"input"`
		Quoted  bool     `json:"quoted"`
		Filters []string `json:"filters"`
		Keys    []string `json:"keys"`
		Error   string   `json:"error"`
	} `json:"cases"`
}

func TestPipeCases(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("testdata/pipes.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx pipeCases
	if err := json.Unmarshal(data, &fx); err != nil {
		t.Fatal(err)
	}
	for _, c := range fx.Cases {
		stages, ok := splitPipe(c.Name)
		if ok != c.Pipe {
			t.Errorf("splitPipe(%q) ok = %v, want %v", c.Name, ok, c.Pipe)
			continue
		}
		if !ok {
			continue
		}
		p, err := parsePipe(stages)
		if c.Error != "" {
			if err == nil || err.Error() != c.Error {
				t.Errorf("parsePipe(%q) error = %v, want %q", c.Name, err, c.Error)
			}
			if IsPipe(c.Name) {
				t.Errorf("IsPipe(%q) = true for an invalid pipe", c.Name)
			}
			continue
		}
		if err != nil {
			t.Errorf("parsePipe(%q) = %v", c.Name, err)
			continue
		}
		var names, keys []string
		for _, s := range p.steps {
			names = append(names, s.name)
			for _, k := range s.keys {
				if !k.quoted {
					keys = append(keys, k.text)
				}
			}
		}
		if p.input.text != c.Input || p.input.quoted != c.Quoted || !slices.Equal(names, c.Filters) || !slices.Equal(keys, c.Keys) {
			t.Errorf("parsePipe(%q) = %+v, want input %q (quoted %v), filters %v, keys %v", c.Name, p, c.Input, c.Quoted, c.Filters, c.Keys)
		}
		if !IsPipe(c.Name) {
			t.Errorf("IsPipe(%q) = false", c.Name)
		}
	}
}
