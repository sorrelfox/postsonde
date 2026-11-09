// Package system is the reference bound service: it reports build and
// platform information and has a cancellable long-running demo call.
//
// It is the template for every other service under internal/app/services;
// see docs/bindings.md for the conventions it follows.
package system

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"strconv"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// MaxWaitMs is the longest Wait accepts, in milliseconds.
const MaxWaitMs = 60_000

// CodeWaitOutOfRange is the errs code of the error Wait returns for an ms
// outside 0..MaxWaitMs.
const CodeWaitOutOfRange = "system.wait_out_of_range"

// unknown is reported for build details that were not provided.
const unknown = "unknown"

// Build describes the running binary. The app layer fills it in; the service
// does not look it up itself so it stays free of Wails and easy to test.
type Build struct {
	AppName      string // e.g. "PostSonde"
	Version      string // main.version, set by -ldflags; "dev" in local builds
	WailsVersion string // version of the Wails module linked in
}

// SystemService exposes build and platform information to the UI.
//
// Every exported method is bound to the frontend. Methods take a
// context.Context first (cancelled when the frontend cancels the call) and
// return a DTO plus an error.
type SystemService struct {
	build  Build
	logger *slog.Logger
	now    func() time.Time
}

// New returns a SystemService for the given build. A nil logger means
// slog.Default().
func New(build Build, logger *slog.Logger) *SystemService {
	if logger == nil {
		logger = slog.Default()
	}
	return &SystemService{
		build:  build,
		logger: logger.With("service", "system"),
		now:    time.Now,
	}
}

// ServiceName names the service in Wails logs.
func (s *SystemService) ServiceName() string { return "system" }

// Info returns the app name and version and the platform it runs on.
func (s *SystemService) Info(ctx context.Context) (InfoDTO, error) {
	if err := ctx.Err(); err != nil {
		return InfoDTO{}, errs.Wrap(err, errs.KindOf(err), "Loading the app info was interrupted.")
	}
	return InfoDTO{
		Name:         orUnknown(s.build.AppName),
		Version:      orUnknown(s.build.Version),
		OS:           runtime.GOOS,
		Arch:         runtime.GOARCH,
		GoVersion:    runtime.Version(),
		WailsVersion: orUnknown(s.build.WailsVersion),
	}, nil
}

// Wait sleeps for ms milliseconds, or until the call is cancelled.
//
// It is the reference for long-running calls and for errors: it watches ctx
// and returns a cancelled (or timeout) errs.Error wrapping ctx's error as
// soon as the frontend cancels the promise (`.cancel()`), and an
// invalid_argument errs.Error with a hint when ms is outside 0..MaxWaitMs.
func (s *SystemService) Wait(ctx context.Context, ms int) (WaitResultDTO, error) {
	if ms < 0 || ms > MaxWaitMs {
		return WaitResultDTO{}, errs.New(errs.InvalidArgument,
			fmt.Sprintf("The wait time must be between 0 and %d ms, not %d ms.", MaxWaitMs, ms)).
			WithCode(CodeWaitOutOfRange).
			WithHint(fmt.Sprintf("Enter a value from 0 to %d ms.", MaxWaitMs)).
			WithDetail("field", "ms").
			WithDetail("min", "0").
			WithDetail("max", strconv.Itoa(MaxWaitMs))
	}

	start := s.now()
	timer := time.NewTimer(time.Duration(ms) * time.Millisecond)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		elapsedMs := s.now().Sub(start).Milliseconds()
		s.logger.Info("wait cancelled", "requestedMs", ms, "elapsedMs", elapsedMs)
		err := ctx.Err()
		kind := errs.KindOf(err)
		msg := fmt.Sprintf("The wait was cancelled after %d ms.", elapsedMs)
		if kind == errs.Timeout {
			msg = fmt.Sprintf("The wait timed out after %d ms.", elapsedMs)
		}
		return WaitResultDTO{}, errs.Wrap(err, kind, msg)
	case <-timer.C:
		end := s.now()
		return toWaitResultDTO(ms, start, end), nil
	}
}

func orUnknown(s string) string {
	if s == "" {
		return unknown
	}
	return s
}
