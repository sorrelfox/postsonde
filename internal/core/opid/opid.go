// Package opid makes operation IDs: the handle the UI uses to follow and
// cancel a long-running operation (see docs/events.md).
package opid

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
)

// Prefix starts every operation ID.
const Prefix = "op_"

// randomBytes is the number of random bytes in an ID: 8 bytes, 16 hex digits.
const randomBytes = 8

// New returns a new operation ID: "op_" followed by 16 lowercase hex digits
// from crypto/rand, e.g. "op_3f9a0c12d4e5b678".
func New() string {
	var b [randomBytes]byte
	// crypto/rand.Read never returns an error; it crashes the program
	// irrecoverably if the OS random source fails.
	_, _ = rand.Read(b[:])
	return Prefix + hex.EncodeToString(b[:])
}

// Valid reports whether id has the shape New produces.
func Valid(id string) bool {
	hexPart, ok := strings.CutPrefix(id, Prefix)
	if !ok || len(hexPart) != 2*randomBytes {
		return false
	}
	for _, c := range hexPart {
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}
