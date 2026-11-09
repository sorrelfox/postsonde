// The state of the stored secrets' encryption (SEC-03) for the
// whole window: the vault's status as Go last sent it, and whether the
// unlock dialog or the "set a master password" dialog is open. The shell
// follows it (followSecrets) and renders the dialogs; the status bar and
// Settings, Privacy read it. See docs/privacy.md#encrypted-secrets.
//
// The unlock dialog opens:
// - at startup, when the secrets are encrypted and locked;
// - when a call fails because they are locked (secretstore.locked), unless
//   the user chose "Not now" since the secrets last changed state;
// - when the user asks (the status bar's lock, Settings).
import { setLockedErrorListener, toAppError } from "../lib/errors";
import { getSecretsStatus, lockSecrets, onSecretsChanged, type SecretsStatus } from "../lib/secrets";

export interface SecretsState {
  /** The vault's status; null until it has loaded (or when it cannot be read). */
  readonly status: SecretsStatus | null;
  /** The unlock dialog is open. */
  readonly unlockOpen: boolean;
  /** The dialog that sets a master password is open (the administrator requires one). */
  readonly setupOpen: boolean;
  /** "Not now" was chosen: a locked error does not open the dialog again until the status changes. */
  readonly dismissed: boolean;
  /**
   * At startup the secrets were locked while Go was still unlocking them
   * from the OS keychain: the unlock dialog opens once that is
   * over and they are still locked.
   */
  readonly awaitingKeychain: boolean;
}

const INITIAL: SecretsState = { status: null, unlockOpen: false, setupOpen: false, dismissed: false, awaitingKeychain: false };

let state: SecretsState = INITIAL;
const listeners = new Set<() => void>();
const unlockedListeners = new Set<() => void>();

function setState(patch: Partial<SecretsState>): void {
  state = { ...state, ...patch };
  for (const l of [...listeners]) l();
}

/** For useSyncExternalStore. */
export function subscribeSecrets(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export function getSecretsState(): SecretsState {
  return state;
}

/** Forgets everything (tests). */
export function resetSecretsState(): void {
  state = INITIAL;
  for (const l of [...listeners]) l();
}

/**
 * Takes a status from Go. Coming from locked to unlocked tells the
 * listeners of onSecretsUnlocked, so views that failed while locked load
 * again; the dialog closes once unlocked.
 */
export function applySecretsStatus(status: SecretsStatus): void {
  const before = state.status;
  const changed = before === null || before.locked !== status.locked || before.enabled !== status.enabled;
  const stillLocked = status.enabled && status.locked;
  // The startup unlock from the keychain is over: ask for the password if it did not unlock.
  const keychainDone = state.awaitingKeychain && !status.keychain.pending;
  setState({
    status,
    unlockOpen: (state.unlockOpen || keychainDone) && stillLocked,
    setupOpen: state.setupOpen && !status.enabled,
    dismissed: changed ? false : state.dismissed,
    awaitingKeychain: state.awaitingKeychain && !keychainDone,
  });
  if (before?.locked === true && !status.locked) {
    for (const l of [...unlockedListeners]) l();
  }
}

/** Calls `listener` whenever the secrets get unlocked (or turned off while locked). */
export function onSecretsUnlocked(listener: () => void): () => void {
  unlockedListeners.add(listener);
  return () => unlockedListeners.delete(listener);
}

/** Loads the status; a failure leaves it as it was. */
export async function refreshSecretsStatus(): Promise<SecretsStatus | null> {
  try {
    const status = await getSecretsStatus();
    applySecretsStatus(status);
    return status;
  } catch (err: unknown) {
    console.warn(`secrets: ${toAppError(err).message}`);
    return null;
  }
}

/** Opens the unlock dialog, when there is something to unlock. */
export function requestUnlock(): void {
  const s = state.status;
  if (s !== null && s.enabled && s.locked) setState({ unlockOpen: true });
}

/** Closes the unlock dialog; `dismissed` ("Not now") keeps locked errors from opening it again for now. */
export function closeUnlock(dismissed: boolean): void {
  setState({ unlockOpen: false, dismissed: state.dismissed || dismissed });
}

/** Opens or closes the dialog that sets a master password. */
export function setSetupOpen(open: boolean): void {
  setState({ setupOpen: open });
}

/** A call failed because the secrets are locked: ask for the password, unless "Not now" was chosen. */
export function noteLockedError(): void {
  if (state.dismissed || state.unlockOpen) return;
  const s = state.status;
  if (s !== null && s.enabled && s.locked) {
    setState({ unlockOpen: true });
  } else {
    // The status may be stale (the idle lock): load it; the dialog opens once it says locked.
    void refreshSecretsStatus().then((st) => {
      if (st !== null && st.enabled && st.locked && !state.dismissed) setState({ unlockOpen: true });
    });
  }
}

/** "Lock now". Rejects with an AppError. */
export async function lockSecretsNow(): Promise<void> {
  applySecretsStatus(await lockSecrets());
}

/**
 * Follows the vault for the window: loads the status, asks for the
 * password at startup when the secrets are locked (or for a new one when
 * the administrator requires encryption and it is off), applies every
 * secrets:changed event and opens the dialog when a call fails because
 * the secrets are locked. Returns the function that stops it.
 */
export function followSecrets(): () => void {
  let live = true;
  let off: () => void = () => {};
  try {
    off = onSecretsChanged((status) => applySecretsStatus(status));
  } catch (err: unknown) {
    console.warn(`secrets: ${toAppError(err).message}`);
  }
  const offLocked = setLockedErrorListener(() => noteLockedError());
  void refreshSecretsStatus().then((status) => {
    if (!live || status === null || !status.available) return;
    // Still unlocking from the keychain: wait for its secrets:changed.
    if (status.enabled && status.locked && status.keychain.pending) setState({ awaitingKeychain: true });
    else if (status.enabled && status.locked) setState({ unlockOpen: true });
    else if (status.required && !status.enabled) setState({ setupOpen: true });
  });
  return () => {
    live = false;
    off();
    offLocked();
  };
}
