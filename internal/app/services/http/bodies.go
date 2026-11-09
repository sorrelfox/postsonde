package http

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Retention of received bodies for SaveBody (HTTP-34), FilterBody
// (HTTP-36) and ReadBody (HTTP-37).
const (
	// MaxKeptBodies is how many response bodies are kept for saving: the
	// latest ones, one per send.
	MaxKeptBodies = 8
	// BodyTTL is how long a body is kept for saving after it arrived.
	BodyTTL = time.Hour
	// SpoolThreshold is the largest body kept in memory; larger ones are
	// written to the spool directory while they arrive (HTTP-37) and kept
	// there until they are saved or dropped.
	SpoolThreshold = MaxBodyText
	// HeadBytes is how much of the start of a body a spool keeps in
	// memory for the view (ResponseDTO.Body), the history and the
	// assertions on small bodies: MaxBodyText and the longest byte order
	// mark.
	HeadBytes = MaxBodyText + 4
)

// keptBody is a response body kept for SaveBody: in memory (data) or in
// a spool file (path).
type keptBody struct {
	id   string
	data []byte
	path string
	size int64
	// name is the suggested file name.
	name string
	at   time.Time
	// exchange is the rest of the send's exchange, for SaveExchange
	// (exchange.go); nil when not attached.
	exchange *keptExchange
}

// bodyStore keeps the bytes of the latest responses so the user can save,
// filter and page through them after the view clipped them (HTTP-34,
// HTTP-37). Bodies over SpoolThreshold go to files in dir (0600, under
// the app data directory) while they arrive, so they are never whole in
// memory; with dir "" they are kept in memory. Files are removed when
// their body is dropped or its send fails, at shutdown, and (leftovers of
// a crash) when the store is created.
type bodyStore struct {
	dir string
	now func() time.Time

	mu   sync.Mutex
	list []*keptBody // oldest first
}

func newBodyStore(dir string) *bodyStore {
	s := &bodyStore{dir: dir, now: time.Now}
	if dir != "" {
		s.removeSpool()
	}
	return s
}

// put keeps body under a new ID and returns the ID. A body that cannot
// be spooled is kept in memory.
func (s *bodyStore) put(body []byte, name string) string {
	sp := s.newSpool()
	if _, err := sp.Write(body); err != nil {
		sp.abort()
		sp = &spool{id: newBodyID(), mem: true, data: body, n: int64(len(body))}
	}
	return s.keep(sp, name)
}

// keep closes sp and keeps its body under sp's ID, which it returns.
func (s *bodyStore) keep(sp *spool, name string) string {
	sp.close()
	kb := &keptBody{id: sp.id, data: sp.data, path: sp.path, size: sp.n, name: name, at: s.now()}
	s.mu.Lock()
	s.list = append(s.list, kb)
	dropped := s.expireLocked()
	s.mu.Unlock()
	drop(dropped)
	return kb.id
}

// open returns the body with id and a reader of its bytes, which the
// caller closes; ok is false when it is not kept (any more).
func (s *bodyStore) open(id string) (kb keptBody, r io.ReadSeekCloser, ok bool, err error) {
	s.mu.Lock()
	dropped := s.expireLocked()
	var found *keptBody
	for _, b := range s.list {
		if b.id == id {
			found = b
			break
		}
	}
	var f *os.File
	if found != nil && found.path != "" {
		// Opened under the lock, so a concurrent drop cannot remove it
		// first (removing an open file is fine where it is allowed).
		f, err = os.Open(found.path)
	}
	s.mu.Unlock()
	drop(dropped)
	switch {
	case found == nil:
		return keptBody{}, nil, false, nil
	case err != nil:
		return keptBody{}, nil, false, err
	case f != nil:
		return *found, f, true, nil
	}
	return *found, nopSeekCloser{bytes.NewReader(found.data)}, true, nil
}

// attach keeps ex with the body id, for SaveExchange. A body no longer
// kept is left alone.
func (s *bodyStore) attach(id string, ex *keptExchange) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range s.list {
		if b.id == id {
			b.exchange = ex
			return
		}
	}
}

type nopSeekCloser struct{ *bytes.Reader }

func (nopSeekCloser) Close() error { return nil }

// close drops every body and removes the spool directory.
func (s *bodyStore) close() {
	s.mu.Lock()
	s.list = nil
	s.mu.Unlock()
	if s.dir != "" {
		s.removeSpool()
	}
}

// expireLocked removes bodies over MaxKeptBodies and older than BodyTTL
// from the list and returns them.
func (s *bodyStore) expireLocked() []*keptBody {
	cutoff := s.now().Add(-BodyTTL)
	var dropped []*keptBody
	kept := s.list[:0]
	for i, b := range s.list {
		if len(s.list)-i > MaxKeptBodies || b.at.Before(cutoff) {
			dropped = append(dropped, b)
			continue
		}
		kept = append(kept, b)
	}
	clear(s.list[len(kept):])
	s.list = kept
	return dropped
}

// drop removes the spool files of dropped bodies.
func drop(dropped []*keptBody) {
	for _, b := range dropped {
		if b.path != "" {
			_ = os.Remove(b.path)
		}
	}
}

// removeSpool deletes the spool directory and what is in it.
func (s *bodyStore) removeSpool() { _ = os.RemoveAll(s.dir) }

// newBodyID returns a random ID for a kept body.
func newBodyID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "body_" + hex.EncodeToString(b[:])
}

// spool receives a body while it arrives (the writer of an
// httpclient.BodyTarget): in memory up to SpoolThreshold bytes, then in a
// new file of the store's directory (0600), written through as the bytes
// come, so a body of any size needs little memory (HTTP-37). The first
// HeadBytes stay in memory either way. A spool is kept with
// bodyStore.keep or discarded with abort; it is not safe for concurrent
// use.
type spool struct {
	id  string
	dir string
	// mem keeps the whole body in memory (no directory).
	mem  bool
	data []byte
	head []byte
	f    *os.File
	path string
	n    int64
}

// newSpool returns a spool for a new body. A nil store, or one without a
// directory, gives a spool that keeps the body in memory.
func (s *bodyStore) newSpool() *spool {
	sp := &spool{id: newBodyID(), mem: true}
	if s != nil && s.dir != "" {
		sp.dir, sp.mem = s.dir, false
	}
	return sp
}

func (sp *spool) Write(p []byte) (int, error) {
	if len(sp.head) < HeadBytes {
		sp.head = append(sp.head, p[:min(len(p), HeadBytes-len(sp.head))]...)
	}
	if sp.f == nil && (sp.mem || sp.n+int64(len(p)) <= SpoolThreshold) {
		sp.data = append(sp.data, p...)
		sp.n += int64(len(p))
		return len(p), nil
	}
	if sp.f == nil {
		if err := sp.create(); err != nil {
			return 0, err
		}
	}
	n, err := sp.f.Write(p)
	sp.n += int64(n)
	return n, err
}

// create moves the body received so far to a new file.
func (sp *spool) create() error {
	if err := os.MkdirAll(sp.dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(sp.dir, sp.id+".body")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(sp.data); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return err
	}
	sp.f, sp.path, sp.data = f, path, nil
	return nil
}

// close closes the file, if any.
func (sp *spool) close() {
	if sp.f != nil {
		_ = sp.f.Close()
		sp.f = nil
	}
}

// abort discards the body of a failed or cancelled send, removing its
// file.
func (sp *spool) abort() {
	sp.close()
	if sp.path != "" {
		_ = os.Remove(sp.path)
		sp.path = ""
	}
	sp.data, sp.head = nil, nil
}

// headBytes returns the first HeadBytes of the body.
func (sp *spool) headBytes() []byte {
	if sp.head == nil {
		return []byte{}
	}
	return sp.head
}

// readAll returns the whole body, reading it back from its file. Call it
// after close.
func (sp *spool) readAll() ([]byte, error) {
	if sp.path == "" {
		return sp.data, nil
	}
	return os.ReadFile(sp.path)
}
