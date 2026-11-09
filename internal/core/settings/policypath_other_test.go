//go:build !windows

package settings

import (
	"path/filepath"
	"testing"
)

func TestSystemPolicyPathFor(t *testing.T) {
	t.Parallel()
	if got := systemPolicyPathFor("darwin"); got != filepath.Join("/Library", "Application Support", "PostSonde", "policy.json") {
		t.Errorf("darwin: %q", got)
	}
	if got := systemPolicyPathFor("linux"); got != filepath.Join("/etc", "postsonde", "policy.json") {
		t.Errorf("linux: %q", got)
	}
}
