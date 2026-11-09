import {
  useCallback,
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
  type KeyboardEvent,
  type ReactNode,
} from "react";
import { createPortal } from "react-dom";
import type { Suggestion } from "../../lib/suggest";
import { IconInfo } from "./icons";
import styles from "./Autocomplete.module.css";

/** Where the popup belongs: the text field typed into and the box it is placed under. */
export interface AutocompleteTarget {
  /** A key naming the field across renders, e.g. "row-id:key". */
  owner: string;
  input: HTMLInputElement;
  /** The popup is placed under (or above) this element and at least as wide. Default: the input. */
  anchor?: HTMLElement;
}

export interface AutocompleteOptions {
  /** Accessible name of the list, e.g. "Header names". */
  label: string;
  /** Called with the chosen suggestion and the field it was shown for. The popup is already closed. */
  onAccept: (suggestion: Suggestion, input: HTMLInputElement) => void;
}

export interface Autocomplete {
  /**
   * Shows `items` for the field, with the first highlighted, or closes the
   * popup when there is nothing worth showing: no items, or only one that
   * equals the field's text.
   */
  show: (target: AutocompleteTarget, items: readonly Suggestion[]) => void;
  close: () => void;
  /** Whether the popup is open for the field `owner`. */
  isOpenFor: (owner: string) => boolean;
  /**
   * Handles a key press in the field while its popup is open: Up/Down move
   * the highlight (wrapping), Enter accepts it, Escape closes, Tab closes
   * and lets focus move. Returns true when the key was used (the event is
   * then prevented); anything else is left to the caller.
   */
  onKeyDown: (e: KeyboardEvent<HTMLElement>) => boolean;
  /** ARIA combobox attributes for the field `owner`, which can show suggestions. */
  comboboxProps: (owner: string) => ComboboxProps;
  /** The popup (a portal on document.body), or null. Render it anywhere. */
  popup: ReactNode;
}

export interface ComboboxProps {
  role: "combobox";
  "aria-autocomplete": "list";
  "aria-expanded": boolean;
  "aria-controls"?: string;
  "aria-activedescendant"?: string;
}

interface OpenState {
  owner: string;
  input: HTMLInputElement;
  anchor: HTMLElement;
  items: readonly Suggestion[];
  active: number;
}

/** Gap between the anchor and the popup, and the smallest distance to the window's edge. */
const GAP = 2;
const EDGE = 4;

/**
 * An autocomplete popup for a text field: the WAI-ARIA combobox
 * pattern with a listbox popup. The field keeps focus the whole time
 * (aria-activedescendant points at the highlighted option); the caller
 * decides when to show suggestions and what accepting one does.
 *
 * The popup is a portal on document.body placed under the anchor, or above
 * it when there is no room below. It closes on Escape, Tab, when the field
 * loses focus, on window resize and on scrolling outside it. Clicking an
 * option accepts it without taking focus from the field.
 */
export function useAutocomplete({ label, onAccept }: AutocompleteOptions): Autocomplete {
  const listId = useId();
  const [open, setOpen] = useState<OpenState | null>(null);
  const openRef = useRef(open);
  openRef.current = open;
  const onAcceptRef = useRef(onAccept);
  onAcceptRef.current = onAccept;

  const popupRef = useRef<HTMLDivElement>(null);
  const close = useCallback(() => setOpen(null), []);

  const show = useCallback((target: AutocompleteTarget, items: readonly Suggestion[]) => {
    const text = target.input.value;
    if (items.length === 0 || (items.length === 1 && items[0]?.value === text)) {
      setOpen(null);
      return;
    }
    setOpen({ owner: target.owner, input: target.input, anchor: target.anchor ?? target.input, items, active: 0 });
  }, []);

  const accept = useCallback((index: number) => {
    const state = openRef.current;
    const item = state?.items[index];
    if (state === null || item === undefined) return;
    setOpen(null);
    onAcceptRef.current(item, state.input);
  }, []);

  const onKeyDown = useCallback(
    (e: KeyboardEvent<HTMLElement>): boolean => {
      const state = openRef.current;
      if (state === null || e.target !== state.input) return false;
      if (e.altKey || e.ctrlKey || e.metaKey || e.nativeEvent.isComposing) {
        return false;
      }
      const n = state.items.length;
      switch (e.key) {
        case "ArrowDown":
          setOpen({ ...state, active: (state.active + 1) % n });
          break;
        case "ArrowUp":
          setOpen({ ...state, active: (state.active - 1 + n) % n });
          break;
        case "Enter":
          if (e.shiftKey) return false;
          accept(state.active);
          break;
        case "Escape":
          setOpen(null);
          e.stopPropagation();
          break;
        case "Tab":
          setOpen(null);
          return false;
        default:
          return false;
      }
      e.preventDefault();
      return true;
    },
    [accept],
  );

  // Close when the field goes away, loses focus, or the layout moves under the popup.
  useEffect(() => {
    if (open === null) return;
    const { input } = open;
    if (!input.isConnected) {
      setOpen(null);
      return;
    }
    const onBlur = () => setOpen(null);
    const onResize = () => setOpen(null);
    const onScroll = (e: Event) => {
      if (!(e.target instanceof Node && popupRef.current?.contains(e.target))) setOpen(null);
    };
    input.addEventListener("blur", onBlur);
    window.addEventListener("resize", onResize);
    document.addEventListener("scroll", onScroll, true);
    return () => {
      input.removeEventListener("blur", onBlur);
      window.removeEventListener("resize", onResize);
      document.removeEventListener("scroll", onScroll, true);
    };
  }, [open]);

  const [placement, setPlacement] = useState<{ left: number; top: number; minWidth: number } | null>(null);

  useLayoutEffect(() => {
    const el = popupRef.current;
    if (open === null || el === null) {
      setPlacement(null);
      return;
    }
    const a = open.anchor.getBoundingClientRect();
    const height = el.getBoundingClientRect().height;
    const width = Math.max(el.getBoundingClientRect().width, a.width);
    const below = a.bottom + GAP;
    const above = a.top - GAP - height;
    const top = below + height > window.innerHeight - EDGE && above >= EDGE ? above : below;
    const left = Math.max(EDGE, Math.min(a.left, window.innerWidth - width - EDGE));
    setPlacement((p) =>
      p !== null && p.left === left && p.top === top && p.minWidth === a.width ? p : { left, top, minWidth: a.width },
    );
  }, [open]);

  const optionId = (i: number) => `${listId}-option-${i}`;

  const comboboxProps = (owner: string): ComboboxProps => {
    const expanded = open !== null && open.owner === owner;
    return {
      role: "combobox",
      "aria-autocomplete": "list",
      "aria-expanded": expanded,
      ...(expanded && { "aria-controls": listId, "aria-activedescendant": optionId(open.active) }),
    };
  };

  const isOpenFor = (owner: string) => open !== null && open.owner === owner;

  const activeNote = open?.items[open.active]?.note;

  const popup =
    open === null
      ? null
      : createPortal(
          <div
            ref={popupRef}
            className={styles.popup}
            style={
              placement === null
                ? { left: 0, top: 0, visibility: "hidden" }
                : { left: placement.left, top: placement.top, minWidth: placement.minWidth }
            }
            data-autocomplete-popup=""
            // Keep focus in the field: a click on an option must not blur it.
            onMouseDown={(e) => e.preventDefault()}
          >
            <div id={listId} role="listbox" aria-label={label} className={styles.list}>
              {open.items.map((item, i) => (
                <div
                  key={`${i}:${item.value}`}
                  id={optionId(i)}
                  role="option"
                  aria-selected={i === open.active}
                  className={styles.option}
                  onClick={() => accept(i)}
                  onPointerMove={() => {
                    if (i !== openRef.current?.active) setOpen((s) => (s === null ? s : { ...s, active: i }));
                  }}
                >
                  <span className={styles.label}>{item.label ?? item.value}</span>
                  {item.detail !== undefined && <span className={styles.detail}>{item.detail}</span>}
                  {item.note !== undefined && (
                    <span className={styles.noteMark} title={item.note}>
                      <IconInfo size={12} />
                      <span className={styles.srOnly}>{item.note}</span>
                    </span>
                  )}
                </div>
              ))}
            </div>
            {activeNote !== undefined && (
              <div className={styles.note} aria-hidden="true">
                {activeNote}
              </div>
            )}
          </div>,
          document.body,
        );

  return { show, close, isOpenFor, onKeyDown, comboboxProps, popup };
}
