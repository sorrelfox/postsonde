package http

import (
	"errors"

	"github.com/sorrelfox/postsonde/internal/app/services/collection"
	"github.com/sorrelfox/postsonde/internal/core/fileaccess"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

// File contents (DYN-08): {{$file PATH}} inserts a local file, read when
// the request is built, from the open project's folder or an allowed
// directory of the settings (PRV-10, fileaccess). Sends, repeats, SSE
// streams, GraphQL subscriptions and schema fetches, Copy as curl and HAR
// export read the file; the header preview, which is computed after every
// edit, and the other previews leave the reference as written
// (docs/templates.md#file-contents).

// WithAllowedDirs gives the allow-listed directories of the settings
// (privacy.allowedDirs), read on every build: {{$file}}, the JWT key file
// and netrc file of an auth, and the schema files of matches_schema
// assertions are read from them and the project folder only. Without it
// only the open project's folder is allowed.
func WithAllowedDirs(dirs func() []string) Option {
	return func(s *HTTPService) { s.allowedDirs = dirs }
}

// fileReader is what {{$file}} references of src read through: the
// allowlist of the settings and src's project folder.
func (s *HTTPService) fileReader(src collection.HTTPSource) template.FileReader {
	return s.filePolicy(src)
}

// filePolicy is what the files src names are read through (PRV-10): its
// project folder and the allowed directories of the settings. The
// {{$file}} references, the JWT key file and netrc file of its auth and
// the schema files of its assertions all use it.
func (s *HTTPService) filePolicy(src collection.HTTPSource) fileaccess.Policy {
	return fileaccess.Policy{ProjectRoot: workspaceRoot(src), Allowed: s.allowed()}
}

// allowed are the allowed directories of the settings, read now.
func (s *HTTPService) allowed() []string {
	if s.allowedDirs == nil {
		return nil
	}
	return s.allowedDirs()
}

// filesDeferred reports whether p is a {{$file}} reference rendered
// without a reader (a preview), which a preview leaves as written.
func filesDeferred(p template.Problem) bool {
	return p.Kind == template.ProblemFile && errors.Is(p.Cause, template.ErrFilesUnavailable)
}
