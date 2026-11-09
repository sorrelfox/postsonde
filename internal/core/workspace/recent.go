package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/storage/atomicfile"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// RecentLimit is the number of workspaces the recent list keeps.
const RecentLimit = 20

// recentVersion is the version of recent.json.
const recentVersion = 1

// Recent is an entry of the recent-workspaces list, most recent first.
type Recent struct {
	ID   string
	Name string
	// Path is the project folder.
	Path string
	// OpenedAt is when it was last opened.
	OpenedAt time.Time
	// Available is true when the folder still has a postsonde.json.
	Available bool
}

// recentFile is recent.json. It is a cache: a file that cannot be read is
// treated as empty and replaced on the next change.
type recentFile struct {
	Version int `json:"version"`
	// LastOpen is the folder of the workspace that was open when the app
	// last quit, "" after Close.
	LastOpen   string        `json:"lastOpen"`
	Workspaces []recentEntry `json:"workspaces"`
}

type recentEntry struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Path     string    `json:"path"`
	OpenedAt time.Time `json:"openedAt"`
}

// touch moves w to the top of the list and makes it the last open one.
func (r *recentFile) touch(w *Workspace) {
	entries := make([]recentEntry, 0, len(r.Workspaces)+1)
	entries = append(entries, recentEntry{ID: w.ID(), Name: w.Name(), Path: w.Root, OpenedAt: w.OpenedAt.UTC()})
	for _, e := range r.Workspaces {
		if e.Path != w.Root && len(entries) < RecentLimit {
			entries = append(entries, e)
		}
	}
	r.Workspaces = entries
	r.LastOpen = w.Root
}

// Recent returns the recent workspaces, most recent first.
func (m *Manager) Recent() ([]Recent, error) {
	if err := m.checkDataDir(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	r := m.readRecentLocked()
	m.mu.Unlock()
	out := make([]Recent, 0, len(r.Workspaces))
	for _, e := range r.Workspaces {
		out = append(out, Recent{
			ID:        e.ID,
			Name:      e.Name,
			Path:      e.Path,
			OpenedAt:  e.OpenedAt,
			Available: isFile(filepath.Join(e.Path, projectfile.WorkspaceFileName)),
		})
	}
	return out, nil
}

// RemoveRecent removes the folder at path from the recent list. The folder
// and its local data are left alone. Removing a path that is not listed does
// nothing.
func (m *Manager) RemoveRecent(path string) error {
	if err := m.checkDataDir(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.updateRecentLocked(func(r *recentFile) {
		kept := make([]recentEntry, 0, len(r.Workspaces))
		for _, e := range r.Workspaces {
			if e.Path != path {
				kept = append(kept, e)
			}
		}
		r.Workspaces = kept
		if r.LastOpen == path {
			r.LastOpen = ""
		}
	})
}

// ClearRecent empties the recent list except for the open workspace, which
// stays so it is reopened on the next start. Folders and their local data
// are left alone.
func (m *Manager) ClearRecent() error {
	if err := m.checkDataDir(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	open := ""
	if m.current != nil {
		open = m.current.Root
	}
	return m.updateRecentLocked(func(r *recentFile) {
		kept := make([]recentEntry, 0, 1)
		for _, e := range r.Workspaces {
			if open != "" && e.Path == open {
				kept = append(kept, e)
			}
		}
		r.Workspaces = kept
		if r.LastOpen != open {
			r.LastOpen = ""
		}
	})
}

// Known reports whether path is the folder of the open workspace or of an
// entry of the recent list.
func (m *Manager) Known(path string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current != nil && m.current.Root == path {
		return true
	}
	if m.dataDir == "" {
		return false
	}
	for _, e := range m.readRecentLocked().Workspaces {
		if e.Path == path {
			return true
		}
	}
	return false
}

func (m *Manager) recentPath() string { return filepath.Join(m.baseDir(), RecentFileName) }

func (m *Manager) readRecentLocked() recentFile {
	var r recentFile
	data, err := os.ReadFile(m.recentPath())
	if err != nil || json.Unmarshal(data, &r) != nil {
		return recentFile{}
	}
	return r
}

func (m *Manager) updateRecentLocked(change func(*recentFile)) error {
	r := m.readRecentLocked()
	change(&r)
	r.Version = recentVersion
	if r.Workspaces == nil {
		r.Workspaces = []recentEntry{}
	}
	if err := os.MkdirAll(m.baseDir(), localDirPerm); err != nil {
		return localDataError(err, m.baseDir())
	}
	if err := atomicfile.WriteJSON(m.recentPath(), r, localFilePerm); err != nil {
		return localDataError(err, m.recentPath())
	}
	return nil
}
