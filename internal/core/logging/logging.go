package logging

import (
	"io"
	"log/slog"
	"os"
)

// Format selects the log output format.
type Format int

const (
	// FormatJSON writes one JSON object per record. It is the default.
	FormatJSON Format = iota
	// FormatText writes key=value pairs, for reading in a terminal.
	FormatText
)

// Options configures New.
type Options struct {
	// Output receives the log records, usually a *RotatingFile. Nil means
	// os.Stderr.
	Output io.Writer
	// Format is the output format; the zero value is FormatJSON.
	Format Format
	// Level is the minimum level logged; nil means slog.LevelInfo. Pass a
	// *slog.LevelVar to change it at run time.
	Level slog.Leveler
	// AddSource adds the source file and line to each record.
	AddSource bool
	// Redactor masks secrets. Nil means a new Redactor with the built-in
	// sensitive keys; pass one to register secret values with AddSecret.
	Redactor *Redactor
	// Ring, if set, also receives every record at its own level (debug by
	// default), after redaction, for the in-app console (DBG-06). The
	// output's level and format are unchanged by it.
	Ring *Ring
}

// New returns a logger writing to opts.Output with redaction applied, and
// to opts.Ring when set.
func New(opts Options) *slog.Logger {
	w := opts.Output
	if w == nil {
		w = os.Stderr
	}
	r := opts.Redactor
	if r == nil {
		r = NewRedactor()
	}
	ho := &slog.HandlerOptions{Level: opts.Level, AddSource: opts.AddSource}
	var h slog.Handler
	if opts.Format == FormatText {
		h = slog.NewTextHandler(w, ho)
	} else {
		h = slog.NewJSONHandler(w, ho)
	}
	if opts.Ring != nil {
		h = slog.NewMultiHandler(h, opts.Ring.Handler())
	}
	return slog.New(r.Handler(h))
}
