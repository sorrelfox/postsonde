import type { ReactNode } from "react";
import { useT } from "../../i18n/react";
import styles from "./StatusBar.module.css";

export interface StatusBarProps {
  /** Left side. Default: "Ready". */
  start?: ReactNode;
  /** Right side, e.g. the app version. Default: a version placeholder. */
  end?: ReactNode;
  /** Shell controls at the far right, after `end` (the console button). */
  tools?: ReactNode;
}

/** The thin bar under the main column. */
export function StatusBar({ start, end, tools }: StatusBarProps) {
  const t = useT();
  return (
    <footer className={styles.statusBar} aria-label={t("shell.statusBar.label")}>
      <div className={styles.group}>
        {start ?? (
          <span className={styles.item}>
            <span className={styles.dot} />
            {t("shell.statusBar.ready")}
          </span>
        )}
      </div>
      <div className={styles.spacer} />
      <div className={styles.group}>
        {end ?? <span className={styles.version}>{t("shell.statusBar.devVersion")}</span>}
        {tools}
      </div>
    </footer>
  );
}
