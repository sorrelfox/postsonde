// A fake of the Wails runtime's event bus and of OpsService, for tests of
// code that follows Go operations (docs/events.md). Use it from vi.mock
// factories, which are hoisted above imports:
//
//   vi.mock("@wailsio/runtime", async () => (await import("../test/fakeWails")).fakeRuntime);
//   vi.mock("<path to bindings>/internal/app/services/ops", async () =>
//     (await import("../test/fakeWails")).fakeOpsBindings);
import { vi } from "vitest";

type Listener = (event: { name: string; data: unknown }) => void;

const listeners = new Map<string, Set<Listener>>();

/** Stands in for the runtime's CancelError, so isCancelled() works. */
export class FakeCancelError extends Error {
  constructor() {
    super("call cancelled");
    this.name = "CancelError";
  }
}

export const fakeRuntime = {
  Events: {
    On: vi.fn((name: string, callback: Listener) => {
      let set = listeners.get(name);
      if (!set) {
        set = new Set();
        listeners.set(name, set);
      }
      set.add(callback);
      return () => {
        listeners.get(name)?.delete(callback);
      };
    }),
  },
  CancelError: FakeCancelError,
  Clipboard: {
    /** Resolves; tests read what was copied from its calls. */
    SetText: vi.fn((_text: string) => Promise.resolve()),
  },
  Browser: {
    /** Resolves; tests read what was opened from its calls. */
    OpenURL: vi.fn((_url: string | URL) => Promise.resolve()),
  },
};

/** OpsService.Cancel: resolves to true. */
export const opsCancel = vi.fn((_opId: string) => Promise.resolve(true));

export const fakeOpsBindings = { OpsService: { Cancel: opsCancel } };

/** Emits an event from "Go" to the current listeners, synchronously. */
export function emit(name: string, data: unknown): void {
  for (const callback of [...(listeners.get(name) ?? [])]) {
    callback({ name, data });
  }
}

/** Number of listeners still subscribed, over all event names. */
export function listenerCount(): number {
  let n = 0;
  for (const set of listeners.values()) n += set.size;
  return n;
}

/** Clears listeners and mock calls; call in beforeEach. */
export function resetFakeWails(): void {
  listeners.clear();
  fakeRuntime.Events.On.mockClear();
  fakeRuntime.Clipboard.SetText.mockClear();
  fakeRuntime.Browser.OpenURL.mockClear();
  opsCancel.mockClear();
}

/** A promise with its resolve and reject, to control a start call. */
export function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

/** Lets pending promise callbacks run. */
export function settle(): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, 0));
}
