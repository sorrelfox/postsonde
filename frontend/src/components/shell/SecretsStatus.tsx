import { lockSecretsNow, requestUnlock } from "../../commands/secrets";
import { useSecrets } from "../../commands/useSecrets";
import { formatError } from "../../lib/errors";
import { useT } from "../../i18n/react";
import { IconLock } from "../common/icons";
import { Tooltip } from "../common/Tooltip";
import { useShortcut } from "./useShortcut";
import styles from "./StatusBar.module.css";

/**
 * The status bar's lock of the encrypted secrets (SEC-03), shown while
 * they are encrypted: "Locked" opens the unlock dialog, "Unlocked" locks
 * them now. The "Lock stored secrets now" shortcut (and palette command)
 * is bound while they are unlocked. Nothing while encryption is off.
 */
export function SecretsStatus() {
  const t = useT();
  const { status } = useSecrets();
  const unlocked = status !== null && status.enabled && !status.locked;
  const lockNow = () => {
    lockSecretsNow().catch((err: unknown) => console.warn(`secrets: ${formatError(err)}`));
  };
  useShortcut("secrets.lock", lockNow, unlocked);

  if (status === null || !status.enabled) return null;
  const label = status.locked ? t("shell.secrets.status.lockedLabel") : t("shell.secrets.status.unlockedLabel");
  return (
    <Tooltip text={label} placement="top">
      <button
        type="button"
        className={`${styles.item} ${styles.statusButton}`}
        aria-label={label}
        data-testid="secrets-status"
        data-locked={status.locked ? "true" : undefined}
        onClick={status.locked ? requestUnlock : lockNow}
      >
        <IconLock size={12} />
        {status.locked ? t("shell.secrets.status.locked") : t("shell.secrets.status.unlocked")}
      </button>
    </Tooltip>
  );
}
