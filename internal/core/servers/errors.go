package servers

import (
	"fmt"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// Error codes. Listening errors keep their listen.* codes
// (docs/network.md#errors).
const (
	CodeNotFound     = "servers.not_found"
	CodeUnknownType  = "servers.unknown_type"
	CodeShuttingDown = "servers.shutting_down"
	CodeServeFailed  = "servers.serve_failed"
	CodeStartFailed  = "servers.start_failed"
	CodeNotRunning   = "servers.not_running"
	// CodeActive: the address or type of a server cannot change while it
	// holds a socket (Redefine).
	CodeActive = "servers.active"
	// CodeTypeChanged: Redefine cannot turn a server into another kind.
	CodeTypeChanged = "servers.type_changed"
	// CodeCaptureNotFound: the request asked for is not kept (anymore).
	CodeCaptureNotFound = "servers.capture_not_found"
)

func notFound(id string) *errs.Error {
	return errs.New(errs.NotFound, "This server no longer exists.").
		WithCode(CodeNotFound).WithDetail("id", id)
}

func unknownType(name string) *errs.Error {
	return errs.New(errs.InvalidArgument, fmt.Sprintf("%q is not a kind of server PostSonde can run.", name)).
		WithCode(CodeUnknownType).WithDetail("type", name)
}

func shuttingDown() *errs.Error {
	return errs.New(errs.Cancelled, "PostSonde is quitting; servers cannot be started.").
		WithCode(CodeShuttingDown)
}

func serveFailed(err error) *errs.Error {
	if err == nil {
		return errs.New(errs.Network, "The server stopped unexpectedly.").WithCode(CodeServeFailed)
	}
	return errs.Wrap(err, errs.KindOf(err), "The server stopped unexpectedly.").WithCode(CodeServeFailed)
}

func startFailed(err error) *errs.Error {
	return errs.Wrap(err, errs.KindOf(err), "The server could not start.").WithCode(CodeStartFailed)
}

func notRunning(id string) *errs.Error {
	return errs.New(errs.InvalidArgument, "This server is not running.").
		WithCode(CodeNotRunning).WithDetail("id", id)
}

func captureNotFound(id string, seq uint64) *errs.Error {
	return errs.New(errs.NotFound, "This request is no longer kept.").
		WithCode(CodeCaptureNotFound).
		WithHint("The server keeps its most recent requests; older ones and cleared ones are dropped.").
		WithDetail("id", id).WithDetail("seq", fmt.Sprint(seq))
}

func active(id string) *errs.Error {
	return errs.New(errs.InvalidArgument, "The server's address cannot change while it is running.").
		WithCode(CodeActive).
		WithHint("Stop the server, change it, then start it again.").
		WithDetail("id", id)
}

func typeChanged(id string) *errs.Error {
	return errs.New(errs.InvalidArgument, "A server cannot be turned into another kind of server.").
		WithCode(CodeTypeChanged).
		WithHint("Remove the server and add one of the other kind.").
		WithDetail("id", id)
}
