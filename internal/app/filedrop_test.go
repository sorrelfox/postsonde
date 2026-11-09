package app

import (
	"slices"
	"testing"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// The main window takes file drops (IMP-11).
func TestMainWindowTakesFileDrops(t *testing.T) {
	t.Parallel()
	if !mainWindowOptions(darkBackground).EnableFileDrop {
		t.Error("EnableFileDrop is off")
	}
}

// A drop is forwarded with its paths and the target element's
// data-file-drop-target value; empty drops are not.
func TestFilesDropped(t *testing.T) {
	t.Parallel()
	target := &application.DropTargetDetails{
		ElementID:  "shell",
		Attributes: map[string]string{"class": "shell", dropTargetAttribute: "import"},
	}
	got, ok := filesDropped([]string{"/tmp/a.json", "", "/tmp/b.har"}, target)
	if !ok || !slices.Equal(got.Paths, []string{"/tmp/a.json", "/tmp/b.har"}) || got.Target != "import" {
		t.Errorf("filesDropped = %+v, %v", got, ok)
	}
	if got, ok := filesDropped([]string{"/tmp/a.json"}, nil); !ok || got.Target != "" {
		t.Errorf("without a target = %+v, %v", got, ok)
	}
	if _, ok := filesDropped(nil, target); ok {
		t.Error("an empty drop is forwarded")
	}
	if _, ok := filesDropped([]string{""}, target); ok {
		t.Error("a drop of blank paths is forwarded")
	}
}
