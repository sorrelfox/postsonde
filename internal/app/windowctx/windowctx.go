// Package windowctx tells the bound services which window a call came from
// (APP-06, docs/windows.md). Wails puts the calling window in the context
// of every bound method call (application.WindowKey); ID reads it. Tests,
// and Go code acting for a window, set it with With.
//
// ID 0 is "no window": a call from Go itself or from a test that set none.
// The services treat 0 like any other window, so code that knows nothing
// about windows behaves as one window.
package windowctx

import (
	"context"

	"github.com/wailsapp/wails/v3/pkg/application"
)

type key struct{}

// With returns ctx for a call from window id. It wins over the window
// Wails put in ctx.
func With(ctx context.Context, id uint) context.Context {
	return context.WithValue(ctx, key{}, id)
}

// ID returns the window ctx's call came from: the one set by With, else
// the one Wails put in it, else 0.
func ID(ctx context.Context) uint {
	if ctx == nil {
		return 0
	}
	if id, ok := ctx.Value(key{}).(uint); ok {
		return id
	}
	if w, ok := ctx.Value(application.WindowKey).(application.Window); ok && w != nil {
		return w.ID()
	}
	return 0
}
