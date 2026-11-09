import { useId, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import type { SseStream } from "../../commands/sseStreams";
import { copyText } from "../../lib/clipboard";
import type { MessageSource } from "../../lib/messageSource";
import { DEFAULT_MERGE_OPTIONS, mergeEvents, type MergeEvent, type MergeExtract, type MergeJoin, type MergeOptions } from "../../lib/sseMerge";
import type { MessageKey } from "../../i18n";
import { useT } from "../../i18n/react";
import { formatSize } from "../messagelog/format";
import view from "./ResponseView.module.css";
import styles from "./SseResponseView.module.css";

/** The events kept in a stream's log, oldest first, as the merge reads them. */
export function logEvents(log: MessageSource): MergeEvent[] {
  const snap = log.getSnapshot();
  const out: MergeEvent[] = [];
  for (let seq = snap.firstSeq; seq < snap.nextSeq; seq++) {
    const m = log.get(seq);
    if (m?.kind === "event") out.push({ event: m.badge ?? "message", data: m.text ?? "" });
  }
  return out;
}

const EXTRACT_HELP: Record<MergeExtract, MessageKey> = {
  auto: "response.merge.help.auto",
  data: "response.merge.help.data",
  field: "response.merge.help.field",
};

/**
 * The Merged tab of a stream (SSE-04): the data of its events joined into
 * one text, for token streams that send a word or less per event. Filters
 * by event type, takes the data as it is or a JSON field ("auto" knows the
 * common token-stream shapes), and joins the pieces with nothing or new
 * lines. It reads the stream's log, so it follows the stream live and
 * holds what the log holds.
 */
export function SseMergedView({ stream }: { stream: SseStream }) {
  const t = useT();
  const snap = useSyncExternalStore(stream.log.subscribe, stream.log.getSnapshot);
  const [options, setOptions] = useState<MergeOptions>(DEFAULT_MERGE_OPTIONS);
  const [copied, setCopied] = useState<"" | "done" | "failed">("");
  const typeId = useId();
  const extractId = useId();
  const pathId = useId();
  const joinId = useId();
  const textRef = useRef<HTMLPreElement>(null);
  const follow = useRef(true);

  // The snapshot changes with every applied append or clear.
  const events = useMemo(() => logEvents(stream.log), [stream.log, snap]);
  const types = useMemo(() => [...new Set(events.map((e) => e.event))], [events]);
  const result = useMemo(() => mergeEvents(events, options), [events, options]);
  const set = (patch: Partial<MergeOptions>) => {
    setOptions((o) => ({ ...o, ...patch }));
    setCopied("");
  };

  // Follow the end while the text grows, unless the user scrolled up.
  useLayoutEffect(() => {
    const el = textRef.current;
    if (el !== null && follow.current) el.scrollTop = el.scrollHeight;
  }, [result.text]);

  const copy = () => {
    copyText(result.text).then(
      () => setCopied("done"),
      () => setCopied("failed"),
    );
  };

  const typeOptions = options.eventType !== "" && !types.includes(options.eventType) ? [...types, options.eventType] : types;
  let info: string;
  if (result.matched === 0) info = events.length === 0 ? t("response.merge.noEvents") : t("response.merge.noEventsOfType");
  else {
    const size = formatSize(new TextEncoder().encode(result.text).length);
    info = t("response.merge.info", { pieces: result.pieces, count: result.matched, size });
    if (result.skipped > 0) info = t("response.merge.infoSkipped", { info, skipped: result.skipped });
  }

  return (
    <div className={styles.merged} data-merged-view="">
      <div className={styles.mergeBar} role="group" aria-label={t("response.merge.options")}>
        <label className={styles.mergeLabel} htmlFor={typeId}>
          {t("response.merge.events")}
        </label>
        <select id={typeId} className={styles.mergeSelect} value={options.eventType} onChange={(e) => set({ eventType: e.currentTarget.value })}>
          <option value="">{t("response.merge.allTypes")}</option>
          {typeOptions.map((type) => (
            <option key={type} value={type}>
              {type}
            </option>
          ))}
        </select>
        <label className={styles.mergeLabel} htmlFor={extractId}>
          {t("response.merge.text")}
        </label>
        <select
          id={extractId}
          className={styles.mergeSelect}
          value={options.extract}
          title={t(EXTRACT_HELP[options.extract])}
          onChange={(e) => set({ extract: e.currentTarget.value as MergeExtract })}
        >
          <option value="auto">{t("response.merge.extract.auto")}</option>
          <option value="data">{t("response.merge.extract.data")}</option>
          <option value="field">{t("response.merge.extract.field")}</option>
        </select>
        {options.extract === "field" && (
          <input
            id={pathId}
            className={styles.mergePath}
            aria-label={t("response.merge.extract.field")}
            placeholder="choices.0.delta.content"
            spellCheck={false}
            autoComplete="off"
            value={options.path}
            onChange={(e) => set({ path: e.currentTarget.value })}
          />
        )}
        <label className={styles.mergeLabel} htmlFor={joinId}>
          {t("response.merge.joinWith")}
        </label>
        <select id={joinId} className={styles.mergeSelect} value={options.join} onChange={(e) => set({ join: e.currentTarget.value as MergeJoin })}>
          <option value="none">{t("response.merge.join.none")}</option>
          <option value="newline">{t("response.merge.join.newline")}</option>
        </select>
        <button type="button" className={styles.stop} disabled={result.text === ""} onClick={copy}>
          {copied === "done" ? t("response.merge.copied") : t("response.merge.copy")}
        </button>
      </div>
      <p className={styles.mergeInfo} data-merge-info="">
        {info}
        {snap.dropped > 0 && ` ${t("response.merge.dropped", { max: snap.maxMessages })}`}
        {copied === "failed" && ` ${t("response.merge.copyFailed")}`}
      </p>
      {result.text === "" ? (
        <p className={view.empty}>{t(EXTRACT_HELP[options.extract])}</p>
      ) : (
        <pre
          ref={textRef}
          className={styles.mergedText}
          data-merged-text=""
          tabIndex={0}
          aria-label={t("response.merge.textLabel")}
          onScroll={(e) => {
            const el = e.currentTarget;
            follow.current = el.scrollHeight - el.scrollTop - el.clientHeight < 8;
          }}
        >
          {result.text}
        </pre>
      )}
    </div>
  );
}
