import { useEffect, useId, useLayoutEffect, useMemo, useRef, useState, type KeyboardEvent, type MouseEvent, type UIEvent, type WheelEvent } from "react";
import { clearConsoleLog, consoleFeed, useConsoleFeed } from "../../commands/console";
import { copyLogText } from "../../commands/logExport";
import { showToast } from "../../commands/toasts";
import { formatNumber, t } from "../../i18n";
import { useT } from "../../i18n/react";
import { CONSOLE_LEVELS, formatConsoleEntry, type ConsoleFeed, type EntryDTO } from "../../lib/console";
import { formatError } from "../../lib/errors";
import { jsonLogDocument, textLogDocument, type LogFormat } from "../../lib/logExport";
import { IconClose, IconCopy, IconSearch, IconTrash } from "../common/icons";
import { SaveLogButton, useEntryMenu, type LogEntryExport } from "../common/LogActions";
import { useVirtualRows } from "../common/useVirtualRows";
import styles from "./Console.module.css";
import { ConsoleViews, type ConsoleView } from "./ConsoleViews";
import { countLevels, DEFAULT_LEVELS, entryLevel, fieldsText, filterEntries, formatClock, LEVEL_LABELS, type LevelFilter } from "./consoleView";

/** Row height in px. */
export const CONSOLE_ROW_HEIGHT = 22;

export interface ConsolePanelProps {
  /** Closes the panel (the close button and Escape). */
  onClose: () => void;
  /** The feed to show. Default: the app's. */
  feed?: ConsoleFeed;
  /** Switches the console to another view; shows the Log / Connections switch when set. */
  onViewChange?: (view: ConsoleView) => void;
}

/** A console record as a copyable entry (DBG-07): the text line, or the record as Go sent it. */
export function consoleEntryExport(e: EntryDTO): LogEntryExport {
  return { text: () => formatConsoleEntry(e), json: () => e };
}

/** The shown records as a saved log: one line each, or JSON. */
export function consoleDocument(rows: readonly EntryDTO[], format: LogFormat): string {
  const info = { kind: "console", title: t("console.title") };
  return format === "json" ? jsonLogDocument(info, rows) : textLogDocument(info, rows.map(formatConsoleEntry), true);
}

/**
 * The console (DBG-06): the app log, live, below the work area. Records come
 * redacted from Go. Level toggles and a text filter narrow the list; it
 * follows new records until the user scrolls up or pauses it. Copy puts the
 * shown records on the clipboard, Save writes them to a file (DBG-07, both
 * masked in Go again); a focused record copies with ⌘C / Ctrl+C and has a
 * menu (Shift+F10, the Menu key or a right-click). Clear empties the
 * console (not the log file). See docs/frontend.md (Console).
 */
export function ConsolePanel({ onClose, feed = consoleFeed, onViewChange }: ConsolePanelProps) {
  const t = useT();
  const snap = useConsoleFeed(feed);
  const [levels, setLevels] = useState<LevelFilter>(DEFAULT_LEVELS);
  const [query, setQuery] = useState("");
  const [following, setFollowing] = useState(true);
  const [focusedSeq, setFocusedSeq] = useState<number | null>(null);
  const headingId = useId();
  const searchRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLDivElement | null>(null);
  const pendingFocus = useRef(false);
  const entryMenu = useEntryMenu(t("console.log.record"));

  const rows = useMemo(() => filterEntries(snap.entries, levels, query), [snap.entries, levels, query]);
  const counts = useMemo(() => countLevels(snap.entries), [snap.entries]);
  const focusIndex = focusedSeq === null ? -1 : rows.findIndex((r) => r.seq === focusedSeq);

  // Records are seen while the panel shows them: the status bar badge resets.
  useEffect(() => {
    if (snap.unseenProblems > 0) feed.markSeen();
  }, [feed, snap.unseenProblems]);

  const virtual = useVirtualRows({
    count: rows.length,
    rowHeight: CONSOLE_ROW_HEIGHT,
    fallbackViewport: 200,
    followEnd: following,
    pinned: [focusIndex],
  });

  useLayoutEffect(() => {
    if (!pendingFocus.current || focusedSeq === null) return;
    pendingFocus.current = false;
    listRef.current?.querySelector<HTMLElement>(`[data-seq="${focusedSeq}"]`)?.focus({ preventScroll: true });
  });

  const isAtEnd = (el: HTMLDivElement) => el.scrollTop >= rows.length * CONSOLE_ROW_HEIGHT - virtual.height - CONSOLE_ROW_HEIGHT / 2;

  const onScroll = (e: UIEvent<HTMLDivElement>) => {
    if (virtual.onScroll(e)) setFollowing(isAtEnd(e.currentTarget));
  };
  // Scrolling up pauses at once, before new records pull the list back down.
  const onWheel = (e: WheelEvent<HTMLDivElement>) => {
    if (e.deltaY < 0) setFollowing(false);
  };

  const copy = () => {
    const what = t("console.log.copied", { count: rows.length, number: formatNumber(rows.length) });
    void copyLogText(rows.map(formatConsoleEntry).join("\n"), "text", what);
  };

  const clear = () => {
    clearConsoleLog(feed).catch((err: unknown) => showToast({ message: t("console.log.clearFailed", { error: formatError(err) }) }));
    setFollowing(true);
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

  // The row in the Tab order: the focused one, else the first in view.
  const firstInView = Math.min(rows.length - 1, Math.max(0, Math.ceil(virtual.scrollTop / CONSOLE_ROW_HEIGHT)));
  const tabbableIndex = focusIndex >= 0 ? focusIndex : rows.length > 0 ? firstInView : -1;

  const moveFocus = (index: number) => {
    const i = Math.max(0, Math.min(rows.length - 1, index));
    pendingFocus.current = true;
    setFocusedSeq(rows[i].seq);
    if (i === rows.length - 1) {
      setFollowing(true);
    } else {
      setFollowing(false);
      virtual.scrollToIndex(i);
    }
  };

  const onListKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    const current = focusIndex >= 0 ? focusIndex : tabbableIndex;
    if (current < 0 || e.defaultPrevented) return;
    if (e.target !== e.currentTarget && entryMenu.onKeyDown(e, consoleEntryExport(rows[current]))) return;
    if (e.altKey || e.metaKey || e.ctrlKey || e.shiftKey) return;
    const page = Math.max(1, Math.floor(virtual.height / CONSOLE_ROW_HEIGHT) - 1);
    const next: Record<string, number> = {
      ArrowDown: current + 1,
      ArrowUp: current - 1,
      PageDown: current + page,
      PageUp: current - page,
      Home: 0,
      End: rows.length - 1,
    };
    if (!(e.key in next)) return;
    e.preventDefault();
    moveFocus(next[e.key]);
  };

  const hidden = snap.entries.length - rows.length;

  return (
    <section className={styles.panel} aria-labelledby={headingId} onKeyDown={onKeyDown}>
      <div className={styles.toolbar}>
        <h2 id={headingId} className={styles.title}>
          {t("console.title")}
        </h2>
        {onViewChange && <ConsoleViews view="log" onChange={onViewChange} />}
        <div className={styles.levels} role="group" aria-label={t("console.log.levels")}>
          {CONSOLE_LEVELS.map((level) => (
            <button
              key={level}
              type="button"
              className={styles.levelToggle}
              data-level={level}
              aria-pressed={levels[level]}
              onClick={() => setLevels((l) => ({ ...l, [level]: !l[level] }))}
            >
              {t(LEVEL_LABELS[level])}
              <span className={styles.count}>{formatNumber(counts[level])}</span>
            </button>
          ))}
        </div>
        <div className={styles.search}>
          <IconSearch size={13} className={styles.searchIcon} />
          <input
            ref={searchRef}
            type="text"
            className={styles.searchInput}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder={t("console.filter.placeholder")}
            aria-label={t("console.log.filter")}
            spellCheck={false}
            autoComplete="off"
          />
        </div>
        <span className={styles.status} aria-live="polite">
          {hidden > 0 ? t("console.hidden", { count: hidden, number: formatNumber(hidden) }) : ""}
        </span>
        <div className={styles.spacer} />
        <button
          type="button"
          className={styles.textButton}
          aria-pressed={!following}
          title={following ? t("console.log.pause.title") : t("console.log.resume.title")}
          onClick={() => setFollowing((f) => !f)}
        >
          {following ? t("console.log.pause") : t("console.log.resume")}
        </button>
        <button type="button" className={styles.iconButton} aria-label={t("console.log.copy")} title={t("console.log.copy")} disabled={rows.length === 0} onClick={copy}>
          <IconCopy size={14} />
        </button>
        <SaveLogButton
          title={t("console.title")}
          fileName="postsonde-console"
          build={(format) => consoleDocument(rows, format)}
          disabled={rows.length === 0}
          className={styles.iconButton}
          label={t("console.log.save")}
        />
        <button type="button" className={styles.iconButton} aria-label={t("console.log.clear")} title={t("console.log.clear")} disabled={snap.entries.length === 0} onClick={clear}>
          <IconTrash size={14} />
        </button>
        <button type="button" className={styles.iconButton} aria-label={t("console.close")} title={t("console.close")} onClick={onClose}>
          <IconClose size={14} />
        </button>
      </div>
      <div
        ref={(el) => {
          listRef.current = el;
          virtual.ref(el);
        }}
        className={styles.list}
        role="log"
        aria-live="off"
        aria-label={t("console.log.records")}
        tabIndex={rows.length === 0 ? 0 : -1}
        data-shortcut-scope="log"
        onScroll={onScroll}
        onWheel={onWheel}
        onKeyDown={onListKeyDown}
      >
        {rows.length === 0 ? (
          <p className={styles.empty}>{snap.entries.length === 0 ? t("console.log.empty") : t("console.log.noMatch")}</p>
        ) : (
          <div className={styles.sizer} style={{ height: rows.length * CONSOLE_ROW_HEIGHT }}>
            {virtual.rendered.map((i) => (
              <ConsoleRow
                key={rows[i].seq}
                entry={rows[i]}
                top={i * CONSOLE_ROW_HEIGHT}
                tabbable={i === tabbableIndex}
                onFocus={() => setFocusedSeq(rows[i].seq)}
                onContextMenu={(e) => {
                  setFocusedSeq(rows[i].seq);
                  entryMenu.onContextMenu(e, consoleEntryExport(rows[i]));
                }}
              />
            ))}
          </div>
        )}
      </div>
      {entryMenu.menu}
    </section>
  );
}

interface ConsoleRowProps {
  entry: EntryDTO;
  top: number;
  tabbable: boolean;
  onFocus: () => void;
  onContextMenu: (e: MouseEvent<HTMLDivElement>) => void;
}

function ConsoleRow({ entry, top, tabbable, onFocus, onContextMenu }: ConsoleRowProps) {
  const level = entryLevel(entry);
  const fields = fieldsText(entry);
  const op = [entry.opKind, entry.opId].filter((s) => s !== "").join(" ");
  return (
    <div
      className={styles.row}
      data-level={level}
      data-seq={entry.seq}
      style={{ top, height: CONSOLE_ROW_HEIGHT }}
      title={formatConsoleEntry(entry)}
      tabIndex={tabbable ? 0 : -1}
      onFocus={onFocus}
      onContextMenu={onContextMenu}
    >
      <span className={styles.time}>{formatClock(entry.at)}</span>
      <span className={styles.level}>{level}</span>
      {entry.source !== "" && <span className={styles.source}>{entry.source}</span>}
      {op !== "" && <span className={styles.op}>{op}</span>}
      <span className={styles.message}>{entry.message}</span>
      {fields !== "" && <span className={styles.fields}>{fields}</span>}
    </div>
  );
}
