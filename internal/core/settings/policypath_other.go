//go:build !windows

package settings

import (
	"path/filepath"
	"runtime"
)

// SystemPolicyPath returns where the admin policy file is looked for: a
// system-wide folder only an administrator (root) can write. It is fixed:
// no environment variable changes it, so a user cannot point PostSonde
// elsewhere to get around the policy.
//
//   - macOS: /Library/Application Support/PostSonde/policy.json
//   - Linux and other Unix: /etc/postsonde/policy.json
func SystemPolicyPath() string {
	return systemPolicyPathFor(runtime.GOOS)
}

func systemPolicyPathFor(goos string) string {
	if goos == "darwin" {
		return filepath.Join("/Library", "Application Support", "PostSonde", PolicyFileName)
	}
	return filepath.Join("/etc", "postsonde", PolicyFileName)
}
