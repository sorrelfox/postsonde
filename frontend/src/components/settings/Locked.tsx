// Settings locked by the admin policy (SET-02): the settings screen shows
// them with a lock and their controls disabled. See docs/settings.md.
import { useSyncExternalStore, type ReactNode } from "react";
import { currentSettingsPolicy, isSettingLocked, subscribeSettingsPolicy } from "../../commands/settings";
import type { SettingsPolicy } from "../../lib/settings";
import { IconLock } from "../common/icons";
import { Tooltip } from "../common/Tooltip";
import { useT } from "../../i18n/react";
import screen from "./SettingsScreen.module.css";

/** The admin policy in effect, re-rendering when it loads. */
export function useSettingsPolicy(): SettingsPolicy {
  return useSyncExternalStore(subscribeSettingsPolicy, currentSettingsPolicy);
}

/** Whether the admin policy locks `key` (settings.json key or store key); false without a key. */
export function useSettingLocked(key: string | undefined): boolean {
  const policy = useSettingsPolicy();
  return key !== undefined && isSettingLocked(key, policy);
}

/**
 * The lock shown after a locked setting's label. It is a Tab stop with a
 * tooltip (UI-11): the disabled controls next to it cannot take focus, so
 * this is where keyboard and screen reader users learn why.
 */
export function LockBadge() {
  const t = useT();
  return (
    <Tooltip text={t("settings.locked.tooltip")}>
      <span className={screen.lockBadge} role="img" tabIndex={0} aria-label={t("settings.locked.badge")}>
        <IconLock size={12} />
      </span>
    </Tooltip>
  );
}

/**
 * The control column of a settings row. When locked it is a disabled
 * fieldset, which disables every button, input and select inside it
 * without each control having to know.
 */
export function RowControl({ locked, children }: { locked: boolean; children: ReactNode }) {
  const t = useT();
  if (!locked) return <div className={screen.rowControl}>{children}</div>;
  return (
    <fieldset className={`${screen.rowControl} ${screen.lockedControl}`} disabled title={t("settings.locked.tooltip")}>
      {children}
    </fieldset>
  );
}
