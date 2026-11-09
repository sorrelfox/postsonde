package app

import (
	"context"
	"errors"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/sorrelfox/postsonde/internal/app/services/collection"
	"github.com/sorrelfox/postsonde/internal/app/services/datadir"
	graphqlservice "github.com/sorrelfox/postsonde/internal/app/services/graphql"
	"github.com/sorrelfox/postsonde/internal/app/services/grpc"
	httpservice "github.com/sorrelfox/postsonde/internal/app/services/http"
	"github.com/sorrelfox/postsonde/internal/app/services/servers"
	"github.com/sorrelfox/postsonde/internal/app/services/settings"
	"github.com/sorrelfox/postsonde/internal/app/services/workspace"
)

// dialogParent is the window a dialog asked for by a call with ctx is
// attached to: the window the call came from (APP-06), else the active
// one.
func dialogParent(ctx context.Context, a *application.App) application.Window {
	if w, ok := ctx.Value(application.WindowKey).(application.Window); ok && w != nil {
		return w
	}
	return a.Window.Current()
}

// pickFolder shows the native folder dialog (workspace.FolderPicker), or
// the file dialog when d.ChooseFile is set, attached to the calling window
// as a sheet where the OS supports it. It returns "" when the user
// cancels. The native dialog cannot be closed from Go, so ctx is not
// honoured once it is shown.
func pickFolder(ctx context.Context, d workspace.FolderDialog) (string, error) {
	a := application.Get()
	if a == nil {
		return "", errors.New("the application is not running")
	}
	return a.Dialog.OpenFileWithOptions(&application.OpenFileDialogOptions{
		CanChooseDirectories: !d.ChooseFile,
		CanChooseFiles:       d.ChooseFile,
		CanCreateDirectories: !d.ChooseFile,
		ResolvesAliases:      true,
		Title:                d.Title,
		Message:              d.Message,
		ButtonText:           d.ButtonText,
		Window:               dialogParent(ctx, a),
	}).PromptForSingleSelection()
}

// pickDataFolder shows the native folder dialog for a new data directory
// (datadir.FolderPicker), attached to the calling window. It returns ""
// when the user cancels.
func pickDataFolder(ctx context.Context, d datadir.FolderDialog) (string, error) {
	a := application.Get()
	if a == nil {
		return "", errors.New("the application is not running")
	}
	return a.Dialog.OpenFileWithOptions(&application.OpenFileDialogOptions{
		CanChooseDirectories: true,
		CanCreateDirectories: true,
		ResolvesAliases:      true,
		Title:                d.Title,
		Message:              d.Message,
		ButtonText:           d.ButtonText,
		Directory:            d.Directory,
		Window:               dialogParent(ctx, a),
	}).PromptForSingleSelection()
}

// pickImportFile shows the native dialog to choose a file to import
// (collection.FilePicker). It returns "" when the user cancels.
func pickImportFile(ctx context.Context, d collection.FileDialog) (string, error) {
	return pickFolder(ctx, workspace.FolderDialog{Title: d.Title, Message: d.Message, ButtonText: d.ButtonText, ChooseFile: true})
}

// pickPaths shows the native dialog a grpc.PathPicker describes: several
// files with a filter, or one folder. It returns no paths when the user
// cancels.
func pickPaths(ctx context.Context, d grpc.Dialog) ([]string, error) {
	a := application.Get()
	if a == nil {
		return nil, errors.New("the application is not running")
	}
	opts := &application.OpenFileDialogOptions{
		CanChooseDirectories:    d.Folder,
		CanChooseFiles:          !d.Folder,
		CanCreateDirectories:    d.Folder,
		AllowsMultipleSelection: d.Multiple,
		ResolvesAliases:         true,
		Title:                   d.Title,
		Message:                 d.Message,
		ButtonText:              d.ButtonText,
		Directory:               d.Directory,
		Window:                  dialogParent(ctx, a),
	}
	if d.FilterPattern != "" {
		opts.Filters = []application.FileFilter{{DisplayName: d.FilterName, Pattern: d.FilterPattern}}
	}
	dialog := a.Dialog.OpenFileWithOptions(opts)
	if d.Multiple {
		return dialog.PromptForMultipleSelection()
	}
	path, err := dialog.PromptForSingleSelection()
	if err != nil || path == "" {
		return nil, err
	}
	return []string{path}, nil
}

// pickFile shows the native dialog for one file a settings.FileDialog
// describes (a custom CA bundle), or one folder with d.Folder (an allowed
// directory), attached to the calling window. It returns "" when the user
// cancels.
func pickFile(ctx context.Context, d settings.FileDialog) (string, error) {
	a := application.Get()
	if a == nil {
		return "", errors.New("the application is not running")
	}
	opts := &application.OpenFileDialogOptions{
		CanChooseFiles:       !d.Folder,
		CanChooseDirectories: d.Folder,
		CanCreateDirectories: d.Folder,
		ResolvesAliases:      true,
		Title:                d.Title,
		Message:              d.Message,
		ButtonText:           d.ButtonText,
		Window:               dialogParent(ctx, a),
	}
	if d.FilterPattern != "" && !d.Folder {
		opts.Filters = []application.FileFilter{{DisplayName: d.FilterName, Pattern: d.FilterPattern}}
	}
	return a.Dialog.OpenFileWithOptions(opts).PromptForSingleSelection()
}

// pickSchemaFile shows the native dialog for a GraphQL SDL file
// (graphqlservice.FilePicker). It returns "" when the user cancels.
func pickSchemaFile(ctx context.Context, d graphqlservice.FileDialog) (string, error) {
	paths, err := pickPaths(ctx, grpc.Dialog{
		Title: d.Title, Message: d.Message, ButtonText: d.ButtonText,
		FilterName: d.FilterName, FilterPattern: d.FilterPattern, Directory: d.Directory,
	})
	if err != nil || len(paths) == 0 {
		return "", err
	}
	return paths[0], nil
}

// pickOpenAPIFile shows the native dialog for an OpenAPI document to
// generate mock routes from (servers.FilePicker). It returns "" when the
// user cancels.
func pickOpenAPIFile(ctx context.Context, d servers.FileDialog) (string, error) {
	return pickFile(ctx, settings.FileDialog{
		Title: d.Title, Message: d.Message, ButtonText: d.ButtonText,
		FilterName: d.FilterName, FilterPattern: d.FilterPattern,
	})
}

// saveFile shows the native save dialog an httpservice.SaveDialog
// describes, attached to the calling window, and returns the chosen path,
// or "" when the user cancels. The OS asks before replacing a file.
func saveFile(ctx context.Context, d httpservice.SaveDialog) (string, error) {
	a := application.Get()
	if a == nil {
		return "", errors.New("the application is not running")
	}
	return a.Dialog.SaveFileWithOptions(&application.SaveFileDialogOptions{
		CanCreateDirectories: true,
		Title:                d.Title,
		Message:              d.Message,
		ButtonText:           d.ButtonText,
		Filename:             d.Filename,
		Window:               dialogParent(ctx, a),
	}).PromptForSingleSelection()
}

// openPath opens path in its default application (settings.Desktop.Open).
func openPath(path string) error {
	a := application.Get()
	if a == nil {
		return errors.New("the application is not running")
	}
	return a.Browser.OpenFile(path)
}

// openURL opens url in the system browser (Browser.OpenURL): the OAuth 2.0
// sign-in of the authorization code grant (AUTH-07), which the user asked
// for.
func openURL(url string) error {
	a := application.Get()
	if a == nil {
		return errors.New("the application is not running")
	}
	return a.Browser.OpenURL(url)
}

// revealPath shows path, selected, in the file manager
// (settings.Desktop.Reveal).
func revealPath(path string) error {
	a := application.Get()
	if a == nil {
		return errors.New("the application is not running")
	}
	return a.Env.OpenFileManager(path, true)
}
