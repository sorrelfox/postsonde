import { Fragment, useEffect, useLayoutEffect, useRef, useState, type KeyboardEvent } from "react";
import { createPortal } from "react-dom";
import styles from "./ContextMenu.module.css";
import { IconCheck } from "./icons";

export interface ContextMenuItem {
  /** Stable key; also `data-menu-item` on the element. */
  readonly id: string;
  readonly label: string;
  readonly disabled?: boolean;
  /** Shown right-aligned, e.g. "⌘W". Display only: the menu does not bind it. */
  readonly shortcut?: string;
  /** Draws a separator line above the item, to group items. */
  readonly separatorBefore?: boolean;
  /** Tooltip, e.g. the full path of a project. */
  readonly title?: string;
  /**
   * Makes it one of a group of choices (`menuitemradio`): true shows a
   * check mark before the label. Items without it are plain `menuitem`s.
   */
  readonly checked?: boolean;
  readonly onSelect: () => void;
}

export interface ContextMenuProps {
  /** Accessible name of the menu. */
  label: string;
  /** Viewport coordinates of the top-left corner; the menu is moved back inside the window if needed. */
  x: number;
  y: number;
  items: readonly ContextMenuItem[];
  /** Called when the menu should close: an item was chosen, Escape or Tab, a pointer down outside, blur, resize. */
  onClose: () => void;
}

/** Keeps a menu of this size inside the viewport, with a small margin. */
const EDGE = 4;

/**
 * A small popup menu at a point (reused by the tree's context menu):
 * WAI-ARIA menu pattern with `role="menu"` and `menuitem`s.
 * Rendered in a portal on `document.body`, so no overflow clips it.
 *
 * Keyboard: the first enabled item gets focus when it opens; Up/Down move
 * (wrapping), Home/End jump, Enter or Space choose, Escape and Tab close.
 * Typing a letter moves to the next item starting with it. When it closes,
 * focus returns to the element that had it before it opened.
 */
export function ContextMenu({ label, x, y, items, onClose }: ContextMenuProps) {
  const menuRef = useRef<HTMLDivElement>(null);
  const itemRefs = useRef<(HTMLDivElement | null)[]>([]);
  const [position, setPosition] = useState({ left: x, top: y });
  const [active, setActive] = useState(() => items.findIndex((item) => item.disabled !== true));
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;

  // Clamp into the viewport once the menu has a size.
  useLayoutEffect(() => {
    const el = menuRef.current;
    if (!el) return;
    const rect = el.getBoundingClientRect();
    const left = Math.max(EDGE, Math.min(x, window.innerWidth - rect.width - EDGE));
    const top = Math.max(EDGE, Math.min(y, window.innerHeight - rect.height - EDGE));
    setPosition((p) => (p.left === left && p.top === top ? p : { left, top }));
  }, [x, y]);

  // Remember what had focus, and give it back on close.
  useEffect(() => {
    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    return () => {
      const current = document.activeElement;
      const lost = current === null || current === document.body || menuRef.current?.contains(current) === true;
      if (lost && previous?.isConnected === true) previous.focus({ preventScroll: true });
    };
  }, []);

  useEffect(() => {
    if (active >= 0) itemRefs.current[active]?.focus({ preventScroll: true });
    else menuRef.current?.focus({ preventScroll: true });
  }, [active]);

  useEffect(() => {
    const close = () => onCloseRef.current();
    const onPointerDown = (e: PointerEvent) => {
      if (!menuRef.current?.contains(e.target as Node)) close();
    };
    document.addEventListener("pointerdown", onPointerDown, true);
    window.addEventListener("blur", close);
    window.addEventListener("resize", close);
    return () => {
      document.removeEventListener("pointerdown", onPointerDown, true);
      window.removeEventListener("blur", close);
      window.removeEventListener("resize", close);
    };
  }, []);

  const enabled = items.map((item, i) => (item.disabled === true ? -1 : i)).filter((i) => i >= 0);

  const move = (delta: number) => {
    if (enabled.length === 0) return;
    const at = enabled.indexOf(active);
    const next = at === -1 ? (delta > 0 ? 0 : enabled.length - 1) : (at + delta + enabled.length) % enabled.length;
    setActive(enabled[next]);
  };

  const choose = (index: number) => {
    const item = items[index];
    if (item === undefined || item.disabled === true) return;
    onClose();
    item.onSelect();
  };

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    let handled = true;
    switch (e.key) {
      case "ArrowDown":
        move(1);
        break;
      case "ArrowUp":
        move(-1);
        break;
      case "Home":
        if (enabled.length > 0) setActive(enabled[0]);
        break;
      case "End":
        if (enabled.length > 0) setActive(enabled[enabled.length - 1]);
        break;
      case "Enter":
      case " ":
        choose(active);
        break;
      case "Escape":
        onClose();
        break;
      case "Tab":
        onClose();
        break;
      default: {
        handled = false;
        if (e.key.length === 1 && !e.altKey && !e.ctrlKey && !e.metaKey && e.key.trim() !== "") {
          const letter = e.key.toLowerCase();
          const start = enabled.indexOf(active);
          for (let k = 1; k <= enabled.length; k++) {
            const i = enabled[(start + k + enabled.length) % enabled.length];
            if (items[i].label.toLowerCase().startsWith(letter)) {
              setActive(i);
              break;
            }
          }
          handled = true;
        }
      }
    }
    if (handled) {
      e.preventDefault();
      e.stopPropagation();
    }
  };

  return createPortal(
    <div
      ref={menuRef}
      className={styles.menu}
      role="menu"
      aria-label={label}
      aria-orientation="vertical"
      tabIndex={-1}
      style={{ left: position.left, top: position.top }}
      onKeyDown={onKeyDown}
      onContextMenu={(e) => e.preventDefault()}
    >
      {items.map((item, i) => (
        <Fragment key={item.id}>
          {item.separatorBefore === true && i > 0 && <div role="separator" className={styles.separator} />}
          <div
            ref={(el) => {
              itemRefs.current[i] = el;
            }}
            className={styles.item}
            role={item.checked === undefined ? "menuitem" : "menuitemradio"}
            aria-checked={item.checked}
            tabIndex={i === active ? 0 : -1}
            aria-disabled={item.disabled === true || undefined}
            data-menu-item={item.id}
            title={item.title}
            onClick={() => choose(i)}
            onPointerMove={() => {
              if (item.disabled !== true && i !== active) setActive(i);
            }}
          >
            {item.checked !== undefined && (
              <span className={styles.check} aria-hidden="true">
                {item.checked && <IconCheck size={12} />}
              </span>
            )}
            <span className={styles.label}>{item.label}</span>
            {item.shortcut !== undefined && <span className={styles.shortcut}>{item.shortcut}</span>}
          </div>
        </Fragment>
      ))}
    </div>,
    document.body,
  );
}
