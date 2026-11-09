package secrets

import (
	"sync"

	"github.com/sorrelfox/postsonde/internal/app/apperr"
	"github.com/sorrelfox/postsonde/internal/core/secretstore"
	"github.com/sorrelfox/postsonde/internal/core/secretstore/keychain"
	coresettings "github.com/sorrelfox/postsonde/internal/core/settings"
)

// KeyKeyStore is the settings key of where the key is kept besides behind
// the master password: "keychain" (default) or "password".
const KeyKeyStore = "privacy.secretsKeyStore"

// Keychain is the OS keychain the vault's key is kept in, and what
// the app last heard from it: whether the startup unlock is still running
// and the last problem. Created before the vault (its KeyStore) and shared
// with the service and the change events, so every StatusDTO carries it.
type Keychain struct {
	store    keychain.KeyStore // nil: no keychain on this system
	settings *coresettings.Store

	mu      sync.Mutex
	pending bool
	problem error
}

// NewKeychain returns the keychain state over store (nil: none).
func NewKeychain(store keychain.KeyStore, settings *coresettings.Store) *Keychain {
	return &Keychain{store: store, settings: settings}
}

// KeyStore is the vault's KeyStore: nil without a keychain. The vault
// always gets it, whatever the mode, so turning encryption off and the
// reset remove a key stored earlier.
func (k *Keychain) KeyStore() secretstore.KeyStore {
	if k == nil || k.store == nil {
		return nil
	}
	return k.store
}

// mode is privacy.secretsKeyStore.
func (k *Keychain) mode() string {
	if k == nil || k.settings == nil {
		return coresettings.SecretsKeyStorePassword
	}
	return k.settings.Settings().Privacy.SecretsKeyStore
}

// active: the key is to be kept in a keychain that exists.
func (k *Keychain) active() bool {
	return k != nil && k.store != nil && k.mode() == coresettings.SecretsKeyStoreKeychain
}

func (k *Keychain) setPending(p bool) {
	if k == nil {
		return
	}
	k.mu.Lock()
	k.pending = p
	k.mu.Unlock()
}

// setProblem records the outcome of the last keychain call: nil clears it.
func (k *Keychain) setProblem(err error) {
	if k == nil {
		return
	}
	k.mu.Lock()
	k.problem = err
	k.mu.Unlock()
}

func (k *Keychain) dto() KeychainDTO {
	out := KeychainDTO{Mode: coresettings.SecretsKeyStorePassword}
	if k == nil {
		return out
	}
	out.Mode = k.mode()
	if k.settings != nil {
		out.ModeLocked = k.settings.Policy().IsLocked(KeyKeyStore)
	}
	if k.store != nil {
		out.Available = true
		out.Name = k.store.Name()
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	out.Pending = k.pending
	if k.problem != nil {
		p := apperr.ToDTO(k.problem)
		out.Problem = &p
	}
	return out
}
