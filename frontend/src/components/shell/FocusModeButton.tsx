import { ariaShortcut, shortcutLabel } from "../../lib/shortcuts";
import { useT } from "../../i18n/react";
import styles from "./FocusModeButton.module.css";

export interface FocusModeButtonProps {
  onExit: () => void;
}

/**
 * Shown in the status bar while focus mode (UI-09) is on: says so and
 * leaves it, since the rail and the sidebar that would otherwise lead back
 * are hidden.
 */
export function FocusModeButton({ onExit }: FocusModeButtonProps) {
  const t = useT();
  return (
    <button
      type="button"
      className={styles.button}
      aria-pressed="true"
      aria-keyshortcuts={ariaShortcut("view.focusMode")}
      title={t("shell.focusMode.leave", { shortcut: shortcutLabel("view.focusMode") })}
      onClick={onExit}
    >
      <svg width={13} height={13} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={1.9} strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false">
        <path d="M4 9V4h5M20 9V4h-5M4 15v5h5M20 15v5h-5" />
      </svg>
      {t("shell.focusMode.label")}
    </button>
  );
}
