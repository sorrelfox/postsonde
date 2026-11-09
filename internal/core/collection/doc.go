// Package collection reads and writes the request tree of a project
// (STO-02): folders are directories with a _folder.json, each request is
// one JSON file, and the project root, marked by postsonde.json, is the
// top of the tree. The file contents are package projectfile's;
// docs/file-format.md describes them and docs/collection.md the tree.
//
// # Loading
//
// [Load] walks the project directory into a [Tree] of [Node] values. It is
// tolerant: a file that does not load becomes a [Problem] and, where it
// has a place in the tree, a read-only node of kind [KindInvalid]; a file
// of a kind this version does not know becomes a read-only [KindUnknown]
// node that is never written. Loading never writes.
//
// Every node has an ID, normally the file's id. When files share an id
// (a file copied by hand) the one with the smallest path keeps it and the
// others get an ID derived from the id and their path; a file without an
// id gets one derived from its path. Both are reported as problems and
// written into the file the next time it is saved.
//
// # Saving
//
// [Tree.SaveRequest], [Tree.SaveFolder] and [Tree.SaveWorkspace] replace a
// node's file and write it only when its encoding differs from the bytes
// on disk, so saving an unchanged tree ([Tree.SaveAll]) touches no file.
//
// # Operations
//
// [Tree.CreateFolder], [Tree.CreateRequest], [Tree.Rename],
// [Tree.Duplicate], [Tree.Move] and [Tree.Delete] change the files and
// keep the order member of every folder they touch complete, then reload
// the tree. Delete moves the item into a [Trash] outside the project, from
// which [Tree.Restore] puts it back.
//
// # Names
//
// [Slug] turns an item's name into an ASCII file name base and
// [UniqueName] makes it unique in a directory, ignoring case and Unicode
// normalization, and avoiding reserved names on every OS.
package collection
