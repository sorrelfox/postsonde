import { forwardRef, useEffect, useLayoutEffect, useMemo, useRef } from "react";
import { describeEntry, formatPartTime, partGap, summarizeIncremental, type IncrementalMode } from "../../lib/graphqlIncremental";
import { readGraphqlPayload, type QueryLocation } from "../../lib/graphqlResponse";
import type { IncrementalDTO } from "../../lib/http";
import { useT } from "../../i18n/react";
import { IconWarning } from "../common/icons";
import { CodeEditor, type CodeEditorHandle } from "../editor/CodeEditor";
import { formatSize } from "../messagelog/format";
import { GraphqlBody } from "./GraphqlBody";
import styles from "./ResponseView.module.css";

export interface IncrementalBodyProps {
  incremental: IncrementalDTO;
  mode: IncrementalMode;
  hideExtensions: boolean;
  onLocation?: (location: QueryLocation) => void;
}

/**
 * The GraphQL view of an incremental response (GQL-08, @defer and
 * @stream): the summary line, then either the merged result in the
 * GraphQL view (GraphqlBody) or the parts as they arrived, each with its
 * time from the send, the time since the part before, what it did
 * (initial result, deferred data at a path, streamed items, pending and
 * completed entries), its hasNext and its JSON. While the response
 * arrives Go sends it again with each new part, so both grow live.
 */
export const IncrementalBody = forwardRef<CodeEditorHandle, IncrementalBodyProps>(function IncrementalBody(
  { incremental: inc, mode, hideExtensions, onLocation },
  ref,
) {
  const t = useT();
  const merged = useMemo(() => (inc.merged !== "" ? readGraphqlPayload(inc.merged) : null), [inc.merged]);
  const summary = summarizeIncremental(inc);
  return (
    <div className={styles.bodyPane} data-incremental-view={mode}>
      <div className={styles.incSummary} data-tone={summary.tone} role="status">
        {summary.tone === "warn" && <IconWarning size={12} />}
        <span>{summary.text}</span>
      </div>
      {mode === "parts" ? (
        <PartsList incremental={inc} />
      ) : inc.mergedTooLarge ? (
        <p className={styles.empty}>
          {t("response.incremental.mergedTooLarge", { size: formatSize(inc.mergedSize) })}
        </p>
      ) : merged !== null ? (
        <GraphqlBody ref={ref} graphql={merged} hideExtensions={hideExtensions} onLocation={onLocation} />
      ) : inc.merged !== "" ? (
        <CodeEditor ref={ref} className={styles.bodyEditor} value={inc.merged} readOnly language="json" ariaLabel={t("response.incremental.mergedLabel")} />
      ) : (
        <p className={styles.empty}>{t("response.incremental.nothingMerged")}</p>
      )}
    </div>
  );
});

/** The most text of one part Go sends (the rest is clipped). */
const PART_TEXT_BYTES = 64 * 1024;

/** The parts, oldest first; the list follows new parts while it is scrolled to its end. */
function PartsList({ incremental: inc }: { incremental: IncrementalDTO }) {
  const t = useT();
  const parts = inc.parts ?? [];
  const empty = parts.length === 0;
  const listRef = useRef<HTMLOListElement>(null);
  const atEnd = useRef(true);
  useEffect(() => {
    const el = listRef.current;
    if (el === null) return;
    const onScroll = () => {
      atEnd.current = el.scrollHeight - el.scrollTop - el.clientHeight < 8;
    };
    el.addEventListener("scroll", onScroll);
    return () => el.removeEventListener("scroll", onScroll);
  }, [empty]);
  useLayoutEffect(() => {
    const el = listRef.current;
    if (el !== null && atEnd.current && inc.receiving) el.scrollTop = el.scrollHeight;
  }, [inc]);

  if (empty) return <p className={styles.empty}>{t("response.incremental.noParts")}</p>;
  const hidden = inc.partCount - parts.length;
  return (
    <ol ref={listRef} className={styles.incParts} aria-label={t("response.incremental.partsLabel")}>
      {parts.map((part, i) => (
        <li key={part.index} className={styles.incPart} data-part={part.index}>
          <div className={styles.incPartHead}>
            <span className={styles.incIndex}>{t("response.incremental.part", { number: part.index + 1 })}</span>
            <span className={styles.incTime} title={t("response.incremental.partTimeTitle")}>
              {formatPartTime(part.atMs)}
            </span>
            {i > 0 && (
              <span className={styles.incGap} title={t("response.incremental.partGapTitle")}>
                {partGap(parts, i)}
              </span>
            )}
            <span className={styles.incSize}>{formatSize(part.size)}</span>
            {part.hasNext !== null && <span className={styles.incNext}>hasNext: {String(part.hasNext)}</span>}
          </div>
          {(part.entries ?? []).length > 0 && (
            <ul className={styles.incEntries}>
              {(part.entries ?? []).map((e, j) => (
                <li key={j} className={styles.incEntry} data-kind={e.kind} data-problem={e.problem !== "" || undefined}>
                  {describeEntry(e)}
                  {e.problem !== "" && <span className={styles.incProblem}> {e.problem}</span>}
                </li>
              ))}
            </ul>
          )}
          {part.problem !== "" && (
            <p className={styles.incProblem} role="note">
              <IconWarning size={12} /> {part.problem}
            </p>
          )}
          {part.text !== "" ? (
            <pre className={styles.incText}>{part.text}</pre>
          ) : (
            <p className={styles.empty}>{t("response.incremental.partNotListed")}</p>
          )}
          {part.clipped && part.text !== "" && (
            <p className={styles.bodyNote}>{t("response.incremental.partClipped", { size: formatSize(PART_TEXT_BYTES) })}</p>
          )}
        </li>
      ))}
      {hidden > 0 && <li className={styles.empty}>{t("response.incremental.hiddenParts", { count: hidden })}</li>}
    </ol>
  );
}
