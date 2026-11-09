// Demo of the Go bindings and the error model: shows
// SystemService.Info, runs a cancellable SystemService.Wait and shows the
// structured error Wait returns for bad input. Mounted in the response pane
// until real views land.
import { useEffect, useRef, useState } from "react";
import { useT, type Translate } from "../i18n/react";
import type { CancellablePromise } from "../lib/calls";
import { errorTitle, formatError, isCancelled, toAppError, type AppError } from "../lib/errors";
import { describeInfo, getInfo, wait, type InfoDTO, type WaitResultDTO } from "../lib/system";

const WAIT_MS = 3000;
/** Outside 0–60000 ms, so Go answers with an invalid_argument error. */
const BAD_WAIT_MS = -1;

type WaitState =
  | { kind: "idle" }
  | { kind: "running" }
  | { kind: "done"; elapsedMs: number }
  | { kind: "cancelled"; afterMs: number }
  | { kind: "failed"; error: AppError };

function describeWait(t: Translate, state: WaitState): string {
  switch (state.kind) {
    case "idle":
      return t("app.systemDemo.status.idle");
    case "running":
      return t("app.systemDemo.status.running");
    case "done":
      return t("app.systemDemo.status.done", { ms: state.elapsedMs });
    case "cancelled":
      return t("app.systemDemo.status.cancelled", { ms: state.afterMs });
    case "failed":
      return t("app.systemDemo.status.failed");
  }
}

function ErrorDetails({ error }: { error: AppError }) {
  return (
    <div role="alert">
      <p style={{ margin: "0.25rem 0" }}>
        <strong>{errorTitle(error.kind)}</strong>{" "}
        <code>
          {error.kind}
          {error.code && ` · ${error.code}`}
        </code>
      </p>
      <p style={{ margin: "0.25rem 0" }}>{error.message}</p>
      {error.hint && <p style={{ margin: "0.25rem 0" }}>{error.hint}</p>}
    </div>
  );
}

export default function SystemDemo() {
  const t = useT();
  const [info, setInfo] = useState<InfoDTO | null>(null);
  const [infoError, setInfoError] = useState<string | null>(null);
  const [waitState, setWaitState] = useState<WaitState>({ kind: "idle" });
  const running = useRef<CancellablePromise<WaitResultDTO> | null>(null);

  useEffect(() => {
    const call = getInfo();
    call.then(setInfo, (err: unknown) => {
      if (!isCancelled(err)) {
        setInfoError(formatError(err));
      }
    });
    // Cancel whatever is still running when the component goes away.
    return () => {
      void call.cancel();
      void running.current?.cancel();
    };
  }, []);

  function run(ms: number) {
    const startedAt = performance.now();
    const call = wait(ms);
    running.current = call;
    setWaitState({ kind: "running" });
    call
      .then(
        (result) => setWaitState({ kind: "done", elapsedMs: result.elapsedMs }),
        (err: unknown) =>
          setWaitState(
            isCancelled(err)
              ? { kind: "cancelled", afterMs: Math.round(performance.now() - startedAt) }
              : { kind: "failed", error: toAppError(err) },
          ),
      )
      .finally(() => {
        if (running.current === call) {
          running.current = null;
        }
      });
  }

  function cancel() {
    void running.current?.cancel();
  }

  const isRunning = waitState.kind === "running";

  return (
    <section aria-label={t("app.systemDemo.label")}>
      <p>
        {info
          ? describeInfo(info)
          : infoError
            ? t("app.systemDemo.unavailable", { error: infoError })
            : t("app.systemDemo.connecting")}
      </p>
      <div style={{ display: "flex", gap: "0.5rem", justifyContent: "center", margin: "0.75rem 0" }}>
        <button type="button" onClick={() => run(WAIT_MS)} disabled={isRunning}>
          {t("app.systemDemo.run", { seconds: WAIT_MS / 1000 })}
        </button>
        <button type="button" onClick={() => run(BAD_WAIT_MS)} disabled={isRunning}>
          {t("app.systemDemo.badInput")}
        </button>
        <button type="button" onClick={cancel} disabled={!isRunning}>
          {t("common.cancel")}
        </button>
      </div>
      <p role="status">{describeWait(t, waitState)}</p>
      {waitState.kind === "failed" && <ErrorDetails error={waitState.error} />}
    </section>
  );
}
