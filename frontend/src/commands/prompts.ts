// Values asked for at send time (DYN-07): askPrompts(fields) opens one
// dialog with a field per {{$prompt}} label, through the PromptHost mounted
// once in the app shell, and resolves with the values by label, or null
// when the user cancels. The values are handed to the caller only: nothing
// here keeps them after the dialog closes. See docs/templates.md (Prompts).
//
// Same queue pattern as confirm() in commands/dialogs.ts: pending requests
// hold resolve functions, so they live here instead of in a store.

import type { PromptField } from "../lib/http";

/** What the values are for: one send, or every send of a run (HTTP-27). */
export type PromptPurpose = "send" | "run";

export interface PromptOptions {
  readonly fields: readonly PromptField[];
  readonly purpose?: PromptPurpose;
}

export interface PromptRequest extends PromptOptions {
  readonly id: number;
}

/** The values typed, by label. */
export type PromptValues = Record<string, string>;

export interface PromptQueue {
  /** Queues a request; resolves with the values, or null on cancel or when no host is mounted. */
  request(options: PromptOptions): Promise<PromptValues | null>;
  /** The request shown now (the oldest unanswered one), or null. */
  current(): PromptRequest | null;
  subscribe(listener: () => void): () => void;
  /** Answers request `id` (null: cancelled). Unknown or already answered ids are ignored. */
  settle(id: number, values: PromptValues | null): void;
  /** Called by a mounted host. Returns the detach function, which cancels what is still pending. */
  attach(): () => void;
}

export function createPromptQueue(): PromptQueue {
  let nextId = 1;
  let hosts = 0;
  let queue: { request: PromptRequest; resolve: (values: PromptValues | null) => void }[] = [];
  const listeners = new Set<() => void>();
  const notify = () => listeners.forEach((l) => l());

  return {
    request(options) {
      if (hosts === 0) {
        console.error("prompts: askPrompts() called without a mounted PromptHost; treated as cancelled");
        return Promise.resolve(null);
      }
      return new Promise<PromptValues | null>((resolve) => {
        queue = [...queue, { request: { ...options, id: nextId++ }, resolve }];
        notify();
      });
    },
    current() {
      return queue[0]?.request ?? null;
    },
    subscribe(listener) {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    settle(id, values) {
      const entry = queue.find((e) => e.request.id === id);
      if (entry === undefined) return;
      queue = queue.filter((e) => e !== entry);
      entry.resolve(values);
      notify();
    },
    attach() {
      hosts++;
      return () => {
        hosts--;
        if (hosts > 0 || queue.length === 0) return;
        const pending = queue;
        queue = [];
        pending.forEach((e) => e.resolve(null));
        notify();
      };
    },
  };
}

/** The app's prompt queue, shown by PromptHost. */
export const promptQueue: PromptQueue = createPromptQueue();

/**
 * Asks for the values of `fields` in one dialog. Resolves with them by
 * label, or null when the user cancels (the send is then not made).
 */
export function askPrompts(fields: readonly PromptField[], purpose: PromptPurpose = "send"): Promise<PromptValues | null> {
  return promptQueue.request({ fields, purpose });
}
