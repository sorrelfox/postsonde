// A frame scheduler for MessageSource tests: nothing flushes until frame() runs.
/** A scheduler the test runs by hand: `frame()` runs the pending flush. */
export function manualFrames() {
  let pending: (() => void) | null = null;
  let scheduled = 0;
  return {
    schedule: (flush: () => void) => {
      scheduled++;
      pending = flush;
      return () => {
        if (pending === flush) pending = null;
      };
    },
    frame: () => {
      const f = pending;
      pending = null;
      f?.();
    },
    get scheduled() {
      return scheduled;
    },
    get pending() {
      return pending !== null;
    },
  };
}
