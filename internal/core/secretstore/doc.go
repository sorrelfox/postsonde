// Package secretstore encrypts PostSonde's local secret files at rest
// with a master password (SEC-03).
//
// # What it covers
//
// Every file PostSonde keeps secrets in lives in a directory named
// "secrets" under the data directory, and is read and written through
// ReadFile and WriteFile here instead of os.ReadFile and atomicfile:
//
//	<data>/secrets/app.json                          appsecrets (proxy password, passphrases)
//	<data>/workspaces/<key>/secrets/requests/*.json  requestsecrets
//	<data>/workspaces/<key>/secrets/environments/*.json  environments
//	<data>/workspaces/<key>/secrets/runtime/variables.json  runtimevars
//	<data>/workspaces/<key>/secrets/oauth2/tokens.json      oauth2/tokenstore
//
// Each store keeps its own file format; this package only turns the bytes
// into an encrypted envelope and back, so the stores' methods do not
// change. Local values (localvalues) are not secrets and stay plain.
//
// # Opt-in
//
// Encryption is off until the user sets a master password (Vault.Enable):
// the app works without one, as before. With it on, the secret files are
// encrypted and the vault starts locked each time PostSonde starts; the
// UI asks for the password. An administrator can require encryption
// (Options.Required, the privacy.requireSecretEncryption setting locked by
// the policy file): turning it off is then refused, and so is writing a
// secret while it is off.
//
// # Keys
//
// A random 256-bit data key (DEK) encrypts the files. The master password
// never encrypts a file itself: PBKDF2-HMAC-SHA256 (crypto/pbkdf2, 600 000
// iterations by default, a random 32-byte salt per installation) derives a
// key-encryption key that wraps the DEK with AES-256-GCM. The KDF name,
// hash, iteration count and salt are stored next to the wrapped key in
// <data>/secrets/vault.json, so they can change later. A wrong password
// fails the GCM authentication of the wrapped key and is rejected; it
// never yields a wrong key. Changing the password re-wraps the DEK and
// replaces vault.json in one atomic write; the files are not touched.
//
// # Files
//
// An encrypted file is one line of JSON:
//
//	{"postsondeSecret":1,"cipher":"AES-256-GCM","keyId":"…","nonce":"…","data":"…"}
//
// data is AES-256-GCM with a fresh random 96-bit nonce per write. The
// associated data binds it to the key ID and to the file's name below its
// "secrets" directory ("requests/req_1.json", "app.json"), so a file
// copied over another is rejected, as are tampered and truncated files. A
// data directory or a workspace's local directory moved or copied as a
// whole still opens.
//
// # Migration and recovery
//
// Turning encryption on writes vault.json first, marked "encrypting", then
// encrypts the files one by one, each with atomicfile (the encrypted copy
// is complete and synced before it replaces the plain one), then clears
// the mark. ReadFile reads plain and encrypted files alike, so a crash at
// any point loses nothing: the next start finds the mark, and the next
// unlock encrypts what is left. Turning it off does the same the other
// way ("decrypting"), removing vault.json last. A forgotten password
// cannot be recovered: Reset deletes the encrypted files and vault.json.
//
// # Keychain
//
// A KeyStore can keep the DEK outside the password (the OS keychain):
// UnlockFromKeyStore unlocks with it, RememberKey saves the unlocked key,
// ForgetKey removes it, and Disable and Reset delete it. The OS keychains
// are in the keychain subpackage (docs/privacy.md#keychain).
//
// Standard library only. See docs/privacy.md#encrypted-secrets.
package secretstore
