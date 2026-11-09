// Windowing for long lists of fixed-height rows, shared by the collection
// tree and the message log. No library: rows are absolutely
// positioned at index × rowHeight inside a sizer as tall as all rows, and
// only the rows in the scroll window (plus overscan and pinned rows) are
// rendered. See docs/frontend.md (Collection tree, Message log).
import { useCallback, useLayoutEffect, useReducer, useRef, useState, type UIEvent } from "react";

export interface RowRange {
  /** First rendered index (inclusive). */
  first: number;
  /** Last rendered index (exclusive). */
  last: number;
}

/** The rows in view at `scrollTop`, plus `overscan` rows above and below, clamped to `count`. */
export function visibleRange(scrollTop: number, height: number, rowHeight: number, count: number, overscan: number): RowRange {
  const first = Math.max(0, Math.floor(scrollTop / rowHeight) - overscan);
  const last = Math.max(first, Math.min(count, Math.ceil((scrollTop + height) / rowHeight) + overscan));
  return { first: Math.min(first, count), last };
}

/**
 * The indices to render: the range, plus each pinned index outside it (a
 * focused row, so focus survives scrolling), in ascending order.
 */
export function renderedIndices(range: RowRange, count: number, pinned: readonly number[] = []): number[] {
  const before: number[] = [];
  const after: number[] = [];
  for (const p of pinned) {
    if (p < 0 || p >= count) continue;
    if (p < range.first && !before.includes(p)) before.push(p);
    else if (p >= range.last && !after.includes(p)) after.push(p);
  }
  before.sort((a, b) => a - b);
  after.sort((a, b) => a - b);
  const out = before;
  for (let i = range.first; i < range.last; i++) out.push(i);
  return out.concat(after);
}

export interface VirtualRowsOptions {
  count: number;
  rowHeight: number;
  /** Rows rendered above and below the window. Default 10. */
  overscan?: number;
  /** Viewport height used until the container is measured (and in jsdom, where it is 0). Default 600. */
  fallbackViewport?: number;
  /** Indices rendered even outside the window. */
  pinned?: readonly number[];
  /**
   * Keep the list scrolled to its end: renders the last rows and scrolls the
   * container there before paint (a live log following new rows).
   */
  followEnd?: boolean;
}

export type ScrollAlign = "nearest" | "center" | "end";

export interface VirtualRows {
  /** Callback ref for the scroll container. */
  ref: (el: HTMLDivElement | null) => void;
  /** The scroll container, once mounted. */
  element: () => HTMLDivElement | null;
  /**
   * The container's scroll handler. Returns false when the event only reports
   * a scroll this hook made itself (scrollTo, scrollToIndex, followEnd), true
   * for a scroll by the user.
   */
  onScroll: (e: UIEvent<HTMLDivElement>) => boolean;
  /** Measured viewport height, or the fallback. */
  height: number;
  /** The scroll offset the render used. */
  scrollTop: number;
  range: RowRange;
  /** Indices to render, ascending. */
  rendered: number[];
  /** Scrolls so row `index` is in view, synchronously (the next render includes it). */
  scrollToIndex: (index: number, align?: ScrollAlign) => void;
  /** Scrolls to an offset, clamped to 0. */
  scrollTo: (top: number) => void;
}

/**
 * Tracks the scroll offset and viewport of a scroll container and returns
 * the rows to render. Scrolling re-renders only when the rendered range
 * changes, not on every scroll event.
 */
export function useVirtualRows({
  count,
  rowHeight,
  overscan = 10,
  fallbackViewport = 600,
  pinned,
  followEnd = false,
}: VirtualRowsOptions): VirtualRows {
  const elRef = useRef<HTMLDivElement | null>(null);
  const [el, setEl] = useState<HTMLDivElement | null>(null);
  const [viewport, setViewport] = useState(0);
  const [, rerender] = useReducer((n: number) => n + 1, 0);
  const scrollTopRef = useRef(0);
  /** The offset of the last scroll this hook made, until its scroll event arrives. */
  const ownScroll = useRef<number | null>(null);

  const height = viewport > 0 ? viewport : fallbackViewport;
  const endTop = Math.max(0, count * rowHeight - height);
  const scrollTop = followEnd ? endTop : scrollTopRef.current;
  const range = visibleRange(scrollTop, height, rowHeight, count, overscan);

  // What the last render used, for the scroll handler to compare against.
  const latest = useRef({ range, height, count, rowHeight, overscan });
  latest.current = { range, height, count, rowHeight, overscan };

  const ref = useCallback((node: HTMLDivElement | null) => {
    elRef.current = node;
    setEl(node);
  }, []);

  useLayoutEffect(() => {
    if (!el) return;
    const measure = () => setViewport(el.clientHeight);
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(el);
    return () => observer.disconnect();
  }, [el]);

  useLayoutEffect(() => {
    if (!followEnd) return;
    scrollTopRef.current = endTop;
    const node = elRef.current;
    if (node && node.scrollTop !== endTop) {
      node.scrollTop = endTop;
      ownScroll.current = node.scrollTop;
    }
  });

  const update = useCallback((top: number, force: boolean) => {
    scrollTopRef.current = top;
    const l = latest.current;
    const next = visibleRange(top, l.height, l.rowHeight, l.count, l.overscan);
    if (force || next.first !== l.range.first || next.last !== l.range.last) rerender();
  }, []);

  const onScroll = useCallback(
    (e: UIEvent<HTMLDivElement>) => {
      const top = e.currentTarget.scrollTop;
      const own = ownScroll.current !== null && Math.abs(ownScroll.current - top) < 1;
      ownScroll.current = null;
      update(top, false);
      return !own;
    },
    [update],
  );

  const scrollTo = useCallback(
    (top: number) => {
      const next = Math.max(0, top);
      const node = elRef.current;
      if (node && node.scrollTop !== next) {
        node.scrollTop = next;
        ownScroll.current = node.scrollTop;
      }
      update(next, true);
    },
    [update],
  );

  const scrollToIndex = useCallback(
    (index: number, align: ScrollAlign = "nearest") => {
      const { height: h, rowHeight: rh } = latest.current;
      const top = index * rh;
      const current = elRef.current?.scrollTop ?? scrollTopRef.current;
      let next = current;
      if (align === "center") next = top - (h - rh) / 2;
      else if (align === "end") next = top + rh - h;
      else if (top < current) next = top;
      else if (top + rh > current + h) next = top + rh - h;
      next = Math.max(0, next);
      if (next !== current || next !== scrollTopRef.current) scrollTo(next);
    },
    [scrollTo],
  );

  const element = useCallback(() => elRef.current, []);

  return {
    ref,
    element,
    onScroll,
    height,
    scrollTop,
    range,
    rendered: renderedIndices(range, count, pinned),
    scrollToIndex,
    scrollTo,
  };
}
