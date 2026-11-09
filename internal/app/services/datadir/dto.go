package datadir

import (
	"github.com/sorrelfox/postsonde/internal/app/apperr"
	core "github.com/sorrelfox/postsonde/internal/core/datadir"
)

// InfoDTO describes the data directory for the settings screen.
type InfoDTO struct {
	// Mode is "default", "configured", "portable" or "env" (POSTSONDE_HOME).
	Mode string `json:"mode"`
	// DataDir is the data directory in use; "" when none could be used
	// (Problems says why).
	DataDir string `json:"dataDir"`
	// Wanted is the data directory the setting asks for.
	Wanted string `json:"wanted"`
	// Default is the OS default data directory.
	Default string `json:"default"`
	// Root is POSTSONDE_HOME or the portable folder ("" otherwise).
	Root string `json:"root"`
	// Marker is the portable marker file (portable mode only).
	Marker string `json:"marker"`
	// Pending is the change made at the next start, or null.
	Pending *PendingDTO `json:"pending"`
	// Previous is the folder a move copied from, still holding a copy of
	// the data, or null.
	Previous *PreviousDTO `json:"previous"`
	// Moved is true when the data was moved at this start.
	Moved bool `json:"moved"`
	// Problems are what went wrong at start (a folder that cannot be
	// written or is missing, a failed move).
	Problems []apperr.ErrorDTO `json:"problems"`
	// ChangeBlocked says why Change is not available (portable mode,
	// POSTSONDE_HOME), or null.
	ChangeBlocked *apperr.ErrorDTO `json:"changeBlocked"`
	// CanRestart is false when the app cannot restart itself (tests).
	CanRestart bool `json:"canRestart"`
}

// PendingDTO is a data directory change scheduled for the next start.
type PendingDTO struct {
	// Action is "move", "fresh" or "use".
	Action string `json:"action"`
	// DataDir is the new data directory.
	DataDir string `json:"dataDir"`
}

// PreviousDTO is the old folder of a move.
type PreviousDTO struct {
	DataDir string `json:"dataDir"`
	// MovedAt is an RFC 3339 time.
	MovedAt string `json:"movedAt"`
}

// CandidateDTO is a folder chosen with Pick, already checked.
type CandidateDTO struct {
	// Path is the folder; "" when the dialog was cancelled.
	Path string `json:"path"`
	// Exists is false for a folder that will be created.
	Exists bool `json:"exists"`
	// HasData is true when it holds PostSonde data: only "use" fits.
	// Otherwise it is empty: "move" or "fresh".
	HasData bool `json:"hasData"`
	// IsDefault is true for the OS default data directory.
	IsDefault bool `json:"isDefault"`
	// CanMove is false when there is no data directory in use to move.
	CanMove bool `json:"canMove"`
}

// ScheduleDTO asks for a change at the next start.
type ScheduleDTO struct {
	Path string `json:"path"`
	// Action is "move", "fresh" or "use".
	Action string `json:"action"`
}

func toInfoDTO(info core.Info, canRestart bool) InfoDTO {
	dto := InfoDTO{
		Mode:       string(info.Mode),
		DataDir:    info.DataDir,
		Wanted:     info.Wanted,
		Default:    info.Default,
		Root:       info.Root,
		Marker:     info.Marker,
		Moved:      info.Moved,
		Problems:   make([]apperr.ErrorDTO, 0, len(info.Problems)),
		CanRestart: canRestart,
	}
	if p := info.Pending; p != nil {
		dto.Pending = &PendingDTO{Action: string(p.Action), DataDir: p.DataDir}
	}
	if p := info.Previous; p != nil {
		dto.Previous = &PreviousDTO{DataDir: p.DataDir, MovedAt: p.MovedAt}
	}
	for _, p := range info.Problems {
		dto.Problems = append(dto.Problems, apperr.ToDTO(p))
	}
	if info.ChangeBlocked != nil {
		b := apperr.ToDTO(info.ChangeBlocked)
		dto.ChangeBlocked = &b
	}
	return dto
}

func toCandidateDTO(c core.Candidate) CandidateDTO {
	return CandidateDTO{Path: c.Path, Exists: c.Exists, HasData: c.HasData, IsDefault: c.IsDefault, CanMove: c.CanMove}
}
