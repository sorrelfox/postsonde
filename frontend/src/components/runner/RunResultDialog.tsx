import { useEffect, useState } from "react";
import { loadFolderRunResponse } from "../../commands/folderRuns";
import { openRequest } from "../../commands/tabs";
import { useT } from "../../i18n/react";
import { toAppError } from "../../lib/errors";
import { formatDurationMs, type ResponseDTO } from "../../lib/http";
import type { RunResultDTO } from "../../lib/runner";
import { useCollection, useStores } from "../../stores";
import { Dialog } from "../common/Dialog";
import { IconWarning } from "../common/icons";
import { ResponseView } from "../response/ResponseView";
import styles from "./FolderRunPanel.module.css";
import { OUTCOME_TEXT, runBadge, SKIP_TEXT } from "./runBadge";

export interface RunResultDialogProps {
  result: RunResultDTO;
  /** The run's operation ID: Go keeps its responses by it. */
  opId: string | null;
  /** The run's iterations: with several, the title names the result's. */
  iterations: number;
  /** The result is of a past run (RUN-09): its response was not kept. */
  past?: boolean;
  onClose: () => void;
}

type Loaded =
  | { readonly status: "none" }
  | { readonly status: "loading" }
  | { readonly status: "ok"; readonly response: ResponseDTO }
  | { readonly status: "gone"; readonly message: string };

/**
 * One result of a run, opened from the results or the summary (RUN-07):
 * its outcome, the request's badge and name, the request as sent (method
 * and URL, secrets masked, and in the Timeline tab its headers and body),
 * **Open request** (its tab), and its response in the response view with
 * the Tests tab, as the runner kept it (RunnerService.Response). A result
 * without a response says why: skipped, the error, an event stream's
 * status and events, or a response no longer kept.
 */
export function RunResultDialog({ result: r, opId, iterations, past = false, onClose }: RunResultDialogProps) {
  const t = useT();
  const stores = useStores();
  const exists = useCollection((s) => s.nodes[r.requestId] !== undefined);
  const [loaded, setLoaded] = useState<Loaded>({ status: r.hasResponse && opId !== null ? "loading" : "none" });

  useEffect(() => {
    if (!r.hasResponse || opId === null) {
      setLoaded({ status: "none" });
      return;
    }
    let live = true;
    setLoaded({ status: "loading" });
    loadFolderRunResponse(opId, r.index).then(
      (response) => {
        if (live) setLoaded({ status: "ok", response });
      },
      (err: unknown) => {
        if (live) setLoaded({ status: "gone", message: toAppError(err).message });
      },
    );
    return () => {
      live = false;
    };
  }, [opId, r]);

  const badge = runBadge(r);
  const path = (r.path ?? []).join(" / ");
  const sent = loaded.status === "ok" ? (loaded.response.timeline?.hops?.[0]?.request ?? null) : null;
  const title = iterations > 1 ? t("runner.result.titleInIteration", { name: r.name, n: r.iteration }) : t("runner.result.title", { name: r.name });

  return (
    <Dialog open title={title} onCancel={onClose} width="min(960px, 94vw)" bodyClassName={styles.resultBody}>
      <div className={styles.resultHead}>
        <span className={styles.outcome} data-outcome={r.outcome}>
          {t(OUTCOME_TEXT[r.outcome] ?? "runner.outcome.error")}
        </span>
        <span className={styles.badge} data-tone={badge.tone}>
          {badge.label}
        </span>
        <span className={styles.name}>
          {r.name}
          {path !== "" && <span className={styles.path}>{path}</span>}
        </span>
        {r.outcome !== "skipped" && <span className={styles.duration}>{formatDurationMs(r.durationMs)}</span>}
        <button
          type="button"
          className={styles.tool}
          disabled={!exists}
          title={exists ? undefined : t("runner.result.requestGone")}
          onClick={() => {
            onClose();
            openRequest(stores, r.requestId);
          }}
        >
          {t("runner.result.openRequest")}
        </button>
      </div>
      {sent !== null && (
        <p className={styles.sentLine}>
          <span className={styles.label}>{t("runner.result.sent")}</span>
          <code className={styles.sentRequest}>
            {sent.method} {sent.url}
          </code>
          <span className={styles.hint}>{t("runner.result.timelineHint")}</span>
        </p>
      )}
      {r.outcome === "skipped" && <p className={styles.hint}>{t(SKIP_TEXT[r.skip] ?? "runner.skip.kind")}</p>}
      {r.error !== null && (
        <p className={styles.runError}>
          <IconWarning size={13} />
          {toAppError(r.error).message}
        </p>
      )}
      {r.kind === "sse" && r.error === null && (
        <p className={styles.hint}>{t("runner.result.stream", { status: r.status !== "" ? r.status : String(r.statusCode), count: r.events })}</p>
      )}
      {past && r.outcome !== "skipped" && r.kind !== "sse" && r.error === null && <p className={styles.hint}>{t("runner.result.notKept")}</p>}
      {past && (r.tests ?? []).length > 0 && (
        <ul className={styles.failures}>
          {(r.tests ?? []).map((x) => (
            <li key={x.index} className={styles.failure}>
              <span className={styles.outcome} data-outcome={x.outcome === "passed" ? "passed" : "failed"}>
                {t(x.outcome === "passed" ? "runner.outcome.passed" : "runner.outcome.failed")}
              </span>
              <span className={styles.failureText}>{x.message}</span>
            </li>
          ))}
        </ul>
      )}
      {loaded.status === "loading" && <p className={styles.hint}>{t("runner.result.loading")}</p>}
      {loaded.status === "gone" && (
        <p className={styles.runError} role="alert">
          <IconWarning size={13} />
          {loaded.message}
        </p>
      )}
      {loaded.status === "ok" && (
        <div className={styles.resultResponse}>
          <ResponseView response={loaded.response} tests={{}} graphql={r.kind === "graphql" ? {} : undefined} />
        </div>
      )}
    </Dialog>
  );
}
