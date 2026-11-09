package secrets

import (
	"time"

	"github.com/sorrelfox/postsonde/internal/app/apperr"
	"github.com/sorrelfox/postsonde/internal/core/secretstore"
	coresettings "github.com/sorrelfox/postsonde/internal/core/settings"
)

// DTOs are the only types SecretsService methods take or return. See
// docs/bindings.md and, for the behaviour, docs/privacy.md#encrypted-secrets.

// StatusDTO is what the vault is doing.
type StatusDTO struct {
	// Available is false without a data directory: nothing is stored, so
	// there is nothing to encrypt.
	Available bool `json:"available"`
	// Enabled: the secrets are encrypted with a master password.
	Enabled bool `json:"enabled"`
	// Locked: Enabled, and not unlocked yet (or locked again). Secrets
	// cannot be read or written until the password is typed.
	Locked bool `json:"locked"`
	// Migration is an interrupted "encrypting" or "decrypting", finished
	// at the next unlock; "" when none.
	Migration string `json:"migration"`
	// Required: the administrator requires encryption
	// (privacy.requireSecretEncryption): it cannot be turned off, and
	// secrets cannot be stored while it is off.
	Required bool `json:"required"`
	// IdleLockMinutes locks the secrets after that many minutes without
	// one being used; 0 never (privacy.secretsLockMinutes).
	IdleLockMinutes int `json:"idleLockMinutes"`
	// IdleLockLocked: the admin policy locks IdleLockMinutes.
	IdleLockLocked bool `json:"idleLockLocked"`
	// KDF and Iterations describe the key derivation ("" and 0 while off).
	KDF        string `json:"kdf"`
	Iterations int    `json:"iterations"`
	// MinPasswordLength is the shortest master password accepted.
	MinPasswordLength int `json:"minPasswordLength"`
	// Problem is why the key file cannot be used; null when it can. Only
	// Reset helps then.
	Problem *apperr.ErrorDTO `json:"problem"`
	// Keychain is the OS keychain that keeps the key besides the master
	// password.
	Keychain KeychainDTO `json:"keychain"`
}

// KeychainDTO is where the key of the secrets is kept.
type KeychainDTO struct {
	// Mode is privacy.secretsKeyStore: "keychain" (the key is also kept
	// in the OS keychain, and the secrets unlock at startup) or
	// "password" (only the master password unlocks them).
	Mode string `json:"mode"`
	// ModeLocked: the admin policy locks Mode.
	ModeLocked bool `json:"modeLocked"`
	// Available: this system has a keychain PostSonde can try (Name).
	// Whether it works is only known once used (Problem).
	Available bool `json:"available"`
	// Name names it: "macOS Keychain", "Secret Service", "Windows DPAPI";
	// "" when none.
	Name string `json:"name"`
	// Pending: the unlock from the keychain at startup is still running;
	// the UI waits for it before asking for the password.
	Pending bool `json:"pending"`
	// Problem is why the keychain could not be used the last time (no
	// Secret Service, access denied); null when it worked or was not
	// used. The master password still works.
	Problem *apperr.ErrorDTO `json:"problem"`
}

// KeyStoreInputDTO is the input of SetKeyStore.
type KeyStoreInputDTO struct {
	// Mode is "keychain" or "password".
	Mode string `json:"mode"`
}

// PasswordInputDTO is the input of Enable, Unlock and Disable.
type PasswordInputDTO struct {
	Password string `json:"password"`
}

// ChangePasswordInputDTO is the input of ChangePassword.
type ChangePasswordInputDTO struct {
	OldPassword string `json:"oldPassword"`
	NewPassword string `json:"newPassword"`
}

// ResetInputDTO is the input of Reset.
type ResetInputDTO struct {
	// Confirm must be true: the UI sets it after the user typed the
	// confirmation, since the secrets are deleted for good.
	Confirm bool `json:"confirm"`
}

func toStatusDTO(st secretstore.Status, settings *coresettings.Store, kc *Keychain) StatusDTO {
	out := StatusDTO{
		Available:         true,
		Enabled:           st.Enabled,
		Locked:            st.Locked,
		Migration:         st.Migration,
		Required:          st.Required,
		IdleLockMinutes:   int(st.IdleTimeout / time.Minute),
		KDF:               st.KDF,
		Iterations:        st.Iterations,
		MinPasswordLength: secretstore.MinPasswordLength,
		Keychain:          kc.dto(),
	}
	if settings != nil {
		out.IdleLockLocked = settings.Policy().IsLocked(KeyLockMinutes)
	}
	if st.Problem != nil {
		p := apperr.ToDTO(st.Problem)
		out.Problem = &p
	}
	return out
}
