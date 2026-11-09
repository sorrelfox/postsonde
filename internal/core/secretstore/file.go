package secretstore

import (
	"bytes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/sorrelfox/postsonde/internal/core/storage/atomicfile"
	"github.com/sorrelfox/postsonde/internal/core/storage/stablejson"
)

// SecretsDirName is the name of the directories secret files live in.
// Migrations look for them directly in a vault's root (<data>/secrets)
// and two levels down (<data>/workspaces/<key>/secrets), nowhere else, so
// a folder a project happens to call "secrets" (in the local trash, say)
// is never touched.
const SecretsDirName = "secrets"

// cipherName is the only cipher of the envelope.
const cipherName = "AES-256-GCM"

// envelopePrefix starts every encrypted file.
const envelopePrefix = `{"postsondeSecret":`

// envelope is an encrypted file.
type envelope struct {
	PostsondeSecret int    `json:"postsondeSecret"`
	Cipher          string `json:"cipher"`
	KeyID           string `json:"keyId"`
	Nonce           string `json:"nonce"`
	Data            string `json:"data"`
}

// registry holds the vaults of the process, by root: a secret file is
// handled by the vault whose root holds it. Files under no vault's root
// (tests of the stores, a run without a data directory) are plain.
var registry struct {
	mu     sync.RWMutex
	vaults []*Vault
}

// Register makes v handle the secret files under its root, replacing a
// vault registered for the same root, and returns a function that undoes
// it.
func Register(v *Vault) (unregister func()) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.vaults = slices.DeleteFunc(registry.vaults, func(o *Vault) bool { return o.root == v.root })
	registry.vaults = append(registry.vaults, v)
	return func() {
		registry.mu.Lock()
		defer registry.mu.Unlock()
		registry.vaults = slices.DeleteFunc(registry.vaults, func(o *Vault) bool { return o == v })
	}
}

// vaultFor returns the registered vault whose root holds path (the
// deepest one), and path relative to it; nil when none does.
func vaultFor(path string) (*Vault, string) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, ""
	}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	var best *Vault
	var bestRel string
	for _, v := range registry.vaults {
		rel, err := filepath.Rel(v.root, abs)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			continue
		}
		if best == nil || len(v.root) > len(best.root) {
			best, bestRel = v, rel
		}
	}
	return best, bestRel
}

// IsEncrypted reports whether data is an encrypted file (possibly a
// damaged one).
func IsEncrypted(data []byte) bool {
	return bytes.HasPrefix(bytes.TrimLeft(data, " \t\r\n"), []byte(envelopePrefix))
}

// ReadFile reads the secret file at path and returns its plain content.
// Errors are those of os.ReadFile (fs.ErrNotExist for a missing file) or
// errors of this package: CodeLocked while the vault is locked,
// CodeCorrupt for a damaged, truncated, tampered or swapped file (and for
// a plain file while every file should be encrypted), CodeOtherKey,
// CodeNotSetUp.
func ReadFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	v, rel := vaultFor(path)
	if !IsEncrypted(data) {
		if v != nil {
			if err := v.checkPlain(path); err != nil {
				return nil, err
			}
		}
		return data, nil
	}
	if v == nil {
		return nil, notSetUp(path)
	}
	return v.decrypt(path, rel, data)
}

// WriteFile replaces the secret file at path with data, atomically
// (atomicfile), encrypted when its vault is enabled. It fails with
// CodeLocked while the vault is locked, and with CodeRequired when the
// administrator requires encryption and it is off; the file is then left
// as it was.
func WriteFile(path string, data []byte, perm fs.FileMode) error {
	v, rel := vaultFor(path)
	if v == nil {
		return atomicfile.WriteFile(path, data, perm)
	}
	v.fileMu.Lock()
	defer v.fileMu.Unlock()
	out, err := v.encrypt(path, rel, data)
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(path, out, perm)
}

// WriteJSON encodes value with stablejson, as atomicfile.WriteJSON does,
// and writes it with WriteFile.
func WriteJSON(path string, value any, perm fs.FileMode) error {
	data, err := stablejson.Marshal(value)
	if err != nil {
		return fmt.Errorf("secretstore: encode %s: %w", path, err)
	}
	return WriteFile(path, data, perm)
}

// checkPlain refuses a plain file while every secret file must be
// encrypted: the vault is enabled and no migration is under way, so the
// file was put there by someone else.
func (v *Vault) checkPlain(path string) error {
	v.mu.Lock()
	cfg := v.cfg
	v.mu.Unlock()
	if cfg == nil || cfg.Migration != "" {
		return nil
	}
	return corrupt(path, errors.New("the file is not encrypted"))
}

// useKey returns the config and the data key's cipher (nil while locked),
// after locking the vault if it has been idle too long; a use counts for
// the idle timeout. It never calls OnChange (LockIfIdle does).
func (v *Vault) useKey() (*config, error, cipher.AEAD, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.expireLocked()
	aead, err := v.aeadLocked()
	return v.cfg, v.cfgErr, aead, err
}

func (v *Vault) decrypt(path, rel string, data []byte) ([]byte, error) {
	cfg, cfgErr, aead, err := v.useKey()
	switch {
	case err != nil:
		return nil, err
	case cfg == nil && cfgErr != nil:
		return nil, cfgErr
	case cfg == nil:
		return nil, notSetUp(path)
	case aead == nil:
		return nil, lockedError(path)
	}
	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, corrupt(path, err)
	}
	if env.PostsondeSecret != 1 || env.Cipher != cipherName {
		return nil, corrupt(path, fmt.Errorf("unsupported format %d %q", env.PostsondeSecret, env.Cipher))
	}
	if env.KeyID != cfg.KeyID {
		return nil, otherKey(path)
	}
	nonce, err := base64.StdEncoding.DecodeString(env.Nonce)
	if err != nil || len(nonce) != aead.NonceSize() {
		return nil, corrupt(path, errors.New("invalid nonce"))
	}
	ct, err := base64.StdEncoding.DecodeString(env.Data)
	if err != nil {
		return nil, corrupt(path, err)
	}
	plain, err := aead.Open(nil, nonce, ct, fileAAD(cfg.KeyID, rel))
	if err != nil {
		return nil, corrupt(path, errors.New("authentication failed"))
	}
	return plain, nil
}

// encrypt returns what WriteFile writes for data: an envelope while the
// vault is enabled, data itself while it is off.
func (v *Vault) encrypt(path, rel string, data []byte) ([]byte, error) {
	cfg, cfgErr, aead, err := v.useKey()
	switch {
	case err != nil:
		return nil, err
	case cfg == nil && cfgErr != nil:
		return nil, cfgErr
	case cfg == nil && v.required():
		return nil, required()
	case cfg == nil:
		return data, nil
	case aead == nil:
		return nil, lockedError(path)
	}
	return sealFile(aead, cfg.KeyID, rel, data), nil
}

func sealFile(aead cipher.AEAD, keyID, rel string, data []byte) []byte {
	nonce := randomBytes(aead.NonceSize())
	env := envelope{
		PostsondeSecret: 1,
		Cipher:          cipherName,
		KeyID:           keyID,
		Nonce:           base64.StdEncoding.EncodeToString(nonce),
		Data:            base64.StdEncoding.EncodeToString(aead.Seal(nil, nonce, data, fileAAD(keyID, rel))),
	}
	out, _ := json.Marshal(env) // plain strings and an int: cannot fail
	return append(out, '\n')
}

// fileAAD binds a file's content to the key and to the file's name below
// its secrets directory ("requests/req_1.json"), so moving the data
// directory or copying a workspace's local directory keeps it readable
// while a file copied over another is not.
func fileAAD(keyID, rel string) []byte {
	return []byte("postsonde/secret-file/1\n" + keyID + "\n" + secretName(rel))
}

// secretName is rel (a path relative to the vault root) below its first
// "secrets" directory, slash-separated; rel itself when it has none.
func secretName(rel string) string {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if i := slices.Index(parts, SecretsDirName); i >= 0 && i < len(parts)-1 {
		return strings.Join(parts[i+1:], "/")
	}
	return strings.Join(parts, "/")
}

// secretFile is a file a migration handles.
type secretFile struct {
	path string
	rel  string
	// temp is a ".<name>.tmp-*" leftover of an interrupted atomic write,
	// which may hold a secret in the clear: migrations delete it.
	temp bool
}

// secretFiles lists the files in the vault's secrets directories (see
// SecretsDirName), vault.json left out, in a fixed order. Symbolic links
// are not followed.
func (v *Vault) secretFiles() ([]secretFile, error) {
	var dirs []string
	dirs = append(dirs, filepath.Join(v.root, SecretsDirName))
	tops, err := os.ReadDir(v.root)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, failed(err, "PostSonde could not list its data directory.", v.root)
	}
	for _, top := range tops {
		if !top.IsDir() || top.Name() == SecretsDirName {
			continue
		}
		subs, err := os.ReadDir(filepath.Join(v.root, top.Name()))
		if err != nil {
			continue
		}
		for _, sub := range subs {
			if sub.IsDir() {
				dirs = append(dirs, filepath.Join(v.root, top.Name(), sub.Name(), SecretsDirName))
			}
		}
	}
	var out []secretFile
	for _, dir := range dirs {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) && path == dir {
					return nil
				}
				return err
			}
			if !d.Type().IsRegular() || path == v.config {
				return nil
			}
			rel, err := filepath.Rel(v.root, path)
			if err != nil {
				return err
			}
			name := d.Name()
			out = append(out, secretFile{path: path, rel: rel, temp: strings.HasPrefix(name, ".") && strings.Contains(name, ".tmp-")})
			return nil
		})
		if err != nil {
			return nil, failed(err, "PostSonde could not list its stored secrets.", dir)
		}
	}
	return out, nil
}

// migrate carries on the migration vault.json is marked with: it
// encrypts (or decrypts) every secret file that is not yet, deletes
// leftovers of interrupted writes, then clears the mark (or removes
// vault.json). v.fileMu is held and the vault unlocked. Each file is
// replaced atomically, so it is always either the old or the new form,
// and ReadFile reads both.
func (v *Vault) migrate() error {
	v.mu.Lock()
	cfg := v.cfg
	aead, err := v.aeadLocked()
	v.mu.Unlock()
	if err != nil {
		return err
	}
	if cfg == nil || cfg.Migration == "" {
		return nil
	}
	if aead == nil {
		return lockedError("")
	}
	files, err := v.secretFiles()
	if err != nil {
		return err
	}
	for _, f := range files {
		if err := v.step("file", f.path); err != nil {
			return err
		}
		if f.temp {
			if err := os.Remove(f.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return failed(err, "PostSonde could not delete a leftover file.", f.path)
			}
			continue
		}
		data, err := os.ReadFile(f.path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return failed(err, "PostSonde could not read a stored secrets file.", f.path)
		}
		var out []byte
		switch {
		case cfg.Migration == MigrationEncrypting && !IsEncrypted(data):
			out = sealFile(aead, cfg.KeyID, f.rel, data)
		case cfg.Migration == MigrationDecrypting && IsEncrypted(data):
			if out, err = v.decrypt(f.path, f.rel, data); err != nil {
				return err
			}
		default:
			continue
		}
		if err := atomicfile.WriteFile(f.path, out, filePerm); err != nil {
			return failed(err, "PostSonde could not rewrite a stored secrets file.", f.path)
		}
	}
	if err := v.step("finish", v.config); err != nil {
		return err
	}
	if cfg.Migration == MigrationEncrypting {
		next := *cfg
		next.Migration = ""
		if err := v.writeConfig(&next); err != nil {
			return err
		}
		v.mu.Lock()
		v.cfg = &next
		v.mu.Unlock()
		return nil
	}
	if err := os.Remove(v.config); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return failed(err, "PostSonde could not delete its key file.", v.config)
	}
	v.mu.Lock()
	v.lockLocked()
	v.cfg = nil
	v.mu.Unlock()
	if v.opts.KeyStore != nil {
		_ = v.opts.KeyStore.DeleteKey(cfg.KeyID)
	}
	return nil
}
