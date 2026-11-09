import {
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useSyncExternalStore,
  type CSSProperties,
  type KeyboardEvent,
  type ReactNode,
  type RefObject,
} from "react";
import { createPortal } from "react-dom";
import { confirmQueue as defaultConfirmQueue, type ConfirmQueue, type DialogVariant } from "../../commands/dialogs";
import { DIALOG_SCOPE, SCOPE_ATTRIBUTE } from "../../commands/shortcuts";
import { useT } from "../../i18n/react";
import styles from "./Dialog.module.css";
import { IconClose } from "./icons";

export type { DialogVariant } from "../../commands/dialogs";

export interface DialogProps {
  open: boolean;
  title: ReactNode;
  /** Esc, the close button, Cancel, and a click on the scrim (default variant only). */
  onCancel: () => void;
  /** Confirm button and Enter. Without it the dialog has no footer (only the close button). */
  onConfirm?: () => void;
  /** Default: "OK". */
  confirmLabel?: string;
  /** Default: "Cancel". */
  cancelLabel?: string;
  /** Leaves the Cancel button out of the footer (a dialog whose last step only has Done); Esc and the close button still cancel. */
  hideCancel?: boolean;
  /** An icon before the confirm label. */
  confirmIcon?: ReactNode;
  /** "danger": destructive confirm button, Cancel focused first, the scrim does not cancel. */
  variant?: DialogVariant;
  confirmDisabled?: boolean;
  /** An async confirm is running: spinner on the confirm button, confirm and cancel are blocked. */
  busy?: boolean;
  /** CSS width. Default 440px; never wider than the window. */
  width?: number | string;
  /** The element focused when the dialog opens (see initialFocusTarget for the default). */
  initialFocus?: RefObject<HTMLElement>;
  /** The id of the element that describes the dialog (aria-describedby). */
  describedBy?: string;
  /** Replaces the body's padding and gap, for dialogs laying out their own content. */
  bodyClassName?: string;
  children?: ReactNode;
}

const FOCUSABLE = [
  "a[href]",
  "button:not([disabled])",
  "input:not([disabled]):not([type='hidden'])",
  "select:not([disabled])",
  "textarea:not([disabled])",
  "[contenteditable]:not([contenteditable='false'])",
  "[tabindex]:not([tabindex='-1'])",
].join(", ");

const FIELDS = "[data-autofocus], input:not([disabled]):not([type='hidden']), select:not([disabled]), textarea:not([disabled])";

/**
 * The shared modal dialog: a panel over the scrim, rendered in a portal on
 * document.body.
 *
 * - `role="dialog"`, `aria-modal`, labelled by the title and described by
 *   `describedBy`. Everything else on the page is made `inert` while it is
 *   open, so screen readers and the pointer stay in the dialog, and the page
 *   does not scroll behind it.
 * - Focus moves in on open (see initialFocusTarget), Tab and Shift+Tab wrap
 *   inside it, and focus returns to where it was when it closes.
 * - Esc cancels. Enter confirms, except in a text area, a multi-line editor
 *   (contenteditable, aria-multiline) or on a button or link (they handle
 *   Enter themselves), during IME composition, on key repeat, and while the
 *   confirm button is disabled or busy.
 * - A click on the scrim cancels a default dialog and does nothing for a
 *   danger dialog, so a stray click never decides a delete.
 * - Global shortcuts stop inside it except the ones marked inDialog
 *   (data-shortcut-scope="dialog").
 *
 * Nested dialogs work one level at a time (the inner one makes the outer one
 * inert and gives it back on close) but are not a designed pattern.
 */
export function Dialog(props: DialogProps) {
  if (!props.open) return null;
  return <DialogFrame {...props} />;
}

function DialogFrame({
  title,
  onCancel,
  onConfirm,
  confirmLabel,
  cancelLabel,
  hideCancel = false,
  confirmIcon,
  variant = "default",
  confirmDisabled = false,
  busy = false,
  width = 440,
  initialFocus,
  describedBy,
  bodyClassName,
  children,
}: DialogProps) {
  const t = useT();
  const titleId = useId();
  const backdropRef = useRef<HTMLDivElement>(null);
  const panelRef = useRef<HTMLDivElement>(null);
  const cancelRef = useRef<HTMLButtonElement>(null);
  const confirmRef = useRef<HTMLButtonElement>(null);
  const danger = variant === "danger";

  // Mount: take focus, make the rest of the page inert, lock scrolling. Undo it all on close.
  useLayoutEffect(() => {
    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const backdrop = backdropRef.current;
    const panel = panelRef.current;
    if (!backdrop || !panel) return;

    const madeInert: Element[] = [];
    for (const el of Array.from(document.body.children)) {
      if (el === backdrop || el.hasAttribute("inert")) continue;
      el.setAttribute("inert", "");
      madeInert.push(el);
    }
    const overflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";

    initialFocusTarget(panel, {
      initial: initialFocus?.current ?? null,
      danger,
      cancel: cancelRef.current,
      confirm: confirmRef.current,
    }).focus({ preventScroll: true });

    // Where inert is not enforced (older engines, jsdom), pull focus back in.
    const onFocusIn = (e: FocusEvent) => {
      const target = e.target;
      if (target instanceof Node && madeInert.some((el) => el.contains(target))) panel.focus({ preventScroll: true });
    };
    document.addEventListener("focusin", onFocusIn);

    return () => {
      document.removeEventListener("focusin", onFocusIn);
      madeInert.forEach((el) => el.removeAttribute("inert"));
      document.body.style.overflow = overflow;
      if (previous?.isConnected) previous.focus({ preventScroll: true });
    };
    // Initial focus is decided once, when the dialog opens.
  }, []);

  const cancel = () => {
    if (!busy) onCancel();
  };
  const confirmEnabled = onConfirm !== undefined && !confirmDisabled && !busy;
  const confirm = () => {
    if (confirmEnabled) onConfirm();
  };

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if (e.defaultPrevented || isComposing(e)) return;
    switch (e.key) {
      case "Escape":
        e.preventDefault();
        e.stopPropagation();
        cancel();
        return;
      case "Enter":
        if (onConfirm === undefined || e.repeat || e.shiftKey || e.altKey || e.ctrlKey || e.metaKey) return;
        if (handlesEnterItself(e.target)) return;
        e.preventDefault();
        confirm();
        return;
      case "Tab":
        trapTab(e, panelRef.current);
        return;
    }
  };

  const panelStyle: CSSProperties = { width };
  const scope = { [SCOPE_ATTRIBUTE]: DIALOG_SCOPE };

  return createPortal(
    <div
      ref={backdropRef}
      className={styles.backdrop}
      onPointerDown={(e) => {
        if (e.target !== e.currentTarget) return;
        e.preventDefault(); // keep focus where it is
        if (!danger) cancel();
      }}
    >
      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={describedBy}
        tabIndex={-1}
        className={styles.panel}
        style={panelStyle}
        onKeyDown={onKeyDown}
        {...scope}
      >
        <div className={styles.header}>
          <h2 id={titleId} className={styles.title}>
            {title}
          </h2>
          <button
            type="button"
            className={styles.close}
            aria-label={t("common.close")}
            title={t("common.dialog.closeTitle")}
            aria-disabled={busy || undefined}
            onClick={cancel}
          >
            <IconClose size={12} strokeWidth={2.4} />
          </button>
        </div>
        <div className={bodyClassName ?? styles.body}>{children}</div>
        {onConfirm !== undefined && (
          <div className={styles.footer}>
            {!hideCancel && (
              <button
                ref={cancelRef}
                type="button"
                className={`${styles.button} ${styles.secondary}`}
                aria-disabled={busy || undefined}
                onClick={cancel}
              >
                {cancelLabel ?? t("common.cancel")}
              </button>
            )}
            <button
              ref={confirmRef}
              type="button"
              className={`${styles.button} ${danger ? styles.danger : styles.primary}`}
              disabled={confirmDisabled}
              aria-disabled={busy || undefined}
              aria-busy={busy || undefined}
              data-dialog-confirm=""
              onClick={confirm}
            >
              {busy ? <span className={styles.spinner} aria-hidden="true" /> : confirmIcon}
              {confirmLabel ?? t("common.ok")}
            </button>
          </div>
        )}
      </div>
    </div>,
    document.body,
  );
}

interface FocusChoice {
  initial: HTMLElement | null;
  danger: boolean;
  cancel: HTMLButtonElement | null;
  confirm: HTMLButtonElement | null;
}

/**
 * What gets focus when a dialog opens: the initialFocus element; else, in a
 * danger dialog, Cancel; else the element marked data-autofocus or the first
 * form field; else the confirm button when enabled; else the first focusable
 * element (Cancel, the close button); else the panel itself.
 */
export function initialFocusTarget(panel: HTMLElement, { initial, danger, cancel, confirm }: FocusChoice): HTMLElement {
  if (initial !== null && panel.contains(initial)) return initial;
  if (danger && cancel !== null) return cancel;
  const field = panel.querySelector<HTMLElement>(FIELDS);
  if (field !== null) return field;
  if (confirm !== null && !confirm.disabled) return confirm;
  if (cancel !== null) return cancel;
  return panel.querySelector<HTMLElement>(FOCUSABLE) ?? panel;
}

function isComposing(e: KeyboardEvent): boolean {
  return e.nativeEvent.isComposing || e.keyCode === 229;
}

/** Elements where Enter means something else than "confirm the dialog". */
function handlesEnterItself(target: EventTarget): boolean {
  if (!(target instanceof Element)) return false;
  if (target.closest("textarea, button, a[href], select, summary, [role='button'], [role='link'], [role='option'], [aria-multiline='true']")) {
    return true;
  }
  return target.closest("[contenteditable]:not([contenteditable='false'])") !== null;
}

function trapTab(e: KeyboardEvent, panel: HTMLElement | null): void {
  if (panel === null) return;
  const items = Array.from(panel.querySelectorAll<HTMLElement>(FOCUSABLE)).filter((el) => el.closest("[hidden], [inert]") === null);
  if (items.length === 0) {
    e.preventDefault();
    return;
  }
  const first = items[0];
  const last = items[items.length - 1];
  const active = document.activeElement;
  const outside = active === panel || !items.includes(active as HTMLElement);
  if (e.shiftKey && (active === first || outside)) {
    e.preventDefault();
    last.focus();
  } else if (!e.shiftKey && (active === last || outside)) {
    e.preventDefault();
    first.focus();
  }
}

export interface ConfirmHostProps {
  /** Default: the app's queue (commands/dialogs). */
  queue?: ConfirmQueue;
}

/** Shows the confirm() requests one at a time. Mount once, in the app shell. */
export function ConfirmHost({ queue = defaultConfirmQueue }: ConfirmHostProps) {
  const request = useSyncExternalStore(queue.subscribe, queue.current);
  const messageId = useId();
  useEffect(() => queue.attach(), [queue]);
  if (request === null) return null;
  return (
    <Dialog
      key={request.id}
      open
      title={request.title}
      describedBy={request.message !== undefined ? messageId : undefined}
      variant={request.variant}
      confirmLabel={request.confirmLabel}
      cancelLabel={request.cancelLabel}
      confirmIcon={request.confirmIcon}
      hideCancel={request.hideCancel}
      width={request.width}
      onCancel={() => queue.settle(request.id, false)}
      onConfirm={() => queue.settle(request.id, true)}
    >
      {request.message !== undefined && (
        <p id={messageId} className={styles.message}>
          {request.message}
        </p>
      )}
      {request.details}
    </Dialog>
  );
}
