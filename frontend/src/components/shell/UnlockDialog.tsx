import { useEffect, useId, useState } from "react";
import {
  applySecretsStatus,
  closeUnlock,
  followSecrets,
  onSecretsUnlocked,
  setSetupOpen,
} from "../../commands/secrets";
import { loadEnvironments } from "../../commands/environments";
import { useSecrets } from "../../commands/useSecrets";
import { formatError, isCancelled, toAppError } from "../../lib/errors";
import { SECRETS_CODES, unlockSecrets, unlockSecretsWithKeychain, type SecretsKeychain } from "../../lib/secrets";
import { useT } from "../../i18n/react";
import { useStores } from "../../stores";
import { Dialog } from "../common/Dialog";
import { IconWarning } from "../common/icons";
import { MasterPasswordDialog, ResetSecretsDialog } from "../settings/SecretEncryption";
import styles from "../settings/SecretEncryption.module.css";

/**
 * Follows the encrypted secrets for the window (SEC-03) and shows their
 * dialogs: the unlock dialog at startup and whenever a call fails because
 * the secrets are locked, and the "set a master password" dialog when the
 * administrator requires one and there is none. After an unlock the
 * environments load again (they fail while locked). Mounted once, by App.
 */
export function SecretsHost() {
  const stores = useStores();
  const { status, unlockOpen, setupOpen } = useSecrets();

  useEffect(() => followSecrets(), []);
  useEffect(() => onSecretsUnlocked(() => void loadEnvironments(stores)), [stores]);

  return (
    <>
      {unlockOpen && status?.enabled && status.locked && (
        <UnlockDialog migration={status.migration !== ""} keychain={status.keychain} />
      )}
      {setupOpen && status !== null && !status.enabled && (
        <MasterPasswordDialog mode="enable" status={status} required onClose={() => setSetupOpen(false)} />
      )}
    </>
  );
}

/**
 * Asks for the master password. "Not now" leaves the secrets locked (sends
 * that need one then fail with a hint); "Forgot password?" offers to delete
 * the encrypted secrets. With the key store "keychain" it
 * offers the OS keychain too, and says why it did not unlock at startup.
 */
export function UnlockDialog({ migration = false, keychain }: { migration?: boolean; keychain?: SecretsKeychain }) {
  const t = useT();
  const ids = { help: useId(), password: useId(), error: useId(), keychain: useId() };
  const useKeychain = keychain !== undefined && keychain.available && keychain.mode === "keychain";
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [resetting, setResetting] = useState(false);

  if (resetting) {
    return <ResetSecretsDialog onClose={() => setResetting(false)} onDone={() => closeUnlock(false)} />;
  }

  const unlock = () => {
    if (password === "" || busy) return;
    setBusy(true);
    setError(null);
    unlockSecrets(password).then(
      (st) => {
        setBusy(false);
        setPassword("");
        applySecretsStatus(st);
        closeUnlock(false);
      },
      (err: unknown) => {
        setBusy(false);
        if (isCancelled(err)) return;
        const e = toAppError(err);
        // A wrong password: type it again, from an empty field.
        if (e.code === SECRETS_CODES.wrongPassword) setPassword("");
        setError(formatError(e));
      },
    );
  };

  const unlockWithKeychain = () => {
    if (busy) return;
    setBusy(true);
    setError(null);
    unlockSecretsWithKeychain().then(
      (st) => {
        setBusy(false);
        applySecretsStatus(st);
        closeUnlock(false);
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
      title={t("shell.secrets.unlock.title")}
      onCancel={() => closeUnlock(true)}
      onConfirm={unlock}
      confirmLabel={t("shell.secrets.unlock.confirm")}
      cancelLabel={t("shell.secrets.unlock.later")}
      confirmDisabled={password === ""}
      busy={busy}
      describedBy={ids.help}
    >
      <div className={styles.form}>
        <p id={ids.help} className={styles.help}>
          {t("shell.secrets.unlock.help")}
          {migration ? ` ${t("shell.secrets.unlock.migration")}` : ""}
        </p>
        {useKeychain && keychain.problem && !error && (
          <p id={ids.keychain} className={styles.help}>
            {t("shell.secrets.unlock.keychainProblem", { name: keychain.name, error: formatError(keychain.problem) })}
          </p>
        )}
        <div className={styles.field}>
          <label className={styles.label} htmlFor={ids.password}>
            {t("shell.secrets.unlock.password")}
          </label>
          <input
            id={ids.password}
            type="password"
            className={styles.input}
            value={password}
            autoComplete="current-password"
            spellCheck={false}
            aria-invalid={error ? true : undefined}
            aria-describedby={error ? ids.error : undefined}
            onChange={(e) => setPassword(e.target.value)}
            data-autofocus
          />
        </div>
        {error && (
          <p id={ids.error} className={styles.error} role="alert">
            <IconWarning size={13} />
            {error}
          </p>
        )}
        {useKeychain && (
          <button type="button" className={styles.link} disabled={busy} onClick={unlockWithKeychain}>
            {t("shell.secrets.unlock.keychain", { name: keychain.name })}
          </button>
        )}
        <button type="button" className={styles.link} onClick={() => setResetting(true)}>
          {t("shell.secrets.unlock.forgot")}
        </button>
      </div>
    </Dialog>
  );
}
