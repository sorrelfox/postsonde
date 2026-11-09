// Demo of event streaming: runs DemoService.Stream and shows items
// arriving in throttled batches, with cancel by operation ID, and feeds them
// into the connection message log as WebSocket-like messages. Replaced
// by real streaming views (responses, sockets) later.
import { useEffect, useRef, useState, type ChangeEvent } from "react";
import { useDemoStream } from "../commands/useDemoStream";
import type { OperationStatus } from "../commands/useOperation";
import type { MessageKey } from "../i18n";
import { useT, type Translate } from "../i18n/react";
import type { ItemDTO } from "../lib/demo";
import { DEMO_MAX_DURATION_MS, DEMO_MAX_RATE_PER_SEC } from "../lib/demo";
import { formatError, type AppError } from "../lib/errors";
import { MessageSource } from "../lib/messageSource";
import { MessageLog } from "./messagelog/MessageLog";
import { demoMessage } from "./streamDemoMessages";

const DEFAULT_RATE = 2000;
const DEFAULT_DURATION_MS = 5000;

function describeStatus(t: Translate, status: OperationStatus, error: AppError | null): string {
  switch (status) {
    case "idle":
      return t("app.streamDemo.status.idle");
    case "starting":
      return t("app.streamDemo.status.starting");
    case "running":
      return t("app.streamDemo.status.running");
    case "ok":
      return t("app.streamDemo.status.ok");
    case "cancelled":
      return t("app.streamDemo.status.cancelled");
    case "error":
      return t("app.streamDemo.status.error", { error: formatError(error) });
  }
}

// i18n-ignore: a CSS value
const STATS_STYLE = { display: "grid", gridTemplateColumns: "auto auto", gap: "0.25rem 1rem", justifyContent: "center" } as const;

function perSecond(count: number, ms: number): string {
  return ms > 0 ? (count / (ms / 1000)).toFixed(1) : "–";
}

function clamp(value: number, max: number): number {
  return Number.isFinite(value) ? Math.min(Math.max(Math.round(value), 1), max) : 1;
}

export function StreamDemo() {
  const t = useT();
  const [rate, setRate] = useState(DEFAULT_RATE);
  const [durationMs, setDurationMs] = useState(DEFAULT_DURATION_MS);
  const [source] = useState(() => new MessageSource());
  const op = useDemoStream((items: readonly ItemDTO[]) => {
    const now = Date.now();
    source.append(items.map((item) => demoMessage(item.index, now)));
  });

  useEffect(() => () => source.dispose(), [source]);

  // Connection-style system messages around the stream.
  const lastStatus = useRef<OperationStatus>(op.status);
  useEffect(() => {
    const previous = lastStatus.current;
    lastStatus.current = op.status;
    if (previous === op.status) return;
    if (op.status === "running") {
      source.append({
        direction: "system",
        kind: "connected",
        text: t("app.streamDemo.log.started", { operation: op.opId ?? "?" }),
      });
    } else if (op.status === "ok" || op.status === "cancelled") {
      source.append({
        direction: "system",
        kind: "closed",
        text: op.status === "ok" ? t("app.streamDemo.log.finished") : t("app.streamDemo.log.cancelled"),
      });
    } else if (op.status === "error") {
      source.append({ direction: "system", kind: "error", text: formatError(op.error) });
    }
  }, [op.status, op.opId, op.error, source, t]);

  const elapsedMs =
    op.startedAt === null ? 0 : Math.round((op.finishedAt ?? op.updatedAt ?? op.startedAt) - op.startedAt);
  const last = op.items.length > 0 ? op.items[op.items.length - 1] : undefined;
  const progress = op.progress;

  const rows: [MessageKey, string][] = [
    ["app.streamDemo.stat.received", String(op.received)],
    ["app.streamDemo.stat.batches", String(op.batches)],
    ["app.streamDemo.stat.batchesPerSecond", perSecond(op.batches, elapsedMs)],
    ["app.streamDemo.stat.itemsPerSecond", perSecond(op.received, elapsedMs)],
    ["app.streamDemo.stat.elapsed", t("app.streamDemo.stat.elapsedValue", { ms: elapsedMs })],
    ["app.streamDemo.stat.progress", progress ? `${progress.done} / ${progress.total}` : "–"],
    ["app.streamDemo.stat.lastItem", last ? last.text : "–"],
    ["app.streamDemo.stat.missed", String(op.gaps)],
    ["app.streamDemo.stat.operation", op.opId ?? "–"],
  ];

  return (
    <section aria-label={t("app.streamDemo.label")}>
      <div style={{ display: "flex", gap: "0.75rem", justifyContent: "center", flexWrap: "wrap", margin: "0.75rem 0" }}>
        <label>
          {t("app.streamDemo.rate")}{" "}
          <input
            type="number"
            min={1}
            max={DEMO_MAX_RATE_PER_SEC}
            value={rate}
            disabled={op.running}
            onChange={(e: ChangeEvent<HTMLInputElement>) => setRate(clamp(e.target.valueAsNumber, DEMO_MAX_RATE_PER_SEC))}
          />
        </label>
        <label>
          {t("app.streamDemo.duration")}{" "}
          <input
            type="number"
            min={1}
            max={DEMO_MAX_DURATION_MS}
            value={durationMs}
            disabled={op.running}
            onChange={(e: ChangeEvent<HTMLInputElement>) => setDurationMs(clamp(e.target.valueAsNumber, DEMO_MAX_DURATION_MS))}
          />
        </label>
        <button type="button" onClick={() => op.start(rate, durationMs)} disabled={op.running}>
          {t("app.streamDemo.start")}
        </button>
        <button type="button" onClick={op.cancel} disabled={!op.running}>
          {t("app.streamDemo.cancel")}
        </button>
      </div>
      <p role="status">{describeStatus(t, op.status, op.error)}</p>
      <dl style={STATS_STYLE}>
        {rows.map(([term, value]) => (
          <div key={term} style={{ display: "contents" }}>
            <dt>{t(term)}</dt>
            <dd style={{ margin: 0, fontVariantNumeric: "tabular-nums" }}>{value}</dd>
          </div>
        ))}
      </dl>
      <div style={{ height: 460, display: "flex", margin: "0.75rem 0" }}>
        <MessageLog source={source} label={t("app.streamDemo.log.label")} />
      </div>
    </section>
  );
}
