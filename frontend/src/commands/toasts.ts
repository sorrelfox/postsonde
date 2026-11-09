// Toast notifications: showToast({ message, action }) shows a short
// notice at the bottom of the work area through the ToastHost mounted in the
// app shell, with an optional action such as Undo. See docs/frontend.md (Toasts).
//
// Toasts carry callbacks (the action), which are not plain data, so they live
// in this small list next to the commands instead of in a store (the same
// pattern as shortcutBindings in commands/shortcuts.ts).

import type { ReactNode } from "react";

/** How long a toast stays, in ms, unless given a duration. */
export const DEFAULT_TOAST_DURATION_MS = 8000;

/** At most this many toasts show at once; a new one drops the oldest. */
export const MAX_TOASTS = 3;

export interface ToastAction {
  label: string;
  /** Runs when the action is chosen; the toast is dismissed first. */
  run: () => void | Promise<unknown>;
}

export interface ToastOptions {
  message: ReactNode;
  /** A small leading icon (decorative). */
  icon?: ReactNode;
  action?: ToastAction;
  /** The action's shortcut as shown, e.g. "⌘Z". Display only: the toast does not bind it. */
  shortcut?: string;
  /** ms before it dismisses itself. 0 or Infinity: stays until dismissed. Default 8000. */
  duration?: number;
}

export interface Toast extends ToastOptions {
  readonly id: string;
  readonly duration: number;
}

export interface Toaster {
  /** Shows a toast; returns its id. */
  show(options: ToastOptions): string;
  /** Removes a toast. Unknown ids are ignored. */
  dismiss(id: string): void;
  /** Dismisses the toast, then runs its action. */
  runAction(id: string): void;
  /** Removes every toast. */
  clear(): void;
  /** The toasts shown, oldest first. The same array until it changes. */
  toasts(): readonly Toast[];
  subscribe(listener: () => void): () => void;
}

export function createToaster(max = MAX_TOASTS): Toaster {
  let nextId = 1;
  let list: readonly Toast[] = [];
  const listeners = new Set<() => void>();
  const set = (next: readonly Toast[]) => {
    list = next;
    listeners.forEach((l) => l());
  };
  const dismiss = (id: string) => {
    if (list.some((t) => t.id === id)) set(list.filter((t) => t.id !== id));
  };

  return {
    show(options) {
      const id = `toast-${nextId++}`;
      const duration = options.duration ?? DEFAULT_TOAST_DURATION_MS;
      set([...list, { ...options, id, duration }].slice(-max));
      return id;
    },
    dismiss,
    runAction(id) {
      const toast = list.find((t) => t.id === id);
      if (toast === undefined) return;
      dismiss(id);
      try {
        Promise.resolve(toast.action?.run()).catch((err: unknown) => console.warn("toasts: action failed", err));
      } catch (err) {
        console.warn("toasts: action failed", err);
      }
    },
    clear() {
      if (list.length > 0) set([]);
    },
    toasts() {
      return list;
    },
    subscribe(listener) {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
  };
}

/** The app's toasts, shown by ToastHost. */
export const toaster: Toaster = createToaster();

/** Shows a toast; returns its id for dismissToast. */
export function showToast(options: ToastOptions): string {
  return toaster.show(options);
}

export function dismissToast(id: string): void {
  toaster.dismiss(id);
}
