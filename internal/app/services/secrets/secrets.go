// Package secrets is the bound service for the encryption of the local
// secrets with a master password (SEC-03): turning it on and off,
// unlocking and locking, changing the password, the reset of a forgotten
// one, and the idle lock; and for keeping the key in the OS keychain too,
// so the secrets unlock at startup without the password. The logic
// is internal/core/secretstore and its keychain package; the idle lock
// time, the administrator's requirement and the key store are settings
// (privacy.secretsLockMinutes, privacy.requireSecretEncryption,
// privacy.secretsKeyStore). See docs/privacy.md#encrypted-secrets.
package secrets

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/secretstore"
	coresettings "github.com/sorrelfox/postsonde/internal/core/settings"
)

// Domain names the secrets service.
const Domain = "secrets"

// EventChanged is sent with a StatusDTO whenever the vault changes: turned
// on or off, reset, locked (by the user or the idle lock) or unlocked, or
// its password changed. The UI updates its lock indicator, asks for the
// password when the secrets get locked while it needs them, and reloads
// what failed while they were locked.
const EventChanged = Domain + ":changed"

// CodeUnavailable: there is no data directory, so there is nothing to
// encrypt (kind unsupported).
const CodeUnavailable = "secrets.unavailable"

// CodeConfirmRequired: Reset was called without its confirmation (kind
// invalid_argument).
const CodeConfirmRequired = "secrets.confirm_required"

// CodeNoKeychain: the keychain was asked for, but this system has none
// PostSonde can use, or the key store is "password" (kind unsupported).
const CodeNoKeychain = "secrets.no_keychain"

// CodeKeychainNoKey: unlocking from the keychain found no key there (kind
// not_found); the master password unlocks, and stores the key again.
const CodeKeychainNoKey = "secrets.keychain_no_key"

// KeyLockMinutes and KeyRequired are the settings keys of the idle lock
// and of the administrator's requirement.
const (
	KeyLockMinutes = "privacy.secretsLockMinutes"
	KeyRequired    = "privacy.requireSecretEncryption"
)

// idleCheckInterval is how often the idle lock is checked, so the UI hears
// of it without a secret being used.
const idleCheckInterval = 15 * time.Second

func init() {
	application.RegisterEvent[StatusDTO](EventChanged)
}

// Changed returns the vault's change hook (secretstore.Options.OnChange):
// it sends EventChanged through emit. A nil emit sends nothing.
func Changed(emit func(name string, data any), settings *coresettings.Store, kc *Keychain) func(secretstore.Status) {
	return func(st secretstore.Status) {
		if emit != nil {
			emit(EventChanged, toStatusDTO(st, settings, kc))
		}
	}
}

// VaultOptions configures the vault of the data directory for the app: the
// idle lock and the requirement read from the settings on every use, the
// keychain's KeyStore (kc may be nil), and onChange (Changed, plus what
// the app drops on a lock).
func VaultOptions(settings *coresettings.Store, kc *Keychain, onChange func(secretstore.Status)) secretstore.Options {
	return secretstore.Options{
		KeyStore: kc.KeyStore(),
		IdleTimeout: func() time.Duration {
			return time.Duration(settings.Settings().Privacy.SecretsLockMinutes) * time.Minute
		},
		Required: func() bool { return settings.Settings().Privacy.RequireSecretEncryption },
		OnChange: onChange,
	}
}

// SecretsService turns the encryption of the local secrets on and off and
// locks and unlocks them. Every call answers with the status after it.
type SecretsService struct {
	vault    *secretstore.Vault // nil without a data directory
	settings *coresettings.Store
	logger   *slog.Logger
	keychain *Keychain                   // nil: none
	emit     func(name string, data any) // nil: no events of its own

	mu   sync.Mutex
	stop chan struct{}
}

// Option configures a SecretsService.
type Option func(*SecretsService)

// WithKeychain keeps the key in the OS keychain too: kc must be the
// one the vault's options were made with.
func WithKeychain(kc *Keychain) Option { return func(s *SecretsService) { s.keychain = kc } }

// WithEmit sends EventChanged when only the keychain's state changed (the
// vault's OnChange sends the others).
func WithEmit(emit func(name string, data any)) Option {
	return func(s *SecretsService) { s.emit = emit }
}

// New returns a SecretsService over vault (nil: no data directory, and
// every change fails with CodeUnavailable). A nil logger means
// slog.Default().
func New(vault *secretstore.Vault, settings *coresettings.Store, logger *slog.Logger, opts ...Option) *SecretsService {
	if logger == nil {
		logger = slog.Default()
	}
	s := &SecretsService{vault: vault, settings: settings, logger: logger.With("service", Domain)}
	for _, o := range opts {
		o(s)
	}
	return s
}

// ServiceName names the service in Wails logs.
func (s *SecretsService) ServiceName() string { return Domain }

// ServiceStartup starts the idle lock's timer and, with the key store
// "keychain", the unlock from the keychain (in the background: the system
// may ask the user to unlock the keychain first). The UI waits for it
// (KeychainDTO.Pending) before asking for the master password.
func (s *SecretsService) ServiceStartup(context.Context, application.ServiceOptions) error {
	s.startIdleTimer(idleCheckInterval)
	if s.startKeychainUnlock() {
		go s.keychainUnlock()
	}
	return nil
}

// startKeychainUnlock marks the startup unlock pending and reports whether
// there is one to run: the key store is "keychain", and the secrets are
// encrypted and locked.
func (s *SecretsService) startKeychainUnlock() bool {
	if s.vault == nil || !s.keychain.active() {
		return false
	}
	if st := s.vault.Status(); !st.Enabled || !st.Locked || st.Problem != nil {
		return false
	}
	s.keychain.setPending(true)
	return true
}

// keychainUnlock unlocks from the keychain at startup. When it cannot, the
// secrets stay locked and the UI asks for the master password.
func (s *SecretsService) keychainUnlock() {
	ok, err := s.vault.UnlockFromKeyStore()
	s.keychain.setProblem(err)
	s.keychain.setPending(false)
	switch {
	case err != nil:
		s.logger.Warn("secrets not unlocked from the keychain", "err", err)
	case ok:
		s.logger.Info("secrets unlocked from the keychain")
	default:
		s.logger.Info("no key in the keychain; the master password is needed")
	}
	s.changed()
}

// changed sends EventChanged with the current status.
func (s *SecretsService) changed() {
	if s.emit != nil {
		s.emit(EventChanged, s.status())
	}
}

// remember keeps the unlocked key in the keychain when the key store is
// "keychain". A failure does not fail the unlock: it is reported in the
// status (KeychainDTO.Problem), and the master password keeps working.
func (s *SecretsService) remember() {
	if !s.keychain.active() {
		return
	}
	err := s.vault.RememberKey()
	s.keychain.setProblem(err)
	if err != nil {
		s.logger.Warn("secrets key not stored in the keychain", "err", err)
	}
	s.changed()
}

// ServiceShutdown stops the timer. It does not lock the vault: services
// shut down after it may still write secrets (the auto-save's last
// flush), and the key goes with the process.
func (s *SecretsService) ServiceShutdown() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stop != nil {
		close(s.stop)
		s.stop = nil
	}
	return nil
}

func (s *SecretsService) startIdleTimer(every time.Duration) {
	if s.vault == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stop != nil {
		return
	}
	stop := make(chan struct{})
	s.stop = stop
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				if s.vault.LockIfIdle() {
					s.logger.Info("secrets locked after the idle time")
				}
			}
		}
	}()
}

func (s *SecretsService) status() StatusDTO {
	if s.vault == nil {
		return StatusDTO{MinPasswordLength: secretstore.MinPasswordLength, Keychain: s.keychain.dto()}
	}
	return toStatusDTO(s.vault.Status(), s.settings, s.keychain)
}

func interrupted(ctx context.Context, what string) error {
	if err := ctx.Err(); err != nil {
		return errs.Wrap(err, errs.KindOf(err), what+" was interrupted.")
	}
	return nil
}

func (s *SecretsService) available() error {
	if s.vault == nil {
		return errs.New(errs.Unsupported, "PostSonde has no data directory, so it stores no secrets to encrypt.").
			WithCode(CodeUnavailable)
	}
	return nil
}

// Status returns what the vault is doing.
func (s *SecretsService) Status(ctx context.Context) (StatusDTO, error) {
	if err := interrupted(ctx, "Reading the state of the secrets"); err != nil {
		return StatusDTO{}, err
	}
	return s.status(), nil
}

// Enable turns encryption on with a new master password and encrypts
// every stored secret. Fails with secretstore.password_too_short,
// secretstore.already_enabled. When encrypting a file fails, the rest is
// finished at the next unlock.
func (s *SecretsService) Enable(ctx context.Context, in PasswordInputDTO) (StatusDTO, error) {
	if err := interrupted(ctx, "Turning on encryption"); err != nil {
		return StatusDTO{}, err
	}
	if err := s.available(); err != nil {
		return StatusDTO{}, err
	}
	if err := s.vault.Enable(in.Password); err != nil {
		return s.status(), err
	}
	s.logger.Info("secrets encryption turned on")
	s.remember()
	return s.status(), nil
}

// Unlock unlocks the secrets. A wrong password is
// secretstore.wrong_password.
func (s *SecretsService) Unlock(ctx context.Context, in PasswordInputDTO) (StatusDTO, error) {
	if err := interrupted(ctx, "Unlocking the secrets"); err != nil {
		return StatusDTO{}, err
	}
	if err := s.available(); err != nil {
		return StatusDTO{}, err
	}
	if err := s.vault.Unlock(in.Password); err != nil {
		if errs.KindOf(err) == errs.PermissionDenied {
			s.logger.Info("secrets unlock refused")
		}
		return s.status(), err
	}
	s.remember()
	return s.status(), nil
}

// UnlockWithKeychain unlocks the secrets with the key kept in the OS
// keychain, as at startup: after a lock, or when the keychain was
// locked then. Fails with CodeNoKeychain when the key store is "password"
// or there is no keychain, CodeKeychainNoKey when the keychain has no key
// (the master password then unlocks and stores it), the keychain's own
// errors (keychain.unavailable, keychain.failed) and
// secretstore.wrong_password for a key that is not this vault's.
func (s *SecretsService) UnlockWithKeychain(ctx context.Context) (StatusDTO, error) {
	if err := interrupted(ctx, "Unlocking the secrets"); err != nil {
		return StatusDTO{}, err
	}
	if err := s.available(); err != nil {
		return StatusDTO{}, err
	}
	if !s.keychain.active() {
		return s.status(), noKeychain()
	}
	if st := s.vault.Status(); !st.Enabled || !st.Locked {
		return s.status(), nil
	}
	ok, err := s.vault.UnlockFromKeyStore()
	s.keychain.setProblem(err)
	if err != nil {
		s.changed()
		return s.status(), err
	}
	if !ok {
		s.changed()
		return s.status(), errs.New(errs.NotFound, "PostSonde's key is not in the "+s.keychain.store.Name()+".").
			WithCode(CodeKeychainNoKey).
			WithHint("Unlock with the master password once; PostSonde then keeps the key in the keychain.")
	}
	return s.status(), nil
}

// SetKeyStore sets privacy.secretsKeyStore and moves the key.
// "keychain" stores the unlocked key in the keychain now (or at the next
// unlock while locked, or when encryption is turned on); when the keychain
// refuses, the setting is not changed and its error is returned.
// "password" removes the key from the keychain; when that fails, the
// setting is still changed and the status reports the problem. The data
// key and the master password never change, so no secret is touched.
// Fails with settings.locked when the admin policy locks the key store,
// settings.invalid_value for another mode, and CodeNoKeychain for
// "keychain" without one.
func (s *SecretsService) SetKeyStore(ctx context.Context, in KeyStoreInputDTO) (StatusDTO, error) {
	if err := interrupted(ctx, "Saving where the key is kept"); err != nil {
		return StatusDTO{}, err
	}
	if s.settings == nil {
		return s.status(), errs.New(errs.Unsupported, "The settings cannot be changed.").WithCode(CodeUnavailable)
	}
	if err := s.settings.Policy().CheckLocked(KeyKeyStore); err != nil {
		return s.status(), err
	}
	mode := in.Mode
	stored := false
	if mode == coresettings.SecretsKeyStoreKeychain {
		if s.keychain == nil || s.keychain.store == nil {
			return s.status(), noKeychain()
		}
		if s.vault != nil {
			if st := s.vault.Status(); st.Enabled && !st.Locked {
				err := s.vault.RememberKey()
				s.keychain.setProblem(err)
				if err != nil {
					s.changed()
					return s.status(), err
				}
				stored = true
			}
		}
	}
	if _, _, err := s.settings.Update(coresettings.Patch{Privacy: coresettings.PrivacyPatch{SecretsKeyStore: &mode}}); err != nil {
		if stored {
			_ = s.vault.ForgetKey()
		}
		return s.status(), err
	}
	if mode == coresettings.SecretsKeyStorePassword && s.vault != nil && s.keychain != nil {
		err := s.vault.ForgetKey()
		s.keychain.setProblem(err)
		if err != nil {
			s.logger.Warn("secrets key not removed from the keychain", "err", err)
		}
	}
	s.logger.Info("secrets key store changed", "mode", mode)
	s.changed()
	return s.status(), nil
}

func noKeychain() error {
	return errs.New(errs.Unsupported, "PostSonde cannot keep the key of the secrets in a keychain here.").
		WithCode(CodeNoKeychain).
		WithHint("Choose OS keychain in Settings, Privacy, Stored secrets, or unlock with the master password.")
}

// Lock locks the secrets now ("Lock now").
func (s *SecretsService) Lock(ctx context.Context) (StatusDTO, error) {
	if err := interrupted(ctx, "Locking the secrets"); err != nil {
		return StatusDTO{}, err
	}
	if s.vault != nil {
		s.vault.Lock()
	}
	return s.status(), nil
}

// ChangePassword replaces the master password; the old one must be right.
func (s *SecretsService) ChangePassword(ctx context.Context, in ChangePasswordInputDTO) (StatusDTO, error) {
	if err := interrupted(ctx, "Changing the master password"); err != nil {
		return StatusDTO{}, err
	}
	if err := s.available(); err != nil {
		return StatusDTO{}, err
	}
	if err := s.vault.ChangePassword(in.OldPassword, in.NewPassword); err != nil {
		return s.status(), err
	}
	s.logger.Info("secrets master password changed")
	return s.status(), nil
}

// Disable turns encryption off, writing the secrets back plain; the
// password must be right. Refused with secretstore.required when the
// administrator requires encryption.
func (s *SecretsService) Disable(ctx context.Context, in PasswordInputDTO) (StatusDTO, error) {
	if err := interrupted(ctx, "Turning off encryption"); err != nil {
		return StatusDTO{}, err
	}
	if err := s.available(); err != nil {
		return StatusDTO{}, err
	}
	if err := s.vault.Disable(in.Password); err != nil {
		return s.status(), err
	}
	s.keychain.setProblem(nil)
	s.logger.Info("secrets encryption turned off")
	return s.status(), nil
}

// Reset deletes every encrypted secret and the key file, for a forgotten
// password. Encryption is off afterwards (the UI offers to set a new
// password). Confirm must be true.
func (s *SecretsService) Reset(ctx context.Context, in ResetInputDTO) (StatusDTO, error) {
	if err := interrupted(ctx, "Deleting the encrypted secrets"); err != nil {
		return StatusDTO{}, err
	}
	if err := s.available(); err != nil {
		return StatusDTO{}, err
	}
	if !in.Confirm {
		return s.status(), errs.New(errs.InvalidArgument, "Deleting the encrypted secrets needs a confirmation.").
			WithCode(CodeConfirmRequired)
	}
	if err := s.vault.Reset(); err != nil {
		return s.status(), err
	}
	s.keychain.setProblem(nil)
	s.logger.Warn("encrypted secrets deleted (reset)")
	return s.status(), nil
}

// SetIdleLock sets privacy.secretsLockMinutes: the secrets lock after that
// many minutes without one being used, 0 never (0 to 1440). Fails with
// settings.locked when the admin policy locks it, and
// settings.invalid_value out of range.
func (s *SecretsService) SetIdleLock(ctx context.Context, minutes int) (StatusDTO, error) {
	if err := interrupted(ctx, "Saving the idle lock"); err != nil {
		return StatusDTO{}, err
	}
	if s.settings == nil {
		return s.status(), errs.New(errs.Unsupported, "The settings cannot be changed.").WithCode(CodeUnavailable)
	}
	if _, _, err := s.settings.Update(coresettings.Patch{Privacy: coresettings.PrivacyPatch{SecretsLockMinutes: &minutes}}); err != nil {
		return s.status(), err
	}
	return s.status(), nil
}
