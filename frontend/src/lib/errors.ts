// The shared error model on the UI side. See docs/errors.md.
//
// Go services return internal/core/errs errors. The app's MarshalError turns
// each into an ErrorDTO (internal/app/apperr) and Wails rejects the call's
// promise with a RuntimeError whose `cause` is that DTO. toAppError turns
// any rejection reason into an AppError, so components never look at the
// raw reason.
//
// Go writes error messages and hints in English. An error with a code the
// UI language has a message for (i18n/en/errors.ts) gets that message
// instead, with its details as placeholders; see
// docs/frontend.md#localization.
import { CancelError } from "@wailsio/runtime";
import { errorText, t } from "../i18n";

/** Every error kind, in the order of internal/core/errs. */
export const ERROR_KINDS = [
  "invalid_argument",
  "not_found",
  "already_exists",
  "permission_denied",
  "cancelled",
  "timeout",
  "network",
  "tls",
  "protocol",
  "parse",
  "io",
  "internal",
  "unsupported",
] as const;

export type ErrorKind = (typeof ERROR_KINDS)[number];

/** An error from Go or the runtime; the shape of apperr.ErrorDTO. */
export interface AppError {
  kind: ErrorKind;
  /** Finer machine code such as "system.wait_out_of_range", or "". */
  code: string;
  /** User-facing message; never empty. */
  message: string;
  /** What the user can do about it, or "". */
  hint: string;
  details: Record<string, string>;
  /** Underlying error messages, outermost first, for diagnostics. */
  causes: string[];
}

// The texts of defaultMessages in internal/app/apperr, in the UI language.
const cancelledMessage = () => t("lib.errors.cancelled");
const internalMessage = () => t("lib.errors.internal");

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

function isErrorKind(v: unknown): v is ErrorKind {
  return typeof v === "string" && (ERROR_KINDS as readonly string[]).includes(v);
}

function isStringRecord(v: unknown): v is Record<string, string> {
  return isRecord(v) && Object.values(v).every((x) => typeof x === "string");
}

function isStringArray(v: unknown): v is string[] {
  return Array.isArray(v) && v.every((x) => typeof x === "string");
}

function appError(kind: ErrorKind, message: string, causes: string[] = []): AppError {
  return { kind, code: "", message, hint: "", details: {}, causes };
}

/** Reports whether v has the full AppError shape, every field present. */
export function isAppError(v: unknown): v is AppError {
  return (
    isRecord(v) &&
    isErrorKind(v.kind) &&
    typeof v.code === "string" &&
    typeof v.message === "string" &&
    typeof v.hint === "string" &&
    isStringRecord(v.details) &&
    isStringArray(v.causes)
  );
}

// Reads an ErrorDTO leniently: it needs a string kind and message, fills in
// missing fields, and treats a kind this UI does not know as internal.
function fromDTO(v: unknown): AppError | null {
  if (!isRecord(v) || typeof v.kind !== "string" || typeof v.message !== "string") {
    return null;
  }
  const code = typeof v.code === "string" ? v.code : "";
  const details = isStringRecord(v.details) ? { ...v.details } : {};
  const hint = typeof v.hint === "string" ? v.hint : "";
  return {
    kind: isErrorKind(v.kind) ? v.kind : "internal",
    code,
    message: errorText(code, "message", v.message || internalMessage(), details),
    hint: errorText(code, "hint", hint, details),
    details,
    causes: isStringArray(v.causes) ? [...v.causes] : [],
  };
}

/**
 * Converts any rejection reason into an AppError.
 *
 * - A cancelled call (the runtime's CancelError) becomes `cancelled`.
 * - A bare ErrorDTO (the `error` of an operation's done event) is read like
 *   a `cause`: missing fields filled in, unknown kinds become `internal`.
 * - A Wails RuntimeError or TypeError carries the ErrorDTO in `cause` (an
 *   array of them if a method returned several errors; the first is used).
 * - Any other Error or string (a failed fetch, an unknown binding, an error
 *   thrown by UI code) becomes `internal` with its own message. Go errors
 *   always arrive with an ErrorDTO, so Go's raw `err.Error()` (which Wails
 *   puts in RuntimeError.message) is never used.
 * - Anything else becomes `internal` with a generic message.
 *
 * An error saying the stored secrets are locked also tells the listener of
 * setLockedErrorListener, which asks for the master password.
 */
export function toAppError(reason: unknown): AppError {
  const err = convert(reason);
  if (err.code === LOCKED_CODE && lockedListener !== null) {
    // After the caller's render or handler: the listener updates a store.
    const listener = lockedListener;
    queueMicrotask(() => listener(err));
  }
  return err;
}

/** The code of a call refused because the stored secrets are locked. */
const LOCKED_CODE = "secretstore.locked";

let lockedListener: ((err: AppError) => void) | null = null;

/**
 * Registers the one listener told when an error says the stored secrets
 * are locked (secretstore.locked): the shell asks for the master password
 * then, on first use of a secret (docs/privacy.md#encrypted-secrets).
 * Returns the function that removes it.
 */
export function setLockedErrorListener(listener: (err: AppError) => void): () => void {
  lockedListener = listener;
  return () => {
    if (lockedListener === listener) lockedListener = null;
  };
}

function convert(reason: unknown): AppError {
  if (reason instanceof CancelError) {
    return appError("cancelled", cancelledMessage());
  }
  if (isAppError(reason)) {
    return reason;
  }
  if (isRecord(reason) && !(reason instanceof Error)) {
    // A bare ErrorDTO, e.g. the `error` of an operation's done event.
    const dto = fromDTO(reason);
    if (dto) {
      return dto;
    }
  }
  if (reason instanceof Error) {
    // Error.cause is ES2022; tsconfig targets ES2020 typings.
    const cause: unknown = (reason as { cause?: unknown }).cause;
    const candidates = Array.isArray(cause) ? cause : [cause];
    for (const c of candidates) {
      const dto = fromDTO(c);
      if (dto) {
        return dto;
      }
    }
    return appError("internal", reason.message || internalMessage());
  }
  if (typeof reason === "string" && reason !== "") {
    return appError("internal", reason);
  }
  return appError("internal", internalMessage());
}

/**
 * Reports whether a call rejected because it was cancelled: with
 * `.cancel()` (CancelError) or by Go returning a `cancelled` error.
 */
export function isCancelled(reason: unknown): boolean {
  return toAppError(reason).kind === "cancelled";
}

/** A short title for an error kind, e.g. "Invalid input". */
export function errorTitle(kind: ErrorKind): string {
  return t(`lib.errors.kind.${isErrorKind(kind) ? kind : "internal"}`);
}

/** One line for a status bar or toast: the message, then the hint. */
export function formatError(reason: unknown): string {
  const e = toAppError(reason);
  return e.hint ? t("lib.errors.withHint", { message: e.message, hint: e.hint }) : e.message;
}
