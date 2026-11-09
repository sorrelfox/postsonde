package opid

import (
	"sync"
	"testing"
)

func TestNew(t *testing.T) {
	t.Parallel()

	const n = 10_000
	seen := make(map[string]bool, n)
	for range n {
		id := New()
		if !Valid(id) {
			t.Fatalf("New() = %q, not a valid ID", id)
		}
		if seen[id] {
			t.Fatalf("New() returned %q twice", id)
		}
		seen[id] = true
	}
}

func TestNewConcurrent(t *testing.T) {
	t.Parallel()

	const workers, each = 8, 500
	var mu sync.Mutex
	seen := make(map[string]bool, workers*each)
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for range each {
				id := New()
				mu.Lock()
				dup := seen[id]
				seen[id] = true
				mu.Unlock()
				if dup {
					t.Errorf("duplicate ID %q", id)
				}
			}
		})
	}
	wg.Wait()
}

func TestValid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		id   string
		want bool
	}{
		{"valid", "op_0123456789abcdef", true},
		{"empty", "", false},
		{"no prefix", "0123456789abcdef", false},
		{"wrong prefix", "id_0123456789abcdef", false},
		{"too short", "op_0123456789abcde", false},
		{"too long", "op_0123456789abcdef0", false},
		{"upper case hex", "op_0123456789ABCDEF", false},
		{"not hex", "op_0123456789abcdeg", false},
		{"prefix only", "op_", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := Valid(tt.id); got != tt.want {
				t.Errorf("Valid(%q) = %v, want %v", tt.id, got, tt.want)
			}
		})
	}
}
