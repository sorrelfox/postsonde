import { useNetworkFeed } from "../../commands/network";
import { useT } from "../../i18n/react";
import { isActive, proxyKind, type ConnectionFeed } from "../../lib/network";
import styles from "./Network.module.css";

export interface NetworkIndicatorProps {
  /** Whether the connection log shows. */
  open: boolean;
  /** Shows or hides the connection log. */
  onToggle: () => void;
  /** The feed to show. Default: the app's. */
  feed?: ConnectionFeed;
}

/**
 * The status bar's network activity indicator (PRV-06): "Idle" while the
 * app has no connection open, otherwise how many are connecting or open,
 * with a pulsing dot (steady when motion is reduced). It opens the
 * console's Connections tab, the log of every outgoing connection. While
 * an open connection goes through a proxy, a marker names its kind
 * (NET-06: SOCKS5, HTTP, ...) and the proxies in its tooltip.
 */
export function NetworkIndicator({ open, onToggle, feed }: NetworkIndicatorProps) {
  const t = useT();
  const { active, conns } = useNetworkFeed(feed);
  const proxies = [...new Set(conns.filter((c) => (c.via ?? "") !== "" && isActive(c)).map((c) => c.via))];
  const kinds = [...new Set(proxies.map(proxyKind))];
  const label = active === 0 ? t("console.network.idle") : t("console.network.count", { count: active });
  let ariaLabel: string;
  if (active === 0) ariaLabel = open ? t("console.network.label.idle.hide") : t("console.network.label.idle.show");
  else ariaLabel = open ? t("console.network.label.open.hide", { count: active }) : t("console.network.label.open.show", { count: active });
  return (
    <button
      type="button"
      className={styles.indicator}
      data-active={active > 0}
      aria-pressed={open}
      aria-label={ariaLabel}
      title={open ? t("console.network.hide") : t("console.network.show")}
      onClick={onToggle}
    >
      <span className={styles.pulse} aria-hidden="true" />
      <svg width={13} height={13} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={1.9} strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false">
        <path d="M7 20V5M3 9l4-4 4 4M17 4v15M21 15l-4 4-4-4" />
      </svg>
      <span className={styles.indicatorCount}>{label}</span>
      {proxies.length > 0 && (
        <span className={styles.proxyMark} data-proxy={kinds.join(" ")} title={t("console.network.proxyTitle", { proxies: proxies.join(", ") })}>
          {t("console.network.proxy", { kind: kinds.join(", ") })}
        </span>
      )}
    </button>
  );
}
