import { useEffect, useId, useMemo, useRef, useState, type KeyboardEvent, type MouseEvent } from "react";
import {
  canRestoreIntoRequest,
  clearAllHistory,
  clearRequestHistoryCommand,
  deleteHistoryEntryCommand,
  restoreHistoryEntry,
  undoRestore,
  type RestoreResult,
  type RestoreTarget,
} from "../../commands/history";
import { confirm } from "../../commands/dialogs";
import { showToast } from "../../commands/toasts";
import { useHistoryList } from "../../commands/useHistoryList";
import { formatDate, t as translate } from "../../i18n";
import { useT } from "../../i18n/react";
import { formatError } from "../../lib/errors";
import { dayBound, groupByDay, NO_FILTER, statusTone, type HiddenDTO, type HistoryFilter } from "../../lib/history";
import type { HistorySummaryDTO } from "../../lib/responseHistory";
import { useCollection, useStores, useTabs } from "../../stores";
import { ContextMenu, type ContextMenuItem } from "../common/ContextMenu";
import { EmptyState } from "../common/EmptyState";
import { IconClose, IconFilter, IconHistory, IconSearch, IconTrash } from "../common/icons";
import { methodBadge } from "../tree/badges";
import styles from "./HistoryPanel.module.css";

export interface HistoryPanelProps {
  /** The sidebar heading. */
  label: string;
}

/** How long typing pauses before the search runs. */
const SEARCH_DELAY_MS = 250;

// Protocol tokens, not text.
const METHODS = ["GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS", "QUERY"] as const;
const STATUS_CLASSES = ["2xx", "3xx", "4xx", "5xx", "1xx"] as const;

/** The filters as the form holds them (dates as `<input type="date">` values). */
interface FilterForm {
  readonly method: string;
  readonly status: string;
  readonly kind: string;
  readonly environment: string;
  readonly from: string;
  readonly to: string;
  readonly deep: boolean;
  readonly thisRequest: boolean;
}

const NO_FORM: FilterForm = { method: "", status: "", kind: "", environment: "", from: "", to: "", deep: false, thisRequest: false };

function formActive(f: FilterForm): boolean {
  return f.method !== "" || f.status !== "" || f.kind !== "" || f.environment !== "" || f.from !== "" || f.to !== "" || f.deep || f.thisRequest;
}

/** The badge of an entry: its method, or GQL for GraphQL. */
function entryBadge(e: HistorySummaryDTO) {
  return e.kind === "graphql" ? { label: "GQL", tone: "gql" as const } : methodBadge(e.method);
}

/** The status an entry shows: "200 OK", "Failed", "Cancelled". */
function statusLabel(e: HistorySummaryDTO): string {
  if (e.outcome === "cancelled") return translate("history.row.cancelled");
  if (e.outcome !== "ok") return translate("history.row.failed");
  return e.status !== "" ? e.status : String(e.statusCode);
}

/** The name of an entry: its request's, or its URL for a replay. */
function entryName(e: HistorySummaryDTO): string {
  return e.requestName !== "" ? e.requestName : e.url;
}

function timeOf(e: HistorySummaryDTO): string {
  return formatDate(new Date(e.time), { hour: "2-digit", minute: "2-digit" });
}

/** The fields a restore did not write, as the toast names them. */
function hiddenFields(hidden: readonly HiddenDTO[]): string {
  const names: string[] = [];
  for (const h of hidden) {
    let name: string;
    switch (h.section) {
      case "params":
        name = translate("history.section.params", { name: h.name });
        break;
      case "pathParams":
        name = translate("history.section.pathParams", { name: h.name });
        break;
      case "headers":
        name = translate("history.section.headers", { name: h.name });
        break;
      case "bodyFields":
        name = translate("history.section.bodyFields", { name: h.name });
        break;
      case "bodyParts":
        name = translate("history.section.bodyParts", { name: h.name });
        break;
      case "url":
        name = translate("history.section.url");
        break;
      case "query":
        name = translate("history.section.query");
        break;
      case "variables":
        name = translate("history.section.variables");
        break;
      default:
        name = translate("history.section.body");
    }
    if (!names.includes(name)) names.push(name);
  }
  // i18n-ignore: a list separator between names that are already localized
  return names.join(", ");
}

/** What the toast says after a restore. */
function restoreMessage(result: RestoreResult, name: string): string {
  const r = result.restored;
  const parts = [
    result.target === "request" ? translate("history.restore.request", { name }) : translate("history.restore.scratch", { name }),
  ];
  if (!r.templated) parts.push(translate("history.restore.asSent"));
  const hidden = r.hidden ?? [];
  if (hidden.length > 0) {
    const fields = hiddenFields(hidden);
    parts.push(result.target === "request" ? translate("history.restore.hiddenRequest", { fields }) : translate("history.restore.hiddenScratch", { fields }));
  }
  return parts.join(" ");
}

interface Menu {
  readonly x: number;
  readonly y: number;
  readonly entry: HistorySummaryDTO;
}

/**
 * The History sidebar view (NAV-02, NAV-03): the open project's request
 * history, searched and filtered in Go (method, status class, kind,
 * environment, date range, the open request only, and optionally the
 * headers and bodies), grouped by day and paged. An entry is restored
 * into the request it was sent from (Enter, a double-click, the context
 * menu) as an undoable edit, or opened as a new scratch request; entries,
 * a request's history or all of it are deleted after asking. See
 * docs/history.md#the-history-panel.
 */
export function HistoryPanel({ label }: HistoryPanelProps) {
  const t = useT();
  const stores = useStores();
  const workspaceId = useCollection((s) => s.workspaceId);
  const activeId = useTabs((s) => s.activeId);
  const activeRequest = useCollection((s) => {
    const node = activeId !== null ? s.nodes[activeId] : undefined;
    return node?.kind === "request" ? node.name : null;
  });

  const [text, setText] = useState("");
  const [searchText, setSearchText] = useState("");
  const [form, setForm] = useState<FilterForm>(NO_FORM);
  const [filtersOpen, setFiltersOpen] = useState(false);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [focusId, setFocusId] = useState<string | null>(null);
  const [menu, setMenu] = useState<Menu | null>(null);
  const [clearMenu, setClearMenu] = useState<{ x: number; y: number } | null>(null);
  const searchRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLDivElement>(null);
  const filtersId = useId();
  const statusId = useId();

  // Another project starts unfiltered.
  useEffect(() => {
    setText("");
    setSearchText("");
    setForm(NO_FORM);
    setSelectedId(null);
    setFocusId(null);
  }, [workspaceId]);

  useEffect(() => {
    if (text === searchText) return;
    const timer = setTimeout(() => setSearchText(text), SEARCH_DELAY_MS);
    return () => clearTimeout(timer);
  }, [text, searchText]);

  const requestId = form.thisRequest && activeRequest !== null && activeId !== null ? activeId : "";
  const filter: HistoryFilter = useMemo(
    () => ({
      ...NO_FILTER,
      text: searchText,
      deep: form.deep,
      method: form.method,
      status: form.status,
      kind: form.kind,
      environment: form.environment,
      since: dayBound(form.from),
      until: dayBound(form.to, true),
      requestId,
    }),
    [searchText, form, requestId],
  );
  const { state, loadMore, refresh } = useHistoryList(filter, workspaceId);
  const groups = useMemo(() => groupByDay(state.entries, new Date()), [state.entries]);

  // The one row in the Tab order: the focused one, else the selected one, else the first.
  const ids = state.entries.map((e) => e.id);
  const tabId = focusId !== null && ids.includes(focusId) ? focusId : selectedId !== null && ids.includes(selectedId) ? selectedId : (ids[0] ?? null);

  const set = (patch: Partial<FilterForm>) => setForm((f) => ({ ...f, ...patch }));
  const filtered = searchText.trim() !== "" || formActive(form);

  const focusRow = (id: string) => {
    setFocusId(id);
    setSelectedId(id);
    listRef.current?.querySelector<HTMLElement>(`[data-entry="${CSS.escape(id)}"]`)?.focus();
  };

  const scratchName = (e: HistorySummaryDTO) =>
    e.requestName !== "" ? t("history.restore.scratchName", { name: e.requestName }) : t("history.restore.scratchNameUnnamed");

  const restore = (e: HistorySummaryDTO, target: RestoreTarget) => {
    restoreHistoryEntry(stores, e, target, scratchName(e)).then(
      (result) => {
        if (result === null) {
          showToast({ message: t("history.restore.gone") });
          return;
        }
        const name = stores.collection.getState().nodes[result.requestId]?.name ?? entryName(e);
        const long = !result.restored.templated || (result.restored.hidden ?? []).length > 0;
        showToast({
          message: restoreMessage(result, name),
          duration: long ? 14000 : undefined,
          action: result.target === "request" ? { label: t("history.restore.undo"), run: () => void undoRestore(stores, result.requestId) } : undefined,
        });
      },
      (err: unknown) => showToast({ message: t("history.failed", { message: formatError(err) }) }),
    );
  };

  const restoreDefault = (e: HistorySummaryDTO) => restore(e, canRestoreIntoRequest(stores, e) ? "request" : "scratch");

  const remove = (e: HistorySummaryDTO) => {
    const i = ids.indexOf(e.id);
    const next = ids[i + 1] ?? ids[i - 1] ?? null;
    deleteHistoryEntryCommand(e.id).then(
      () => {
        showToast({ message: t("history.delete.done") });
        if (next !== null) setFocusId(next);
        refresh();
      },
      (err: unknown) => showToast({ message: t("history.failed", { message: formatError(err) }) }),
    );
  };

  const clearRequest = async (id: string, name: string) => {
    const ok = await confirm({
      title: t("history.clear.requestTitle", { name }),
      message: t("history.clear.requestMessage"),
      confirmLabel: t("history.clear.allConfirm"),
      variant: "danger",
      confirmIcon: <IconTrash size={14} />,
    });
    if (!ok) return;
    try {
      const count = await clearRequestHistoryCommand(id);
      showToast({ message: t("history.clear.requestDone", { count, name }) });
      refresh();
    } catch (err: unknown) {
      showToast({ message: t("history.failed", { message: formatError(err) }) });
    }
  };

  const clearAll = async () => {
    const ok = await confirm({
      title: t("history.clear.allTitle"),
      message: t("history.clear.allMessage", { count: state.all }),
      confirmLabel: t("history.clear.allConfirm"),
      variant: "danger",
      confirmIcon: <IconTrash size={14} />,
    });
    if (!ok) return;
    try {
      await clearAllHistory();
      showToast({ message: t("history.clear.allDone") });
      refresh();
    } catch (err: unknown) {
      showToast({ message: t("history.failed", { message: formatError(err) }) });
    }
  };

  const openMenuAt = (e: HistorySummaryDTO, el: HTMLElement | null) => {
    const rect = el?.getBoundingClientRect();
    setMenu({ x: (rect?.left ?? 0) + 24, y: rect?.bottom ?? 0, entry: e });
  };

  const onListKeyDown = (ev: KeyboardEvent<HTMLDivElement>) => {
    if (ids.length === 0 || tabId === null) return;
    const i = Math.max(0, ids.indexOf(tabId));
    const entry = state.entries[i];
    const go = (j: number) => {
      ev.preventDefault();
      const id = ids[Math.max(0, Math.min(ids.length - 1, j))];
      if (id !== undefined) focusRow(id);
    };
    switch (ev.key) {
      case "ArrowDown":
        return go(i + 1);
      case "ArrowUp":
        return go(i - 1);
      case "Home":
        return go(0);
      case "End":
        return go(ids.length - 1);
      case "PageDown":
        return go(i + 10);
      case "PageUp":
        return go(i - 10);
      case "Enter":
        if (entry === undefined) return;
        ev.preventDefault();
        setSelectedId(entry.id);
        restoreDefault(entry);
        return;
      case "Delete":
      case "Backspace":
        if (entry === undefined) return;
        ev.preventDefault();
        remove(entry);
        return;
      case "ContextMenu":
      case "F10":
        if (ev.key === "F10" && !ev.shiftKey) return;
        if (entry === undefined) return;
        ev.preventDefault();
        openMenuAt(entry, ev.target instanceof HTMLElement ? ev.target : null);
        return;
    }
  };

  const onRowContextMenu = (ev: MouseEvent, e: HistorySummaryDTO) => {
    ev.preventDefault();
    setSelectedId(e.id);
    setFocusId(e.id);
    setMenu({ x: ev.clientX, y: ev.clientY, entry: e });
  };

  const menuItems: ContextMenuItem[] = [];
  if (menu !== null) {
    const e = menu.entry;
    const into = canRestoreIntoRequest(stores, e);
    const name = stores.collection.getState().nodes[e.requestId]?.name ?? e.requestName;
    menuItems.push(
      into
        ? { id: "restore", label: t("history.menu.restore", { name }), onSelect: () => restore(e, "request") }
        : { id: "restore", label: t("history.menu.restoreUnavailable"), title: t("history.menu.restoreUnavailableTitle"), disabled: true, onSelect: () => undefined },
      { id: "scratch", label: t("history.menu.scratch"), onSelect: () => restore(e, "scratch") },
      { id: "delete", label: t("history.menu.delete"), separatorBefore: true, onSelect: () => remove(e) },
    );
    if (e.requestId !== "") {
      menuItems.push({ id: "clearRequest", label: t("history.menu.clearRequest", { name: name || e.url }), onSelect: () => void clearRequest(e.requestId, name || e.url) });
    }
  }

  const clearItems: ContextMenuItem[] = [
    {
      id: "request",
      label: activeRequest !== null ? t("history.clear.request", { name: activeRequest }) : t("history.filters.thisRequestNone"),
      disabled: activeRequest === null || activeId === null,
      onSelect: () => {
        if (activeId !== null && activeRequest !== null) void clearRequest(activeId, activeRequest);
      },
    },
    { id: "all", label: t("history.clear.all"), disabled: state.all === 0, onSelect: () => void clearAll() },
  ];

  let body;
  if (workspaceId === null) {
    // No project, no history.
    body = (
      <EmptyState placement="top" icon={<IconHistory size={28} strokeWidth={1.5} />} title={t("history.empty.title")}>
        {t("history.empty.body")}
      </EmptyState>
    );
  } else if (state.status === "loading" && state.entries.length === 0) {
    body = <p className={styles.hint}>{t("history.loading")}</p>;
  } else if (state.status === "error") {
    body = (
      <div className={styles.message}>
        <div className={styles.error} role="alert">
          <p>{t("history.error.title")}</p>
          <p>{state.error?.message}</p>
        </div>
        <button type="button" className={styles.moreButton} onClick={refresh}>
          {t("history.retry")}
        </button>
      </div>
    );
  } else if (state.entries.length === 0) {
    body = filtered ? (
      <div className={styles.message}>
        <EmptyState placement="top" icon={<IconSearch size={28} strokeWidth={1.5} />} title={t("history.noMatch.title")}>
          {t("history.noMatch.body")}
        </EmptyState>
        <button
          type="button"
          className={styles.linkButton}
          onClick={() => {
            setText("");
            setSearchText("");
            setForm(NO_FORM);
          }}
        >
          {t("history.filters.reset")}
        </button>
      </div>
    ) : (
      <EmptyState placement="top" icon={<IconHistory size={28} strokeWidth={1.5} />} title={t("history.empty.title")}>
        {t("history.empty.body")}
      </EmptyState>
    );
  } else {
    body = (
      <>
        <div
          ref={listRef}
          className={styles.list}
          role="listbox"
          aria-label={t("history.list.label")}
          aria-describedby={statusId}
          onKeyDown={onListKeyDown}
        >
          {groups.map((g) => {
            const titleId = `${filtersId}-g${g.key}`;
            const title =
              g.day === "today"
                ? t("history.group.today")
                : g.day === "yesterday"
                  ? t("history.group.yesterday")
                  : formatDate(g.date, { weekday: "short", day: "numeric", month: "short", year: "numeric" });
            return (
              <div key={g.key} role="group" aria-labelledby={titleId} className={styles.group}>
                <div id={titleId} className={styles.groupTitle}>
                  {title}
                </div>
                {g.entries.map((e) => {
                  const badge = entryBadge(e);
                  const name = entryName(e);
                  const status = statusLabel(e);
                  const time = timeOf(e);
                  return (
                    <div
                      key={e.id}
                      role="option"
                      data-entry={e.id}
                      className={styles.row}
                      tabIndex={e.id === tabId ? 0 : -1}
                      aria-selected={e.id === selectedId}
                      aria-label={t("history.row.label", { method: badge.label, name, status, time })}
                      title={t("history.row.hint")}
                      onFocus={() => setFocusId(e.id)}
                      onClick={() => setSelectedId(e.id)}
                      onDoubleClick={() => restoreDefault(e)}
                      onContextMenu={(ev) => onRowContextMenu(ev, e)}
                    >
                      <span className={styles.badge} data-tone={badge.tone} aria-hidden="true">
                        {badge.label}
                      </span>
                      <span className={styles.main} aria-hidden="true">
                        <span className={styles.name}>{e.kind === "replay" ? t("history.row.replay") : name}</span>
                        {e.requestName !== "" || e.kind === "replay" ? <span className={styles.url}>{e.url}</span> : null}
                        <span className={styles.meta}>
                          <span className={styles.statusText} data-tone={statusTone(e)}>
                            {status}
                          </span>
                          <span>{time}</span>
                          {e.environment !== "" && <span>{t("history.row.environment", { name: e.environment })}</span>}
                        </span>
                      </span>
                    </div>
                  );
                })}
              </div>
            );
          })}
        </div>
        {state.entries.length < state.total && (
          <div className={styles.more}>
            <button type="button" className={styles.moreButton} disabled={state.loadingMore} onClick={loadMore}>
              {state.loadingMore ? t("history.loadingMore") : t("history.loadMore")}
            </button>
          </div>
        )}
      </>
    );
  }

  return (
    <div className={styles.panel}>
      <div className={styles.header}>
        <h2 className={styles.title}>{label}</h2>
        <div className={styles.headerActions}>
          <button
            type="button"
            className={styles.headerButton}
            aria-expanded={filtersOpen}
            aria-controls={filtersId}
            aria-label={filtersOpen ? t("history.filters.hide") : t("history.filters.show")}
            title={filtersOpen ? t("history.filters.hide") : t("history.filters.show")}
            onClick={() => setFiltersOpen((o) => !o)}
          >
            <IconFilter size={15} />
          </button>
          <button
            type="button"
            className={styles.headerButton}
            aria-haspopup="menu"
            aria-label={t("history.clear.menu")}
            title={t("history.clear.menu")}
            onClick={(ev) => {
              const rect = ev.currentTarget.getBoundingClientRect();
              setClearMenu({ x: rect.left, y: rect.bottom });
            }}
          >
            <IconTrash size={15} />
          </button>
        </div>
      </div>

      <div className={styles.searchRow}>
        <label className={styles.search}>
          <IconSearch size={13} />
          <input
            ref={searchRef}
            className={styles.searchInput}
            type="text"
            role="searchbox"
            placeholder={t("history.search.placeholder")}
            aria-label={t("history.search.label")}
            spellCheck={false}
            autoComplete="off"
            value={text}
            onChange={(ev) => setText(ev.target.value)}
            onKeyDown={(ev) => {
              if (ev.nativeEvent.isComposing) return;
              if (ev.key === "Escape" && text !== "") {
                ev.preventDefault();
                ev.stopPropagation();
                setText("");
                setSearchText("");
              } else if (ev.key === "Enter") {
                setSearchText(text);
              } else if (ev.key === "ArrowDown" && tabId !== null) {
                ev.preventDefault();
                focusRow(tabId);
              }
            }}
          />
          {text !== "" && (
            <button
              type="button"
              className={styles.searchClear}
              aria-label={t("history.search.clear")}
              onClick={() => {
                setText("");
                setSearchText("");
                searchRef.current?.focus();
              }}
            >
              <IconClose size={12} />
            </button>
          )}
        </label>
      </div>

      <div id={filtersId} className={styles.filters} role="group" aria-label={t("history.filters.label")} hidden={!filtersOpen}>
        <div className={styles.fieldRow}>
          <label className={styles.field}>
            <span>{t("history.filters.method")}</span>
            <select className={styles.select} value={form.method} onChange={(ev) => set({ method: ev.target.value })}>
              <option value="">{t("history.filters.any")}</option>
              {METHODS.map((m) => (
                <option key={m} value={m}>
                  {m}
                </option>
              ))}
            </select>
          </label>
          <label className={styles.field}>
            <span>{t("history.filters.status")}</span>
            <select className={styles.select} value={form.status} onChange={(ev) => set({ status: ev.target.value })}>
              <option value="">{t("history.filters.any")}</option>
              {STATUS_CLASSES.map((s) => (
                <option key={s} value={s}>
                  {s}
                </option>
              ))}
              <option value="error">{t("history.filters.status.error")}</option>
            </select>
          </label>
        </div>
        <div className={styles.fieldRow}>
          <label className={styles.field}>
            <span>{t("history.filters.kind")}</span>
            <select className={styles.select} value={form.kind} onChange={(ev) => set({ kind: ev.target.value })}>
              <option value="">{t("history.filters.any")}</option>
              <option value="http">{t("history.filters.kind.http")}</option>
              <option value="graphql">{t("history.filters.kind.graphql")}</option>
              <option value="replay">{t("history.filters.kind.replay")}</option>
            </select>
          </label>
          <label className={styles.field}>
            <span>{t("history.filters.environment")}</span>
            <select className={styles.select} value={form.environment} onChange={(ev) => set({ environment: ev.target.value })}>
              <option value="">{t("history.filters.any")}</option>
              {[...new Set([...state.environments, ...(form.environment !== "" ? [form.environment] : [])])].map((name) => (
                <option key={name} value={name}>
                  {name}
                </option>
              ))}
            </select>
          </label>
        </div>
        <div className={styles.fieldRow}>
          <label className={styles.field}>
            <span>{t("history.filters.from")}</span>
            <input className={styles.date} type="date" value={form.from} max={form.to || undefined} onChange={(ev) => set({ from: ev.target.value })} />
          </label>
          <label className={styles.field}>
            <span>{t("history.filters.to")}</span>
            <input className={styles.date} type="date" value={form.to} min={form.from || undefined} onChange={(ev) => set({ to: ev.target.value })} />
          </label>
        </div>
        <label className={styles.check}>
          <input type="checkbox" checked={form.thisRequest} disabled={activeRequest === null} onChange={(ev) => set({ thisRequest: ev.target.checked })} />
          <span>{activeRequest !== null ? t("history.filters.thisRequest", { name: activeRequest }) : t("history.filters.thisRequestNone")}</span>
        </label>
        <label className={styles.check}>
          <input type="checkbox" checked={form.deep} onChange={(ev) => set({ deep: ev.target.checked })} />
          <span>{t("history.filters.deep")}</span>
        </label>
        <p className={styles.hint}>{t("history.filters.deepHint")}</p>
        {formActive(form) && (
          <button type="button" className={styles.linkButton} onClick={() => setForm(NO_FORM)}>
            {t("history.filters.reset")}
          </button>
        )}
      </div>

      <p id={statusId} className={styles.status} role="status">
        {state.status === "ready" &&
          (filtered ? t("history.count.matching", { count: state.total, all: state.all }) : t("history.count.all", { count: state.all }))}
      </p>

      <div className={styles.body}>{body}</div>

      {menu !== null && <ContextMenu label={t("history.menu.label")} x={menu.x} y={menu.y} items={menuItems} onClose={() => setMenu(null)} />}
      {clearMenu !== null && (
        <ContextMenu label={t("history.clear.menuLabel")} x={clearMenu.x} y={clearMenu.y} items={clearItems} onClose={() => setClearMenu(null)} />
      )}
    </div>
  );
}
