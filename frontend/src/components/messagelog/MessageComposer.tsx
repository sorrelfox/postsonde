import { useCallback, useEffect, useMemo, useRef, useState, type KeyboardEvent, type ReactNode } from "react";
import type { EditorView } from "@codemirror/view";
import { useJsonCheck } from "../../commands/useJsonCheck";
import type { AppError } from "../../lib/errors";
import { isCancelled, toAppError } from "../../lib/errors";
import { beautifyJson } from "../../lib/format";
import { parseBase64 } from "../../lib/base64Bytes";
import { parseHex } from "../../lib/hexBytes";
import { ariaShortcut, matchesShortcut, shortcutLabel } from "../../lib/shortcuts";
import { IconPlay, IconWarning } from "../common/icons";
import { useShortcut } from "../shell/useShortcut";
import { useT } from "../../i18n/react";
import { CodeEditor, type CodeEditorHandle } from "../editor/CodeEditor";
import { installErrorMarker, markerRange, setErrorMarker } from "../editor/errorMarker";
import { formatByteCount } from "./format";
import styles from "./MessageComposer.module.css";

export type ComposerFormat = "text" | "json" | "hex" | "base64";

// i18n-ignore: names of formats, the same in every language ("Text" comes from the catalogue)
const FORMAT_NAMES: Readonly<Record<Exclude<ComposerFormat, "text">, string>> = { json: "JSON", hex: "Hex", base64: "Base64" };

const DEFAULT_FORMATS: readonly ("text" | "json")[] = ["text", "json"];

/** One send button of the composer. */
export interface ComposerAction {
  /** The button text ("Send", "Send to all"). */
  readonly label: string;
  /** Disabled for a reason of the caller (not connected, no client). */
  readonly disabled?: boolean;
  /** The tooltip, e.g. why it is disabled. */
  readonly title?: string;
  /** Sends the message; a rejection (an AppError) is shown next to the buttons. */
  readonly send: (text: string) => Promise<unknown>;
}

export interface MessageComposerProps<F extends ComposerFormat = "text" | "json"> {
  readonly format: F;
  readonly onFormatChange: (format: F) => void;
  readonly message: string;
  readonly onMessageChange: (text: string) => void;
  /** Nothing can be edited; sending still works. */
  readonly readOnly?: boolean;
  /** The send buttons, in order; the first is the primary one that ⌘Enter runs. */
  readonly actions: readonly ComposerAction[];
  /** The accessible name of the message editor. */
  readonly ariaLabel?: string;
  /** The formats offered, in order. Default text and JSON; hex and Base64 bytes are checked while typing. */
  readonly formats?: readonly F[];
  /** More controls for the bar, after the format buttons (a line ending). */
  readonly extra?: ReactNode;
}

function errorText(err: AppError): string {
  return err.hint ? `${err.message} ${err.hint}` : err.message;
}

/** The UTF-16 position of the code point at `offset` in `text`. */
function utf16Offset(text: string, offset: number): number {
  return Array.from(text).slice(0, offset).join("").length;
}

/**
 * A message composer for the connection views (WS-03, SSRV-01, SSRV-05):
 * text, JSON (checked in Go while typing, with the error marked in the
 * editor, and beautified with the button or ⇧⌘F), or bytes as hex or
 * Base64 (WS-04; checked while typing, lib/hexBytes.ts and
 * lib/base64Bytes.ts, with their count shown), sent with the buttons or
 * ⌘Enter (the first action). The text is kept after sending. The owner
 * keeps the format and the text.
 */
export function MessageComposer<F extends ComposerFormat = "text" | "json">({
  format,
  onFormatChange,
  message,
  onMessageChange,
  readOnly = false,
  actions,
  ariaLabel,
  formats = DEFAULT_FORMATS as readonly F[],
  extra,
}: MessageComposerProps<F>) {
  const t = useT();
  const json = format === "json";
  const bytes = useMemo(() => (format === "hex" ? parseHex(message) : format === "base64" ? parseBase64(message) : null), [format, message]);
  const bytesError = bytes !== null && !bytes.ok ? bytes : null;
  const bytesEmpty = bytes !== null && bytes.ok && bytes.bytes.length === 0;

  const [sendError, setSendError] = useState<AppError | null>(null);
  const [formatFailure, setFormatFailure] = useState<string | null>(null);
  const [sending, setSending] = useState(false);
  const [formatting, setFormatting] = useState(false);

  const editorRef = useRef<CodeEditorHandle>(null);
  const [view, setView] = useState<EditorView | null>(null);
  const onReady = useCallback((v: EditorView) => {
    installErrorMarker(v);
    setView(v);
  }, []);

  const check = useJsonCheck(message, json);
  const jsonError = json && check.error !== null && check.text === message ? check.error : null;
  useEffect(() => {
    if (view === null) return;
    const doc = view.state.doc.toString();
    if (bytesError !== null && doc === message) {
      view.dispatch({ effects: setErrorMarker.of({ offset: utf16Offset(doc, bytesError.offset), message: bytesError.message }) });
      return;
    }
    const error = check.error !== null && json && check.text === doc ? check.error : null;
    view.dispatch({ effects: setErrorMarker.of(error === null ? null : { offset: error.offset, message: error.message }) });
  }, [view, check, json, bytesError, message]);

  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  const goTo = useCallback((offset: number) => {
    const v = editorRef.current?.view;
    if (!v) return;
    const range = markerRange(v.state.doc, offset);
    v.dispatch({ selection: { anchor: range?.from ?? 0 }, scrollIntoView: true });
    v.focus();
  }, []);

  const latestMessage = useRef(message);
  latestMessage.current = message;

  const beautify = useCallback(async () => {
    const text = latestMessage.current;
    if (!json || readOnly || text.trim() === "") return;
    setFormatting(true);
    setFormatFailure(null);
    try {
      const result = await beautifyJson(text);
      if (!mounted.current || latestMessage.current !== text) return;
      if (result.error !== null) {
        editorRef.current?.view?.dispatch({ effects: setErrorMarker.of({ offset: result.error.offset, message: result.error.message }) });
        goTo(result.error.offset);
      } else if (result.valid) {
        onMessageChange(result.text);
      }
    } catch (err: unknown) {
      if (mounted.current && !isCancelled(err)) setFormatFailure(toAppError(err).message);
    } finally {
      if (mounted.current) setFormatting(false);
    }
  }, [json, readOnly, onMessageChange, goTo]);

  useShortcut(
    "request.beautify",
    () => {
      void beautify();
    },
    json && !readOnly,
  );

  const canSend = (action: ComposerAction | undefined): action is ComposerAction =>
    action !== undefined && !action.disabled && !sending && message !== "" && jsonError === null && bytesError === null && !bytesEmpty;

  const send = (action: ComposerAction | undefined) => {
    if (!canSend(action)) return;
    setSending(true);
    setSendError(null);
    action
      .send(message)
      .catch((err: unknown) => {
        if (mounted.current) setSendError(toAppError(err));
      })
      .finally(() => {
        if (mounted.current) setSending(false);
      });
  };

  // ⌘Enter runs the first action. Handled in the capture phase, before
  // CodeMirror's own Mod-Enter (insert a blank line) sees it.
  const onKeyDownCapture = (e: KeyboardEvent<HTMLDivElement>) => {
    if (matchesShortcut(e, "composer.send")) {
      e.preventDefault();
      e.stopPropagation();
      send(actions[0]);
    }
  };

  const onText = useCallback(
    (text: string) => {
      setSendError(null);
      if (!readOnly) onMessageChange(text);
    },
    [onMessageChange, readOnly],
  );

  const failure = sendError !== null ? errorText(sendError) : formatFailure;

  return (
    <div className={styles.composer} data-shortcut-scope="composer" onKeyDownCapture={onKeyDownCapture}>
      <div className={styles.composerBar}>
        <div className={styles.segmented} role="group" aria-label={t("messagelog.composer.format")}>
          {formats.map((f) => (
            <button
              key={f}
              type="button"
              className={styles.segment}
              aria-pressed={format === f}
              disabled={readOnly}
              onClick={() => {
                setSendError(null);
                onFormatChange(f);
              }}
            >
              {f === "text" ? t("messagelog.composer.format.text") : FORMAT_NAMES[f as Exclude<ComposerFormat, "text">]}
            </button>
          ))}
        </div>
        {json && (
          <button
            type="button"
            className={styles.smallButton}
            disabled={readOnly || formatting || message.trim() === ""}
            title={t("messagelog.composer.beautifyTitle", { shortcut: shortcutLabel("request.beautify") })}
            aria-keyshortcuts={ariaShortcut("request.beautify")}
            onClick={() => void beautify()}
          >
            {t("messagelog.composer.beautify")}
          </button>
        )}
        {extra}
        {bytes !== null && bytes.ok && bytes.bytes.length > 0 && (
          <span className={styles.byteCount}>
            {formatByteCount(bytes.bytes.length)}
          </span>
        )}
        <p className={styles.composerError} aria-live="polite">
          {jsonError !== null && (
            <>
              <IconWarning size={12} />
              <button type="button" className={styles.position} onClick={() => goTo(jsonError.offset)}>
                {t("messagelog.composer.position", { line: jsonError.line, column: jsonError.column })}
              </button>
              <span>{jsonError.message}</span>
            </>
          )}
          {bytesError !== null && (
            <>
              <IconWarning size={12} />
              <button type="button" className={styles.position} onClick={() => goTo(utf16Offset(message, bytesError.offset))}>
                {bytesError.message}
              </button>
            </>
          )}
          {jsonError === null && bytesError === null && failure !== null && (
            <>
              <IconWarning size={12} />
              <span>{failure}</span>
            </>
          )}
        </p>
        <kbd className={styles.kbd}>{shortcutLabel("composer.send")}</kbd>
        {actions.map((action, i) => (
          <button
            key={i}
            type="button"
            className={i === 0 ? styles.send : `${styles.send} ${styles.secondary}`}
            onClick={() => send(action)}
            disabled={!canSend(action)}
            title={action.title}
            aria-keyshortcuts={i === 0 ? ariaShortcut("composer.send") : undefined}
          >
            {i === 0 && <IconPlay size={11} />}
            {action.label}
          </button>
        ))}
      </div>
      <div className={styles.messageFrame} data-invalid={jsonError !== null || undefined}>
        <CodeEditor
          ref={editorRef}
          value={message}
          language={json ? "json" : "text"}
          ariaLabel={ariaLabel ?? t("messagelog.composer.label")}
          readOnly={readOnly}
          onChange={readOnly ? undefined : onText}
          onReady={onReady}
        />
      </div>
    </div>
  );
}
