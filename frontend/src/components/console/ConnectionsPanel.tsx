import { useId, useMemo, useRef, useState, type KeyboardEvent, type MouseEvent } from "react";
import { copyLogText } from "../../commands/logExport";
import { clearConnectionLog, networkFeed, useNetworkFeed } from "../../commands/network";
import { showToast } from "../../commands/toasts";
import { formatNumber, t } from "../../i18n";
import { useT } from "../../i18n/react";
import { formatError } from "../../lib/errors";
import { jsonLogDocument, textLogDocument, type LogFormat } from "../../lib/logExport";
import {
  connectionCause,
  formatBytes,
  formatConnection,
  formatDuration,
  isActive,
  proxyKind,
  type ConnDTO,
  type ConnectionFeed,
} from "../../lib/network";
import { IconClose, IconCopy, IconSearch, IconTrash } from "../common/icons";
import { SaveLogButton, useEntryMenu, type LogEntryExport } from "../common/LogActions";
import styles from "./Console.module.css";
import { formatClock } from "./consoleView";
import { ConsoleViews, type ConsoleView } from "./ConsoleViews";
import net from "./Network.module.css";

export interface ConnectionsPanelProps {
  /** Closes the panel (the close button and Escape). */
  onClose: () => void;
  /** Switches the console to another view. */
  onViewChange: (view: ConsoleView) => void;
  /** The feed to show. Default: the app's. */
  feed?: ConnectionFeed;
}

/** Whether c matches the text filter: its target, addresses, cause, state or error code. */
export function matchesConnection(c: ConnDTO, query: string): boolean {
  const q = query.trim().toLowerCase();
  if (q === "") return true;
  return [c.address, c.remoteAddr, c.via ?? "", c.localAddr, c.opKind, c.opId, c.state, c.errorCode, c.network].some((s) => s.toLowerCase().includes(q));
}

/** A connection as a copyable entry (DBG-07): its text line, or the record with its cause. */
export function connectionEntryExport(c: ConnDTO): LogEntryExport {
  return { text: () => formatConnection(c), json: () => ({ ...c, cause: connectionCause(c) }) };
}

/** The shown connections as a saved log, newest first: one line each, or JSON. */
export function connectionsDocument(rows: readonly ConnDTO[], format: LogFormat): string {
  const info = { kind: "connections", title: t("console.connections.logTitle") };
  return format === "json"
    ? jsonLogDocument(info, rows.map((c) => ({ ...c, cause: connectionCause(c) })))
    : textLogDocument(info, rows.map(formatConnection), true);
}

/**
 * The console's Connections tab (PRV-06): every outgoing connection the app
 * opened or refused, newest first, with the user action that caused it,
 * target host:port, addresses, timing, bytes and how it ended. "Active
 * only" narrows it to the open ones; Clear drops the ended ones here and in
 * Go. Copy and Save take the shown rows; a focused row copies with ⌘C /
 * Ctrl+C and has a menu (DBG-07). See docs/frontend.md (Network activity).
 */
export function ConnectionsPanel({ onClose, onViewChange, feed = networkFeed }: ConnectionsPanelProps) {
  const t = useT();
  const snap = useNetworkFeed(feed);
  const [query, setQuery] = useState("");
  const [activeOnly, setActiveOnly] = useState(false);
  const headingId = useId();
  const searchRef = useRef<HTMLInputElement>(null);
  const [focusedId, setFocusedId] = useState<number | null>(null);
  const entryMenu = useEntryMenu(t("console.connections.connection"));

  const rows = useMemo(
    () => snap.conns.filter((c) => (!activeOnly || isActive(c)) && matchesConnection(c, query)).reverse(),
    [snap.conns, activeOnly, query],
  );

  const copy = () => {
    const what = t("console.connections.copied", { count: rows.length, number: formatNumber(rows.length) });
    void copyLogText(rows.map(formatConnection).join("\n"), "text", what);
  };

  // One row in the Tab order; Up/Down, Home/End move; ⌘C and the menu keys act on it.
  const focusIndex = Math.max(0, rows.findIndex((c) => c.id === focusedId));
  const onBodyKeyDown = (e: KeyboardEvent<HTMLTableSectionElement>) => {
    const row = e.target instanceof Element ? e.target.closest("tr") : null;
    const i = rows.findIndex((c) => String(c.id) === row?.dataset.id);
    if (i < 0) return;
    if (entryMenu.onKeyDown(e, connectionEntryExport(rows[i]))) return;
    if (e.altKey || e.metaKey || e.ctrlKey || e.shiftKey) return;
    let next = -1;
    if (e.key === "ArrowDown") next = i + 1;
    else if (e.key === "ArrowUp") next = i - 1;
    else if (e.key === "Home") next = 0;
    else if (e.key === "End") next = rows.length - 1;
    if (next < 0 || next >= rows.length) return;
    e.preventDefault();
    setFocusedId(rows[next].id);
    e.currentTarget.querySelector<HTMLElement>(`tr[data-id="${rows[next].id}"]`)?.focus();
  };

  const clear = () => {
    clearConnectionLog(feed).catch((err: unknown) => showToast({ message: t("console.connections.clearFailed", { error: formatError(err) }) }));
  };

  const onKeyDown = (e: KeyboardEvent<HTMLElement>) => {
    if (e.key === "Escape" && !e.defaultPrevented) {
      if (e.target === searchRef.current && query !== "") {
        setQuery("");
      } else {
        onClose();
      }
      e.preventDefault();
      e.stopPropagation();
    }
  };

  const hidden = snap.conns.length - rows.length;
  const ended = snap.conns.length - snap.active;

  return (
    <section className={styles.panel} aria-labelledby={headingId} onKeyDown={onKeyDown}>
      <div className={styles.toolbar}>
        <h2 id={headingId} className={styles.title}>
          {t("console.title")}
        </h2>
        <ConsoleViews view="connections" onChange={onViewChange} feed={feed} />
        <div className={styles.search}>
          <IconSearch size={13} className={styles.searchIcon} />
          <input
            ref={searchRef}
            type="text"
            className={styles.searchInput}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder={t("console.filter.placeholder")}
            aria-label={t("console.connections.filter")}
            spellCheck={false}
            autoComplete="off"
          />
        </div>
        <button type="button" className={styles.levelToggle} aria-pressed={activeOnly} onClick={() => setActiveOnly((a) => !a)}>
          {t("console.connections.activeOnly")}
        </button>
        <span className={styles.status} aria-live="polite">
          {hidden > 0 ? t("console.hidden", { count: hidden, number: formatNumber(hidden) }) : ""}
        </span>
        <div className={styles.spacer} />
        <button
          type="button"
          className={styles.iconButton}
          aria-label={t("console.connections.copy")}
          title={t("console.connections.copy")}
          disabled={rows.length === 0}
          onClick={copy}
        >
          <IconCopy size={14} />
        </button>
        <SaveLogButton
          title={t("console.connections.logTitle")}
          fileName="postsonde-connections"
          build={(format) => connectionsDocument(rows, format)}
          disabled={rows.length === 0}
          className={styles.iconButton}
          label={t("console.connections.save")}
        />
        <button
          type="button"
          className={styles.iconButton}
          aria-label={t("console.connections.clear")}
          title={t("console.connections.clear.title")}
          disabled={ended === 0}
          onClick={clear}
        >
          <IconTrash size={14} />
        </button>
        <button type="button" className={styles.iconButton} aria-label={t("console.close")} title={t("console.close")} onClick={onClose}>
          <IconClose size={14} />
        </button>
      </div>
      <div className={styles.list} role="region" aria-label={t("console.connections.label")} tabIndex={0}>
        {rows.length === 0 ? (
          <p className={styles.empty}>{snap.conns.length === 0 ? t("console.connections.none") : t("console.connections.noMatch")}</p>
        ) : (
          <table className={net.table}>
            <thead>
              <tr>
                <th scope="col">{t("console.connections.col.started")}</th>
                <th scope="col">{t("console.connections.col.state")}</th>
                <th scope="col">{t("console.connections.col.cause")}</th>
                <th scope="col">{t("console.connections.col.target")}</th>
                <th scope="col">{t("console.connections.col.remote")}</th>
                <th scope="col">{t("console.connections.col.local")}</th>
                <th scope="col" className={net.num}>
                  {t("console.connections.col.connect")}
                </th>
                <th scope="col" className={net.num}>
                  {t("console.connections.col.duration")}
                </th>
                <th scope="col" className={net.num}>
                  {t("console.connections.col.in")}
                </th>
                <th scope="col" className={net.num}>
                  {t("console.connections.col.out")}
                </th>
                <th scope="col">{t("console.connections.col.error")}</th>
              </tr>
            </thead>
            <tbody onKeyDown={onBodyKeyDown} data-shortcut-scope="log">
              {rows.map((c, i) => (
                <ConnectionRow
                  key={c.id}
                  conn={c}
                  tabbable={i === focusIndex}
                  onFocus={() => setFocusedId(c.id)}
                  onContextMenu={(e) => {
                    setFocusedId(c.id);
                    entryMenu.onContextMenu(e, connectionEntryExport(c));
                  }}
                />
              ))}
            </tbody>
          </table>
        )}
      </div>
      {entryMenu.menu}
    </section>
  );
}

interface ConnectionRowProps {
  conn: ConnDTO;
  tabbable: boolean;
  onFocus: () => void;
  onContextMenu: (e: MouseEvent<HTMLTableRowElement>) => void;
}

function ConnectionRow({ conn: c, tabbable, onFocus, onContextMenu }: ConnectionRowProps) {
  const t = useT();
  const dash = <span className={net.muted}>—</span>;
  const via = c.via ?? ""; // absent from connections logged before NET-06
  return (
    <tr
      data-state={c.state}
      data-id={c.id}
      title={formatConnection(c)}
      tabIndex={tabbable ? 0 : -1}
      onFocus={onFocus}
      onContextMenu={onContextMenu}
    >
      <td className={net.muted}>{formatClock(c.startedAt)}</td>
      <td className={net.state}>{c.state}</td>
      <td className={net.cause}>{connectionCause(c)}</td>
      <td className={net.target}>
        {c.address}
        {c.network !== "tcp" && <span className={net.muted}> ({c.network})</span>}
      </td>
      <td>
        {c.remoteAddr || (via === "" && dash)}
        {via !== "" && (
          <span className={net.via} data-via={via} title={t("console.connections.viaTitle", { proxy: via })}>
            {t("console.connections.via", { kind: proxyKind(via) })}
          </span>
        )}
      </td>
      <td className={net.muted}>{c.localAddr || "—"}</td>
      <td className={net.num}>{c.connectedAt !== "" ? formatDuration(c.connectMs) : dash}</td>
      <td className={net.num}>{c.endedAt !== "" ? formatDuration(c.durationMs) : dash}</td>
      <td className={net.num}>{c.connectedAt !== "" ? formatBytes(c.bytesIn) : dash}</td>
      <td className={net.num}>{c.connectedAt !== "" ? formatBytes(c.bytesOut) : dash}</td>
      <td className={net.error} title={c.error}>
        {c.errorCode}
      </td>
    </tr>
  );
}
