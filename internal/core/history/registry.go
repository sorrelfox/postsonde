package history

import (
	"path/filepath"
	"sync"
)

// Registry keeps one Store per workspace, so every writer of a directory
// shares its index and lock. It is safe for concurrent use.
type Registry struct {
	lim Limits

	mu     sync.Mutex
	stores map[string]*Store
}

// NewRegistry returns a registry whose stores use lim.
func NewRegistry(lim Limits) *Registry {
	return &Registry{lim: lim, stores: map[string]*Store{}}
}

// Store returns the store of the workspace whose local directory is
// localDir (its DirName subdirectory), opened on first use.
func (r *Registry) Store(localDir string) (*Store, error) {
	dir := filepath.Join(localDir, DirName)
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.stores[dir]; ok {
		return s, nil
	}
	s, err := Open(dir, r.lim)
	if err != nil {
		return nil, err
	}
	r.stores[dir] = s
	return s, nil
}

// Close closes the open segment file of every store.
func (r *Registry) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var first error
	for _, s := range r.stores {
		if err := s.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}
