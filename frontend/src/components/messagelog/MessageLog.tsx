import {
  memo,
  useCallback,
  useEffect,
  useId,
  useLayoutEffect,
  useMemo,
  useReducer,
  useRef,
  useState,
  type ChangeEvent,
  type KeyboardEvent,
  type MouseEvent,
  type PointerEvent,
  type ReactNode,
  type UIEvent,
  type WheelEvent,
} from "react";
import {
  MESSAGE_DIRECTIONS,
  type LogMessage,
  type MessageDirection,
  type MessageSource,
} from "../../lib/messageSource";
import { formatNumber } from "../../i18n";
import { useT } from "../../i18n/react";
import { IconChevron, IconFilter, IconSearch } from "../common/icons";
import { SaveLogButton, useEntryMenu, type LogEntryExport } from "../common/LogActions";
import { useVirtualRows } from "../common/useVirtualRows";
import {
  DIRECTION_KEYS,
  formatClock,
  formatRelative,
  formatSize,
  formatTimestamp,
  highlightParts,
  messageEntryJson,
  messageEntryText,
  messagePreview,
  messageSize,
  type BinaryView,
} from "./format";
import { jsonLogDocument, textLogDocument, type LogFormat } from "../../lib/logExport";
import { MessageDetail } from "./MessageDetail";
import { DEFAULT_LOG_FILTER, MessageView, type LogFilter } from "./messageView";
import styles from "./MessageLog.module.css";

/** Row height in px. Every row has it; the expanded payload is shown in the detail pane instead. */
export const MESSAGE_ROW_HEIGHT = 26;
/** Rows rendered above and below the visible window. */
export const MESSAGE_OVERSCAN = 10;
/** Viewport height assumed until the list has been measured (and in jsdom). */
export const MESSAGE_FALLBACK_VIEWPORT = 400;

export type TimeMode = "absolute" | "relative";

/** A toolbar toggle that shows or hides messages of some kinds (pings and pongs, reconnect notes). */
export interface KindToggle {
  /** The button text, from the caller's catalogue. */
  label: string;
  kinds: readonly string[];
  /** Tooltip. */
  title?: string;
}

export interface MessageLogProps {
  /** The messages. Give each connection its own source, and remount (key) the log when it changes. */
  source: MessageSource;
  /** Accessible name of the log, and the title of a saved log. Default "Messages". */
  label?: string;
  initialFilter?: Partial<LogFilter>;
  initialTimeMode?: TimeMode;
  /** Show a Clear button that clears the source. Default true. */
  clearable?: boolean;
  /** Called after the Clear button cleared the source (to clear a copy kept elsewhere). */
  onClear?: () => void;
  className?: string;
  /** How binary payloads are shown at first (WS-06). Default hex. */
  initialBinaryView?: BinaryView;
  /** Show the Hex/Base64 switch for binary payloads in the toolbar. Default false (hex; the detail pane can still switch). */
  binaryToggle?: boolean;
  /**
   * Shows a text payload in the detail pane in a protocol's own way (a
   * GraphQL result with its data and errors marked); null keeps the
   * default pretty JSON or text.
   */
  renderPayload?: (message: LogMessage) => ReactNode | null;
  /** Toolbar toggles that hide messages of some kinds; all start shown unless initialFilter.hiddenKinds hides them. */
  kindToggles?: readonly KindToggle[];
  /** Suggested file name of a saved log (DBG-07), without extension. Default: the label. */
  saveName?: string;
}

// i18n-ignore: the names of the two encodings, the same in every language
const BINARY_VIEW_LABELS: Record<BinaryView, string> = { hex: "Hex", base64: "Base64" };

const DIRECTION_GLYPHS: Record<MessageDirection, string> = { sent: "↑", received: "↓", system: "•" };

/** A count for people, digits grouped in the language in use. */
const fmt = (n: number) => formatNumber(n);

/** Colour group of a kind label. */
function kindTone(kind: string): string {
  switch (kind) {
    case "error":
      return "bad";
    case "close":
    case "closed":
    case "half-close":
    case "disconnected":
    case "dropped":
      return "warn";
    case "open":
    case "connected":
      return "ok";
    case "binary":
      return "binary";
    case "ping":
    case "pong":
    case "ka":
    case "keep-alive":
    case "reconnect":
      return "control";
    default:
      return "plain";
  }
}

/**
 * The message log of a streaming connection (WS-05): WebSocket, gRPC
 * streams, SSE and the local servers. Shows a MessageSource as a virtualized
 * grid of fixed-height rows (time, direction, kind, size, preview), with
 * search, direction and kind filters, follow mode and a detail pane for the
 * selected message. Binary payloads show as hex or Base64 (WS-06): the
 * optional toolbar switch sets it for the whole log, the detail pane for
 * the selected message. A focused message copies with ⌘C / Ctrl+C and has
 * a menu (Shift+F10, the Menu key, a right-click); Save… saves the shown
 * messages as text or JSON, masked in Go (DBG-07). See docs/frontend.md
 * (Message log).
 */
export const MessageLog = memo(function MessageLog({
  source,
  label: labelProp,
  initialFilter,
  initialTimeMode = "absolute",
  clearable = true,
  onClear,
  className,
  initialBinaryView = "hex",
  binaryToggle = false,
  renderPayload,
  kindToggles,
  saveName,
}: MessageLogProps) {
  const [filter, setFilter] = useState<LogFilter>(() => ({ ...DEFAULT_LOG_FILTER, ...initialFilter }));
  const [binaryView, setBinaryView] = useState<BinaryView>(initialBinaryView);
  const view = useMemo(() => new MessageView(source, filter), [source, filter]);
  const [, bump] = useReducer((n: number) => n + 1, 0);
  const snap = source.getSnapshot();
  const t = useT();
  const label = labelProp ?? t("messagelog.log.label");
  const entryMenu = useEntryMenu(t("messagelog.log.entry"));

  /** A message as a copyable entry (DBG-07), formatted when copied. */
  const entryOf = useCallback(
    (seq: number | null): LogEntryExport | undefined => {
      const m = seq === null ? undefined : source.get(seq);
      if (m === undefined) return undefined;
      return { text: () => messageEntryText(m, source.getSnapshot().startTime), json: () => messageEntryJson(m) };
    },
    [source],
  );

  /** The messages the filters show, as a saved log (DBG-07). */
  const buildLog = (format: LogFormat): string => {
    const messages: LogMessage[] = [];
    for (let i = 0; i < view.rows.length; i++) {
      const m = source.get(view.rows.at(i));
      if (m !== undefined) messages.push(m);
    }
    const info = { kind: "messages", title: label };
    if (format === "json") return jsonLogDocument(info, messages.map(messageEntryJson));
    const start = source.getSnapshot().startTime;
    return textLogDocument(info, messages.map((m) => messageEntryText(m, start)));
  };

  const [timeMode, setTimeMode] = useState<TimeMode>(initialTimeMode);
  const [focusedSeq, setFocusedSeq] = useState<number | null>(null);
  const [selectedSeq, setSelectedSeq] = useState<number | null>(null);
  const [activeHit, setActiveHit] = useState<number | null>(null);
  const [following, setFollowingState] = useState(true);
  const followingRef = useRef(true);
  /** view.appendedRows when following stopped, for the "N new" count. */
  const pausedAt = useRef<{ view: MessageView; rows: number }>({ view, rows: view.appendedRows });

  const baseId = useId();
  const searchRef = useRef<HTMLInputElement>(null);
  const rowEls = useRef(new Map<number, HTMLDivElement>());
  const pendingFocus = useRef(false);

  // Row 0 is the "older messages dropped" marker while messages have been dropped.
  const offset = snap.dropped > 0 ? 1 : 0;
  const rowCount = view.rows.length;
  const total = offset + rowCount;

  const focusIndex = focusedSeq === null ? -1 : indexOfSeq(view, focusedSeq, offset);

  const virtual = useVirtualRows({
    count: total,
    rowHeight: MESSAGE_ROW_HEIGHT,
    overscan: MESSAGE_OVERSCAN,
    fallbackViewport: MESSAGE_FALLBACK_VIEWPORT,
    pinned: [focusIndex],
    followEnd: following,
  });
  const { height, rendered, scrollToIndex } = virtual;

  // State the source listener needs without re-subscribing.
  const live = useRef({ offset, virtual });
  live.current = { offset, virtual };

  const setFollowing = useCallback(
    (next: boolean) => {
      if (followingRef.current === next) return;
      followingRef.current = next;
      if (!next) pausedAt.current = { view, rows: view.appendedRows };
      setFollowingState(next);
    },
    [view],
  );

  // Apply source changes to the view: one store update per animation frame, so one render.
  const renderedVersion = useRef(snap.version);
  renderedVersion.current = snap.version;
  useEffect(() => {
    const apply = () => {
      const r = view.sync();
      const snapNow = source.getSnapshot();
      if (snapNow.version === renderedVersion.current && r.addedRows === 0 && r.removedRows === 0) return;
      renderedVersion.current = snapNow.version;
      const { offset: prevOffset, virtual: v } = live.current;
      const nextOffset = snapNow.dropped > 0 ? 1 : 0;
      if (!followingRef.current && !r.reset) {
        // Keep the rows in view where they are when older rows leave the top.
        const shift = r.removedRows - (nextOffset - prevOffset);
        const el = v.element();
        if (shift !== 0 && el) v.scrollTo(el.scrollTop - shift * MESSAGE_ROW_HEIGHT);
      }
      bump();
    };
    apply();
    return source.subscribe(apply);
  }, [source, view]);

  // A new filter: keep "N new" counting from here, and keep the focused message in view.
  useLayoutEffect(() => {
    pausedAt.current = { view, rows: view.appendedRows };
    if (!followingRef.current && focusedSeq !== null) {
      const i = indexOfSeq(view, focusedSeq, live.current.offset);
      if (i >= 0) live.current.virtual.scrollToIndex(i, "center");
    }
    // Only when the view changes.
  }, [view]);

  useLayoutEffect(() => {
    if (!pendingFocus.current || focusIndex < 0) return;
    pendingFocus.current = false;
    rowEls.current.get(view.rows.at(focusIndex - offset))?.focus({ preventScroll: true });
  });

  // Opening the detail pane shrinks the list: keep the selected row in view.
  useLayoutEffect(() => {
    if (selectedSeq === null || followingRef.current) return;
    const i = indexOfSeq(view, selectedSeq, live.current.offset);
    if (i >= 0) live.current.virtual.scrollToIndex(i);
    // Not on every view change: only when the selection or the list height changes.
  }, [selectedSeq, height]);

  const registerRow = useCallback((seq: number, el: HTMLDivElement | null) => {
    if (el) rowEls.current.set(seq, el);
    else rowEls.current.delete(seq);
  }, []);

  // Selecting a message pauses following, so it stays in view; End or "Jump to latest" resume.
  const onRowClick = useCallback(
    (seq: number) => {
      setFocusedSeq(seq);
      setSelectedSeq(seq);
      setFollowing(false);
    },
    [setFollowing],
  );

  const onRowContextMenu = useCallback(
    (seq: number, e: MouseEvent<HTMLDivElement>) => {
      const entry = entryOf(seq);
      if (entry === undefined) return;
      setFocusedSeq(seq);
      entryMenu.onContextMenu(e, entry);
    },
    [entryOf, entryMenu.onContextMenu],
  );

  const updateFilter = (patch: Partial<LogFilter>) => {
    setFilter((f) => ({ ...f, ...patch }));
  };

  // ---- Scrolling and follow mode ----------------------------------------

  const isAtEnd = (el: HTMLDivElement) => el.scrollTop >= total * MESSAGE_ROW_HEIGHT - height - MESSAGE_ROW_HEIGHT / 2;

  // Only the user's scrolling changes follow mode; scrolls the log makes itself (following,
  // keeping a paused view in place, revealing a row) set it explicitly or leave it.
  const onScroll = (e: UIEvent<HTMLDivElement>) => {
    if (virtual.onScroll(e)) setFollowing(isAtEnd(e.currentTarget));
  };

  // Scrolling up with the wheel stops following at once, before new rows could pull the list back down.
  const onWheel = (e: WheelEvent<HTMLDivElement>) => {
    if (e.deltaY < 0) setFollowing(false);
  };

  // A press on the list itself (its scrollbar) pauses following until released.
  const onPointerDown = (e: PointerEvent<HTMLDivElement>) => {
    if (e.target === e.currentTarget) setFollowing(false);
  };
  const onPointerUp = (e: PointerEvent<HTMLDivElement>) => {
    if (e.target === e.currentTarget && isAtEnd(e.currentTarget)) setFollowing(true);
  };

  const jumpToLatest = () => {
    setFollowing(true);
    setActiveHit(null);
  };

  const newRows = following || pausedAt.current.view !== view ? 0 : Math.min(rowCount, Math.max(0, view.appendedRows - pausedAt.current.rows));

  // ---- Keyboard -----------------------------------------------------------

  const moveFocus = (index: number) => {
    if (rowCount === 0) return;
    const i = Math.max(offset, Math.min(total - 1, index));
    pendingFocus.current = true;
    setFocusedSeq(view.rows.at(i - offset));
    if (i === total - 1) {
      setFollowing(true);
    } else {
      setFollowing(false);
      scrollToIndex(i);
    }
  };

  // The row in the Tab order: the focused one, else the first row fully in view.
  const firstInView = Math.min(total - 1, Math.max(offset, Math.ceil(virtual.scrollTop / MESSAGE_ROW_HEIGHT)));
  const tabbableIndex = focusIndex >= 0 ? focusIndex : rowCount > 0 ? firstInView : -1;

  const onGridKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if (e.defaultPrevented) return;
    const current = focusIndex >= 0 ? focusIndex : tabbableIndex;
    if (current < 0) return;
    // ⌘C / Ctrl+C and the menu keys on the focused message (DBG-07).
    if (entryMenu.onKeyDown(e, entryOf(view.rows.at(current - offset) ?? null))) return;
    if (e.altKey || e.metaKey || e.ctrlKey) return;
    const page = Math.max(1, Math.floor(height / MESSAGE_ROW_HEIGHT) - 1);
    let handled = true;
    switch (e.key) {
      case "ArrowDown":
        moveFocus(current + 1);
        break;
      case "ArrowUp":
        moveFocus(current - 1);
        break;
      case "PageDown":
        moveFocus(current + page);
        break;
      case "PageUp":
        moveFocus(current - page);
        break;
      case "Home":
        moveFocus(offset);
        break;
      case "End":
        moveFocus(total - 1);
        break;
      case "Enter":
      case " ": {
        const seq = view.rows.at(current - offset);
        setFocusedSeq(seq);
        setSelectedSeq((s) => (s === seq ? null : seq));
        break;
      }
      case "Escape":
        handled = selectedSeq !== null;
        setSelectedSeq(null);
        break;
      default:
        handled = false;
    }
    if (handled) {
      e.preventDefault();
      e.stopPropagation();
    }
  };

  // ---- Search -------------------------------------------------------------

  const hitCount = view.hits.length;
  const activeHitIndex = activeHit === null ? -1 : view.hits.indexOf(activeHit);

  const goToHit = (dir: 1 | -1) => {
    const seq = view.nextHit(activeHitIndex >= 0 ? activeHit : null, dir);
    if (seq === null) return;
    setActiveHit(seq);
    setFocusedSeq(seq);
    setFollowing(false);
    const i = indexOfSeq(view, seq, offset);
    if (i >= 0) scrollToIndex(i, "center");
  };

  const onSearchKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Enter") {
      e.preventDefault();
      goToHit(e.shiftKey ? -1 : 1);
    } else if (e.key === "Escape" && filter.query !== "") {
      e.preventDefault();
      e.stopPropagation();
      updateFilter({ query: "" });
      setActiveHit(null);
    }
  };

  const onQueryChange = (e: ChangeEvent<HTMLInputElement>) => {
    updateFilter({ query: e.target.value });
    setActiveHit(null);
  };

  const toggleDirection = (d: MessageDirection) => {
    updateFilter({ directions: { ...filter.directions, [d]: !filter.directions[d] } });
  };

  const kindsShown = (t: KindToggle) => !t.kinds.some((k) => filter.hiddenKinds.includes(k));
  const toggleKinds = (t: KindToggle) => {
    const hiddenKinds = kindsShown(t) ? [...filter.hiddenKinds, ...t.kinds] : filter.hiddenKinds.filter((k) => !t.kinds.includes(k));
    updateFilter({ hiddenKinds });
  };

  const clear = () => {
    source.clear();
    onClear?.();
    setSelectedSeq(null);
    setFocusedSeq(null);
    setActiveHit(null);
    setFollowing(true);
  };

  const pattern = view.query.pattern;
  const errorId = `${baseId}-query-error`;
  const selected = selectedSeq === null ? undefined : source.get(selectedSeq);
  const filtered = rowCount !== snap.count;
  const rootClass = className ? `${styles.root} ${className}` : styles.root;

  let matchText = "";
  if (filter.query !== "" && view.query.error === null) {
    matchText =
      hitCount === 0
        ? t("messagelog.log.noMatches")
        : activeHitIndex >= 0
          ? t("messagelog.log.matchOf", { index: fmt(activeHitIndex + 1), count: fmt(hitCount) })
          : t("messagelog.log.matches", { count: hitCount, n: fmt(hitCount) });
  }

  return (
    <section className={rootClass} aria-label={label}>
      <div className={styles.toolbar}>
        <div className={styles.search} data-invalid={view.query.error !== null || undefined}>
          <IconSearch size={13} className={styles.searchIcon} />
          <input
            ref={searchRef}
            type="text"
            className={styles.searchInput}
            value={filter.query}
            onChange={onQueryChange}
            onKeyDown={onSearchKeyDown}
            placeholder={filter.regex ? t("messagelog.log.searchRegex") : t("messagelog.log.search")}
            aria-label={t("messagelog.log.searchLabel")}
            aria-invalid={view.query.error !== null || undefined}
            aria-describedby={view.query.error !== null ? errorId : undefined}
            spellCheck={false}
            autoComplete="off"
          />
          <button
            type="button"
            className={styles.inlineToggle}
            aria-pressed={filter.regex}
            aria-label={t("messagelog.log.regex")}
            title={t("messagelog.log.regex")}
            onClick={() => updateFilter({ regex: !filter.regex })}
          >
            .*
          </button>
        </div>
        <span className={styles.matchCount}>{matchText}</span>
        <button
          type="button"
          className={styles.iconButton}
          aria-label={t("messagelog.log.previousMatch")}
          title={t("messagelog.log.previousMatchTitle")}
          disabled={hitCount === 0}
          onClick={() => goToHit(-1)}
        >
          <IconChevron direction="up" size={14} />
        </button>
        <button
          type="button"
          className={styles.iconButton}
          aria-label={t("messagelog.log.nextMatch")}
          title={t("messagelog.log.nextMatchTitle")}
          disabled={hitCount === 0}
          onClick={() => goToHit(1)}
        >
          <IconChevron direction="down" size={14} />
        </button>
        <button
          type="button"
          className={styles.toggle}
          aria-pressed={filter.onlyMatching}
          title={t("messagelog.log.onlyMatchingTitle")}
          onClick={() => updateFilter({ onlyMatching: !filter.onlyMatching })}
        >
          <IconFilter size={13} />
          {t("messagelog.log.onlyMatching")}
        </button>
        <span className={styles.separator} aria-hidden="true" />
        <div role="group" aria-label={t("messagelog.log.direction")} className={styles.group}>
          {MESSAGE_DIRECTIONS.map((d) => (
            <button
              key={d}
              type="button"
              className={styles.toggle}
              data-direction={d}
              aria-pressed={filter.directions[d]}
              onClick={() => toggleDirection(d)}
            >
              <span className={styles.toggleGlyph} aria-hidden="true">
                {DIRECTION_GLYPHS[d]}
              </span>
              {t(DIRECTION_KEYS[d])}
            </button>
          ))}
        </div>
        <select
          className={styles.select}
          aria-label={t("messagelog.log.kind")}
          value={filter.kind ?? ""}
          onChange={(e) => updateFilter({ kind: e.target.value === "" ? null : e.target.value })}
        >
          <option value="">{t("messagelog.log.allKinds")}</option>
          {snap.kinds.map((k) => (
            <option key={k} value={k}>
              {k}
            </option>
          ))}
          {filter.kind !== null && !snap.kinds.includes(filter.kind) && <option value={filter.kind}>{filter.kind}</option>}
        </select>
        {kindToggles !== undefined && kindToggles.length > 0 && (
          <div role="group" aria-label={t("messagelog.log.showKinds")} className={styles.group}>
            {kindToggles.map((toggle) => (
              <button
                key={toggle.label}
                type="button"
                className={styles.toggle}
                aria-pressed={kindsShown(toggle)}
                title={toggle.title}
                onClick={() => toggleKinds(toggle)}
              >
                {toggle.label}
              </button>
            ))}
          </div>
        )}
        <span className={styles.spacer} />
        {binaryToggle && (
          <div role="group" aria-label={t("messagelog.log.binaryAs")} className={styles.group} title={t("messagelog.log.binaryAsTitle")}>
            {(["hex", "base64"] as const).map((v) => (
              <button key={v} type="button" className={styles.toggle} aria-pressed={binaryView === v} onClick={() => setBinaryView(v)}>
                {BINARY_VIEW_LABELS[v]}
              </button>
            ))}
          </div>
        )}
        <button
          type="button"
          className={styles.toggle}
          aria-pressed={timeMode === "relative"}
          title={t("messagelog.log.relativeTimeTitle")}
          onClick={() => setTimeMode((m) => (m === "relative" ? "absolute" : "relative"))}
        >
          {t("messagelog.log.relativeTime")}
        </button>
        <SaveLogButton
          title={label}
          fileName={saveName ?? label}
          build={buildLog}
          disabled={rowCount === 0}
          className={styles.button}
          label={t("messagelog.log.saveLabel")}
        >
          {t("messagelog.log.save")}
        </SaveLogButton>
        {clearable && (
          <button type="button" className={styles.button} onClick={clear} disabled={snap.count === 0 && snap.dropped === 0}>
            {t("messagelog.log.clear")}
          </button>
        )}
      </div>
      {view.query.error !== null && (
        <p id={errorId} className={styles.queryError}>
          {t("messagelog.log.invalidRegex", { error: view.query.error })}
        </p>
      )}

      <div className={styles.body}>
        <div className={styles.listArea}>
          <div
            className={styles.grid}
            role="grid"
            aria-label={label}
            aria-rowcount={total + 1}
            aria-colcount={5}
            aria-readonly="true"
          >
            <div role="rowgroup" className={styles.head}>
              <div role="row" aria-rowindex={1} className={styles.headRow}>
                <span role="columnheader" className={styles.cTime}>
                  {t("messagelog.log.column.time")}
                </span>
                <span role="columnheader" className={styles.cDir}>
                  <span className={styles.srOnly}>{t("messagelog.log.column.direction")}</span>
                </span>
                <span role="columnheader" className={styles.cKind}>
                  {t("messagelog.log.column.kind")}
                </span>
                <span role="columnheader" className={styles.cSize}>
                  {t("messagelog.log.column.size")}
                </span>
                <span role="columnheader" className={styles.cPayload}>
                  {t("messagelog.log.column.payload")}
                </span>
              </div>
            </div>
            <div
              ref={virtual.ref}
              role="rowgroup"
              className={styles.scroller}
              onScroll={onScroll}
              onWheel={onWheel}
              onPointerDown={onPointerDown}
              onPointerUp={onPointerUp}
              onKeyDown={onGridKeyDown}
              data-shortcut-scope="log"
            >
              <div className={styles.sizer} style={{ height: total * MESSAGE_ROW_HEIGHT }}>
                {rendered.map((index) => {
                  if (index < offset) {
                    return (
                      <div key="dropped" role="row" aria-rowindex={2} className={styles.marker} style={{ top: 0 }}>
                        <span role="gridcell" aria-colspan={5}>
                          {t("messagelog.log.dropped", { count: snap.dropped, n: fmt(snap.dropped), max: fmt(snap.maxMessages) })}
                        </span>
                      </div>
                    );
                  }
                  const seq = view.rows.at(index - offset);
                  const message = source.get(seq);
                  if (message === undefined) return null;
                  return (
                    <MessageRow
                      key={seq}
                      message={message}
                      index={index}
                      tabbable={index === tabbableIndex}
                      selected={seq === selectedSeq}
                      current={seq === activeHit}
                      pattern={pattern}
                      timeMode={timeMode}
                      binaryView={binaryView}
                      startTime={snap.startTime}
                      onClick={onRowClick}
                      onContextMenu={onRowContextMenu}
                      register={registerRow}
                    />
                  );
                })}
              </div>
            </div>
          </div>
          {rowCount === 0 && (
            <p className={styles.empty}>
              {snap.count === 0 ? t("messagelog.log.empty") : t("messagelog.log.noneMatch")}
            </p>
          )}
          {!following && (
            <button type="button" className={styles.jump} onClick={jumpToLatest}>
              <IconChevron direction="down" size={13} />
              {newRows > 0 ? t("messagelog.log.jumpToLatestNew", { n: fmt(newRows) }) : t("messagelog.log.jumpToLatest")}
            </button>
          )}
        </div>
        {selectedSeq !== null && (
          <MessageDetail
            key={`${selectedSeq}-${binaryView}`}
            seq={selectedSeq}
            message={selected}
            binaryView={binaryView}
            startTime={snap.startTime}
            onClose={() => setSelectedSeq(null)}
            renderPayload={renderPayload}
            entry={entryOf(selectedSeq)}
          />
        )}
      </div>
      {entryMenu.menu}

      <div className={styles.footer}>
        <span>
          {filtered
            ? t("messagelog.log.countFiltered", { shown: fmt(rowCount), n: fmt(snap.count) })
            : t("messagelog.log.count", { count: snap.count, n: fmt(snap.count) })}
        </span>
        <span className={styles.followState} data-following={following || undefined}>
          {following ? t("messagelog.log.following") : t("messagelog.log.paused")}
        </span>
      </div>
    </section>
  );
});

function indexOfSeq(view: MessageView, seq: number, offset: number): number {
  const i = view.rows.indexOf(seq);
  return i < 0 ? -1 : i + offset;
}

interface MessageRowProps {
  message: LogMessage;
  index: number;
  tabbable: boolean;
  selected: boolean;
  current: boolean;
  pattern: RegExp | null;
  timeMode: TimeMode;
  binaryView: BinaryView;
  startTime: number | null;
  onClick: (seq: number) => void;
  onContextMenu: (seq: number, e: MouseEvent<HTMLDivElement>) => void;
  register: (seq: number, el: HTMLDivElement | null) => void;
}

const MessageRow = memo(function MessageRow({
  message,
  index,
  tabbable,
  selected,
  current,
  pattern,
  timeMode,
  binaryView,
  startTime,
  onClick,
  onContextMenu,
  register,
}: MessageRowProps) {
  const t = useT();
  const { seq, direction, kind, badge } = message;
  const ref = useCallback((el: HTMLDivElement | null) => register(seq, el), [register, seq]);
  const preview = useMemo(() => messagePreview(message, binaryView), [message, binaryView]);
  const parts = useMemo(() => highlightParts(preview, pattern), [preview, pattern]);
  const size = messageSize(message);
  const classes = [styles.row];
  if (selected) classes.push(styles.selected);
  if (current) classes.push(styles.current);
  return (
    <div
      ref={ref}
      role="row"
      aria-rowindex={index + 2}
      aria-selected={selected}
      tabIndex={tabbable ? 0 : -1}
      className={classes.join(" ")}
      style={{ top: index * MESSAGE_ROW_HEIGHT }}
      data-seq={seq}
      data-direction={direction}
      onClick={() => onClick(seq)}
      onContextMenu={(e) => onContextMenu(seq, e)}
    >
      <span role="gridcell" className={styles.cTime} title={formatTimestamp(message.time)}>
        {timeMode === "relative" ? formatRelative(message.time, startTime) : formatClock(message.time)}
      </span>
      <span role="gridcell" className={styles.cDir} data-direction={direction}>
        <span aria-hidden="true">{DIRECTION_GLYPHS[direction]}</span>
        <span className={styles.srOnly}>{t(DIRECTION_KEYS[direction])}</span>
      </span>
      <span role="gridcell" className={styles.cKind} data-tone={kindTone(kind)} title={badge ? t("messagelog.log.kindBadge", { kind, badge }) : kind}>
        {kind}
        {badge !== undefined && <span className={styles.badge}>{badge}</span>}
      </span>
      <span role="gridcell" className={styles.cSize}>
        {formatSize(size)}
      </span>
      <span role="gridcell" className={styles.cPayload} data-binary={message.data !== undefined || undefined}>
        {parts.map((p, i) => (p.match ? <mark key={i}>{p.text}</mark> : p.text))}
      </span>
    </div>
  );
});
