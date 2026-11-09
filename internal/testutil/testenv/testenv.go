// Package testenv adapts the tests that measure time to the build they run
// in.
//
// Budget is the wall-clock bound of a speed test, one that catches
// pathological slowness: a parser gone quadratic, a search index no longer
// used, a limit that no longer stops the work. The race detector makes
// code 2 to 20 times slower by the Go documentation, and on a loaded CI
// runner far more and unevenly: the same parse took 19 s in one run and
// 63 s in the next. No bound fits that and still catches anything, so
// under the race detector there is none, and the speed tests check their
// bounds in plain builds (go test without -race, as in CI's Windows job).
// Work gone exponential still ends at go test's timeout.
package testenv

import (
	"math"
	"time"
)

// Budget returns the bound d of a speed test; under the race detector, the
// largest duration, which no run exceeds.
func Budget(d time.Duration) time.Duration {
	if raceEnabled {
		return math.MaxInt64
	}
	return d
}
