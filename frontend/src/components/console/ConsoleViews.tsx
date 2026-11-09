import { useNetworkFeed } from "../../commands/network";
import { useT } from "../../i18n/react";
import type { ConnectionFeed } from "../../lib/network";
import styles from "./Network.module.css";

/** What the console panel shows: the app log (DBG-06) or the connection log (PRV-06). */
export type ConsoleView = "log" | "connections";

export interface ConsoleViewsProps {
  view: ConsoleView;
  onChange: (view: ConsoleView) => void;
  /** The connection feed to count. Default: the app's. */
  feed?: ConnectionFeed;
}

/** The console toolbar's Log / Connections switch; Connections counts the active ones. */
export function ConsoleViews({ view, onChange, feed }: ConsoleViewsProps) {
  const t = useT();
  const { active } = useNetworkFeed(feed);
  return (
    <div className={styles.views} role="group" aria-label={t("console.views.label")}>
      <button type="button" className={styles.viewButton} aria-pressed={view === "log"} onClick={() => onChange("log")}>
        {t("console.views.log")}
      </button>
      <button type="button" className={styles.viewButton} aria-pressed={view === "connections"} onClick={() => onChange("connections")}>
        {t("console.views.connections")}
        {active > 0 && (
          <span className={styles.viewCount} aria-label={t("console.views.active", { count: active })}>
            {active}
          </span>
        )}
      </button>
    </div>
  );
}
