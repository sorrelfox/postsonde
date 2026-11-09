import { afterAll, afterEach } from "vitest";
import { cleanup } from "@testing-library/react";

// Vitest globals are off (tests import from "vitest" explicitly), so
// Testing Library can't register its automatic cleanup. Unmount rendered
// trees after each test so tests don't leak DOM into each other.
afterEach(() => {
  cleanup();
});

// Importing @wailsio/runtime starts a 50ms `window.setInterval` in its
// drag module whose first tick calls `window.clearInterval`. When a test
// file ends within those 50ms, the tick runs after Vitest has torn jsdom
// down and fails the run with "window is not defined". Track the intervals
// started in the file and clear the ones still running once it is done.
// (`window` is `globalThis` in Vitest's jsdom environment.)
const runningIntervals = new Set<ReturnType<typeof setInterval>>();
const realSetInterval = globalThis.setInterval;
const realClearInterval = globalThis.clearInterval;
globalThis.setInterval = ((...args: Parameters<typeof setInterval>) => {
  const id = realSetInterval(...args);
  runningIntervals.add(id);
  return id;
}) as typeof setInterval;
globalThis.clearInterval = ((id?: ReturnType<typeof setInterval>) => {
  if (id !== undefined) runningIntervals.delete(id);
  realClearInterval(id);
}) as typeof clearInterval;
afterAll(() => {
  for (const id of runningIntervals) realClearInterval(id);
  runningIntervals.clear();
});

// jsdom has no layout: Range lacks getClientRects/getBoundingClientRect,
// which CodeMirror calls when it measures the selection and cursor.
if (typeof Range !== "undefined" && typeof Range.prototype.getClientRects !== "function") {
  Range.prototype.getClientRects = function getClientRects() {
    return Object.assign([], { item: () => null }) as unknown as DOMRectList;
  };
  Range.prototype.getBoundingClientRect = function getBoundingClientRect() {
    return new DOMRect(0, 0, 0, 0);
  };
}
