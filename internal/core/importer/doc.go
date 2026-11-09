// Package importer is the import pipeline every format goes through
// (IMP-10, docs/import.md): a format's Importer reads a file into a Model
// (folders, requests, environments and variables, each with its own
// diagnostics and source location), Preview describes what would be
// created, and Write materialises the model into the open project and
// returns a Report of what was imported, skipped or converted lossily.
//
// Importers never write anything; Write never overwrites anything: it
// creates a new folder (or new items in an existing folder), picks free
// names for folders, files and environments, and keeps typed secrets in
// the workspace's local side files, never in project files.
//
// Formats live in their own packages (internal/core/importer/curlimport,
// and later Postman, Insomnia, OpenAPI, HAR…) and are put together in a
// Registry by the caller. Standard library only.
package importer
