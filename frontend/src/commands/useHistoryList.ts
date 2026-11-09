// The history panel's list (NAV-02): the pages of the entries a filter
// selects, asked of Go (HistoryService.List: the search runs over the
// store there), kept up to date from history:changed. Paged: the first
// page comes with the filter, "Load more" adds the next.

import { useCallback, useEffect, useRef, useState } from "react";
import { toAppError, type AppError } from "../lib/errors";
import { HISTORY_MAX_PAGE, HISTORY_PAGE_SIZE, listHistory, type HistoryFilter } from "../lib/history";
import { onHistoryChanged, type HistorySummaryDTO } from "../lib/responseHistory";

export interface HistoryListState {
  /** "loading" until the first page of the filter arrived. */
  readonly status: "loading" | "ready" | "error";
  readonly entries: readonly HistorySummaryDTO[];
  /** How many entries the filter selects, and how many the history keeps. */
  readonly total: number;
  readonly all: number;
  /** The environments of the kept entries, for the filter. */
  readonly environments: readonly string[];
  readonly error: AppError | null;
  /** Set while the next page loads. */
  readonly loadingMore: boolean;
}

const INITIAL: HistoryListState = { status: "loading", entries: [], total: 0, all: 0, environments: [], error: null, loadingMore: false };

/** How long history:changed events are gathered before the list is read again. */
const CHANGE_DELAY_MS = 150;

export interface HistoryList {
  readonly state: HistoryListState;
  /** Loads the next page. */
  readonly loadMore: () => void;
  /** Reads the loaded pages again. */
  readonly refresh: () => void;
}

/**
 * The entries `filter` selects, for project `workspaceId` (another project
 * starts over). A new filter loads its first page; a change of the history
 * reads again as many entries as are shown.
 */
export function useHistoryList(filter: HistoryFilter, workspaceId: string | null): HistoryList {
  const [state, setState] = useState<HistoryListState>(INITIAL);
  const stateRef = useRef(state);
  stateRef.current = state;
  // Bumped by every read, so a late answer does nothing.
  const generation = useRef(0);

  const read = useCallback(
    (limit: number, keep: boolean) => {
      const gen = ++generation.current;
      if (!keep) setState((s) => ({ ...s, status: "loading", error: null, loadingMore: false }));
      listHistory(filter, 0, Math.min(Math.max(limit, HISTORY_PAGE_SIZE), HISTORY_MAX_PAGE)).then(
        (page) => {
          if (gen !== generation.current) return;
          setState({
            status: "ready",
            entries: page.entries ?? [],
            total: page.total,
            all: page.all,
            environments: page.environments ?? [],
            error: null,
            loadingMore: false,
          });
        },
        (err: unknown) => {
          if (gen !== generation.current) return;
          setState({ ...INITIAL, status: "error", error: toAppError(err) });
        },
      );
    },
    [filter],
  );

  useEffect(() => {
    if (workspaceId === null) return;
    read(HISTORY_PAGE_SIZE, false);
  }, [read, workspaceId]);

  useEffect(() => {
    if (workspaceId === null) return;
    let timer: ReturnType<typeof setTimeout> | null = null;
    let stop: (() => void) | null = null;
    try {
      stop = onHistoryChanged(() => {
        if (timer !== null) clearTimeout(timer);
        timer = setTimeout(() => {
          timer = null;
          read(stateRef.current.entries.length, true);
        }, CHANGE_DELAY_MS);
      });
    } catch {
      // No runtime (a test without events): the list is read on demand only.
    }
    return () => {
      if (timer !== null) clearTimeout(timer);
      stop?.();
    };
  }, [read, workspaceId]);

  const loadMore = useCallback(() => {
    const s = stateRef.current;
    if (s.status !== "ready" || s.loadingMore || s.entries.length >= s.total) return;
    const gen = ++generation.current;
    setState({ ...s, loadingMore: true });
    listHistory(filter, s.entries.length, HISTORY_PAGE_SIZE).then(
      (page) => {
        if (gen !== generation.current) return;
        setState((cur) => {
          const seen = new Set(cur.entries.map((e) => e.id));
          const more = (page.entries ?? []).filter((e) => !seen.has(e.id));
          return { ...cur, entries: [...cur.entries, ...more], total: page.total, all: page.all, environments: page.environments ?? [], loadingMore: false };
        });
      },
      (err: unknown) => {
        if (gen !== generation.current) return;
        setState((cur) => ({ ...cur, loadingMore: false, error: toAppError(err) }));
      },
    );
  }, [filter]);

  const refresh = useCallback(() => read(stateRef.current.entries.length, true), [read]);

  return { state, loadMore, refresh };
}
