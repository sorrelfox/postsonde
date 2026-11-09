// Package atomicfile writes files so that they are never left half-written:
// a reader, or the next start of PostSonde after a crash, sees either the
// complete old content or the complete new content.
//
// # How
//
// [WriteFile] writes the data to a temporary file named ".<base>.tmp-*"
// in the target's own directory (so it is on the same file system), sets
// its permissions, fsyncs and closes it, and renames it over the target.
// On Unix it then fsyncs the directory so the rename itself is durable.
// On any error before the rename the temporary file is removed and the
// target is not touched.
//
// [WriteJSON] encodes a value with [stablejson.Marshal] and writes it with
// WriteFile.
//
// # Guarantees
//
//   - Atomic replace. On Unix, rename(2) within one file system replaces
//     the target atomically. On Windows, [os.Rename] uses MoveFileEx with
//     MOVEFILE_REPLACE_EXISTING, which replaces an existing target; on
//     NTFS this is atomic in practice, though Windows does not formally
//     promise it.
//   - Durability. The file data is fsynced before the rename (Go uses
//     F_FULLFSYNC on macOS), and on Unix the directory is fsynced after
//     it; a file system that does not support directory fsync (EINVAL) is
//     accepted. Windows has no directory fsync; the rename becomes durable
//     when NTFS flushes its journal.
//   - Concurrent writers. Several WriteFile calls on the same path, from
//     goroutines or processes, each use their own temporary file; the
//     target ends up holding one complete version (the last rename wins).
//     There is no locking and no lost-update detection.
//
// # Limits
//
//   - A crash or power loss before the rename can leave a ".<base>.tmp-*"
//     file next to the target. The target is intact; the leftover is
//     garbage that is safe to delete. Code that lists project directories
//     must ignore such dot-files.
//   - An error from the directory fsync is returned after the rename has
//     happened: the new content is in place but may not be durable yet.
//   - Permissions: an existing target keeps its permission bits (perm is
//     ignored), so a user's chmod survives saves. A new file gets perm
//     exactly, without the process umask being applied. On Windows only
//     the read-only attribute is affected.
//   - The target is replaced by a new file: ownership, extended attributes
//     and ACLs of the old file are not carried over, other hard links keep
//     the old content, and a symbolic link at path is replaced by a
//     regular file rather than followed.
//   - The directory must already exist and be writable.
//   - On Windows, the rename fails if another process holds the target
//     open without FILE_SHARE_DELETE; the error is returned and the target
//     is unchanged.
package atomicfile
