import { useEffect, useId, useMemo, useRef, useState, type KeyboardEvent } from "react";
import {
  onCommandPaletteRequest,
  paletteItems,
  runPaletteAction,
  touchRecentAction,
  type PaletteItem,
  type PaletteOptions,
} from "../../commands/palette";
import { showToast } from "../../commands/toasts";
import { formatChord, IS_MAC, scopeLabel, shortcutChords, shortcutName, type ShortcutDefinition } from "../../lib/shortcuts";
import { Dialog } from "../common/Dialog";
import { Highlighted } from "../common/Highlighted";
import { IconSearch } from "../common/icons";
import { t as translate } from "../../i18n";
import { useT } from "../../i18n/react";
import styles from "./CommandPalette.module.css";
import { useShortcut } from "./useShortcut";
import { useShortcutOverrides } from "./useShortcutOverridesSync";

export interface CommandPaletteProps extends PaletteOptions {
  /** Show macOS chords (⌘) instead of Ctrl. Default: the running platform. */
  mac?: boolean;
}

/**
 * The command palette (NAV-05): ⇧⌘P (Ctrl+Shift+P), or ">" in the quick
 * switcher, opens a searchable list of every action of the shortcut
 * registry with its chords and where it applies. Actions that cannot run
 * where focus was are listed after the others, disabled, with the reason.
 * Enter or a click closes the palette, gives focus back and runs the action
 * there. The frame is the shared Dialog (focus trap, Escape, scrim); the
 * field is a combobox over a listbox.
 */
export function CommandPalette({ mac = IS_MAC, ...options }: CommandPaletteProps) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const [initialQuery, setInitialQuery] = useState("");
  // Where focus was before the palette opened: scoped actions run there.
  const target = useRef<Element | null>(null);
  // Chosen, to run once the palette has closed and focus is back.
  const pending = useRef<ShortcutDefinition | null>(null);
  const [recent, setRecent] = useState<readonly string[]>([]);
  const [session, setSession] = useState(0);

  const show = (query: string) => {
    const focused = document.activeElement;
    target.current = focused instanceof Element && focused !== document.body ? focused : null;
    setInitialQuery(query);
    setSession((n) => n + 1);
    setOpen(true);
  };
  const latestShow = useRef(show);
  latestShow.current = show;

  useShortcut("palette.open", () => {
    if (!open) show("");
  });
  useEffect(() => onCommandPaletteRequest((query) => latestShow.current(query)), []);

  // After the Dialog has closed (its cleanup gave focus back), run the chosen action.
  useEffect(() => {
    if (open) return;
    const def = pending.current;
    pending.current = null;
    if (def === null) return;
    const result = runPaletteAction(def, target.current, { ...options, mac });
    if (result === "declined") showToast({ message: translate("shell.palette.declined", { command: shortcutName(def) }) });
    else if (result === "unavailable") showToast({ message: translate("shell.palette.unavailable", { command: shortcutName(def) }) });
    // options are stable per mount; running once per close is the point.
  }, [open]);

  const choose = (item: PaletteItem) => {
    if (!item.available) return;
    pending.current = item.def;
    setRecent((r) => touchRecentAction(r, item.def.id));
    setOpen(false);
  };

  return (
    <Dialog open={open} title={t("shell.palette.title")} width={600} bodyClassName={styles.content} onCancel={() => setOpen(false)}>
      <PaletteList
        key={session}
        initialQuery={initialQuery}
        target={target.current}
        recent={recent}
        options={options}
        mac={mac}
        onChoose={choose}
      />
    </Dialog>
  );
}

interface PaletteListProps {
  initialQuery: string;
  target: Element | null;
  recent: readonly string[];
  options: PaletteOptions;
  mac: boolean;
  onChoose: (item: PaletteItem) => void;
}

function PaletteList({ initialQuery, target, recent, options, mac, onChoose }: PaletteListProps) {
  const t = useT();
  const [query, setQuery] = useState(initialQuery);
  const [index, setIndex] = useState(0);
  const overrides = useShortcutOverrides();
  const listRef = useRef<HTMLUListElement>(null);
  const baseId = useId();
  const listId = `${baseId}-list`;
  const optionId = (i: number) => `${baseId}-opt-${i}`;

  // The bindings of the page behind do not change while the palette is open.
  // Labels and reasons are in the language in use (t changes with it).
  const items = useMemo(() => paletteItems(query, target, recent, options), [query, target, recent, overrides, t]);
  const current = items.length === 0 ? -1 : Math.min(index, items.length - 1);
  const active = current >= 0 ? items[current] : undefined;

  useEffect(() => {
    if (current < 0) return;
    const el = listRef.current?.children[current] as (HTMLElement & { scrollIntoView?: (o: object) => void }) | undefined;
    el?.scrollIntoView?.({ block: "nearest" });
  }, [current]);

  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    const n = items.length;
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
        if (e.nativeEvent.isComposing || e.altKey || e.ctrlKey || e.metaKey || e.shiftKey) {
          handled = false;
          break;
        }
        if (active !== undefined) onChoose(active);
        break;
      default:
        handled = false;
    }
    if (handled) e.preventDefault();
  };

  const available = items.filter((i) => i.available).length;
  let status = items.length === 0 ? t("shell.palette.noMatches") : t("shell.palette.count", { count: available });
  if (items.length > available) status = t("shell.palette.countUnavailable", { available: status, count: items.length - available });
  if (active !== undefined && !active.available) {
    status = t("shell.palette.reason", { status, command: t(active.def.labelKey), reason: active.reason });
  }

  return (
    <>
      <div className={styles.field}>
        <IconSearch size={14} />
        <input
          className={styles.input}
          type="text"
          role="combobox"
          aria-label={t("shell.palette.label")}
          aria-expanded="true"
          aria-controls={listId}
          aria-autocomplete="list"
          aria-activedescendant={current >= 0 ? optionId(current) : undefined}
          placeholder={t("shell.palette.placeholder")}
          spellCheck={false}
          autoComplete="off"
          value={query}
          onChange={(e) => {
            setQuery(e.target.value);
            setIndex(0);
          }}
          onKeyDown={onKeyDown}
          data-autofocus
        />
      </div>
      <ul ref={listRef} id={listId} role="listbox" aria-label={t("shell.palette.title")} className={styles.list}>
        {items.map((item, i) => {
          const { def } = item;
          const keys = shortcutChords(def, mac).map((c) => formatChord(c, mac));
          const label = t(def.labelKey);
          const where = scopeLabel(def.scope);
          const note = item.available ? where : item.reason;
          let optionLabel = label;
          if (keys.length > 0) {
            const joined = keys.reduce((a, b) => t("shell.palette.keysOr", { first: a, second: b }));
            optionLabel = t("shell.palette.optionKeys", { label: optionLabel, keys: joined });
          }
          if (note) optionLabel = t("shell.palette.optionNote", { label: optionLabel, note });
          return (
            <li
              key={def.id}
              id={optionId(i)}
              role="option"
              aria-selected={i === current}
              aria-disabled={item.available ? undefined : true}
              aria-label={optionLabel}
              className={styles.option}
              data-action-id={def.id}
              onMouseDown={(e) => e.preventDefault()}
              onMouseMove={() => {
                if (i !== current) setIndex(i);
              }}
              onClick={() => onChoose(item)}
            >
              <span className={styles.text}>
                <span className={styles.label}>
                  <Highlighted text={label} hits={item.hits} className={styles.hit} />
                </span>
                {note && <span className={styles.note}>{note}</span>}
              </span>
              <span className={styles.keys} aria-hidden="true">
                {keys.map((k) => (
                  <kbd key={k} className={styles.kbd}>
                    {k}
                  </kbd>
                ))}
              </span>
            </li>
          );
        })}
      </ul>
      {items.length === 0 && <div className={styles.empty}>{t("shell.palette.empty", { query: query.trim() })}</div>}
      <div className={styles.footer} aria-hidden="true">
        <span>
          <kbd className={styles.kbd}>↑</kbd> <kbd className={styles.kbd}>↓</kbd> {t("shell.keys.move")}
        </span>
        <span>
          <kbd className={styles.kbd}>{t("shell.keys.enter")}</kbd> {t("shell.keys.run")}
        </span>
        <span>
          <kbd className={styles.kbd}>{t("shell.keys.escape")}</kbd> {t("shell.keys.close")}
        </span>
      </div>
      <div className={styles.srOnly} role="status" aria-live="polite">
        {status}
      </div>
    </>
  );
}
