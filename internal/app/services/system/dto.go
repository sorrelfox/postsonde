package system

import "time"

// DTOs are the only types SystemService methods take or return. They are
// plain structs with camelCase JSON tags; the TypeScript models in
// frontend/bindings are generated from them. See docs/bindings.md.

// InfoDTO describes the running app and its platform.
type InfoDTO struct {
	// Name is the product name, "PostSonde".
	Name string `json:"name"`
	// Version is the build version ("dev" for local builds).
	Version string `json:"version"`
	// OS is the Go GOOS value, e.g. "darwin".
	OS string `json:"os"`
	// Arch is the Go GOARCH value, e.g. "arm64".
	Arch string `json:"arch"`
	// GoVersion is the Go toolchain the binary was built with.
	GoVersion string `json:"goVersion"`
	// WailsVersion is the Wails module version linked in.
	WailsVersion string `json:"wailsVersion"`
}

// WaitResultDTO reports a Wait call that ran to completion.
type WaitResultDTO struct {
	// RequestedMs is the duration asked for, in milliseconds.
	RequestedMs int `json:"requestedMs"`
	// ElapsedMs is the time actually waited, in milliseconds.
	ElapsedMs int64 `json:"elapsedMs"`
	// FinishedAt is when the wait ended, as an RFC 3339 UTC timestamp.
	FinishedAt string `json:"finishedAt"`
}

func toWaitResultDTO(requestedMs int, start, end time.Time) WaitResultDTO {
	return WaitResultDTO{
		RequestedMs: requestedMs,
		ElapsedMs:   end.Sub(start).Milliseconds(),
		FinishedAt:  formatTime(end),
	}
}

// formatTime renders t for a DTO: RFC 3339 in UTC, with sub-second digits
// only when present. The zero time becomes "".
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}
