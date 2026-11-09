//go:build !unix

package atomicfile

// syncDir is a no-op where directories cannot be fsynced, notably Windows:
// there, MoveFileEx updates the directory entry through the NTFS journal.
func syncDir(string) error { return nil }
