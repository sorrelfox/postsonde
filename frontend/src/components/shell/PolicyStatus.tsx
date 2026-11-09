import { IconLock, IconWarning } from "../common/icons";
import { Tooltip } from "../common/Tooltip";
import { useSettingsPolicy } from "../settings/Locked";
import { useT } from "../../i18n/react";
import styles from "./StatusBar.module.css";

/**
 * The status bar's "Managed" indicator (SET-02): shown while an admin
 * policy locks settings, or when its file has problems, with the details
 * as the tooltip, which focus shows too (UI-11). Nothing without a policy.
 */
export function PolicyStatus() {
  const t = useT();
  const policy = useSettingsPolicy();
  if (!policy.active && policy.problems.length === 0) return null;
  const lines: string[] = [];
  if (policy.active) {
    lines.push(t("shell.policy.tooltip.managed"));
    if (policy.locked.length > 0) lines.push(t("shell.policy.tooltip.locked", { keys: policy.locked.join(", ") }));
    if (policy.requireTlsVerification) lines.push(t("shell.policy.tooltip.tls"));
  }
  for (const p of policy.problems) lines.push(p.message);
  if (policy.path) lines.push(t("shell.policy.tooltip.file", { path: policy.path }));
  const broken = policy.problems.length > 0;
  return (
    <Tooltip text={lines.join("\n")} placement="top">
      <span className={styles.item} role="status" tabIndex={0} data-testid="policy-status">
        {broken ? <IconWarning size={12} /> : <IconLock size={12} />}
        {policy.active ? t("shell.policy.managed") : t("shell.policy.notApplied")}
      </span>
    </Tooltip>
  );
}
