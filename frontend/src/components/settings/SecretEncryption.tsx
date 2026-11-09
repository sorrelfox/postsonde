import { useEffect, useId, useState, type ReactNode } from "react";
import { applySecretsStatus, lockSecretsNow, refreshSecretsStatus, requestUnlock } from "../../commands/secrets";
import { useSecrets } from "../../commands/useSecrets";
import { formatError, isCancelled } from "../../lib/errors";
import {
  changeSecretsPassword,
  disableSecrets,
  enableSecrets,
  IDLE_LOCK_CHOICES,
  resetSecrets,
  setSecretsIdleLock,
  setSecretsKeyStore,
  type SecretsKeyStore,
  type SecretsStatus,
} from "../../lib/secrets";
import { useT } from "../../i18n/react";
import { Dialog } from "../common/Dialog";
import { IconWarning } from "../common/icons";
import { LockBadge, RowControl } from "./Locked";
import styles from "./SecretEncryption.module.css";
import screen from "./SettingsScreen.module.css";

/** The shortest master password when the status has not said. */
const DEFAULT_MIN_LENGTH = 8;

/**
 * Settings, Privacy, Stored secrets (SEC-03): whether the stored secrets
 * are encrypted with a master password, and the actions for it (set it,
 * unlock, lock now, change it, turn it off), the idle lock
 * (privacy.secretsLockMinutes), and the way out of a forgotten password.
 * Shows nothing until Go has answered (no backend in some tests).
 */
export function SecretEncryptionRows() {
  const t = useT();
  const { status } = useSecrets();
  const [dialog, setDialog] = useState<"enable" | "change" | "disable" | "reset" | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const errorId = useId();
  const idleId = useId();

  useEffect(() => {
    void refreshSecretsStatus();
  }, []);

  if (status === null) return null;
  if (!status.available) {
    return (
      <Row label={t("settings.secrets.encryption.label")} description={t("settings.secrets.state.unavailable")}>
        {null}
      </Row>
    );
  }

  const act = (action: () => Promise<unknown>) => {
    if (busy) return;
    setBusy(true);
    setError(null);
    action()
      .catch((err: unknown) => {
        if (!isCancelled(err)) setError(formatError(err));
      })
      .finally(() => setBusy(false));
  };

  const parts = [stateText(t, status)];
  if (status.migration !== "") parts.push(t("settings.secrets.state.migration"));
  if (status.required) parts.push(t("settings.secrets.state.required"));
  if (status.problem) parts.push(formatError(status.problem));
  const description = parts.join(" ");

  return (
    <>
      <Row label={t("settings.secrets.encryption.label")} description={description} error={error} errorId={errorId}>
        {!status.enabled && (
          <button type="button" className={screen.smallButton} disabled={busy} onClick={() => setDialog("enable")}>
            {t("settings.secrets.turnOn")}
          </button>
        )}
        {status.enabled && status.locked && !status.problem && (
          <button type="button" className={screen.smallButton} disabled={busy} onClick={requestUnlock}>
            {t("settings.secrets.unlock")}
          </button>
        )}
        {status.enabled && !status.locked && (
          <>
            <button type="button" className={screen.smallButton} disabled={busy} onClick={() => act(lockSecretsNow)}>
              {t("settings.secrets.lockNow")}
            </button>
            <button type="button" className={screen.smallButton} disabled={busy} onClick={() => setDialog("change")}>
              {t("settings.secrets.change")}
            </button>
          </>
        )}
        {status.enabled && !status.problem && (
          <button
            type="button"
            className={screen.smallButton}
            disabled={busy || status.required}
            onClick={() => setDialog("disable")}
          >
            {t("settings.secrets.turnOff")}
          </button>
        )}
      </Row>
      <KeyStoreRow status={status} busy={busy} act={act} />
      <Row
        label={t("settings.secrets.idle.label")}
        controlId={idleId}
        locked={status.idleLockLocked}
        description={t("settings.secrets.idle.description")}
      >
        <select
          id={idleId}
          className={screen.select}
          value={status.idleLockMinutes}
          onChange={(e) => {
            const minutes = Number(e.currentTarget.value);
            act(async () => applySecretsStatus(await setSecretsIdleLock(minutes)));
          }}
        >
          {idleChoices(status.idleLockMinutes).map((m) => (
            <option key={m} value={m}>
              {idleLabel(t, m)}
            </option>
          ))}
        </select>
      </Row>
      {status.enabled && (
        <Row label={t("settings.secrets.reset.label")} description={t("settings.secrets.reset.description")}>
          <button type="button" className={screen.smallButton} disabled={busy} onClick={() => setDialog("reset")}>
            {t("settings.secrets.reset.button")}
          </button>
        </Row>
      )}
      {(dialog === "enable" || dialog === "change" || dialog === "disable") && (
        <MasterPasswordDialog mode={dialog} status={status} onClose={() => setDialog(null)} />
      )}
      {dialog === "reset" && <ResetSecretsDialog onClose={() => setDialog(null)} />}
    </>
  );
}

type T = ReturnType<typeof useT>;

/**
 * Where the key is kept besides behind the master password
 * (privacy.secretsKeyStore): also in the OS keychain, so the
 * secrets unlock at startup, or behind the password only. Switching moves
 * only the key; the secrets and the password stay as they are.
 */
function KeyStoreRow({
  status,
  busy,
  act,
}: {
  status: SecretsStatus;
  busy: boolean;
  act: (action: () => Promise<unknown>) => void;
}) {
  const t = useT();
  const id = useId();
  const kc = status.keychain;
  const mode: SecretsKeyStore = kc.available && kc.mode === "keychain" ? "keychain" : "password";
  const parts = [
    !kc.available
      ? t("settings.secrets.keyStore.none")
      : mode === "keychain"
        ? t("settings.secrets.keyStore.keychainDescription", { name: kc.name })
        : t("settings.secrets.keyStore.passwordDescription"),
  ];
  if (kc.problem) parts.push(t("settings.secrets.keyStore.problem", { name: kc.name, error: formatError(kc.problem) }));
  return (
    <Row label={t("settings.secrets.keyStore.label")} controlId={id} locked={kc.modeLocked} description={parts.join(" ")}>
      <select
        id={id}
        className={screen.select}
        value={mode}
        disabled={busy || !kc.available}
        onChange={(e) => {
          const next = e.currentTarget.value as SecretsKeyStore;
          act(async () => applySecretsStatus(await setSecretsKeyStore(next)));
        }}
      >
        {kc.available && <option value="keychain">{t("settings.secrets.keyStore.keychain", { name: kc.name })}</option>}
        <option value="password">{t("settings.secrets.keyStore.password")}</option>
      </select>
    </Row>
  );
}

function stateText(t: T, status: SecretsStatus): string {
  if (!status.enabled) return t("settings.secrets.state.off");
  if (status.locked) return t("settings.secrets.state.locked");
  return t("settings.secrets.state.unlocked", { iterations: status.iterations });
}

/** The choices, with a value set by hand in settings.json kept. */
function idleChoices(current: number): number[] {
  return IDLE_LOCK_CHOICES.includes(current) ? [...IDLE_LOCK_CHOICES] : [...IDLE_LOCK_CHOICES, current].sort((a, b) => a - b);
}

function idleLabel(t: T, minutes: number): string {
  if (minutes === 0) return t("settings.secrets.idle.never");
  if (minutes % 60 === 0) return t("settings.secrets.idle.hours", { count: minutes / 60 });
  return t("settings.secrets.idle.minutes", { count: minutes });
}

export interface MasterPasswordDialogProps {
  /** Set a new password, change it, or turn encryption off. */
  mode: "enable" | "change" | "disable";
  status: SecretsStatus | null;
  /** The administrator requires encryption: says so. */
  required?: boolean;
  onClose: () => void;
}

/**
 * Sets, changes or checks the master password. A new one is typed twice
 * and needs the minimum length; the fields are never prefilled or kept.
 */
export function MasterPasswordDialog({ mode, status, required = false, onClose }: MasterPasswordDialogProps) {
  const t = useT();
  const ids = { help: useId(), current: useId(), next: useId(), repeat: useId(), error: useId(), hint: useId() };
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [repeat, setRepeat] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const min = status?.minPasswordLength || DEFAULT_MIN_LENGTH;
  const needsNew = mode !== "disable";
  const needsCurrent = mode !== "enable";
  const tooShort = needsNew && next !== "" && [...next].length < min;
  const mismatch = needsNew && repeat !== "" && next !== repeat;
  const ready = (!needsCurrent || current !== "") && (!needsNew || (next !== "" && !tooShort && next === repeat));

  const title =
    mode === "enable"
      ? t("settings.secrets.dialog.enableTitle")
      : mode === "change"
        ? t("settings.secrets.dialog.changeTitle")
        : t("settings.secrets.dialog.disableTitle");
  const confirmLabel =
    mode === "enable"
      ? t("settings.secrets.dialog.enableConfirm")
      : mode === "change"
        ? t("settings.secrets.dialog.changeConfirm")
        : t("settings.secrets.dialog.disableConfirm");
  const help =
    mode === "disable"
      ? t("settings.secrets.dialog.disableHelp")
      : required
        ? t("settings.secrets.dialog.requiredHelp")
        : mode === "enable"
          ? t("settings.secrets.dialog.enableHelp")
          : null;

  const save = () => {
    if (!ready || busy) return;
    setBusy(true);
    setError(null);
    const call =
      mode === "enable" ? enableSecrets(next) : mode === "change" ? changeSecretsPassword(current, next) : disableSecrets(current);
    call.then(
      (st) => {
        applySecretsStatus(st);
        setBusy(false);
        onClose();
      },
      (err: unknown) => {
        setBusy(false);
        if (!isCancelled(err)) setError(formatError(err));
      },
    );
  };

  const fieldHint = tooShort ? t("settings.secrets.dialog.tooShort", { min }) : mismatch ? t("settings.secrets.dialog.mismatch") : null;

  return (
    <Dialog
      open
      title={title}
      onCancel={onClose}
      onConfirm={save}
      confirmLabel={confirmLabel}
      confirmDisabled={!ready}
      busy={busy}
      variant={mode === "disable" ? "danger" : undefined}
      describedBy={help ? ids.help : undefined}
    >
      <div className={styles.form}>
        {help && (
          <p id={ids.help} className={styles.help}>
            {help}
          </p>
        )}
        {needsCurrent && (
          <PasswordField
            id={ids.current}
            label={mode === "disable" ? t("settings.secrets.dialog.password") : t("settings.secrets.dialog.current")}
            value={current}
            onChange={setCurrent}
            autoFocus
          />
        )}
        {needsNew && (
          <>
            <PasswordField
              id={ids.next}
              label={mode === "enable" ? t("settings.secrets.dialog.password") : t("settings.secrets.dialog.new")}
              value={next}
              onChange={setNext}
              invalid={tooShort}
              describedBy={fieldHint ? ids.hint : undefined}
              autoFocus={!needsCurrent}
            />
            <PasswordField
              id={ids.repeat}
              label={t("settings.secrets.dialog.repeat")}
              value={repeat}
              onChange={setRepeat}
              invalid={mismatch}
              describedBy={fieldHint ? ids.hint : undefined}
            />
            <p id={ids.hint} className={styles.help}>
              {fieldHint ?? t("settings.secrets.dialog.tooShort", { min })}
            </p>
          </>
        )}
        {error && (
          <p id={ids.error} className={styles.error} role="alert">
            <IconWarning size={13} />
            {error}
          </p>
        )}
      </div>
    </Dialog>
  );
}

function PasswordField({
  id,
  label,
  value,
  onChange,
  invalid = false,
  describedBy,
  autoFocus = false,
}: {
  id: string;
  label: string;
  value: string;
  onChange: (v: string) => void;
  invalid?: boolean;
  describedBy?: string;
  autoFocus?: boolean;
}) {
  return (
    <div className={styles.field}>
      <label className={styles.label} htmlFor={id}>
        {label}
      </label>
      <input
        id={id}
        type="password"
        className={styles.input}
        value={value}
        autoComplete="off"
        spellCheck={false}
        aria-invalid={invalid ? true : undefined}
        aria-describedby={describedBy}
        onChange={(e) => onChange(e.target.value)}
        data-autofocus={autoFocus ? true : undefined}
      />
    </div>
  );
}

/**
 * The way out of a forgotten master password: deletes every encrypted
 * secret after the user typed the confirmation word, since nothing can
 * bring them back. `onDone` runs after the delete (default: onClose).
 */
export function ResetSecretsDialog({ onClose, onDone }: { onClose: () => void; onDone?: () => void }) {
  const t = useT();
  const ids = { body: useId(), word: useId() };
  const word = t("settings.secrets.reset.word");
  const [typed, setTyped] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const ready = typed.trim() === word;

  const confirm = () => {
    if (!ready || busy) return;
    setBusy(true);
    setError(null);
    resetSecrets().then(
      (st) => {
        applySecretsStatus(st);
        setBusy(false);
        (onDone ?? onClose)();
      },
      (err: unknown) => {
        setBusy(false);
        if (!isCancelled(err)) setError(formatError(err));
      },
    );
  };

  return (
    <Dialog
      open
      title={t("settings.secrets.reset.title")}
      onCancel={onClose}
      onConfirm={confirm}
      confirmLabel={t("settings.secrets.reset.confirm")}
      confirmDisabled={!ready}
      busy={busy}
      variant="danger"
      describedBy={ids.body}
    >
      <div className={styles.form}>
        <p id={ids.body} className={styles.help}>
          {t("settings.secrets.reset.body")}
        </p>
        <div className={styles.field}>
          <label className={styles.label} htmlFor={ids.word}>
            {t("settings.secrets.reset.typeLabel", { word })}
          </label>
          <input
            id={ids.word}
            className={styles.input}
            value={typed}
            autoComplete="off"
            spellCheck={false}
            onChange={(e) => setTyped(e.target.value)}
            data-autofocus
          />
        </div>
        {error && (
          <p className={styles.error} role="alert">
            <IconWarning size={13} />
            {error}
          </p>
        )}
      </div>
    </Dialog>
  );
}

/** A settings row, as SettingsScreen's (kept local to avoid an import cycle). */
function Row({
  label,
  controlId,
  description,
  error,
  errorId,
  locked = false,
  children,
}: {
  label: string;
  controlId?: string;
  description?: ReactNode;
  error?: string | null;
  errorId?: string;
  /** Locked by the admin policy (SET-02): a lock, and the controls disabled. */
  locked?: boolean;
  children: ReactNode;
}) {
  const name = (
    <>
      {label}
      {locked && <LockBadge />}
    </>
  );
  return (
    <div className={screen.row} data-invalid={error ? "true" : undefined} data-locked={locked ? "true" : undefined}>
      <div className={screen.rowText}>
        {controlId ? (
          <label className={screen.rowLabel} htmlFor={controlId}>
            {name}
          </label>
        ) : (
          <span className={screen.rowLabel}>{name}</span>
        )}
        {description && <span className={screen.rowDescription}>{description}</span>}
        {error && (
          <span id={errorId} className={screen.rowError} role="alert">
            <IconWarning size={13} />
            {error}
          </span>
        )}
      </div>
      <RowControl locked={locked}>{children}</RowControl>
    </div>
  );
}
