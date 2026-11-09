// Promise-based confirmation dialogs: confirm({ title, message }) opens
// the shared Dialog through the ConfirmHost mounted once in the app shell and
// resolves true (confirmed) or false (cancelled). See docs/frontend.md (Dialogs).
//
// Pending requests hold resolve functions, which are not plain data, so they
// live in this small queue next to the commands instead of in a store (the
// same pattern as shortcutBindings in commands/shortcuts.ts).

import type { ReactNode } from "react";

export type DialogVariant = "default" | "danger";

export interface ConfirmOptions {
  title: string;
  /** The question's details, shown as the dialog's description. */
  message?: ReactNode;
  /** Default: "OK". */
  confirmLabel?: string;
  /** Default: "Cancel". */
  cancelLabel?: string;
  /** "danger" draws the destructive button and focuses Cancel first. */
  variant?: DialogVariant;
  /** An icon before the confirm label (the trash icon for deletes). */
  confirmIcon?: ReactNode;
  /** Shown under the message: content that is not a paragraph (a list, a report). */
  details?: ReactNode;
  /** Only the confirm button (a dialog that informs, with Close); Esc still cancels. */
  hideCancel?: boolean;
  /** CSS width of the dialog. Default 440px. */
  width?: number | string;
}

export interface ConfirmRequest extends ConfirmOptions {
  readonly id: number;
}

export interface ConfirmQueue {
  /** Queues a confirmation; resolves when the user answers. Resolves false when no host is mounted. */
  request(options: ConfirmOptions): Promise<boolean>;
  /** The request shown now (the oldest unanswered one), or null. */
  current(): ConfirmRequest | null;
  subscribe(listener: () => void): () => void;
  /** Answers request `id`. Unknown or already answered ids are ignored. */
  settle(id: number, confirmed: boolean): void;
  /** Called by a mounted host. Returns the detach function, which cancels what is still pending. */
  attach(): () => void;
}

export function createConfirmQueue(): ConfirmQueue {
  let nextId = 1;
  let hosts = 0;
  let queue: { request: ConfirmRequest; resolve: (ok: boolean) => void }[] = [];
  const listeners = new Set<() => void>();
  const notify = () => listeners.forEach((l) => l());

  return {
    request(options) {
      if (hosts === 0) {
        console.error("dialogs: confirm() called without a mounted ConfirmHost; treated as cancelled");
        return Promise.resolve(false);
      }
      return new Promise<boolean>((resolve) => {
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
    settle(id, confirmed) {
      const entry = queue.find((e) => e.request.id === id);
      if (entry === undefined) return;
      queue = queue.filter((e) => e !== entry);
      entry.resolve(confirmed);
      notify();
    },
    attach() {
      hosts++;
      return () => {
        hosts--;
        if (hosts > 0 || queue.length === 0) return;
        const pending = queue;
        queue = [];
        pending.forEach((e) => e.resolve(false));
        notify();
      };
    },
  };
}

/** The app's confirmation queue, shown by ConfirmHost. */
export const confirmQueue: ConfirmQueue = createConfirmQueue();

/** Asks the user to confirm. Resolves true when they confirm, false when they cancel. */
export function confirm(options: ConfirmOptions): Promise<boolean> {
  return confirmQueue.request(options);
}
