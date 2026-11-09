import { useId, useLayoutEffect, useMemo, useState, type DragEvent, type KeyboardEvent } from "react";
import { formatDurationMs } from "../../lib/http";
import type { FolderRun, FolderRunState } from "../../commands/folderRuns";
import { useT } from "../../i18n/react";
import { isDefaultRunSelection, isRunIncluded, orderedRunItems } from "../../lib/runSelection";
import type { RunItemDTO } from "../../lib/runner";
import { IconChevron, IconWarning } from "../common/icons";
import styles from "./FolderRunPanel.module.css";
import { OUTCOME_TEXT, runBadge, SKIP_TEXT } from "./runBadge";
import { requestRunStatuses, type RequestRunStatus } from "./runProgress";

/** The type of a dragged request of the list, so the list reacts to its own drags only. */
const DRAG_TYPE = "application/x-postsonde-run-item";

export interface RunRequestListProps {
  run: FolderRun;
  state: FolderRunState;
  /** A run is running: the choice and the order cannot change. */
  locked: boolean;
}

/** Where a dragged request would land: before or after the request under the pointer. */
interface DropTarget {
  readonly id: string;
  readonly after: boolean;
}

/**
 * The requests of a folder run, in the order of the run (RUN-02): a
 * checkbox per request to put it in the run or leave it out (requests a
 * run does not send are listed with why, and cannot be chosen), Select all,
 * Select none and Reset order, and the order changed with Alt+↑ / Alt+↓ on
 * a request's checkbox (focus stays on it, and the new place is
 * announced), or by mouse with the arrow buttons or drag and drop. The
 * choice and the order are the run slot's, for the session: the folder's
 * own order does not change.
 */
export function RunRequestList({ run, state, locked }: RunRequestListProps) {
  const t = useT();
  const baseId = useId();
  const [announcement, setAnnouncement] = useState("");
  const [focusId, setFocusId] = useState<string | null>(null);
  const [dragId, setDragId] = useState<string | null>(null);
  const [drop, setDrop] = useState<DropTarget | null>(null);
  const { plan, selection, planError } = state;
  const items = useMemo(() => (plan === null ? [] : orderedRunItems(plan, selection.order)), [plan, selection.order]);
  // Live (RUN-06): where each request of the run is.
  const statuses = useMemo(() => requestRunStatuses(state), [state]);
  const boxId = (id: string) => `${baseId}-req-${id}`;
  const headingId = `${baseId}-heading`;
  const hintId = `${baseId}-hint`;

  // Moving an item re-inserts its row: focus goes back to its checkbox.
  useLayoutEffect(() => {
    if (focusId === null) return;
    document.getElementById(boxId(focusId))?.focus();
    setFocusId(null);
  });

  if (plan === null) {
    if (planError === null) return <p className={styles.hint}>{t("runner.requests.loading")}</p>;
    return (
      <p className={styles.runError} role="alert">
        <IconWarning size={13} />
        {t("runner.requests.loadFailed", { message: planError.message })}
      </p>
    );
  }

  const runnable = items.filter((it) => it.skip === "");
  const chosen = runnable.filter((it) => isRunIncluded(it, selection)).length;
  const ids = runnable.map((it) => it.requestId);

  const move = (it: RunItemDTO, delta: number) => {
    if (locked) return;
    const from = items.indexOf(it);
    if (!run.move(it.requestId, delta)) return;
    setFocusId(it.requestId);
    setAnnouncement(t("runner.requests.moved", { name: it.name, position: from + delta + 1, count: items.length }));
  };

  const onKeyDown = (it: RunItemDTO) => (e: KeyboardEvent<HTMLInputElement>) => {
    if (!e.altKey || e.ctrlKey || e.metaKey || e.shiftKey) return;
    if (e.key !== "ArrowUp" && e.key !== "ArrowDown") return;
    // Taken at an edge too, so Alt+arrows never do something else there.
    e.preventDefault();
    move(it, e.key === "ArrowUp" ? -1 : 1);
  };

  const onDragStart = (it: RunItemDTO) => (e: DragEvent<HTMLLIElement>) => {
    if (locked) return;
    e.dataTransfer.setData(DRAG_TYPE, it.requestId);
    e.dataTransfer.effectAllowed = "move";
    setDragId(it.requestId);
  };
  const onDragOver = (it: RunItemDTO) => (e: DragEvent<HTMLLIElement>) => {
    if (dragId === null || locked) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = "move";
    const box = e.currentTarget.getBoundingClientRect();
    const after = e.clientY > box.top + box.height / 2;
    if (drop?.id !== it.requestId || drop.after !== after) setDrop({ id: it.requestId, after });
  };
  const endDrag = () => {
    setDragId(null);
    setDrop(null);
  };
  const onDrop = (it: RunItemDTO) => (e: DragEvent<HTMLLIElement>) => {
    if (dragId === null || locked) return;
    e.preventDefault();
    run.moveTo(dragId, it.requestId, drop?.id === it.requestId ? drop.after : false);
    endDrag();
  };

  return (
    <section className={styles.requests} aria-labelledby={headingId}>
      <div className={styles.requestsHead}>
        <h3 id={headingId} className={styles.requestsTitle}>
          {t("runner.requests.title")}
        </h3>
        <span className={styles.requestsCount}>{t("runner.requests.chosen", { chosen, total: runnable.length })}</span>
        <span className={styles.requestsTools}>
          <button type="button" className={styles.tool} disabled={locked || chosen === runnable.length} onClick={() => run.setIncluded(ids, true)}>
            {t("runner.requests.all")}
          </button>
          <button type="button" className={styles.tool} disabled={locked || chosen === 0} onClick={() => run.setIncluded(ids, false)}>
            {t("runner.requests.none")}
          </button>
          <button
            type="button"
            className={styles.tool}
            disabled={locked || isDefaultRunSelection(plan, selection)}
            onClick={() => {
              run.resetSelection();
              setAnnouncement(t("runner.requests.resetDone"));
            }}
          >
            {t("runner.requests.reset")}
          </button>
        </span>
      </div>
      <p id={hintId} className={styles.hint}>
        {t("runner.requests.hint")}
      </p>
      {items.length === 0 ? (
        <p className={styles.hint}>{t("runner.requests.empty")}</p>
      ) : (
        <ol className={styles.requestList} aria-labelledby={headingId}>
          {items.map((it, i) => {
            const badge = runBadge(it);
            const path = (it.path ?? []).join(" / ");
            const skipped = it.skip !== "";
            const noteId = `${boxId(it.requestId)}-note`;
            return (
              <li
                key={it.requestId}
                className={styles.request}
                aria-current={statuses.get(it.requestId)?.kind === "sending" ? "step" : undefined}
                data-skipped={skipped || undefined}
                data-dragging={dragId === it.requestId || undefined}
                data-drop={drop?.id === it.requestId && dragId !== it.requestId ? (drop.after ? "after" : "before") : undefined}
                draggable={!locked}
                onDragStart={onDragStart(it)}
                onDragOver={onDragOver(it)}
                onDragLeave={() => {
                  if (drop?.id === it.requestId) setDrop(null);
                }}
                onDrop={onDrop(it)}
                onDragEnd={endDrag}
              >
                <input
                  type="checkbox"
                  id={boxId(it.requestId)}
                  className={styles.check}
                  checked={isRunIncluded(it, selection)}
                  disabled={locked || skipped}
                  aria-describedby={skipped ? noteId : hintId}
                  onChange={(e) => run.setIncluded([it.requestId], e.target.checked)}
                  onKeyDown={onKeyDown(it)}
                />
                <label htmlFor={boxId(it.requestId)} className={styles.requestLabel}>
                  <span className={styles.badge} data-tone={badge.tone}>
                    {badge.label}
                  </span>{" "}
                  {/* Spaces between the parts, so the checkbox's name reads "POST Create Users". */}
                  <span className={styles.name}>
                    {it.name}
                    {path !== "" && (
                      <>
                        {" "}
                        <span className={styles.path}>{path}</span>
                      </>
                    )}
                  </span>
                </label>
                {skipped && (
                  <span id={noteId} className={styles.requestNote}>
                    {t(SKIP_TEXT[it.skip] ?? "runner.skip.kind")}
                  </span>
                )}
                <RowStatus status={statuses.get(it.requestId)} />

                {/* For the mouse: the keyboard has Alt+↑ / Alt+↓ on the checkbox. */}
                <span className={styles.moveButtons} aria-hidden="true">
                  <button
                    type="button"
                    tabIndex={-1}
                    className={styles.moveButton}
                    title={t("runner.requests.moveUp")}
                    aria-label={t("runner.requests.moveUp")}
                    disabled={locked || i === 0}
                    onClick={() => move(it, -1)}
                  >
                    <IconChevron direction="up" size={11} />
                  </button>
                  <button
                    type="button"
                    tabIndex={-1}
                    className={styles.moveButton}
                    title={t("runner.requests.moveDown")}
                    aria-label={t("runner.requests.moveDown")}
                    disabled={locked || i === items.length - 1}
                    onClick={() => move(it, 1)}
                  >
                    <IconChevron direction="down" size={11} />
                  </button>
                </span>
              </li>
            );
          })}
        </ol>
      )}
      <span className={styles.srOnly} role="status">
        {announcement}
      </span>
    </section>
  );
}

/** A request's place in the run (RUN-06): queued, sending, waiting, its outcome and time, or not run. */
function RowStatus({ status }: { status: RequestRunStatus | undefined }) {
  const t = useT();
  if (status === undefined) return null;
  switch (status.kind) {
    case "done": {
      const r = status.result;
      return (
        <span className={styles.rowStatus} data-outcome={r.outcome}>
          {r.outcome === "skipped"
            ? t("runner.outcome.skipped")
            : t("runner.live.done", { outcome: t(OUTCOME_TEXT[r.outcome] ?? "runner.outcome.error"), time: formatDurationMs(r.durationMs) })}
        </span>
      );
    }
    case "sending":
      return (
        <span className={styles.rowStatus} data-status="sending">
          <span className={styles.spinner} aria-hidden="true" />
          {t("runner.live.sending")}
        </span>
      );
    case "waiting":
      return (
        <span className={styles.rowStatus} data-status="waiting">
          {t("runner.live.waiting")}
        </span>
      );
    case "queued":
      return (
        <span className={styles.rowStatus} data-status="queued">
          {t("runner.live.queued")}
        </span>
      );
    default:
      return (
        <span className={styles.rowStatus} data-status="notRun">
          {t("runner.live.notRun")}
        </span>
      );
  }
}
