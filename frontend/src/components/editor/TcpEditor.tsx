import { useCallback, useId, useMemo, useState, type KeyboardEvent } from "react";
import { useAutoSave } from "../../commands/useAutoSave";
import { useRequestDraft } from "../../commands/useRequestDraft";
import { useTcpSession } from "../../commands/useTcpSession";
import type { TcpSessionState, TcpView } from "../../commands/tcpSessions";
import { t as translate, type MessageKey } from "../../i18n";
import { useT } from "../../i18n/react";
import type { RequestDTO } from "../../lib/collection";
import type { AppError } from "../../lib/errors";
import { parseHex } from "../../lib/hexBytes";
import { ariaShortcut, matchesShortcut, shortcutLabel } from "../../lib/shortcuts";
import type { FramingDTO, TcpConnectOptions, TcpFramingMode, TcpLineEnding, TcpSendMode } from "../../lib/tcp";
import { useCollection, type RequestData, type RequestId } from "../../stores";
import { EmptyState } from "../common/EmptyState";
import { IconLock, IconPlay, IconWarning } from "../common/icons";
import { formatSize } from "../messagelog/format";
import { MessageLog } from "../messagelog/MessageLog";
import { NotesPanel } from "./NotesPanel";
import styles from "./TcpEditor.module.css";

const READ_ONLY: Readonly<Record<string, MessageKey>> = {
  newer_version: "editorProtocols.readOnly.newerVersion",
  symlink: "editorProtocols.readOnly.symlink",
  deleted: "editorProtocols.readOnly.deleted",
};

/** Why request `id` cannot be edited, or null when it can. Connecting and sending still work. */
function useReadOnlyNotice(id: RequestId): string | null {
  const t = useT();
  const itemReason = useCollection((s) => s.nodes[id]?.readOnly ?? "");
  const projectReason = useCollection((s) => s.readOnly);
  if (itemReason !== "") return t(READ_ONLY[itemReason] ?? "editorProtocols.readOnly.request");
  if (projectReason !== "") return t("editorProtocols.readOnly.project");
  return null;
}

const LINE_ENDINGS: readonly { value: TcpLineEnding; labelKey: MessageKey }[] = [
  { value: "none", labelKey: "editorProtocols.tcp.lineEnding.none" },
  { value: "lf", labelKey: "editorProtocols.tcp.lineEnding.lf" },
  { value: "crlf", labelKey: "editorProtocols.tcp.lineEnding.crlf" },
];

const FRAMING_MODES: readonly { value: TcpFramingMode; labelKey: MessageKey }[] = [
  { value: "none", labelKey: "editorProtocols.tcp.framing.none" },
  { value: "newline", labelKey: "editorProtocols.tcp.framing.newline" },
  { value: "length", labelKey: "editorProtocols.tcp.framing.length" },
];

const PREFIX_SIZES = [1, 2, 4, 8] as const;

/** Largest accepted maximum message size (32 MiB), as Go checks it. */
const MAX_MESSAGE_LIMIT = 32 * 1024 * 1024;

function sendModeOf(request: RequestDTO): TcpSendMode {
  return request.sendMode === "hex" ? "hex" : "text";
}

function lineEndingOf(request: RequestDTO): TcpLineEnding {
  return request.lineEnding === "lf" || request.lineEnding === "crlf" ? request.lineEnding : "none";
}

/** The request's framing with the defaults filled in. */
interface Framing {
  mode: TcpFramingMode;
  delimiter: "lf" | "crlf";
  prefixBytes: number;
  byteOrder: "big" | "little";
  maxMessageBytes: number | null;
}

function framingOf(request: RequestDTO): Framing {
  const f = request.framing ?? null;
  return {
    mode: f?.mode === "newline" || f?.mode === "length" ? f.mode : "none",
    delimiter: f?.delimiter === "crlf" ? "crlf" : "lf",
    prefixBytes: f !== null && (f.prefixBytes === 1 || f.prefixBytes === 2 || f.prefixBytes === 8) ? f.prefixBytes : 4,
    byteOrder: f?.byteOrder === "little" ? "little" : "big",
    maxMessageBytes: f?.maxMessageBytes ?? null,
  };
}

/** The request's framing as stored after `patch`: defaults empty, unknown members kept. */
function storedFraming(request: RequestDTO, patch: Partial<FramingDTO>) {
  const base = request.framing ?? { mode: "", delimiter: "", prefixBytes: 0, byteOrder: "", maxMessageBytes: null, extra: "" };
  const next = { ...base, ...patch };
  if (next.mode === "none") next.mode = "";
  if (next.delimiter === "lf") next.delimiter = "";
  if (next.prefixBytes === 4) next.prefixBytes = 0;
  if (next.byteOrder === "big") next.byteOrder = "";
  return next;
}

/** What Connect takes for `request`: its timeout, TLS settings and framing. */
export function connectOptionsOf(request: RequestDTO): TcpConnectOptions {
  const f = request.framing ?? null;
  return {
    connectTimeoutMs: request.settings.connectTimeoutMs,
    tls: request.settings.tls === true,
    verifyTls: request.settings.verifyTls,
    framing:
      f !== null && f.mode !== "" && f.mode !== "none"
        ? { mode: f.mode, delimiter: f.delimiter, prefixBytes: f.prefixBytes, byteOrder: f.byteOrder, maxMessageBytes: f.maxMessageBytes }
        : null,
  };
}

function errorText(err: AppError): string {
  return err.hint ? `${err.message} ${err.hint}` : err.message;
}

/** The status line under the address bar. */
function statusText(state: TcpSessionState): string {
  switch (state.status) {
    case "idle":
      return state.error !== null ? errorText(state.error) : translate("editorProtocols.stream.notConnected");
    case "connecting":
      return translate("editorProtocols.stream.connecting", { url: state.address });
    case "open": {
      const address = state.remoteAddr || state.address;
      return [
        state.tls !== null
          ? translate("editorProtocols.tcp.status.connectedTls", { address, tls: state.tls.version })
          : translate("editorProtocols.stream.connected", { url: address }),
        translate("editorProtocols.tcp.status.sent", { size: formatSize(state.bytesSent) }),
        translate("editorProtocols.tcp.status.received", { size: formatSize(state.bytesReceived) }),
      ].join(" · ");
    }
    case "closing":
      return translate("editorProtocols.tcp.status.disconnecting");
    case "closed":
      if (state.error !== null) return errorText(state.error);
      return state.close?.initiatedBy === "remote"
        ? translate("editorProtocols.tcp.status.closedByServer")
        : translate("editorProtocols.stream.disconnected");
  }
}

/** Colour of the state dot. */
function statusTone(state: TcpSessionState): string {
  if (state.status === "open") return "ok";
  if (state.status === "connecting" || state.status === "closing") return "busy";
  return state.error !== null ? "bad" : "idle";
}

export interface TcpEditorProps {
  id: RequestId;
}

/**
 * The editor of a raw TCP request (TCP-01): the address with a TLS switch
 * (TCP-02) and Connect/Disconnect, the connection state, the connection
 * options (certificate verification, message framing: TCP-03), the log of
 * what was sent and received (as text or hex), and a composer that sends
 * text with a line ending or hex bytes. The address, the options, the send
 * mode, the line ending and the composer's text are auto-saved. The
 * connection belongs to the request's tab, not to this component
 * (commands/tcpSessions.ts).
 */
export function TcpEditor({ id }: TcpEditorProps) {
  const t = useT();
  const draft = useRequestDraft(id);
  if (draft.status === "loading") {
    return <div className={styles.editor} aria-busy="true" />;
  }
  if (draft.status === "error") {
    return (
      <div className={styles.editor}>
        <EmptyState icon={<IconWarning size={20} />} title={t("editorProtocols.openFailed")}>
          {draft.error.message}{" "}
          <button type="button" className={styles.linkButton} onClick={draft.retry}>
            {t("editorProtocols.tryAgain")}
          </button>
        </EmptyState>
      </div>
    );
  }
  return <LoadedTcpEditor id={id} request={draft.request} />;
}

function LoadedTcpEditor({ id, request }: { id: RequestId; request: RequestDTO }) {
  const t = useT();
  const { edit } = useAutoSave(id);
  const notice = useReadOnlyNotice(id);
  const readOnly = notice !== null;
  const { session, state } = useTcpSession(id);
  const [sendError, setSendError] = useState<AppError | null>(null);
  const [sending, setSending] = useState(false);

  const baseId = useId();
  const statusId = `${baseId}-status`;
  const composerErrorId = `${baseId}-composer-error`;

  const mode = sendModeOf(request);
  const lineEnding = lineEndingOf(request);
  const framing = framingOf(request);
  const framed = framing.mode !== "none";
  const tls = request.settings.tls === true;
  const message = request.message;
  const active = state.status === "connecting" || state.status === "open" || state.status === "closing";
  const open = state.status === "open";
  // The framing the connection was made with decides how sending works.
  const sendFramed = active ? state.framed : framed;

  // Hex is checked while typing; Go checks again when sending.
  const hex = useMemo(() => (mode === "hex" ? parseHex(message) : null), [mode, message]);
  const hexError = hex !== null && !hex.ok ? hex.message : null;
  // With a framing an empty message is a message (an empty line, a zero length).
  const empty =
    !sendFramed && (mode === "hex" ? hex?.ok === true && hex.bytes.length === 0 : message === "" && lineEnding === "none");
  const canSend = open && !sending && hexError === null && !empty;

  const onAddress = (address: string) => {
    if (!readOnly) edit({ address });
  };

  const connect = () => {
    void session.connect(request.address.trim(), connectOptionsOf(request)).catch(() => {
      // Shown as the session's error.
    });
  };
  const disconnect = () => void session.disconnect();

  const send = useCallback(() => {
    if (!canSend) return;
    setSending(true);
    setSendError(null);
    session
      .send(mode, message, lineEnding)
      .catch((err: AppError) => setSendError(err))
      .finally(() => setSending(false));
  }, [canSend, session, mode, message, lineEnding]);

  const onComposerKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (matchesShortcut(e, "composer.send")) {
      e.preventDefault();
      send();
    }
  };

  const onAddressKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Enter" && !active) {
      e.preventDefault();
      connect();
    }
  };

  const setView = (view: TcpView) => session.setView(view);
  const logSource = state.view === "hex" ? session.hexLog : session.textLog;
  const composerError = hexError ?? (sendError !== null ? errorText(sendError) : null);

  return (
    <div className={styles.editor} data-request-id={id}>
      {notice !== null && (
        <p className={styles.notice} role="note">
          <IconLock size={13} />
          {notice}
        </p>
      )}

      <div className={styles.bar}>
        <div className={styles.addressGroup}>
          <span className={styles.protocol} aria-hidden="true">
            TCP
          </span>
          <input
            className={styles.address}
            aria-label={t("editorProtocols.tcp.address")}
            aria-describedby={statusId}
            placeholder={t("editorProtocols.tcp.addressPlaceholder")}
            spellCheck={false}
            autoComplete="off"
            value={request.address}
            readOnly={readOnly}
            onChange={(e) => onAddress(e.target.value)}
            onKeyDown={onAddressKeyDown}
          />
          <button
            type="button"
            className={styles.tls}
            aria-pressed={tls}
            disabled={readOnly || active}
            title={
              active
                ? t("editorProtocols.tcp.locked")
                : tls
                  ? t("editorProtocols.grpc.tlsOn")
                  : t("editorProtocols.tcp.tlsOff")
            }
            onClick={() => edit({ settings: { ...request.settings, tls: tls ? null : true } })}
          >
            <IconLock size={12} />
            TLS
          </button>
        </div>
        {active ? (
          <button type="button" className={styles.disconnect} onClick={disconnect} disabled={state.status === "closing"}>
            {state.status === "connecting" ? t("editorProtocols.cancel") : t("editorProtocols.stream.disconnect")}
          </button>
        ) : (
          <button type="button" className={styles.connect} onClick={connect} disabled={request.address.trim() === ""}>
            {t("editorProtocols.stream.connect")}
          </button>
        )}
      </div>

      <p className={styles.status} id={statusId} role="status" data-tone={statusTone(state)}>
        <span className={styles.dot} aria-hidden="true" />
        <span className={styles.statusText}>{statusText(state)}</span>
      </p>

      <ConnectionOptions request={request} framing={framing} readOnly={readOnly} active={active} edit={edit} />

      {/* No sub-tabs here: the notes (COL-07) fold open under the options. */}
      <details className={styles.notes}>
        <summary className={styles.notesSummary}>{t("editorProtocols.tab.notes")}</summary>
        <div className={styles.notesBody}>
          <NotesPanel
            value={request.description ?? ""}
            subject="request"
            readOnly={readOnly}
            onChange={(description) => edit({ description })}
          />
        </div>
      </details>

      <div className={styles.logArea}>
        <div className={styles.logBar}>
          <span className={styles.caption} id={`${baseId}-view`}>
            {t("editorProtocols.tcp.showAs")}
          </span>
          <div className={styles.segmented} role="group" aria-labelledby={`${baseId}-view`}>
            {(["text", "hex"] as const).map((v) => (
              <button
                key={v}
                type="button"
                className={styles.segment}
                aria-pressed={state.view === v}
                onClick={() => setView(v)}
              >
                {v === "text" ? t("editorProtocols.tcp.text") : t("editorProtocols.tcp.hex")}
              </button>
            ))}
          </div>
          <span className={styles.spacer} />
          <button type="button" className={styles.smallButton} onClick={() => session.clear()}>
            {t("editorProtocols.tcp.clear")}
          </button>
        </div>
        <MessageLog key={state.view} source={logSource} label={t("editorProtocols.tcp.log")} clearable={false} />
      </div>

      <div className={styles.composer} data-shortcut-scope="composer">
        <textarea
          className={styles.message}
          aria-label={t("editorProtocols.tab.message")}
          aria-invalid={hexError !== null}
          aria-describedby={composerError !== null ? composerErrorId : undefined}
          aria-keyshortcuts={ariaShortcut("composer.send")}
          placeholder={mode === "hex" ? t("editorProtocols.tcp.hexPlaceholder") : t("editorProtocols.tcp.textPlaceholder")}
          spellCheck={false}
          value={message}
          readOnly={readOnly}
          onChange={(e) => {
            setSendError(null);
            if (!readOnly) edit({ message: e.target.value });
          }}
          onKeyDown={onComposerKeyDown}
        />
        <div className={styles.composerBar}>
          <div className={styles.segmented} role="group" aria-label={t("editorProtocols.tcp.sendAs")}>
            {(["text", "hex"] as const).map((m) => (
              <button
                key={m}
                type="button"
                className={styles.segment}
                aria-pressed={mode === m}
                disabled={readOnly}
                onClick={() => {
                  setSendError(null);
                  edit({ sendMode: m });
                }}
              >
                {m === "text" ? t("editorProtocols.tcp.text") : t("editorProtocols.tcp.hex")}
              </button>
            ))}
          </div>
          <select
            className={styles.select}
            aria-label={t("editorProtocols.tcp.lineEnding")}
            value={sendFramed ? "none" : lineEnding}
            disabled={readOnly || mode === "hex" || sendFramed}
            title={
              sendFramed
                ? t("editorProtocols.tcp.lineEndingFramed")
                : mode === "hex"
                  ? t("editorProtocols.tcp.lineEndingHex")
                  : undefined
            }
            onChange={(e) => edit({ lineEnding: e.target.value })}
          >
            {LINE_ENDINGS.map((le) => (
              <option key={le.value} value={le.value}>
                {t(le.labelKey)}
              </option>
            ))}
          </select>
          <p className={styles.composerError} id={composerErrorId} aria-live="polite">
            {composerError !== null && (
              <>
                <IconWarning size={12} />
                {composerError}
              </>
            )}
          </p>
          <kbd className={styles.kbd}>{shortcutLabel("composer.send")}</kbd>
          <button
            type="button"
            className={styles.send}
            onClick={send}
            disabled={!canSend}
            title={open ? undefined : t("editorProtocols.saved.connectToSend")}
          >
            <IconPlay size={11} />
            {t("editorProtocols.send")}
          </button>
        </div>
      </div>
    </div>
  );
}

function validMax(text: string): boolean {
  const v = text.trim();
  const n = Number(v);
  return v !== "" && Number.isInteger(n) && n >= 1 && n <= MAX_MESSAGE_LIMIT;
}

/**
 * The connection options under the status line: certificate verification
 * (with TLS) and the message framing. They apply when connecting, so they
 * are disabled while a connection is active.
 */
function ConnectionOptions({
  request,
  framing,
  readOnly,
  active,
  edit,
}: {
  request: RequestDTO;
  framing: Framing;
  readOnly: boolean;
  active: boolean;
  edit: (patch: RequestData) => void;
}) {
  const t = useT();
  const baseId = useId();
  const disabled = readOnly || active;
  const locked = active ? t("editorProtocols.tcp.locked") : undefined;
  const setFraming = (patch: Partial<FramingDTO>) => edit({ framing: storedFraming(request, patch) });
  // The text typed in the maximum field while it is not a valid size.
  const [maxText, setMaxText] = useState<string | null>(null);
  const maxValue = maxText ?? (framing.maxMessageBytes === null ? "" : String(framing.maxMessageBytes));
  const maxInvalid = maxText !== null && maxText.trim() !== "" && !validMax(maxText);

  const onMax = (text: string) => {
    setMaxText(text);
    if (text.trim() === "") setFraming({ maxMessageBytes: null });
    else if (validMax(text)) setFraming({ maxMessageBytes: Number(text.trim()) });
  };

  return (
    <div className={styles.options} role="group" aria-label={t("editorProtocols.tab.connectionOptions")}>
      {request.settings.tls === true && (
        <label className={styles.option} title={locked}>
          <input
            type="checkbox"
            checked={request.settings.verifyTls !== false}
            disabled={disabled}
            onChange={(e) => edit({ settings: { ...request.settings, verifyTls: e.target.checked ? null : false } })}
          />
          {t("editorProtocols.tcp.verify")}
        </label>
      )}
      <span className={styles.option}>
        <label className={styles.caption} htmlFor={`${baseId}-framing`}>
          {t("editorProtocols.tcp.framing")}
        </label>
        <select
          id={`${baseId}-framing`}
          className={styles.select}
          value={framing.mode}
          disabled={disabled}
          title={locked}
          onChange={(e) => setFraming({ mode: e.target.value })}
        >
          {FRAMING_MODES.map((m) => (
            <option key={m.value} value={m.value}>
              {t(m.labelKey)}
            </option>
          ))}
        </select>
      </span>
      {framing.mode === "newline" && (
        <select
          className={styles.select}
          aria-label={t("editorProtocols.tcp.delimiter")}
          value={framing.delimiter}
          disabled={disabled}
          title={locked ?? t("editorProtocols.tcp.delimiterTitle")}
          onChange={(e) => setFraming({ delimiter: e.target.value })}
        >
          <option value="lf">{t("editorProtocols.tcp.delimiterLf")}</option>
          <option value="crlf">{t("editorProtocols.tcp.delimiterCrlf")}</option>
        </select>
      )}
      {framing.mode === "length" && (
        <>
          <select
            className={styles.select}
            aria-label={t("editorProtocols.tcp.prefix")}
            value={framing.prefixBytes}
            disabled={disabled}
            title={locked ?? t("editorProtocols.tcp.prefixTitle")}
            onChange={(e) => setFraming({ prefixBytes: Number(e.target.value) })}
          >
            {PREFIX_SIZES.map((n) => (
              <option key={n} value={n}>
                {t("editorProtocols.tcp.prefixSize", { count: n })}
              </option>
            ))}
          </select>
          <select
            className={styles.select}
            aria-label={t("editorProtocols.tcp.byteOrder")}
            value={framing.byteOrder}
            disabled={disabled || framing.prefixBytes === 1}
            title={locked}
            onChange={(e) => setFraming({ byteOrder: e.target.value })}
          >
            <option value="big">{t("editorProtocols.tcp.bigEndian")}</option>
            <option value="little">{t("editorProtocols.tcp.littleEndian")}</option>
          </select>
        </>
      )}
      {framing.mode !== "none" && (
        <span className={styles.option}>
          <label className={styles.caption} htmlFor={`${baseId}-max`}>
            {t("editorProtocols.tcp.max")}
          </label>
          <input
            id={`${baseId}-max`}
            className={styles.number}
            type="number"
            min={1}
            max={MAX_MESSAGE_LIMIT}
            step={1}
            placeholder="1048576"
            aria-invalid={maxInvalid}
            value={maxValue}
            disabled={disabled}
            title={locked ?? t("editorProtocols.tcp.maxTitle")}
            onChange={(e) => onMax(e.target.value)}
            onBlur={() => setMaxText(null)}
          />
          <span className={styles.caption}>{t("editorProtocols.tcp.bytes")}</span>
        </span>
      )}
    </div>
  );
}
