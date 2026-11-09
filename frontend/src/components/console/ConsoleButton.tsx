import { useConsoleFeed } from "../../commands/console";
import { useT } from "../../i18n/react";
import { ariaShortcut, shortcutLabel } from "../../lib/shortcuts";
import styles from "./Console.module.css";

export interface ConsoleButtonProps {
  open: boolean;
  onToggle: () => void;
}

/**
 * The status bar's "Console" button (DBG-06): shows or hides the console
 * panel. While the panel is hidden it counts the warnings and errors logged
 * since it was last shown.
 */
export function ConsoleButton({ open, onToggle }: ConsoleButtonProps) {
  const t = useT();
  const { unseenProblems } = useConsoleFeed();
  const badge = !open && unseenProblems > 0 ? (unseenProblems > 99 ? "99+" : String(unseenProblems)) : null;
  const shortcut = shortcutLabel("console.toggle");
  return (
    <button
      type="button"
      className={styles.statusButton}
      aria-pressed={open}
      aria-keyshortcuts={ariaShortcut("console.toggle")}
      title={open ? t("console.button.hide", { shortcut }) : t("console.button.show", { shortcut })}
      onClick={onToggle}
    >
      <svg width={13} height={13} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={1.9} strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false">
        <rect x="3" y="4" width="18" height="16" rx="2" />
        <path d="m7 9 3 3-3 3M13 15h4" />
      </svg>
      {t("console.title")}
      {badge !== null && (
        <span className={styles.badge} aria-label={t("console.button.badge", { count: unseenProblems, badge })}>
          {badge}
        </span>
      )}
    </button>
  );
}
