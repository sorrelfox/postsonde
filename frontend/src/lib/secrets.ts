// Calls into the Go SecretsService (internal/app/services/secrets): the
// encryption of the stored secrets with a master password (SEC-03).
// See docs/privacy.md#encrypted-secrets.
import { Events } from "@wailsio/runtime";
import {
  SecretsService,
  type KeychainDTO,
  type StatusDTO,
} from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/secrets";
import type { CancellablePromise } from "./calls";

export type SecretsStatus = StatusDTO;
export type SecretsKeychain = KeychainDTO;

/** Sent by Go whenever the secrets are turned on or off, locked or unlocked, or reset. */
export const SECRETS_CHANGED_EVENT = "secrets:changed";

/** Error codes the UI reacts to (internal/core/secretstore, internal/app/services/secrets). */
export const SECRETS_CODES = {
  locked: "secretstore.locked",
  wrongPassword: "secretstore.wrong_password",
  passwordTooShort: "secretstore.password_too_short",
  required: "secretstore.required",
  unavailable: "secrets.unavailable",
  noKeychain: "secrets.no_keychain",
  keychainNoKey: "secrets.keychain_no_key",
  keychainUnavailable: "keychain.unavailable",
  keychainFailed: "keychain.failed",
} as const;

/** privacy.secretsKeyStore: the key is also in the OS keychain, or only behind the master password. */
export type SecretsKeyStore = "keychain" | "password";

/** The idle lock choices offered, in minutes; 0 is never. */
export const IDLE_LOCK_CHOICES: readonly number[] = [0, 5, 15, 30, 60, 240];

export function getSecretsStatus(): CancellablePromise<SecretsStatus> {
  return SecretsService.Status();
}

/** Turns encryption on with a new master password; every stored secret is encrypted. */
export function enableSecrets(password: string): CancellablePromise<SecretsStatus> {
  return SecretsService.Enable({ password });
}

export function unlockSecrets(password: string): CancellablePromise<SecretsStatus> {
  return SecretsService.Unlock({ password });
}

/** Unlocks with the key kept in the OS keychain, as at startup. */
export function unlockSecretsWithKeychain(): CancellablePromise<SecretsStatus> {
  return SecretsService.UnlockWithKeychain();
}

/**
 * privacy.secretsKeyStore: "keychain" stores the unlocked key in the OS
 * keychain, "password" removes it from there. The secrets and the master
 * password do not change.
 */
export function setSecretsKeyStore(mode: SecretsKeyStore): CancellablePromise<SecretsStatus> {
  return SecretsService.SetKeyStore({ mode });
}

/** "Lock now". */
export function lockSecrets(): CancellablePromise<SecretsStatus> {
  return SecretsService.Lock();
}

export function changeSecretsPassword(oldPassword: string, newPassword: string): CancellablePromise<SecretsStatus> {
  return SecretsService.ChangePassword({ oldPassword, newPassword });
}

/** Turns encryption off: the secrets are written back plain. */
export function disableSecrets(password: string): CancellablePromise<SecretsStatus> {
  return SecretsService.Disable({ password });
}

/** Deletes every encrypted secret, for a forgotten password. Only after the user confirmed it. */
export function resetSecrets(): CancellablePromise<SecretsStatus> {
  return SecretsService.Reset({ confirm: true });
}

/** privacy.secretsLockMinutes: lock after that many idle minutes, 0 never. */
export function setSecretsIdleLock(minutes: number): CancellablePromise<SecretsStatus> {
  return SecretsService.SetIdleLock(minutes);
}

/** Calls `callback` with the new status after every change. Returns the unsubscribe function. */
export function onSecretsChanged(callback: (status: SecretsStatus) => void): () => void {
  return Events.On(SECRETS_CHANGED_EVENT, (ev) => callback(ev.data));
}
