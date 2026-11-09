// English messages of src/App.tsx, src/main.tsx and the files directly in src/components/ (namespace "app"): t("app.<id>").
// See docs/frontend.md#localization.
import type { Messages } from "../messages";

export default {
  // StreamDemo
  "streamDemo.label": "Event streaming demo",
  "streamDemo.status.idle": "Not started",
  "streamDemo.status.starting": "Starting…",
  "streamDemo.status.running": "Streaming…",
  "streamDemo.status.ok": "Finished",
  "streamDemo.status.cancelled": "Cancelled",
  "streamDemo.status.error": "Failed: {error}",
  "streamDemo.log.started": "Demo stream started ({operation})",
  "streamDemo.log.finished": "Demo stream finished",
  "streamDemo.log.cancelled": "Demo stream cancelled",
  "streamDemo.log.label": "Demo stream messages",
  "streamDemo.rate": "Items / s",
  "streamDemo.duration": "Duration (ms)",
  "streamDemo.start": "Start stream",
  "streamDemo.cancel": "Cancel stream",
  "streamDemo.stat.received": "Items received",
  "streamDemo.stat.batches": "Batches (UI updates)",
  "streamDemo.stat.batchesPerSecond": "Batches / s",
  "streamDemo.stat.itemsPerSecond": "Items / s",
  "streamDemo.stat.elapsed": "Elapsed",
  "streamDemo.stat.elapsedValue": "{ms} ms",
  "streamDemo.stat.progress": "Progress",
  "streamDemo.stat.lastItem": "Last item",
  "streamDemo.stat.missed": "Missed events",
  "streamDemo.stat.operation": "Operation",

  // streamDemoMessages
  "streamDemo.message.simulatedError": "Simulated error after item {index}",

  // SystemDemo
  "systemDemo.label": "Go bindings demo",
  "systemDemo.status.idle": "Not started",
  "systemDemo.status.running": "Waiting in Go…",
  "systemDemo.status.done": "Done after {ms} ms",
  "systemDemo.status.cancelled": "Cancelled after {ms} ms",
  "systemDemo.status.failed": "Failed",
  "systemDemo.unavailable": "Go backend unavailable: {error}",
  "systemDemo.connecting": "Connecting to Go…",
  "systemDemo.run": "Run {seconds} s",
  "systemDemo.badInput": "Bad input",
} as const satisfies Messages;
