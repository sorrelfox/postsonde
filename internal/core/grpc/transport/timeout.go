package transport

import (
	"errors"
	"math"
	"strconv"
	"time"
)

// maxTimeoutValue is the largest TimeoutValue: at most 8 ASCII digits.
const maxTimeoutValue = 99_999_999

var timeoutUnits = []struct {
	unit byte
	d    time.Duration
}{
	{'n', time.Nanosecond},
	{'u', time.Microsecond},
	{'m', time.Millisecond},
	{'S', time.Second},
	{'M', time.Minute},
	{'H', time.Hour},
}

// EncodeTimeout formats d as a grpc-timeout value: at most 8 digits and a
// unit (H, M, S, m, u, n). It uses the finest unit that fits, rounding up,
// so the server never sees a shorter deadline than the client's. A
// duration of zero or less is "0n".
func EncodeTimeout(d time.Duration) string {
	if d <= 0 {
		return "0n"
	}
	for _, u := range timeoutUnits {
		v := d / u.d
		if d%u.d != 0 {
			v++ // round up
		}
		if v <= maxTimeoutValue {
			return strconv.FormatInt(int64(v), 10) + string(u.unit)
		}
	}
	// Unreachable: MaxInt64 nanoseconds is about 2.6 million hours.
	return strconv.Itoa(maxTimeoutValue) + "H"
}

// ParseTimeout parses a grpc-timeout value. Durations too long for
// time.Duration are clamped to its maximum.
func ParseTimeout(s string) (time.Duration, error) {
	if len(s) < 2 || len(s) > 9 {
		return 0, errors.New("grpc-timeout must be 1 to 8 digits and a unit")
	}
	unit := s[len(s)-1]
	var d time.Duration
	for _, u := range timeoutUnits {
		if u.unit == unit {
			d = u.d
		}
	}
	if d == 0 {
		return 0, errors.New("grpc-timeout has an unknown unit " + strconv.Quote(string(unit)))
	}
	digits := s[:len(s)-1]
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return 0, errors.New("grpc-timeout value must be digits")
		}
	}
	v, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return 0, err
	}
	if v > int64(math.MaxInt64/d) {
		return math.MaxInt64, nil
	}
	return time.Duration(v) * d, nil
}
