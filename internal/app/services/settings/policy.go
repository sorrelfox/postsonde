package settings

import (
	"context"

	"github.com/sorrelfox/postsonde/internal/app/apperr"
	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// PolicyDTO is the admin policy in effect (SET-02), for the settings
// screen and the status bar.
type PolicyDTO struct {
	// Active is true when the policy locks or requires anything.
	Active bool `json:"active"`
	// Path is where the policy file is looked for ("" when it is not,
	// e.g. in tests).
	Path string `json:"path"`
	// Exists is true when the policy file is there, even if it cannot be
	// used.
	Exists bool `json:"exists"`
	// Locked are the locked setting keys as in settings.json
	// ("network.verifyTls"), sorted. Their values are those of
	// SettingsDTO: the settings in effect already have them.
	Locked []string `json:"locked"`
	// RequireTLSVerification: no request or connection may turn
	// certificate verification off.
	RequireTLSVerification bool `json:"requireTlsVerification"`
	// Problems are what is wrong with the policy file
	// (settings.policy_invalid); [] when nothing is.
	Problems []apperr.ErrorDTO `json:"problems"`
}

// Policy returns the admin policy the settings are under. It is read once
// at startup, so it does not change while the app runs.
func (s *SettingsService) Policy(ctx context.Context) (PolicyDTO, error) {
	if err := ctx.Err(); err != nil {
		return PolicyDTO{}, errs.Wrap(err, errs.KindOf(err), "Loading the administrator policy was interrupted.")
	}
	p := s.store.Policy()
	locked := p.LockedKeys()
	if locked == nil {
		locked = []string{}
	}
	return PolicyDTO{
		Active:                 p.Active(),
		Path:                   p.Path,
		Exists:                 p.Exists,
		Locked:                 locked,
		RequireTLSVerification: p.RequireTLSVerification,
		Problems:               toProblemDTOs(p.Problems),
	}, nil
}
