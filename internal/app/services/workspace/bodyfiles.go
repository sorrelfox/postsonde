package workspace

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/request"
)

// PickBodyFile shows the native file dialog for a file to send as a
// request body or a multipart part (HTTP-14, HTTP-15) and describes the
// chosen file. Its Path is what the request stores: relative to the open
// project's root when the file is inside it, else absolute
// (docs/file-format.md). A cancelled dialog returns a DTO with an empty
// Path. The file is only looked at, never read or copied.
func (s *WorkspaceService) PickBodyFile(ctx context.Context) (BodyFileDTO, error) {
	return s.pickProjectFile(ctx, FolderDialog{Title: "Choose File", Message: "Choose the file to send.", ButtonText: "Choose", ChooseFile: true})
}

// PickSchemaFile shows the native file dialog for the JSON Schema file of
// a matches_schema assertion (TEST-07) and describes the chosen file, its
// Path stored like a body file's: relative to the open project's root
// when inside it, else absolute. A cancelled dialog returns a DTO with an
// empty Path.
func (s *WorkspaceService) PickSchemaFile(ctx context.Context) (BodyFileDTO, error) {
	return s.pickProjectFile(ctx, FolderDialog{Title: "Choose JSON Schema", Message: "Choose the JSON Schema file to check responses against.", ButtonText: "Choose", ChooseFile: true})
}

func (s *WorkspaceService) pickProjectFile(ctx context.Context, dialog FolderDialog) (BodyFileDTO, error) {
	if err := ctx.Err(); err != nil {
		return BodyFileDTO{}, errs.Wrap(err, errs.KindOf(err), "Choosing a file was interrupted.")
	}
	if s.pick == nil {
		return BodyFileDTO{}, errs.New(errs.Unsupported, "File dialogs are not available.")
	}
	abs, err := s.pick(ctx, dialog)
	if err != nil {
		return BodyFileDTO{}, errs.Wrap(err, errs.KindOf(err), "The file dialog could not be shown.")
	}
	if abs == "" {
		return BodyFileDTO{}, nil
	}
	root := s.root()
	return describeBodyFile(root, request.StoredFilePath(root, abs)), nil
}

// BodyFile describes the file a request stores as path (relative to the
// open project, or absolute): its name, size and whether it exists. A
// missing file is not an error: Exists is false.
func (s *WorkspaceService) BodyFile(ctx context.Context, path string) (BodyFileDTO, error) {
	if err := ctx.Err(); err != nil {
		return BodyFileDTO{}, errs.Wrap(err, errs.KindOf(err), "Checking the file was interrupted.")
	}
	return describeBodyFile(s.root(), path), nil
}

// root is the open workspace's root, or "".
func (s *WorkspaceService) root() string {
	if w := s.manager.Current(); w != nil {
		return w.Root
	}
	return ""
}

func describeBodyFile(root, stored string) BodyFileDTO {
	if stored == "" {
		return BodyFileDTO{}
	}
	dto := BodyFileDTO{Path: stored, Name: path.Base(strings.ReplaceAll(stored, `\`, "/"))}
	abs := request.ResolveFilePath(root, stored)
	if !filepath.IsAbs(abs) {
		return dto
	}
	dto.AbsolutePath = abs
	if info, err := os.Stat(abs); err == nil && info.Mode().IsRegular() {
		dto.Size, dto.Exists = info.Size(), true
	}
	return dto
}
