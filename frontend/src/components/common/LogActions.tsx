// Copy and save controls shared by the logs and the response timeline
// (DBG-07): the entry menu (Copy entry, Copy entry as JSON) opened by a
// right-click, Shift+F10 or the Menu key on a focused entry, or by its
// button; ⌘C / Ctrl+C on a focused entry; and the "Save…" button with its
// text and JSON choices. Copies and saves go through commands/logExport,
// which masks secrets in Go. See docs/frontend.md (Copying and saving logs).
import { useCallback, useRef, useState, type KeyboardEvent, type MouseEvent, type ReactNode } from "react";
import { copyLogText, inSentence, saveLog } from "../../commands/logExport";
import type { LogFormat } from "../../lib/logExport";
import { matchesShortcut, shortcutLabel } from "../../lib/shortcuts";
import { t } from "../../i18n";
import { useT } from "../../i18n/react";
import { ContextMenu, type ContextMenuItem } from "./ContextMenu";
import { IconCopy, IconDownload } from "./icons";

/** One entry of a log or timeline, formatted on demand. */
export interface LogEntryExport {
  /** The entry in full as text. */
  text: () => string;
  /** The entry in full as a JSON value. */
  json: () => unknown;
}

/** Copies an entry as text (the default) or JSON; secrets masked in Go. */
export function copyLogEntry(entry: LogEntryExport, format: LogFormat = "text"): Promise<boolean> {
  return format === "json" ? copyLogText(JSON.stringify(entry.json(), null, 2), "json") : copyLogText(entry.text());
}

/** The items of an entry's menu. */
export function entryMenuItems(entry: LogEntryExport): ContextMenuItem[] {
  return [
    { id: "copy", label: t("common.log.copyEntry"), shortcut: shortcutLabel("log.copyEntry"), onSelect: () => void copyLogEntry(entry) },
    { id: "copyJson", label: t("common.log.copyEntryJson"), onSelect: () => void copyLogEntry(entry, "json") },
  ];
}

interface OpenMenu {
  x: number;
  y: number;
  items: readonly ContextMenuItem[];
  label: string;
}

/** Whether text is selected in the page: ⌘C then copies the selection, as usual. */
function hasSelection(): boolean {
  const sel = typeof window === "undefined" ? null : window.getSelection();
  return sel !== null && !sel.isCollapsed && sel.toString() !== "";
}

/**
 * The entry menu of a list: `menu` renders it while open; `onKeyDown` takes
 * ⌘C / Ctrl+C (copy) and Shift+F10 / Menu (open under the entry) on a
 * focused entry, and `onContextMenu` a right-click. Both return or take
 * the entry to act on.
 */
export function useEntryMenu(label?: string) {
  const t = useT();
  const menuLabel = label ?? t("common.log.entry");
  const [open, setOpen] = useState<OpenMenu | null>(null);

  const openAt = useCallback(
    (x: number, y: number, entry: LogEntryExport) => setOpen({ x, y, items: entryMenuItems(entry), label: menuLabel }),
    [menuLabel],
  );

  const openBelow = useCallback(
    (el: Element, entry: LogEntryExport) => {
      const r = el.getBoundingClientRect();
      openAt(r.left + 8, r.bottom, entry);
    },
    [openAt],
  );

  /** Handles the entry keys; true when it took the key press. */
  const onKeyDown = useCallback(
    (e: KeyboardEvent<Element>, entry: LogEntryExport | undefined): boolean => {
      if (entry === undefined || e.defaultPrevented) return false;
      if (matchesShortcut(e, "log.copyEntry")) {
        if (hasSelection()) return false;
        e.preventDefault();
        e.stopPropagation();
        void copyLogEntry(entry);
        return true;
      }
      if (matchesShortcut(e, "log.menu")) {
        e.preventDefault();
        e.stopPropagation();
        // Under the focused entry, which may be inside the element listening.
        openBelow(e.target instanceof Element ? e.target : e.currentTarget, entry);
        return true;
      }
      return false;
    },
    [openBelow],
  );

  const onContextMenu = useCallback(
    (e: MouseEvent<Element>, entry: LogEntryExport) => {
      e.preventDefault();
      openAt(e.clientX, e.clientY, entry);
    },
    [openAt],
  );

  const menu = open && <ContextMenu label={open.label} x={open.x} y={open.y} items={open.items} onClose={() => setOpen(null)} />;
  return { menu, onKeyDown, onContextMenu, openBelow };
}

export interface EntryMenuButtonProps {
  entry: LogEntryExport;
  /** Accessible name: "Copy redirect 1 of 2". */
  label: string;
  className?: string;
}

/** A copy button that opens an entry's menu (Copy entry, Copy entry as JSON). */
export function EntryMenuButton({ entry, label, className }: EntryMenuButtonProps) {
  const { menu, openBelow } = useEntryMenu();
  return (
    <>
      <button
        type="button"
        className={className}
        aria-label={label}
        title={label}
        aria-haspopup="menu"
        onClick={(e) => openBelow(e.currentTarget, entry)}
      >
        <IconCopy size={13} />
      </button>
      {menu}
    </>
  );
}

export interface SaveLogButtonProps {
  /** Names the log: "Console", "Timeline". */
  title: string;
  /** Suggested file name without extension. */
  fileName: string;
  /** Builds the whole log in the chosen format. */
  build: (format: LogFormat) => string;
  disabled?: boolean;
  className?: string;
  /** Button content. Default: a download icon and "Save…". */
  children?: ReactNode;
  /** Accessible name and tooltip. Default "Save the <title lower case>…". */
  label?: string;
}

/** "Save…": a menu of Save as text… and Save as JSON…, then the native save dialog. */
export function SaveLogButton({ title, fileName, build, disabled, className, children, label }: SaveLogButtonProps) {
  const t = useT();
  const [menu, setMenu] = useState<{ x: number; y: number } | null>(null);
  const busy = useRef(false);
  const name = label ?? t("common.log.saveThe", { what: inSentence(title) });
  const save = (format: LogFormat) => {
    if (busy.current) return;
    busy.current = true;
    void saveLog({ title, fileName, format, build }).finally(() => {
      busy.current = false;
    });
  };
  const items: ContextMenuItem[] = [
    { id: "text", label: t("common.log.saveText"), onSelect: () => save("text") },
    { id: "json", label: t("common.log.saveJson"), onSelect: () => save("json") },
  ];
  return (
    <>
      <button
        type="button"
        className={className}
        aria-label={children === undefined ? name : undefined}
        title={name}
        aria-haspopup="menu"
        aria-expanded={menu !== null}
        disabled={disabled}
        onClick={(e) => {
          const r = e.currentTarget.getBoundingClientRect();
          setMenu({ x: r.left, y: r.bottom + 2 });
        }}
      >
        {children ?? <IconDownload size={14} />}
      </button>
      {menu && <ContextMenu label={name} x={menu.x} y={menu.y} items={items} onClose={() => setMenu(null)} />}
    </>
  );
}
