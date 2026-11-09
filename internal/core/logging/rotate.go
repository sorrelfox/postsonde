package logging

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

const (
	// FileName is the name of the log file inside the logs directory.
	FileName = "postsonde.log"
	// DefaultMaxSize is the default size cap of the log file: 5 MiB.
	DefaultMaxSize int64 = 5 << 20
	// DefaultMaxBackups is the default number of rotated files kept.
	DefaultMaxBackups = 3
)

// FileOptions configures OpenFile.
type FileOptions struct {
	// Path of the log file. Its directory is created (0700) if missing.
	Path string
	// MaxSize is the size cap in bytes; zero or less means DefaultMaxSize.
	MaxSize int64
	// MaxBackups is the number of rotated files to keep: zero means
	// DefaultMaxBackups, a negative value keeps none (the file is truncated
	// when it reaches the cap).
	MaxBackups int
}

// RotatingFile is a log file that rotates when it reaches its size cap. It is
// safe for concurrent use.
type RotatingFile struct {
	path       string
	maxSize    int64
	maxBackups int

	mu   sync.Mutex
	f    *os.File // nil once closed
	size int64
}

// OpenFile opens (or creates) the log file at opts.Path for appending.
func OpenFile(opts FileOptions) (*RotatingFile, error) {
	if opts.Path == "" {
		return nil, errors.New("logging: OpenFile: empty path")
	}
	rf := &RotatingFile{
		path:       opts.Path,
		maxSize:    opts.MaxSize,
		maxBackups: opts.MaxBackups,
	}
	if rf.maxSize <= 0 {
		rf.maxSize = DefaultMaxSize
	}
	if rf.maxBackups == 0 {
		rf.maxBackups = DefaultMaxBackups
	}
	if err := os.MkdirAll(filepath.Dir(rf.path), 0o700); err != nil {
		return nil, fmt.Errorf("logging: %w", err)
	}
	if err := rf.open(0); err != nil {
		return nil, err
	}
	return rf, nil
}

// Path returns the path of the current log file.
func (rf *RotatingFile) Path() string { return rf.path }

// Write appends p to the log file, rotating first if p would take the file
// past its size cap. p is never split across files.
//
// If rotation fails, p is still written to the file that is open (which may
// then exceed the cap) and the rotation error is returned.
func (rf *RotatingFile) Write(p []byte) (int, error) {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	if rf.f == nil {
		return 0, fmt.Errorf("logging: write %s: %w", rf.path, os.ErrClosed)
	}
	var rotErr error
	if rf.size > 0 && rf.size+int64(len(p)) > rf.maxSize {
		rotErr = rf.rotate()
		if rf.f == nil {
			return 0, rotErr
		}
	}
	n, err := rf.f.Write(p)
	rf.size += int64(n)
	if err != nil {
		return n, fmt.Errorf("logging: %w", err)
	}
	return n, rotErr
}

// Sync commits the log file to stable storage.
func (rf *RotatingFile) Sync() error {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	if rf.f == nil {
		return nil
	}
	return rf.f.Sync()
}

// Close closes the log file. Later writes fail; closing again is a no-op.
func (rf *RotatingFile) Close() error {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	if rf.f == nil {
		return nil
	}
	err := rf.f.Close()
	rf.f = nil
	return err
}

// open opens the log file with the extra flag (0 or os.O_TRUNC) and records
// its size. The caller holds mu or owns rf exclusively.
func (rf *RotatingFile) open(flag int) error {
	f, err := os.OpenFile(rf.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND|flag, 0o600)
	if err != nil {
		return fmt.Errorf("logging: %w", err)
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return fmt.Errorf("logging: %w", err)
	}
	rf.f, rf.size = f, fi.Size()
	return nil
}

// rotate shifts the backups, moves the current file to backup 1 and opens a
// fresh file. The caller holds mu. On failure it still tries to leave a file
// open so logging can continue.
func (rf *RotatingFile) rotate() error {
	var errs []error
	if err := rf.f.Close(); err != nil {
		errs = append(errs, err)
	}
	rf.f = nil

	flag := 0
	if rf.maxBackups < 0 {
		flag = os.O_TRUNC
	} else {
		if err := os.Remove(rf.backup(rf.maxBackups)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
		for i := rf.maxBackups - 1; i >= 1; i-- {
			if err := os.Rename(rf.backup(i), rf.backup(i+1)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, err)
			}
		}
		// If this rename fails the old file is reopened and appended to,
		// rather than truncated and lost.
		if err := os.Rename(rf.path, rf.backup(1)); err != nil {
			errs = append(errs, err)
		}
	}
	if err := rf.open(flag); err != nil {
		errs = append(errs, err)
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("logging: rotate %s: %w", rf.path, err)
	}
	return nil
}

func (rf *RotatingFile) backup(i int) string {
	return rf.path + "." + strconv.Itoa(i)
}
