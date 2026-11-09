// Package testenv adapts the tests that measure time to the build and the
// system they run on.
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
//
// Measured and MeasuredWhen check a duration the code measured for work
// that took place, such as a connect to 127.0.0.1 or a round trip on it.
// Where the clock is coarse (CoarseClock) such work can measure zero.
package testenv

import (
	"math"
	"runtime"
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

// CoarseClock is set where time.Now advances in steps longer than a short
// local operation. On Windows Go reads the monotonic clock from the
// interrupt time, which moves on with the system timer, every 0.5 to
// 15.6 ms; elsewhere the steps are nanoseconds.
const CoarseClock = runtime.GOOS == "windows"

// Measured reports whether d, a duration in any unit, can measure work that
// took place: above zero, or zero where the clock is coarse.
func Measured[D ~int64 | ~float64](d D) bool {
	return d > 0 || d == 0 && CoarseClock
}

// MeasuredWhen reports whether d fits work that took place (happened) or
// did not: Measured(d) when it did, zero when it did not.
func MeasuredWhen[D ~int64 | ~float64](d D, happened bool) bool {
	if happened {
		return Measured(d)
	}
	return d == 0
}
