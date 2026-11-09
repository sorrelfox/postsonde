import { useEffect, useId, useMemo, useRef, useState, type CSSProperties, type KeyboardEvent } from "react";
import { openTreeItem, revealNode } from "../../commands/collection";
import { setActiveEnvironment } from "../../commands/environments";
import { openCommandPalette } from "../../commands/palette";
import { showToast } from "../../commands/toasts";
import { formatError } from "../../lib/errors";
import { ariaShortcut, shortcutLabel } from "../../lib/shortcuts";
import { isEnvironmentColor, shallowEqual, useCollection, useEnvironments, useStores, useTabs } from "../../stores";
import { Highlighted } from "../common/Highlighted";
import { IconFolder, IconSearch } from "../common/icons";
import {
  buildSwitchItems,
  environmentKey,
  nodeKey,
  searchSwitchItems,
  touchRecent,
  type SwitchItem,
  type SwitchResult,
} from "./quickSwitch";
import { useShortcut } from "./useShortcut";
import { t as translate } from "../../i18n";
import { useT } from "../../i18n/react";
import styles from "./QuickSwitcher.module.css";

/** Typed first, it hands over to the command palette (NAV-05), as in VS Code. */
export const COMMAND_PREFIX = ">";

function ItemMark({ item, active }: { item: SwitchItem; active: boolean }) {
  if (item.type === "folder") {
    return (
      <span className={styles.mark}>
        <IconFolder size={13} className={styles.folderIcon} />
      </span>
    );
  }
  if (item.type === "environment") {
    const style = isEnvironmentColor(item.color) ? ({ "--env-color": item.color } as CSSProperties) : undefined;
    return (
      <span className={styles.mark}>
        <span className={active ? `${styles.envDot} ${styles.envDotActive}` : styles.envDot} style={style} />
      </span>
    );
  }
  return (
    <span className={`${styles.mark} ${styles.badge}`} data-tone={item.badge?.tone ?? "dir"}>
      {item.badge?.label ?? ""}
    </span>
  );
}

function kindText(item: SwitchItem, activeEnv: boolean): string {
  switch (item.type) {
    case "folder":
      return translate("shell.quickSwitcher.folder");
    case "environment":
      return activeEnv ? translate("shell.quickSwitcher.activeEnvironment") : translate("shell.quickSwitcher.environment");
    default:
      return item.badge?.label ?? translate("shell.quickSwitcher.item");
  }
}

/**
 * The quick switcher (NAV-04): the title bar's jump box. A click or ⌘P
 * (Ctrl+P) turns it into a search field over every request, folder and
 * environment with a fuzzy-matched list below it (a combobox and its
 * listbox). ↑ and ↓ move, Enter opens a request or folder in a kept tab (as
 * Enter in the tree does) or makes an environment active, Escape closes.
 * Recently opened tabs rank higher and are listed first before typing.
 */
export function QuickSwitcher() {
  const stores = useStores();
  const nodes = useCollection((s) => s.nodes);
  const rootIds = useCollection((s) => s.rootIds);
  const workspaceId = useCollection((s) => s.workspaceId);
  const environments = useEnvironments((s) => s.ids.map((id) => s.byId[id]), shallowEqual);
  const activeEnvId = useEnvironments((s) => s.activeId);
  const activeTabId = useTabs((s) => s.activeId);
  const t = useT();

  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [index, setIndex] = useState(0);
  const [recent, setRecent] = useState<readonly string[]>([]);
  const inputRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLUListElement>(null);
  const returnFocus = useRef<HTMLElement | null>(null);
  const baseId = useId();
  const listId = `${baseId}-list`;
  const optionId = (i: number) => `${baseId}-opt-${i}`;

  // Another project: forget the recent items.
  useEffect(() => setRecent([]), [workspaceId]);
  // The tab shown last is the most recent item.
  useEffect(() => {
    if (activeTabId !== null) setRecent((r) => (r[0] === nodeKey(activeTabId) ? r : touchRecent(r, nodeKey(activeTabId))));
  }, [activeTabId]);

  const items = useMemo(() => buildSwitchItems(nodes, rootIds, environments), [nodes, rootIds, environments]);
  const results = useMemo(() => (open ? searchSwitchItems(items, query, recent) : []), [open, items, query, recent]);
  const disabled = workspaceId === null && environments.length === 0;
  const current = results.length === 0 ? -1 : Math.min(index, results.length - 1);

  useEffect(() => {
    if (open) inputRef.current?.focus();
  }, [open]);

  useEffect(() => {
    if (current < 0) return;
    const el = listRef.current?.children[current] as (HTMLElement & { scrollIntoView?: (o: object) => void }) | undefined;
    el?.scrollIntoView?.({ block: "nearest" });
  }, [current]);

  const show = () => {
    if (open) {
      inputRef.current?.select();
      return;
    }
    const focused = document.activeElement;
    returnFocus.current = focused instanceof HTMLElement && focused !== document.body ? focused : null;
    setQuery("");
    setIndex(0);
    setOpen(true);
  };

  const close = (restoreFocus: boolean) => {
    setOpen(false);
    const target = returnFocus.current;
    returnFocus.current = null;
    if (restoreFocus && target !== null && target.isConnected) target.focus();
  };

  useShortcut("jump.open", () => {
    if (disabled) return false;
    show();
  });

  const choose = (result: SwitchResult | undefined) => {
    if (result === undefined) return;
    const { item } = result;
    close(true);
    if (item.type === "environment") {
      setRecent((r) => touchRecent(r, environmentKey(item.id)));
      setActiveEnvironment(stores, item.id).catch((err: unknown) =>
        showToast({ message: t("shell.quickSwitcher.switchFailed", { name: item.name, error: formatError(err) }) }),
      );
      return;
    }
    revealNode(stores, item.id);
    openTreeItem(stores, item.id);
  };

  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    const n = results.length;
    let handled = true;
    switch (e.key) {
      case "ArrowDown":
        if (n > 0) setIndex((current + 1) % n);
        break;
      case "ArrowUp":
        if (n > 0) setIndex((current - 1 + n) % n);
        break;
      case "PageDown":
        if (n > 0) setIndex(Math.min(n - 1, current + 8));
        break;
      case "PageUp":
        if (n > 0) setIndex(Math.max(0, current - 8));
        break;
      case "Enter":
        if (!e.nativeEvent.isComposing) choose(results[current]);
        break;
      case "Escape":
      case "Tab":
        close(true);
        break;
      default:
        handled = false;
    }
    if (handled) {
      e.preventDefault();
      e.stopPropagation();
    }
  };

  const label = t("shell.quickSwitcher.label");

  if (!open) {
    return (
      <div className={styles.root}>
        <button
          type="button"
          className={styles.box}
          disabled={disabled}
          aria-label={label}
          aria-keyshortcuts={ariaShortcut("jump.open")}
          title={t("shell.quickSwitcher.labelWithShortcut", { shortcut: shortcutLabel("jump.open") })}
          onClick={show}
        >
          <IconSearch size={13} />
          <span className={styles.placeholder}>{label}</span>
          <kbd className={styles.kbd}>{shortcutLabel("jump.open")}</kbd>
        </button>
      </div>
    );
  }

  const status =
    results.length === 0 ? t("shell.quickSwitcher.noMatches") : t("shell.quickSwitcher.count", { count: results.length });

  return (
    <div
      className={styles.root}
      onBlur={(e) => {
        if (!e.currentTarget.contains(e.relatedTarget as Node | null)) close(false);
      }}
    >
      <div className={`${styles.box} ${styles.boxOpen}`}>
        <IconSearch size={13} />
        <input
          ref={inputRef}
          className={styles.input}
          type="text"
          role="combobox"
          aria-label={label}
          aria-expanded="true"
          aria-controls={listId}
          aria-autocomplete="list"
          aria-activedescendant={current >= 0 ? optionId(current) : undefined}
          placeholder={label}
          spellCheck={false}
          autoComplete="off"
          value={query}
          onChange={(e) => {
            // ">" first switches to the command palette (NAV-05), with the rest as its query.
            const text = e.target.value;
            if (text.startsWith(COMMAND_PREFIX)) {
              close(true);
              openCommandPalette(text.slice(COMMAND_PREFIX.length));
              return;
            }
            setQuery(text);
            setIndex(0);
          }}
          onKeyDown={onKeyDown}
        />
        <kbd className={styles.kbd}>{t("shell.keys.escape")}</kbd>
      </div>
      <div className={styles.popup}>
        <ul ref={listRef} id={listId} role="listbox" aria-label={t("shell.quickSwitcher.results")} className={styles.list}>
          {results.map((result, i) => {
            const { item } = result;
            const activeEnv = item.type === "environment" && item.id === activeEnvId;
            return (
              <li
                key={item.key}
                id={optionId(i)}
                role="option"
                aria-selected={i === current}
                aria-label={
                  item.path === ""
                    ? t("shell.quickSwitcher.option", { name: item.name, kind: kindText(item, activeEnv) })
                    : t("shell.quickSwitcher.optionInPath", { name: item.name, kind: kindText(item, activeEnv), path: item.path })
                }
                className={styles.option}
                onMouseDown={(e) => e.preventDefault()}
                onMouseMove={() => {
                  if (i !== current) setIndex(i);
                }}
                onClick={() => choose(result)}
              >
                <ItemMark item={item} active={activeEnv} />
                <span className={styles.name}>
                  <Highlighted text={item.name} hits={result.nameHits} className={styles.hit} />
                </span>
                {item.path !== "" && (
                  <span className={styles.path}>
                    <Highlighted text={item.path} hits={result.pathHits} className={styles.hit} />
                  </span>
                )}
                {item.type === "environment" && (
                  <span className={styles.kind}>
                    {activeEnv ? t("shell.quickSwitcher.activeEnvironment") : t("shell.quickSwitcher.environment")}
                  </span>
                )}
              </li>
            );
          })}
        </ul>
        {results.length === 0 && <div className={styles.empty}>{t("shell.quickSwitcher.empty")}</div>}
        <div className={styles.footer} aria-hidden="true">
          <span>
            <kbd className={styles.kbd}>↑</kbd> <kbd className={styles.kbd}>↓</kbd> {t("shell.keys.move")}
          </span>
          <span>
            <kbd className={styles.kbd}>{t("shell.keys.enter")}</kbd> {t("shell.keys.open")}
          </span>
          <span>
            <kbd className={styles.kbd}>{t("shell.keys.escape")}</kbd> {t("shell.keys.close")}
          </span>
          <span>
            <kbd className={styles.kbd}>&gt;</kbd> {t("shell.keys.commands")}
          </span>
        </div>
        <div className={styles.srOnly} role="status" aria-live="polite">
          {status}
        </div>
      </div>
    </div>
  );
}
