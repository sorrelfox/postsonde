package secretstore

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/storage/atomicfile"
)

// ConfigFile is the vault's key file, relative to the vault's root (the
// data directory), slash-separated. It holds no secret in the clear: the
// KDF parameters, the wrapped data key and a key check.
const ConfigFile = "secrets/vault.json"

// KDF parameters of new keys.
const (
	// DefaultIterations is the PBKDF2-HMAC-SHA256 iteration count of a
	// new or changed password (OWASP's 2023 figure for that KDF).
	DefaultIterations = 600_000
	// MaxIterations bounds the count vault.json may ask for, so a broken
	// file cannot make an unlock run for hours.
	MaxIterations = 50_000_000
	// MinPasswordLength is the shortest master password, in characters.
	MinPasswordLength = 8
)

// KDFName names the key derivation in Status and vault.json.
const KDFName = "pbkdf2-hmac-sha256"

// Migrations a vault can be in the middle of (Status.Migration).
const (
	// MigrationEncrypting: turning encryption on did not finish; the files
	// left plain are encrypted at the next unlock.
	MigrationEncrypting = "encrypting"
	// MigrationDecrypting: turning encryption off did not finish; the
	// files left encrypted are decrypted at the next unlock, then
	// vault.json goes.
	MigrationDecrypting = "decrypting"
)

const (
	configVersion             = 1
	kdfPBKDF2                 = "pbkdf2"
	kdfSHA256                 = "sha256"
	saltLen                   = 32
	keyLen                    = 32
	keyIDLen                  = 8
	dirPerm       fs.FileMode = 0o700
	filePerm      fs.FileMode = 0o600
	checkText                 = "postsonde key check"
)

// KeyStore keeps the data key somewhere other than behind the password:
// the OS keychain. Keys are stored by the vault's key ID, so a
// reset vault never unlocks with an old key.
type KeyStore interface {
	// LoadKey returns the key stored for keyID; nil and no error when
	// there is none.
	LoadKey(keyID string) ([]byte, error)
	// StoreKey stores key for keyID, replacing any.
	StoreKey(keyID string, key []byte) error
	// DeleteKey removes the key of keyID; none stored is not an error.
	DeleteKey(keyID string) error
}

// Options configures a Vault. Every field is optional.
type Options struct {
	// Iterations is the PBKDF2 count for a new or changed password;
	// 0 means DefaultIterations. Tests lower it. A vault opens with the
	// count its vault.json names, whatever this is.
	Iterations int
	// IdleTimeout, read on every use, locks the vault once no secret has
	// been read or written for that long; nil or 0 never locks.
	IdleTimeout func() time.Duration
	// Required, read on every use, says whether the administrator
	// requires encryption: Disable is refused, and so is writing a secret
	// file while encryption is off.
	Required func() bool
	// OnChange is called with the new status after the vault is enabled,
	// disabled, reset, locked or unlocked, and after its password
	// changes; for the idle lock, by LockIfIdle. Never with a lock of the
	// vault held, and never from ReadFile or WriteFile, so it may call a
	// store that reads or writes secrets under its own lock.
	OnChange func(Status)
	// KeyStore is where UnlockFromKeyStore and RememberKey find and keep
	// the data key.
	KeyStore KeyStore
	// Now is the clock of the idle timeout; nil means time.Now.
	Now func() time.Time
}

// Status is what the vault is doing.
type Status struct {
	// Enabled: secrets are encrypted (vault.json exists).
	Enabled bool
	// Locked: Enabled, and the key is not in memory.
	Locked bool
	// Migration is an interrupted migration, resumed at the next unlock:
	// MigrationEncrypting, MigrationDecrypting, or "".
	Migration string
	// Required: the administrator requires encryption.
	Required bool
	// IdleTimeout locks the vault after that long without a secret being
	// used; 0 never.
	IdleTimeout time.Duration
	// KDF and Iterations are the key derivation of the password; "" and 0
	// while off.
	KDF        string
	Iterations int
	// Problem is why vault.json cannot be used; nil when it can. Such a
	// vault counts as enabled and locked; Reset clears it.
	Problem error
}

// config is vault.json.
type config struct {
	Version int    `json:"version"`
	KeyID   string `json:"keyId"`
	KDF     kdf    `json:"kdf"`
	// WrappedKey is the data key, sealed with the key derived from the
	// password.
	WrappedKey sealedBox `json:"wrappedKey"`
	// Check is checkText sealed with the data key, to check a key from a
	// KeyStore.
	Check     sealedBox `json:"check"`
	Migration string    `json:"migration"`
}

type kdf struct {
	Name       string `json:"name"`
	Hash       string `json:"hash"`
	Iterations int    `json:"iterations"`
	Salt       string `json:"salt"`
}

type sealedBox struct {
	Nonce string `json:"nonce"`
	Data  string `json:"data"`
}

// Vault holds the key of one data directory's secret files. It is safe for
// concurrent use.
type Vault struct {
	root   string
	config string
	opts   Options

	// fileMu serialises the writes of secret files (WriteFile) with the
	// migrations, the password change and Reset, so a migration never
	// overwrites a newer write.
	fileMu sync.Mutex

	mu      sync.Mutex
	cfg     *config // nil: encryption off
	cfgErr  error   // vault.json could not be used
	key     []byte  // the data key; nil while locked
	lastUse time.Time
	// unnoticed: the idle timeout locked the vault during a use or a
	// Status call; LockIfIdle reports it and calls OnChange.
	unnoticed bool

	// hook, set by tests, is called before each step of a migration
	// ("config" after vault.json is written, "file" before each file,
	// "finish" before vault.json is updated); an error stops the
	// migration there, as a crash would.
	hook func(step, path string) error
}

// Open returns the vault of root (the data directory): off when root has
// no vault.json, else locked. A vault.json that cannot be used is kept as
// Status.Problem.
func Open(root string, opts Options) *Vault {
	if opts.Iterations <= 0 {
		opts.Iterations = DefaultIterations
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	v := &Vault{root: root, config: filepath.Join(root, filepath.FromSlash(ConfigFile)), opts: opts}
	v.cfg, v.cfgErr = readConfig(v.config)
	return v
}

// Root returns the directory the vault covers.
func (v *Vault) Root() string { return v.root }

// Status returns what the vault is doing now.
func (v *Vault) Status() Status {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.expireLocked()
	st := Status{Required: v.required(), IdleTimeout: v.idle(), Problem: v.cfgErr}
	switch {
	case v.cfg != nil:
		st.Enabled, st.Locked, st.Migration = true, v.key == nil, v.cfg.Migration
		st.KDF, st.Iterations = KDFName, v.cfg.KDF.Iterations
	case v.cfgErr != nil:
		st.Enabled, st.Locked = true, true
	}
	return st
}

// Enable turns encryption on with password: it creates the data key and
// vault.json, then encrypts every secret file. The vault is unlocked
// afterwards. When encrypting a file fails, vault.json keeps the
// "encrypting" mark and the next unlock carries on.
func (v *Vault) Enable(password string) error {
	if err := checkPassword(password); err != nil {
		return err
	}
	// OnChange runs once fileMu is released (defers run last in, first
	// out), so it may take locks a secret write holds.
	defer v.notify()
	v.fileMu.Lock()
	defer v.fileMu.Unlock()
	v.mu.Lock()
	cfg, cfgErr := v.cfg, v.cfgErr
	v.mu.Unlock()
	if cfgErr != nil {
		return cfgErr
	}
	if cfg != nil {
		return alreadyEnabled()
	}
	dek := randomBytes(keyLen)
	cfg = &config{Version: configVersion, KeyID: hex.EncodeToString(randomBytes(keyIDLen)), Migration: MigrationEncrypting}
	if err := wrap(cfg, password, dek, v.opts.Iterations); err != nil {
		return err
	}
	check, err := seal(dek, []byte(checkText), checkAAD(cfg.KeyID))
	if err != nil {
		return err
	}
	cfg.Check = check
	if err := v.writeConfig(cfg); err != nil {
		return err
	}
	v.mu.Lock()
	v.cfg, v.key, v.lastUse = cfg, dek, v.opts.Now()
	v.mu.Unlock()
	if err := v.step("config", v.config); err != nil {
		return err
	}
	return v.migrate()
}

// Unlock unlocks the vault with the master password. A wrong password is
// CodeWrongPassword. An interrupted migration is carried on at once; its
// error is returned, with the vault unlocked.
func (v *Vault) Unlock(password string) error {
	cfg, err := v.current()
	if err != nil {
		return err
	}
	dek, err := unwrap(cfg, password)
	if err != nil {
		return err
	}
	return v.unlockWith(cfg, dek)
}

// UnlockWithKey unlocks the vault with the data key itself, as a KeyStore
// keeps it. A key that is not this vault's is CodeWrongPassword.
func (v *Vault) UnlockWithKey(key []byte) error {
	cfg, err := v.current()
	if err != nil {
		return err
	}
	if err := checkKey(cfg, key); err != nil {
		return err
	}
	return v.unlockWith(cfg, append([]byte(nil), key...))
}

// UnlockFromKeyStore unlocks the vault with the key its KeyStore holds. It
// reports false, and no error, when there is no KeyStore, nothing to
// unlock or no key stored; the password is then needed.
func (v *Vault) UnlockFromKeyStore() (bool, error) {
	ks := v.opts.KeyStore
	if ks == nil {
		return false, nil
	}
	v.mu.Lock()
	cfg, locked := v.cfg, v.key == nil
	v.mu.Unlock()
	if cfg == nil || !locked {
		return false, nil
	}
	key, err := ks.LoadKey(cfg.KeyID)
	if err != nil || key == nil {
		return false, err
	}
	if err := v.UnlockWithKey(key); err != nil {
		return false, err
	}
	return true, nil
}

// RememberKey saves the unlocked data key in the KeyStore, so
// UnlockFromKeyStore can unlock without the password.
func (v *Vault) RememberKey() error {
	ks := v.opts.KeyStore
	if ks == nil {
		return errs.New(errs.Unsupported, "There is no keychain to keep the key in.")
	}
	v.mu.Lock()
	cfg, key := v.cfg, append([]byte(nil), v.key...)
	v.mu.Unlock()
	if cfg == nil {
		return notEnabled()
	}
	if len(key) == 0 {
		return lockedError("")
	}
	return ks.StoreKey(cfg.KeyID, key)
}

// ForgetKey removes the vault's key from the KeyStore, so only the master
// password unlocks it. Nothing happens without a KeyStore or while
// encryption is off; the vault's state does not change.
func (v *Vault) ForgetKey() error {
	ks := v.opts.KeyStore
	if ks == nil {
		return nil
	}
	v.mu.Lock()
	cfg := v.cfg
	v.mu.Unlock()
	if cfg == nil {
		return nil
	}
	return ks.DeleteKey(cfg.KeyID)
}

func (v *Vault) unlockWith(cfg *config, dek []byte) error {
	v.mu.Lock()
	if v.cfg == nil || v.cfg.KeyID != cfg.KeyID {
		v.mu.Unlock()
		return notEnabled()
	}
	if v.key != nil {
		clear(v.key)
	}
	v.key, v.lastUse, v.unnoticed = dek, v.opts.Now(), false
	migration := v.cfg.Migration
	v.mu.Unlock()
	defer v.notify()
	if migration == "" {
		return nil
	}
	v.fileMu.Lock()
	defer v.fileMu.Unlock()
	return v.migrate()
}

// Lock forgets the key; secrets cannot be read or written until the next
// unlock. Nothing happens when the vault is off or locked.
func (v *Vault) Lock() {
	v.mu.Lock()
	changed := v.key != nil || v.unnoticed
	v.lockLocked()
	v.unnoticed = false
	v.mu.Unlock()
	if changed {
		v.notify()
	}
}

// LockIfIdle locks the vault when the idle timeout has passed since a
// secret was last used, and reports whether it did, also when a use of a
// secret found the time passed and locked it since the last call. The app
// calls it on a timer, which is where OnChange hears of the idle lock: a
// read or write of a secret file never calls OnChange, since the store
// calling it may hold locks OnChange needs.
func (v *Vault) LockIfIdle() bool {
	v.mu.Lock()
	v.expireLocked()
	changed := v.unnoticed
	v.unnoticed = false
	v.mu.Unlock()
	if changed {
		v.notify()
	}
	return changed
}

// ChangePassword replaces the master password: the data key is wrapped
// with a key derived from newPassword (a new salt, the current iteration
// count) and vault.json replaced in one atomic write. The files keep their
// encryption. oldPassword must be right. The vault is unlocked
// afterwards.
func (v *Vault) ChangePassword(oldPassword, newPassword string) error {
	if err := checkPassword(newPassword); err != nil {
		return err
	}
	v.fileMu.Lock()
	cfg, err := v.current()
	if err != nil {
		v.fileMu.Unlock()
		return err
	}
	dek, err := unwrap(cfg, oldPassword)
	if err != nil {
		v.fileMu.Unlock()
		return err
	}
	next := *cfg
	if err := wrap(&next, newPassword, dek, v.opts.Iterations); err != nil {
		v.fileMu.Unlock()
		return err
	}
	if err := v.writeConfig(&next); err != nil {
		v.fileMu.Unlock()
		return err
	}
	v.mu.Lock()
	if v.key != nil {
		clear(v.key)
	}
	v.cfg, v.key, v.lastUse, v.unnoticed = &next, dek, v.opts.Now(), false
	v.mu.Unlock()
	v.fileMu.Unlock()
	v.notify()
	return nil
}

// Disable turns encryption off: it checks password, marks vault.json
// "decrypting", writes every encrypted file back plain, then removes
// vault.json (and the KeyStore's key). When decrypting a file fails, the
// mark stays and the next unlock carries on. Refused with CodeRequired
// while the administrator requires encryption.
func (v *Vault) Disable(password string) error {
	if v.required() {
		return required()
	}
	defer v.notify() // after fileMu is released
	v.fileMu.Lock()
	defer v.fileMu.Unlock()
	cfg, err := v.current()
	if err != nil {
		return err
	}
	dek, err := unwrap(cfg, password)
	if err != nil {
		return err
	}
	next := *cfg
	next.Migration = MigrationDecrypting
	if err := v.writeConfig(&next); err != nil {
		return err
	}
	v.mu.Lock()
	if v.key != nil {
		clear(v.key)
	}
	v.cfg, v.key, v.lastUse, v.unnoticed = &next, dek, v.opts.Now(), false
	v.mu.Unlock()
	if err := v.step("config", v.config); err != nil {
		return err
	}
	return v.migrate()
}

// Reset is the way out of a forgotten password: it deletes every
// encrypted secret file, then vault.json and the KeyStore's key.
// Encryption is off afterwards; plain secret files (left by an
// interrupted migration) stay. It works without the password and with a
// vault.json that cannot be used. The UI asks for a strong confirmation
// first: the secrets are gone for good.
func (v *Vault) Reset() error {
	defer v.notify() // after fileMu is released
	v.fileMu.Lock()
	defer v.fileMu.Unlock()
	files, err := v.secretFiles()
	if err != nil {
		return err
	}
	for _, f := range files {
		if f.temp {
			_ = os.Remove(f.path)
			continue
		}
		data, err := os.ReadFile(f.path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return failed(err, "PostSonde could not read a stored secrets file.", f.path)
		}
		if IsEncrypted(data) {
			if err := os.Remove(f.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return failed(err, "PostSonde could not delete an encrypted secrets file.", f.path)
			}
		}
	}
	v.mu.Lock()
	cfg := v.cfg
	v.mu.Unlock()
	if err := os.Remove(v.config); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return failed(err, "PostSonde could not delete its key file.", v.config)
	}
	v.mu.Lock()
	v.lockLocked()
	v.cfg, v.cfgErr, v.unnoticed = nil, nil, false
	v.mu.Unlock()
	if cfg != nil && v.opts.KeyStore != nil {
		_ = v.opts.KeyStore.DeleteKey(cfg.KeyID)
	}
	return nil
}

// current returns the config of an enabled vault.
func (v *Vault) current() (*config, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.cfgErr != nil {
		return nil, v.cfgErr
	}
	if v.cfg == nil {
		return nil, notEnabled()
	}
	return v.cfg, nil
}

// aead returns the cipher of the data key and the config, with
// v.mu held by the caller; a nil cipher while locked. It counts as a use
// for the idle timeout.
func (v *Vault) aeadLocked() (cipher.AEAD, error) {
	if v.key == nil {
		return nil, nil
	}
	v.lastUse = v.opts.Now()
	return newAEAD(v.key)
}

func (v *Vault) lockLocked() {
	if v.key != nil {
		clear(v.key)
		v.key = nil
	}
}

// expireLocked locks the vault when it has been idle for too long, and
// marks the lock for LockIfIdle to report. v.mu is held.
func (v *Vault) expireLocked() {
	if v.key == nil {
		return
	}
	d := v.idle()
	if d <= 0 || v.opts.Now().Sub(v.lastUse) < d {
		return
	}
	v.lockLocked()
	v.unnoticed = true
}

func (v *Vault) idle() time.Duration {
	if v.opts.IdleTimeout == nil {
		return 0
	}
	return max(v.opts.IdleTimeout(), 0)
}

func (v *Vault) required() bool { return v.opts.Required != nil && v.opts.Required() }

func (v *Vault) notify() {
	if v.opts.OnChange != nil {
		v.opts.OnChange(v.Status())
	}
}

func (v *Vault) step(step, path string) error {
	if v.hook == nil {
		return nil
	}
	return v.hook(step, path)
}

func (v *Vault) writeConfig(cfg *config) error {
	if err := os.MkdirAll(filepath.Dir(v.config), dirPerm); err != nil {
		return failed(err, "PostSonde could not save its key file.", filepath.Dir(v.config))
	}
	if err := atomicfile.WriteJSON(v.config, cfg, filePerm); err != nil {
		return failed(err, "PostSonde could not save its key file.", v.config)
	}
	return nil
}

// readConfig reads vault.json: nil without one.
func readConfig(path string) (*config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, failed(err, "PostSonde could not read its key file.", path)
	}
	var cfg config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, configInvalid(path, err)
	}
	if err := cfg.validate(); err != nil {
		return nil, configInvalid(path, err)
	}
	return &cfg, nil
}

func configInvalid(path string, cause error) error {
	return errs.Wrap(cause, errs.Parse, "PostSonde's key file for encrypted secrets cannot be used.").
		WithCode(CodeConfigInvalid).
		WithHint("Restore it from a backup, or delete the encrypted secrets (Settings, Privacy, Reset).").
		WithDetail("path", path)
}

func (c *config) validate() error {
	switch {
	case c.Version != configVersion:
		return errors.New("unsupported version " + strconv.Itoa(c.Version))
	case c.KDF.Name != kdfPBKDF2 || c.KDF.Hash != kdfSHA256:
		return errors.New("unsupported key derivation " + c.KDF.Name + "-" + c.KDF.Hash)
	case c.KDF.Iterations < 1 || c.KDF.Iterations > MaxIterations:
		return errors.New("iteration count out of range")
	case c.Migration != "" && c.Migration != MigrationEncrypting && c.Migration != MigrationDecrypting:
		return errors.New("unknown migration " + c.Migration)
	}
	if id, err := hex.DecodeString(c.KeyID); err != nil || len(id) != keyIDLen {
		return errors.New("invalid key ID")
	}
	if salt, err := base64.StdEncoding.DecodeString(c.KDF.Salt); err != nil || len(salt) < 16 {
		return errors.New("invalid salt")
	}
	return nil
}

func checkPassword(password string) error {
	if utf8.RuneCountInString(password) < MinPasswordLength {
		return errs.New(errs.InvalidArgument, "The master password must have at least "+strconv.Itoa(MinPasswordLength)+" characters.").
			WithCode(CodePasswordTooShort).
			WithDetail("min", strconv.Itoa(MinPasswordLength))
	}
	return nil
}

// wrap sets cfg's KDF (a new salt, iterations) and wraps dek with the key
// derived from password.
func wrap(cfg *config, password string, dek []byte, iterations int) error {
	cfg.KDF = kdf{Name: kdfPBKDF2, Hash: kdfSHA256, Iterations: iterations, Salt: base64.StdEncoding.EncodeToString(randomBytes(saltLen))}
	kek, err := deriveKEK(cfg.KDF, password)
	if err != nil {
		return err
	}
	defer clear(kek)
	box, err := seal(kek, dek, wrapAAD(cfg))
	if err != nil {
		return err
	}
	cfg.WrappedKey = box
	return nil
}

// unwrap returns the data key of cfg for password; CodeWrongPassword when
// the password is not the one it was wrapped with.
func unwrap(cfg *config, password string) ([]byte, error) {
	kek, err := deriveKEK(cfg.KDF, password)
	if err != nil {
		return nil, err
	}
	defer clear(kek)
	dek, err := open(kek, cfg.WrappedKey, wrapAAD(cfg))
	if err != nil || len(dek) != keyLen {
		return nil, wrongPassword()
	}
	return dek, nil
}

func checkKey(cfg *config, key []byte) error {
	if len(key) != keyLen {
		return wrongPassword()
	}
	text, err := open(key, cfg.Check, checkAAD(cfg.KeyID))
	if err != nil || string(text) != checkText {
		return wrongPassword()
	}
	return nil
}

func deriveKEK(k kdf, password string) ([]byte, error) {
	salt, err := base64.StdEncoding.DecodeString(k.Salt)
	if err != nil {
		return nil, errs.Wrap(err, errs.Parse, "The key file's salt cannot be read.").WithCode(CodeConfigInvalid)
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, k.Iterations, keyLen)
	if err != nil {
		return nil, errs.Wrap(err, errs.Internal, "The key could not be derived from the password.")
	}
	return key, nil
}

// wrapAAD binds the wrapped key to the vault's key ID and KDF parameters.
func wrapAAD(cfg *config) []byte {
	return []byte("postsonde/vault-key/1\n" + cfg.KeyID + "\n" + cfg.KDF.Name + "-" + cfg.KDF.Hash + "\n" +
		strconv.Itoa(cfg.KDF.Iterations) + "\n" + cfg.KDF.Salt)
}

func checkAAD(keyID string) []byte { return []byte("postsonde/key-check/1\n" + keyID) }

func newAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, errs.Wrap(err, errs.Internal, "The encryption key cannot be used.")
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errs.Wrap(err, errs.Internal, "The encryption key cannot be used.")
	}
	return aead, nil
}

func seal(key, plaintext, aad []byte) (sealedBox, error) {
	aead, err := newAEAD(key)
	if err != nil {
		return sealedBox{}, err
	}
	nonce := randomBytes(aead.NonceSize())
	return sealedBox{
		Nonce: base64.StdEncoding.EncodeToString(nonce),
		Data:  base64.StdEncoding.EncodeToString(aead.Seal(nil, nonce, plaintext, aad)),
	}, nil
}

func open(key []byte, box sealedBox, aad []byte) ([]byte, error) {
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	nonce, err := base64.StdEncoding.DecodeString(box.Nonce)
	if err != nil || len(nonce) != aead.NonceSize() {
		return nil, errors.New("invalid nonce")
	}
	data, err := base64.StdEncoding.DecodeString(box.Data)
	if err != nil {
		return nil, err
	}
	return aead.Open(nil, nonce, data, aad)
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b) // crypto/rand.Read never fails (Go 1.24+)
	return b
}
