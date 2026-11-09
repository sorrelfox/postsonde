import { cloneElement, useEffect, useId, useState, type FocusEvent, type MouseEvent, type ReactElement } from "react";
import { createPortal } from "react-dom";
import styles from "./Tooltip.module.css";

type TriggerProps = {
  "aria-describedby"?: string;
  onMouseEnter?: (e: MouseEvent<HTMLElement>) => void;
  onMouseLeave?: (e: MouseEvent<HTMLElement>) => void;
  onFocus?: (e: FocusEvent<HTMLElement>) => void;
  onBlur?: (e: FocusEvent<HTMLElement>) => void;
};

export interface TooltipProps {
  /** The tooltip's text; line breaks are kept. */
  text: string;
  /** Where it shows: under the trigger (default) or above it (for the status bar). */
  placement?: "bottom" | "top";
  /** The trigger: one element that takes focus (tabIndex 0 on a non-control), given the handlers and aria-describedby. */
  children: ReactElement<TriggerProps>;
}

/**
 * A tooltip that keyboard users get too (UI-11): it shows while the pointer
 * is over the trigger or the trigger has focus, and Escape hides it. The
 * text describes the trigger (aria-describedby) whether it shows or not,
 * so screen readers read it on focus. It is rendered in a portal on
 * document.body, so a clipping parent (the status bar) does not cut it off
 * and it is not part of its trigger's parent's text (a label or heading).
 * Use it instead of a `title` for information a keyboard user needs; see
 * docs/frontend.md#accessibility.
 */
export function Tooltip({ text, placement = "bottom", children }: TooltipProps) {
  const id = useId();
  const [at, setAt] = useState<{ left: number; top: number } | null>(null);

  // Dismissable without moving focus or the pointer (WCAG 1.4.13).
  useEffect(() => {
    if (at === null) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setAt(null);
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [at]);

  const show = (el: HTMLElement) => {
    const rect = el.getBoundingClientRect();
    setAt({ left: rect.left, top: placement === "top" ? rect.top : rect.bottom });
  };
  const hide = () => setAt(null);
  const own = children.props;
  const trigger = cloneElement(children, {
    "aria-describedby": [own["aria-describedby"], id].filter(Boolean).join(" "),
    onMouseEnter: (e: MouseEvent<HTMLElement>) => {
      own.onMouseEnter?.(e);
      show(e.currentTarget);
    },
    onMouseLeave: (e: MouseEvent<HTMLElement>) => {
      own.onMouseLeave?.(e);
      hide();
    },
    onFocus: (e: FocusEvent<HTMLElement>) => {
      own.onFocus?.(e);
      show(e.currentTarget);
    },
    onBlur: (e: FocusEvent<HTMLElement>) => {
      own.onBlur?.(e);
      hide();
    },
  });
  return (
    <>
      {trigger}
      {createPortal(
        <span
          id={id}
          role="tooltip"
          className={styles.tooltip}
          data-placement={placement}
          hidden={at === null}
          style={at === null ? undefined : { left: at.left, top: at.top }}
        >
          {text}
        </span>,
        document.body,
      )}
    </>
  );
}
