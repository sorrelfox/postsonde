import {
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
  type KeyboardEvent,
  type PointerEvent,
  type ReactNode,
} from "react";
import { useT } from "../../i18n/react";
import styles from "./SplitPane.module.css";

export type SplitOrientation = "horizontal" | "vertical";

export interface SplitPaneProps {
  /** "horizontal": panes side by side with a vertical handle; "vertical": stacked. Default horizontal. */
  orientation?: SplitOrientation;
  /** Share of the space taken by the first pane, 0–1. Shown clamped to the limits below. */
  ratio: number;
  /** Called on every change: once per animation frame while dragging, on each key press, on reset. */
  onRatioChange?: (ratio: number) => void;
  /** Called once a change is final: at the end of a drag, on each key press, on reset. Save here. */
  onRatioCommit?: (ratio: number) => void;
  /** Minimum size of the first and second pane, in px. Default 0. */
  minStart?: number;
  minEnd?: number;
  /** Ratio limits on top of the px minimums. Default 0 and 1. */
  minRatio?: number;
  maxRatio?: number;
  /** Ratio restored by Enter or a double-click on the handle. Default 0.5. */
  defaultRatio?: number;
  /** Accessible name of the handle. */
  label?: string;
  className?: string;
  /** The first and second pane. */
  children: readonly [ReactNode, ReactNode];
}

/** Keyboard steps, as a share of the space. */
export const SPLIT_KEY_STEP = 0.02;
export const SPLIT_KEY_STEP_LARGE = 0.1;

interface Drag {
  pointerId: number;
  startPos: number;
  startRatio: number;
  avail: number;
  lo: number;
  hi: number;
  ratio: number;
  /** Pending requestAnimationFrame id, 0 when none. */
  frame: number;
}

const clamp = (value: number, lo: number, hi: number) => Math.min(hi, Math.max(lo, value));
// Keeps keyboard steps free of float drift (0.52, not 0.5200000000000001).
const round = (value: number) => Math.round(value * 10_000) / 10_000;
const percent = (ratio: number) => String(Math.round(ratio * 100));

/**
 * The ratio range that keeps both panes at or above their minimum size in
 * `avail` px. When both minimums don't fit, the space is shared in
 * proportion to them. `avail` 0 (not measured yet) applies only the ratio limits.
 */
function splitBounds(
  avail: number,
  minStart: number,
  minEnd: number,
  minRatio: number,
  maxRatio: number,
): [number, number] {
  if (avail <= 0) return [minRatio, maxRatio];
  const lo = Math.max(minRatio, minStart / avail);
  const hi = Math.min(maxRatio, 1 - minEnd / avail);
  if (lo <= hi) return [lo, hi];
  const total = minStart + minEnd;
  const shared = total > 0 ? minStart / total : 0.5;
  return [shared, shared];
}

/**
 * Two panes and a draggable, focusable separator between them.
 *
 * Controlled: the parent owns `ratio`. While a drag is in progress the pane
 * sizes come from a CSS variable written once per animation frame, so
 * neither this component nor the pane contents re-render per pointer move;
 * `onRatioCommit` fires once, on pointerup. The px minimums are enforced
 * against the container's measured size, also when the window resizes.
 */
export function SplitPane({
  orientation = "horizontal",
  ratio,
  onRatioChange,
  onRatioCommit,
  minStart = 0,
  minEnd = 0,
  minRatio = 0,
  maxRatio = 1,
  defaultRatio = 0.5,
  label,
  className,
  children,
}: SplitPaneProps) {
  const t = useT();
  const horizontal = orientation === "horizontal";
  const containerRef = useRef<HTMLDivElement>(null);
  const handleRef = useRef<HTMLDivElement>(null);
  const drag = useRef<Drag | null>(null);
  const startId = useId();
  // Space the two panes share (container minus handle), in px. 0 until measured.
  const [avail, setAvail] = useState(0);
  const [dragging, setDragging] = useState(false);

  const [lo, hi] = splitBounds(avail, minStart, minEnd, minRatio, maxRatio);
  const shown = clamp(ratio, lo, hi);

  // Latest props for the ResizeObserver callback, which outlives renders.
  const latest = useRef({ ratio, minStart, minEnd, minRatio, maxRatio });
  latest.current = { ratio, minStart, minEnd, minRatio, maxRatio };

  // The pane sizes and aria-valuenow are written here, not in JSX, so a
  // drag can update them per frame without React. Outside a drag every
  // render re-applies the ratio from props.
  const paint = (value: number) => {
    containerRef.current?.style.setProperty("--split-ratio", String(value));
    handleRef.current?.setAttribute("aria-valuenow", percent(value));
  };

  useLayoutEffect(() => {
    if (!drag.current) paint(shown);
  });

  useLayoutEffect(() => {
    const container = containerRef.current;
    const handle = handleRef.current;
    if (!container || !handle) return;
    const size = (el: Element) => {
      const rect = el.getBoundingClientRect();
      return horizontal ? rect.width : rect.height;
    };
    const measure = () => {
      const next = Math.max(0, size(container) - size(handle));
      setAvail(next);
      // Re-clamp in the same frame as the resize, before React re-renders.
      if (!drag.current) {
        const p = latest.current;
        const [l, h] = splitBounds(next, p.minStart, p.minEnd, p.minRatio, p.maxRatio);
        paint(clamp(p.ratio, l, h));
      }
    };
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(container);
    return () => observer.disconnect();
  }, [horizontal]);

  // Unmounting mid-drag must not leave a frame callback behind.
  useEffect(
    () => () => {
      if (drag.current?.frame) cancelAnimationFrame(drag.current.frame);
      drag.current = null;
    },
    [],
  );

  const change = (next: number) => {
    const value = clamp(round(next), lo, hi);
    if (value === shown) return;
    onRatioChange?.(value);
    onRatioCommit?.(value);
  };

  const onPointerDown = (e: PointerEvent<HTMLDivElement>) => {
    if (e.button !== 0 || drag.current || !containerRef.current || !handleRef.current) return;
    const containerRect = containerRef.current.getBoundingClientRect();
    const handleRect = handleRef.current.getBoundingClientRect();
    const space = horizontal ? containerRect.width - handleRect.width : containerRect.height - handleRect.height;
    if (space <= 0) return;
    // Keeps focus on the handle but stops the browser starting a text selection.
    e.preventDefault();
    e.currentTarget.focus({ preventScroll: true });
    e.currentTarget.setPointerCapture?.(e.pointerId);
    const [l, h] = splitBounds(space, minStart, minEnd, minRatio, maxRatio);
    const start = clamp(ratio, l, h);
    drag.current = {
      pointerId: e.pointerId,
      startPos: horizontal ? e.clientX : e.clientY,
      startRatio: start,
      avail: space,
      lo: l,
      hi: h,
      ratio: start,
      frame: 0,
    };
    setDragging(true);
  };

  const onPointerMove = (e: PointerEvent<HTMLDivElement>) => {
    const d = drag.current;
    if (!d || e.pointerId !== d.pointerId) return;
    const delta = (horizontal ? e.clientX : e.clientY) - d.startPos;
    d.ratio = clamp(round(d.startRatio + delta / d.avail), d.lo, d.hi);
    if (d.frame !== 0) return;
    d.frame = requestAnimationFrame(() => {
      d.frame = 0;
      paint(d.ratio);
      onRatioChange?.(d.ratio);
    });
  };

  const endDrag = (commit: boolean) => {
    const d = drag.current;
    if (!d) return;
    drag.current = null;
    const framePending = d.frame !== 0;
    if (framePending) cancelAnimationFrame(d.frame);
    // Re-rendering with dragging=false re-applies the ratio from props.
    setDragging(false);
    if (!commit || d.ratio === d.startRatio) {
      if (!commit && d.ratio !== d.startRatio) onRatioChange?.(d.startRatio);
      return;
    }
    if (framePending) onRatioChange?.(d.ratio);
    onRatioCommit?.(d.ratio);
  };

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if (drag.current || e.altKey || e.ctrlKey || e.metaKey) return;
    const step = e.shiftKey ? SPLIT_KEY_STEP_LARGE : SPLIT_KEY_STEP;
    const back = horizontal ? "ArrowLeft" : "ArrowUp";
    const forward = horizontal ? "ArrowRight" : "ArrowDown";
    let next: number;
    switch (e.key) {
      case back:
        next = shown - step;
        break;
      case forward:
        next = shown + step;
        break;
      case "Home":
        next = lo;
        break;
      case "End":
        next = hi;
        break;
      case "Enter":
        next = defaultRatio;
        break;
      default:
        return;
    }
    e.preventDefault();
    change(next);
  };

  const classes = [styles.split, horizontal ? styles.horizontal : styles.vertical, className]
    .filter(Boolean)
    .join(" ");

  return (
    <div ref={containerRef} className={classes} data-split-pane="" data-dragging={dragging || undefined}>
      <div id={startId} className={`${styles.pane} ${styles.start}`}>
        {children[0]}
      </div>
      <div
        ref={handleRef}
        className={styles.handle}
        role="separator"
        tabIndex={0}
        aria-label={label ?? t("common.splitPane.resize")}
        aria-orientation={horizontal ? "vertical" : "horizontal"}
        aria-controls={startId}
        aria-valuemin={Number(percent(lo))}
        aria-valuemax={Number(percent(hi))}
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={() => endDrag(true)}
        onPointerCancel={() => endDrag(false)}
        onLostPointerCapture={() => endDrag(true)}
        onKeyDown={onKeyDown}
        onDoubleClick={() => change(defaultRatio)}
      />
      <div className={`${styles.pane} ${styles.end}`}>{children[1]}</div>
    </div>
  );
}
