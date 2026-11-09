package app

import (
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"github.com/sorrelfox/postsonde/internal/app/services/collection"
)

// Files dropped on the window (IMP-11, docs/import.md#dropped-files).
//
// Every window has EnableFileDrop: Wails takes files dragged from the
// OS (natively on macOS and Linux, through WebView2 on Windows) and, when
// they are dropped on an element with a data-file-drop-target attribute,
// raises WindowFilesDropped with their real paths and that element's
// attributes. A drop anywhere else is ignored by Wails. Go only forwards
// the drop to the UI as collection:files-dropped; the UI decides what it
// means (the window's own target, "import", opens the Import dialog).

// dropTargetAttribute is the attribute Wails' runtime looks for.
const dropTargetAttribute = "data-file-drop-target"

// forwardFileDrops sends every file drop on w to w's page only: with
// several windows (APP-06) the others must not open an Import dialog.
func forwardFileDrops(w *application.WebviewWindow) {
	w.OnWindowEvent(events.Common.WindowFilesDropped, func(e *application.WindowEvent) {
		ctx := e.Context()
		dto, ok := filesDropped(ctx.DroppedFiles(), ctx.DropTargetDetails())
		if ok {
			w.DispatchWailsEvent(&application.CustomEvent{Name: collection.EventFilesDropped, Data: dto})
		}
	})
}

// filesDropped is the event payload of a drop of files on target; false
// when there is nothing to send (no files).
func filesDropped(files []string, target *application.DropTargetDetails) (collection.FilesDroppedDTO, bool) {
	paths := make([]string, 0, len(files))
	for _, f := range files {
		if f != "" {
			paths = append(paths, f)
		}
	}
	if len(paths) == 0 {
		return collection.FilesDroppedDTO{}, false
	}
	name := ""
	if target != nil {
		name = target.Attributes[dropTargetAttribute]
	}
	return collection.FilesDroppedDTO{Paths: paths, Target: name}, true
}
