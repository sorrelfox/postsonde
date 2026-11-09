package runner

import (
	"strings"
	"sync"
	"testing"

	httpservice "github.com/sorrelfox/postsonde/internal/app/services/http"
)

func response(body string) httpservice.ResponseDTO {
	return httpservice.ResponseDTO{StatusCode: 200, Status: "200 OK", Body: body}
}

func has(s *responseStore, id string, index int) bool {
	_, ok := s.get(id, index)
	return ok
}

// TestResponseStoreLimits: past the count or the size, the oldest
// responses go first, whatever their run.
func TestResponseStoreLimits(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		maxCount int
		maxBytes int64
		body     int
		kept     []int // indexes of run B kept after A's 1–3 and B's 1–3
	}{
		{"by count", 4, 1 << 20, 10, []int{1, 2, 3}},
		{"by size", 100, 3 * jsonSize(response(strings.Repeat("x", 1000))), 1000, []int{1, 2, 3}},
		{"a tighter count", 2, 1 << 20, 10, []int{2, 3}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := newResponseStore(tt.maxCount, tt.maxBytes)
			a, b := s.begin(), s.begin()
			s.name(a, "op_a")
			s.name(b, "op_b")
			for i := 1; i <= 3; i++ {
				s.put(a, i, response(strings.Repeat("a", tt.body)))
			}
			for i := 1; i <= 3; i++ {
				s.put(b, i, response(strings.Repeat("b", tt.body)))
			}
			for i := 1; i <= 3; i++ {
				want := false
				for _, k := range tt.kept {
					want = want || k == i
				}
				if has(s, "op_b", i) != want {
					t.Errorf("op_b %d kept = %v, want %v", i, !want, want)
				}
			}
			n, size := s.usage()
			if n > tt.maxCount || size > tt.maxBytes {
				t.Fatalf("usage = %d, %d over %d, %d", n, size, tt.maxCount, tt.maxBytes)
			}
			// A's oldest went first.
			if has(s, "op_a", 1) {
				t.Error("op_a 1 is still kept")
			}
		})
	}
}

// TestResponseStoreRelease: a released run keeps nothing, now or later;
// a finished run without responses is forgotten.
func TestResponseStoreRelease(t *testing.T) {
	t.Parallel()
	s := newResponseStore(10, 1<<20)
	run := s.begin()
	s.put(run, 1, response("one"))
	s.name(run, "op_1")
	if got, ok := s.get("op_1", 1); !ok || got.Body != "one" {
		t.Fatalf("get = %+v, %v", got, ok)
	}
	if has(s, "op_1", 2) || has(s, "op_2", 1) {
		t.Fatal("found a response never kept")
	}
	s.release("op_1")
	s.put(run, 2, response("two"))
	if n, size := s.usage(); n != 0 || size != 0 || has(s, "op_1", 1) || has(s, "op_1", 2) {
		t.Fatalf("after release: %d responses, %d bytes", n, size)
	}
	s.release("op_unknown")

	// Ended before it was named, with nothing kept: never listed.
	empty := s.begin()
	s.finish(empty)
	s.name(empty, "op_empty")
	// Ended after its last response was dropped: forgotten.
	small := newResponseStore(1, 1<<20)
	x, y := small.begin(), small.begin()
	small.name(x, "op_x")
	small.name(y, "op_y")
	small.put(x, 1, response("x"))
	small.finish(x)
	small.put(y, 1, response("y"))
	small.mu.Lock()
	_, xListed := small.runs["op_x"]
	small.mu.Unlock()
	s.mu.Lock()
	_, emptyListed := s.runs["op_empty"]
	s.mu.Unlock()
	if xListed || emptyListed {
		t.Fatalf("finished runs without responses are still listed: x %v, empty %v", xListed, emptyListed)
	}
	if !has(small, "op_y", 1) {
		t.Fatal("op_y 1 was dropped")
	}
}

// TestResponseStoreConcurrent: runs keep and the UI reads at once.
func TestResponseStoreConcurrent(t *testing.T) {
	t.Parallel()
	s := newResponseStore(50, 1<<20)
	var wg sync.WaitGroup
	for r := range 4 {
		run := s.begin()
		id := "op_" + string(rune('a'+r))
		s.name(run, id)
		wg.Go(func() {
			for i := 1; i <= 100; i++ {
				s.put(run, i, response("body"))
				s.get(id, i-1)
			}
			s.finish(run)
		})
		wg.Go(func() {
			for i := 1; i <= 100; i++ {
				s.get(id, i)
			}
			s.release(id)
		})
	}
	wg.Wait()
	if n, _ := s.usage(); n > 50 {
		t.Fatalf("kept %d responses", n)
	}
}
