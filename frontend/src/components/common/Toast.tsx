import { useEffect, useId, useLayoutEffect, useRef, useState, useSyncExternalStore, type CSSProperties } from "react";
import { toaster as defaultToaster, type Toast as ToastData, type Toaster } from "../../commands/toasts";
import { useShortcut } from "../shell/useShortcut";
import { IconClose } from "./icons";
import { useT } from "../../i18n/react";
import styles from "./Toast.module.css";

export interface ToastProps {
  toast: ToastData;
  /** Stops the countdown (the pointer is over the toasts, or focus is in them). */
  paused: boolean;
  onDismiss: (id: string) => void;
  onAction: (id: string) => void;
}

/**
 * One toast: icon, message, optional action button and shortcut hint,
 * dismiss button, and a 2px bar showing the time left. It dismisses itself
 * after its duration, counting only while not paused.
 */
export function Toast({ toast, paused, onDismiss, onAction }: ToastProps) {
  const t = useT();
  const { id, duration, message, icon, action, shortcut } = toast;
  const messageId = useId();
  const timed = Number.isFinite(duration) && duration > 0;
  const remaining = useRef(duration);
  const dismissRef = useRef(onDismiss);
  dismissRef.current = onDismiss;

  useEffect(() => {
    if (!timed || paused) return;
    const started = Date.now();
    const timer = setTimeout(() => dismissRef.current(id), Math.max(0, remaining.current));
    return () => {
      clearTimeout(timer);
      remaining.current -= Date.now() - started;
    };
  }, [id, timed, paused]);

  const barStyle: CSSProperties = { animationDuration: `${duration}ms`, animationPlayState: paused ? "paused" : "running" };

  return (
    <div className={styles.toast} data-toast-id={id}>
      <div className={styles.row}>
        {icon !== undefined && <span className={styles.icon}>{icon}</span>}
        <span id={messageId} className={styles.message}>
          {message}
        </span>
        {action !== undefined && (
          <button
            type="button"
            className={styles.action}
            aria-describedby={messageId}
            onClick={() => onAction(id)}
            data-toast-focus=""
          >
            {action.label}
          </button>
        )}
        {shortcut !== undefined && (
          <kbd className={styles.kbd} aria-hidden="true">
            {shortcut}
          </kbd>
        )}
        <button
          type="button"
          className={styles.dismiss}
          aria-label={t("common.toast.dismiss")}
          title={t("common.dismiss")}
          onClick={() => onDismiss(id)}
          data-toast-focus={action === undefined ? "" : undefined}
        >
          <IconClose size={12} strokeWidth={2.4} />
        </button>
      </div>
      {timed && (
        <div className={styles.track} aria-hidden="true">
          <div className={styles.bar} style={barStyle} />
        </div>
      )}
    </div>
  );
}

export interface ToastHostProps {
  /** Default: the app's toasts (commands/toasts). */
  toaster?: Toaster;
}

/**
 * The toasts, stacked at the bottom of the work area, newest at the bottom
 * (at most MAX_TOASTS). Mount once inside a positioned container (the shell's
 * main column).
 *
 * - The list is a polite live region that is always in the page, so screen
 *   readers announce each new toast without taking focus.
 * - Pointer over the stack or focus inside it pauses every countdown.
 * - Toasts are in the Tab order after the status bar; the "Focus
 *   notifications" shortcut (F6) jumps to the newest toast. When the focused
 *   toast goes away, focus moves to the next toast or back to where it came
 *   from.
 * - Unmounting the host (closing the project) clears the toasts.
 */
export function ToastHost({ toaster = defaultToaster }: ToastHostProps) {
  const toasts = useSyncExternalStore(toaster.subscribe, toaster.toasts);
  const t = useT();
  const listRef = useRef<HTMLDivElement>(null);
  const [hovered, setHovered] = useState(false);
  const [focused, setFocused] = useState(false);
  /** Where focus was before it entered the toasts, to give it back. */
  const returnFocus = useRef<HTMLElement | null>(null);
  const hadFocus = useRef(false);

  useEffect(() => () => toaster.clear(), [toaster]);

  useShortcut("notifications.focus", () => {
    const list = listRef.current;
    const targets = list?.querySelectorAll<HTMLElement>("[data-toast-focus]");
    if (!targets || targets.length === 0) return false;
    targets[targets.length - 1].focus();
  });

  // The focused toast was removed: keep focus somewhere useful.
  useLayoutEffect(() => {
    const list = listRef.current;
    if (!hadFocus.current || list === null || list.contains(document.activeElement)) return;
    if (document.activeElement !== null && document.activeElement !== document.body) return;
    const targets = list.querySelectorAll<HTMLElement>("[data-toast-focus]");
    if (targets.length > 0) {
      targets[targets.length - 1].focus();
      return;
    }
    hadFocus.current = false;
    setFocused(false);
    const back = returnFocus.current;
    returnFocus.current = null;
    if (back?.isConnected && back.closest("[inert]") === null) back.focus({ preventScroll: true });
  }, [toasts]);

  // The pointer cannot stay over a stack that is gone.
  useEffect(() => {
    if (toasts.length === 0) setHovered(false);
  }, [toasts.length]);

  const paused = hovered || focused;
  const any = toasts.length > 0;

  return (
    <div
      ref={listRef}
      className={styles.host}
      role={any ? "region" : undefined}
      aria-label={any ? t("common.toast.region") : undefined}
      onPointerEnter={() => setHovered(true)}
      onPointerLeave={() => setHovered(false)}
      onFocus={(e) => {
        const from = e.relatedTarget;
        if (from instanceof HTMLElement && !e.currentTarget.contains(from)) returnFocus.current = from;
        hadFocus.current = true;
        setFocused(true);
      }}
      onBlur={(e) => {
        const to = e.relatedTarget;
        const list = e.currentTarget;
        const left = () => {
          hadFocus.current = false;
          returnFocus.current = null;
          setFocused(false);
        };
        if (to instanceof Node) {
          if (!list.contains(to)) left();
          return;
        }
        // No new target: a click on nothing, the window losing focus, or the
        // focused toast being removed (the layout effect above moves focus).
        setTimeout(() => {
          if (!list.contains(document.activeElement) && document.hasFocus()) left();
        }, 0);
      }}
    >
      <div className={styles.list} aria-live="polite" aria-relevant="additions text">
        {toasts.map((t) => (
          <Toast key={t.id} toast={t} paused={paused} onDismiss={toaster.dismiss} onAction={toaster.runAction} />
        ))}
      </div>
    </div>
  );
}
