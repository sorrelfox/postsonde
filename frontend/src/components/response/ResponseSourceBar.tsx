import { useState, type ChangeEvent, type ReactNode } from "react";
import type { HttpSendState } from "../../commands/httpSends";
import type { MockSource } from "../../commands/mockFromExample";
import {
  responseExamples,
  saveResponseAsExample,
  showEarlierResponse,
  showLatestResponse,
  showResponseExample,
  type ShownResponse,
} from "../../commands/responseHistory";
import { useResponseHistory } from "../../commands/useResponseHistory";
import { formatDate } from "../../i18n";
import { useT } from "../../i18n/react";
import type { RequestDTO } from "../../lib/collection";
import { formatDurationMs, type ResponseDTO } from "../../lib/http";
import type { HistorySummaryDTO } from "../../lib/responseHistory";
import { useCollection, useDrafts, useStores, type RequestId } from "../../stores";
import { MockFromExampleDialog } from "../servers/MockFromExampleDialog";
import { CompareResponsesDialog, type CompareOption } from "./CompareResponsesDialog";
import styles from "./ResponseSourceBar.module.css";

/** A history entry's time as the list shows it: the date and time, to the second. */
export function formatSentAt(time: string): string {
  const date = new Date(time);
  if (Number.isNaN(date.getTime())) return time;
  return formatDate(date, { dateStyle: "short", timeStyle: "medium" });
}

export interface ResponseSourceProps {
  id: RequestId;
  /** The request's latest send. */
  send: HttpSendState;
  /** The pane of the latest send. */
  children: ReactNode;
  /** The view of a stored response (an earlier one or an example): a ResponseView without tests. */
  renderStored: (response: ResponseDTO) => ReactNode;
}

/**
 * The response pane of an HTTP or GraphQL request with its earlier
 * responses (HTTP-39) and response examples (HTTP-38): above the latest
 * send's pane, a bar to show an earlier response of the request (from the
 * history, commands/responseHistory.ts) or one of its saved examples
 * instead, until the next send, and Save as example, which adds the
 * response shown (from the history, secrets masked by Go) to the request's
 * examples. Compare… shows two of them side by side (HTTP-40,
 * CompareResponsesDialog), and Create mock route… makes a mock server route
 * of the response shown (MOCK-03). Without earlier responses, examples or
 * a response to save, the bar is left out.
 */
export function ResponseSource({ id, send, children, renderStored }: ResponseSourceProps) {
  const t = useT();
  const stores = useStores();
  const { state } = useResponseHistory(id);
  const examples = useDrafts((s) => (s.byId[id]?.request as unknown as RequestDTO | undefined)?.responseExamples) ?? null;
  const list = responseExamples(examples === null ? undefined : { responseExamples: examples });
  // A read-only request (or project) takes no examples; nor does one whose draft is not loaded.
  const noDraft = useDrafts((s) => s.byId[id] === undefined);
  const itemReadOnly = useCollection((s) => (s.nodes[id]?.readOnly ?? "") !== "" || s.readOnly !== "");
  const readOnly = noDraft || itemReadOnly;
  // The request's method and URL as typed, for a mock route (MOCK-03).
  const typed = useDrafts((s) => s.byId[id]?.request as unknown as { method?: string; url?: string } | undefined);
  const [mockSource, setMockSource] = useState<MockSource | null>(null);
  const [comparing, setComparing] = useState(false);

  // A pick lasts until the next send.
  const shown: ShownResponse = state.shown.kind !== "latest" && state.since === send.startedAt ? state.shown : { kind: "latest" };
  const latest = send.run === null && send.status === "ok" ? send.response : null;
  const latestEntry = latest?.historyId ?? "";
  const earlier = state.entries.filter((e) => e.id !== latestEntry);

  const saveId = shown.kind === "history" ? shown.id : shown.kind === "latest" ? latestEntry : "";
  const shownExample = shown.kind === "example" ? list[shown.index] : undefined;
  const mockFrom: MockSource | null =
    shownExample !== undefined ? { kind: "example", example: shownExample } : saveId !== "" ? { kind: "history", id: saveId } : null;
  const showBar = earlier.length > 0 || list.length > 0 || latestEntry !== "" || shown.kind !== "latest";

  const value = shown.kind === "history" ? `h:${shown.id}` : shown.kind === "example" ? `e:${shown.index}` : "latest";
  const onPick = (e: ChangeEvent<HTMLSelectElement>) => {
    const v = e.target.value;
    if (v.startsWith("h:")) {
      const entry = state.entries.find((x) => x.id === v.slice(2));
      if (entry !== undefined) showEarlierResponse(id, entry.id, entry.time, send.startedAt);
    } else if (v.startsWith("e:")) {
      showResponseExample(stores, id, Number(v.slice(2)), send.startedAt);
    } else {
      showLatestResponse(id);
    }
  };
  const entryLabel = (e: HistorySummaryDTO) =>
    t("response.source.entry", { time: formatSentAt(e.time), status: e.status || String(e.statusCode), duration: formatDurationMs(e.durationMs) });
  const exampleLabel = (name: string, i: number) => (name.trim() === "" ? t("response.source.unnamedExample", { n: i + 1 }) : name);

  // Compare (HTTP-40): any two of the latest response (by its history
  // entry), the earlier ones and the examples. The response shown is on
  // the left, the latest on the right.
  const compareOptions: CompareOption[] = [
    ...(latestEntry !== "" ? [{ key: `h:${latestEntry}`, label: t("response.source.latest") }] : []),
    ...earlier.map((e) => ({ key: `h:${e.id}`, label: entryLabel(e) })),
    ...list.map((ex, i) => ({ key: `e:${i}`, label: exampleLabel(ex.name, i) })),
  ];
  const shownKey = shown.kind === "latest" ? (latestEntry !== "" ? `h:${latestEntry}` : "") : value;
  const compareRight = latestEntry !== "" ? `h:${latestEntry}` : (compareOptions[0]?.key ?? "");
  const compareLeft =
    shownKey !== "" && shownKey !== compareRight ? shownKey : (compareOptions.find((o) => o.key !== compareRight)?.key ?? compareRight);

  let body: ReactNode = children;
  if (shown.kind !== "latest") {
    if (state.status === "ok" && state.response !== null) body = renderStored(state.response);
    else if (state.status === "error")
      body = (
        <p className={styles.error} role="alert">
          {t("response.source.failed", { error: state.error?.message ?? "" })}
        </p>
      );
    else
      body = (
        <p className={styles.loading} role="status">
          {t("response.source.loading")}
        </p>
      );
  }

  return (
    <>
      {showBar && (
        <div className={styles.bar} role="group" aria-label={t("response.source.label")}>
          <select className={styles.select} aria-label={t("response.source.select")} value={value} onChange={onPick}>
            <option value="latest">{t("response.source.latest")}</option>
            {earlier.length > 0 && (
              <optgroup label={t("response.source.earlier")}>
                {earlier.map((e) => (
                  <option key={e.id} value={`h:${e.id}`}>
                    {entryLabel(e)}
                  </option>
                ))}
              </optgroup>
            )}
            {list.length > 0 && (
              <optgroup label={t("response.source.examples")}>
                {list.map((ex, i) => (
                  <option key={i} value={`e:${i}`}>
                    {exampleLabel(ex.name, i)}
                  </option>
                ))}
              </optgroup>
            )}
          </select>
          {shown.kind === "history" && <span className={styles.note}>{t("response.source.sentAt", { time: formatSentAt(shown.time) })}</span>}
          {shown.kind === "example" && <span className={styles.note}>{t("response.source.exampleNote")}</span>}
          <div className={styles.actions}>
            <button
              type="button"
              className={styles.button}
              disabled={compareOptions.length < 2}
              title={compareOptions.length < 2 ? t("response.source.compare.none") : t("response.source.compare.title")}
              onClick={() => setComparing(true)}
            >
              {t("response.source.compare")}
            </button>
            <button
              type="button"
              className={styles.button}
              disabled={mockFrom === null}
              title={mockFrom === null ? t("response.source.mock.none") : t("response.source.mock.title")}
              onClick={() => setMockSource(mockFrom)}
            >
              {t("response.source.mock")}
            </button>
            {shown.kind !== "example" && (
              <button
                type="button"
                className={styles.button}
                disabled={saveId === "" || state.saving || readOnly}
                title={saveId === "" ? t("response.source.save.none") : t("response.source.save.title")}
                onClick={() => void saveResponseAsExample(stores, id, saveId)}
              >
                {t("response.source.save")}
              </button>
            )}
          </div>
        </div>
      )}
      <CompareResponsesDialog
        id={id}
        open={comparing}
        options={compareOptions}
        initialLeft={compareLeft}
        initialRight={compareRight}
        onClose={() => setComparing(false)}
      />
      <MockFromExampleDialog source={mockSource} method={typed?.method ?? ""} url={typed?.url ?? ""} onClose={() => setMockSource(null)} />
      {body}
    </>
  );
}
