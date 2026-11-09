package runner

import (
	"container/list"
	"encoding/json"
	"sync"

	httpservice "github.com/sorrelfox/postsonde/internal/app/services/http"
)

// The responses of runs (RUN-07): a result of a run opens its request and
// response (RunnerService.Response). The runner keeps the ResponseDTO of
// every HTTP and GraphQL request a run sent, as Send returns it (the body
// as the response view shows it, at most httpservice.MaxBodyText of it,
// with its pretty form, headers, cookies, timeline with the request as
// sent, and assertion results), in memory only. Memory is bounded for the
// whole app, not per run: at most MaxKeptResponses responses and about
// MaxKeptResponseBytes bytes (the size of their JSON) over every run,
// the oldest dropped first. The UI releases a run's responses when it
// forgets the run (a new run of the folder, its tab closed). See
// docs/runner.md.
const (
	// MaxKeptResponses is the most responses kept over every run.
	MaxKeptResponses = 1000
	// MaxKeptResponseBytes is about the most memory the kept responses
	// take, measured as their JSON size.
	MaxKeptResponseBytes = 64 << 20
)

// CodeResponseGone: the response of a result is not kept (any more): it
// was dropped for newer ones, its run was released, or the result has no
// response (kind not_found). Details: opId, index.
const CodeResponseGone = "runner.response_gone"

// responseStore keeps the responses of the runs, oldest dropped first
// past its limits. It is safe for concurrent use.
type responseStore struct {
	maxCount int
	maxBytes int64

	mu    sync.Mutex
	runs  map[string]*keptRun
	queue *list.List // of *keptResponse, oldest first
	bytes int64
}

// keptRun is the responses of one run, by the result's index.
type keptRun struct {
	id       string // "" until named
	byIndex  map[int]*list.Element
	finished bool
	released bool
}

type keptResponse struct {
	run   *keptRun
	index int
	size  int64
	resp  httpservice.ResponseDTO
}

func newResponseStore(maxCount int, maxBytes int64) *responseStore {
	return &responseStore{maxCount: maxCount, maxBytes: maxBytes, runs: map[string]*keptRun{}, queue: list.New()}
}

// begin starts keeping the responses of a new run, which name gives its
// operation ID once it has one.
func (s *responseStore) begin() *keptRun {
	return &keptRun{byIndex: map[int]*list.Element{}}
}

// name gives run its operation ID, so Response finds it.
func (s *responseStore) name(run *keptRun, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run.id = id
	if run.released || (run.finished && len(run.byIndex) == 0) {
		return
	}
	s.runs[id] = run
}

// finish marks run as ended: once it has no response left it is
// forgotten.
func (s *responseStore) finish(run *keptRun) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run.finished = true
	s.forgetIfEmpty(run)
}

// put keeps resp as the response of result index of run, dropping the
// oldest responses past the limits.
func (s *responseStore) put(run *keptRun, index int, resp httpservice.ResponseDTO) {
	size := jsonSize(resp)
	s.mu.Lock()
	defer s.mu.Unlock()
	if run.released || size > s.maxBytes {
		return
	}
	if old, ok := run.byIndex[index]; ok {
		s.drop(old)
	}
	run.byIndex[index] = s.queue.PushBack(&keptResponse{run: run, index: index, size: size, resp: resp})
	s.bytes += size
	for s.queue.Len() > s.maxCount || s.bytes > s.maxBytes {
		oldest := s.queue.Front()
		s.drop(oldest)
		s.forgetIfEmpty(oldest.Value.(*keptResponse).run)
	}
}

// get returns the response of result index of run id.
func (s *responseStore) get(id string, index int) (httpservice.ResponseDTO, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[id]
	if !ok {
		return httpservice.ResponseDTO{}, false
	}
	el, ok := run.byIndex[index]
	if !ok {
		return httpservice.ResponseDTO{}, false
	}
	return el.Value.(*keptResponse).resp, true
}

// release drops every response of run id; later puts of it are ignored.
func (s *responseStore) release(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[id]
	if !ok {
		return
	}
	run.released = true
	for _, el := range run.byIndex {
		s.drop(el)
	}
	delete(s.runs, id)
}

// usage returns the number of responses kept and their size.
func (s *responseStore) usage() (int, int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.queue.Len(), s.bytes
}

// drop removes el from the queue and its run. s.mu is held.
func (s *responseStore) drop(el *list.Element) {
	k := el.Value.(*keptResponse)
	s.queue.Remove(el)
	delete(k.run.byIndex, k.index)
	s.bytes -= k.size
}

// forgetIfEmpty forgets a finished run without responses. s.mu is held.
func (s *responseStore) forgetIfEmpty(run *keptRun) {
	if run.finished && len(run.byIndex) == 0 && run.id != "" && s.runs[run.id] == run {
		delete(s.runs, run.id)
	}
}

// jsonSize is the size of v as JSON, as the UI gets it.
func jsonSize(v any) int64 {
	var c counter
	if err := json.NewEncoder(&c).Encode(v); err != nil {
		return 0
	}
	return int64(c)
}

type counter int64

func (c *counter) Write(p []byte) (int, error) {
	*c += counter(len(p))
	return len(p), nil
}
