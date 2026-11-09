import { useDeferredValue, useEffect, useRef, useState, type KeyboardEvent } from "react";
import { EmptyState } from "../common/EmptyState";
import { EnvironmentsPanel } from "../environments/EnvironmentsPanel";
import { HistoryPanel } from "../history/HistoryPanel";
import { IconClose, IconDownload, IconFileImport, IconFilter, IconFolderPlus, IconPlus } from "../common/icons";
import { ariaShortcut, shortcutLabel } from "../../lib/shortcuts";
import { useCollection } from "../../stores";
import { ServersPanel } from "../servers/ServersPanel";
import { CollectionTree } from "../tree/CollectionTree";
import { useNewItemDialog } from "../tree/useNewItemDialog";
import { useTreeActions } from "../tree/useTreeActions";
import { useT } from "../../i18n/react";
import styles from "./Sidebar.module.css";
import { SIDEBAR_VIEWS, type SidebarView } from "./sidebarViews";

export interface SidebarProps {
  id: string;
  view: SidebarView;
  /** Collapsed: the element stays in the DOM (for aria-controls) but is hidden. */
  hidden: boolean;
  /** Focuses the Collections filter field whenever it changes (collection.filter), after the first value. */
  filterFocus?: number;
}

/**
 * The sidebar panel for the active rail view. Collections shows the request
 * tree, History the request history (NAV-02, NAV-03), Environments
 * its panel, Servers the local servers
 * (SRV-01); the other views are empty states
 * for now. The New folder and New request dialogs (COL-04) open from the
 * Collections header, the tree's context menu and ⌘N in any view, the
 * Import cURL dialog (IMP-01) and the Import dialog (IMP-10) from the
 * header and the context menu; rename,
 * duplicate and delete with its Undo toast from the tree (useTreeActions).
 * The filter field above the tree filters it by name or method (NAV-06).
 */
export function Sidebar({ id, view, hidden, filterFocus = 0 }: SidebarProps) {
  const t = useT();
  const { labelKey, Icon, emptyTitleKey, emptyBodyKey } = SIDEBAR_VIEWS[view];
  const label = t(labelKey);
  const isCollections = view === "collections";
  const newItem = useNewItemDialog();
  const treeActions = useTreeActions();

  // The filter (NAV-06). The tree filters with the deferred value, so typing stays smooth in huge trees.
  const [filter, setFilter] = useState("");
  const deferredFilter = useDeferredValue(filter);
  const filterRef = useRef<HTMLInputElement>(null);
  const treeBoxRef = useRef<HTMLDivElement>(null);
  const treeReady = useCollection((s) => s.status === "ready");
  const workspaceId = useCollection((s) => s.workspaceId);

  // Another project starts unfiltered.
  useEffect(() => setFilter(""), [workspaceId]);

  useEffect(() => {
    if (filterFocus === 0) return;
    filterRef.current?.focus();
    filterRef.current?.select();
  }, [filterFocus]);

  const clearFilter = () => {
    setFilter("");
    filterRef.current?.focus();
  };

  /** Down or Enter: into the tree, on its focusable row (the first match while filtering). */
  const onFilterKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.nativeEvent.isComposing) return;
    if (e.key === "Escape" && filter !== "") {
      e.preventDefault();
      e.stopPropagation();
      setFilter("");
      return;
    }
    if (e.key === "ArrowDown" || e.key === "Enter") {
      const row = treeBoxRef.current?.querySelector<HTMLElement>('[role="treeitem"][tabindex="0"]');
      if (row === null || row === undefined) return;
      e.preventDefault();
      row.focus();
    }
  };

  if (view === "servers") {
    return (
      <aside id={id} className={styles.sidebar} hidden={hidden} aria-label={label}>
        <ServersPanel label={label} />
        {newItem.dialog}
        {treeActions.dialog}
      </aside>
    );
  }

  if (view === "history") {
    return (
      <aside id={id} className={styles.sidebar} hidden={hidden} aria-label={label}>
        <HistoryPanel label={label} />
        {newItem.dialog}
        {treeActions.dialog}
      </aside>
    );
  }

  if (view === "environments") {
    return (
      <aside id={id} className={styles.sidebar} hidden={hidden} aria-label={label}>
        <EnvironmentsPanel label={label} />
        {newItem.dialog}
        {treeActions.dialog}
      </aside>
    );
  }

  return (
    <aside id={id} className={styles.sidebar} hidden={hidden} aria-label={label}>
      {newItem.dialog}
      {treeActions.dialog}
      <div className={styles.header}>
        <h2 className={styles.title}>{label}</h2>
        {isCollections && (
          <div className={styles.headerActions}>
            <button
              type="button"
              className={styles.headerButton}
              disabled={!newItem.canCreate}
              aria-label={t("shell.sidebar.newFolder")}
              title={newItem.disabledReason || t("shell.sidebar.newFolder")}
              onClick={() => newItem.ask("folder")}
            >
              <IconFolderPlus size={15} />
            </button>
            <button
              type="button"
              className={styles.headerButton}
              disabled={!newItem.canCreate}
              aria-label={t("shell.sidebar.importCurl")}
              title={newItem.disabledReason || t("shell.sidebar.importCurl")}
              onClick={() => newItem.ask("curl")}
            >
              <IconDownload size={15} />
            </button>
            <button
              type="button"
              className={styles.headerButton}
              disabled={!newItem.canCreate}
              aria-label={t("shell.sidebar.importFile")}
              title={newItem.disabledReason || t("shell.sidebar.importFileTitle")}
              onClick={() => newItem.ask("import")}
            >
              <IconFileImport size={15} />
            </button>
            <button
              type="button"
              className={styles.headerButton}
              disabled={!newItem.canCreate}
              aria-label={t("shell.sidebar.newRequest")}
              aria-keyshortcuts={ariaShortcut("request.new")}
              title={newItem.disabledReason || t("shell.sidebar.newRequestTitle", { shortcut: shortcutLabel("request.new") })}
              onClick={() => newItem.ask("request")}
            >
              <IconPlus size={15} />
            </button>
          </div>
        )}
      </div>

      {isCollections && (
        <div className={styles.filterRow}>
          <label className={styles.filter}>
            <IconFilter size={13} />
            <input
              ref={filterRef}
              className={styles.filterInput}
              type="text"
              role="searchbox"
              placeholder={t("shell.sidebar.filter.placeholder")}
              aria-label={t("shell.sidebar.filter.label")}
              aria-keyshortcuts={ariaShortcut("collection.filter")}
              title={t("shell.sidebar.filter.title", { shortcut: shortcutLabel("collection.filter") })}
              spellCheck={false}
              autoComplete="off"
              value={filter}
              disabled={!treeReady}
              onChange={(e) => setFilter(e.target.value)}
              onKeyDown={onFilterKeyDown}
            />
            {filter !== "" && (
              <button type="button" className={styles.filterClear} aria-label={t("shell.sidebar.filter.clear")} title={t("shell.sidebar.filter.clearTitle")} onClick={clearFilter}>
                <IconClose size={12} />
              </button>
            )}
          </label>
        </div>
      )}

      {isCollections ? (
        <div ref={treeBoxRef} className={styles.treeBox}>
          <CollectionTree label={label} onNewItem={newItem.ask} actions={treeActions} filter={deferredFilter} onClearFilter={clearFilter} />
        </div>
      ) : (
        <div className={styles.content}>
          <EmptyState placement="top" icon={<Icon size={28} strokeWidth={1.5} />} title={t(emptyTitleKey)}>
            {t(emptyBodyKey)}
          </EmptyState>
        </div>
      )}
    </aside>
  );
}
