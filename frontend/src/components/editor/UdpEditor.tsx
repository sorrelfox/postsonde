import { useCallback, useId, useMemo, useState, type KeyboardEvent } from "react";
import { useAutoSave } from "../../commands/useAutoSave";
import { useRequestDraft } from "../../commands/useRequestDraft";
import { useUdpSession } from "../../commands/useUdpSession";
import type { UdpSessionState, UdpView } from "../../commands/udpSessions";
import { t as translate, type MessageKey } from "../../i18n";
import { useT } from "../../i18n/react";
import { parseBase64 } from "../../lib/base64Bytes";
import type { RequestDTO } from "../../lib/collection";
import type { AppError } from "../../lib/errors";
import { parseHex } from "../../lib/hexBytes";
import { ariaShortcut, matchesShortcut, shortcutLabel } from "../../lib/shortcuts";
import { UDP_MAX_DATAGRAM, type UdpIpVersion, type UdpOpenOptions, type UdpSendMode } from "../../lib/udp";
import { useCollection, type RequestData, type RequestId } from "../../stores";
import { EmptyState } from "../common/EmptyState";
import { IconLock, IconPlay, IconWarning } from "../common/icons";
import { formatSize } from "../messagelog/format";
import { MessageLog } from "../messagelog/MessageLog";
import { NotesPanel } from "./NotesPanel";
import { RequestProxyFold } from "./RequestProxySettings";
// The UDP editor shares the TCP editor's layout: address bar, status,
// options, log and composer.
import styles from "./TcpEditor.module.css";

const READ_ONLY: Readonly<Record<string, MessageKey>> = {
  newer_version: "editorProtocols.readOnly.newerVersion",
  symlink: "editorProtocols.readOnly.symlink",
  deleted: "editorProtocols.readOnly.deleted",
};

/** Why request `id` cannot be edited, or null when it can. Opening and sending still work. */
function useReadOnlyNotice(id: RequestId): string | null {
  const t = useT();
  const itemReason = useCollection((s) => s.nodes[id]?.readOnly ?? "");
  const projectReason = useCollection((s) => s.readOnly);
  if (itemReason !== "") return t(READ_ONLY[itemReason] ?? "editorProtocols.readOnly.request");
  if (projectReason !== "") return t("editorProtocols.readOnly.project");
  return null;
}

const SEND_MODES: readonly UdpSendMode[] = ["text", "hex", "base64"];

/** The label of a send mode; Base64 is the encoding's name in every language, as in the message log. */
function sendModeLabel(m: UdpSendMode, t: (key: MessageKey) => string): string {
  if (m === "text") return t("editorProtocols.tcp.text");
  if (m === "hex") return t("editorProtocols.tcp.hex");
  return "Base64"; // i18n-ignore: the encoding's name
}

const IP_VERSIONS: readonly { value: UdpIpVersion; labelKey: MessageKey }[] = [
  { value: "", labelKey: "editorProtocols.udp.ipAny" },
  { value: "ipv4", labelKey: "editorProtocols.udp.ipv4" },
  { value: "ipv6", labelKey: "editorProtocols.udp.ipv6" },
];

function sendModeOf(request: RequestDTO): UdpSendMode {
  return request.sendMode === "hex" || request.sendMode === "base64" ? request.sendMode : "text";
}

/** The request's socket settings with the defaults filled in. */
interface UdpOptions {
  connected: boolean;
  ipVersion: UdpIpVersion;
  maxDatagramBytes: number | null;
  openTimeoutMs: number | null;
}

function optionsOf(request: RequestDTO): UdpOptions {
  const o = request.udp ?? null;
  return {
    connected: o?.connected === true,
    ipVersion: o?.ipVersion === "ipv4" || o?.ipVersion === "ipv6" ? o.ipVersion : "",
    maxDatagramBytes: o?.maxDatagramBytes ?? null,
    openTimeoutMs: o?.openTimeoutMs ?? null,
  };
}

/** What Open takes for `request`: its socket settings as stored (unknown IP versions too, which Go refuses). */
export function openOptionsOf(request: RequestDTO): UdpOpenOptions {
  const o = request.udp ?? null;
  return {
    connected: o?.connected === true,
    ipVersion: o?.ipVersion ?? "",
    maxDatagramBytes: o?.maxDatagramBytes ?? null,
    openTimeoutMs: o?.openTimeoutMs ?? null,
  };
}

function errorText(err: AppError): string {
  return err.hint ? `${err.message} ${err.hint}` : err.message;
}

/** The status line under the address bar. */
function statusText(state: UdpSessionState): string {
  switch (state.status) {
    case "idle":
      return state.error !== null ? errorText(state.error) : translate("editorProtocols.udp.status.notOpen");
    case "opening":
      return translate("editorProtocols.udp.status.opening", { address: state.address });
    case "open":
      return [
        state.via !== ""
          ? translate("editorProtocols.udp.status.openVia", { address: state.address, via: state.via })
          : translate("editorProtocols.udp.status.open", { address: state.address }),
        state.connected ? translate("editorProtocols.udp.status.targetOnly") : translate("editorProtocols.udp.status.anySender"),
        translate("editorProtocols.udp.status.sent", { count: state.datagramsSent }),
        translate("editorProtocols.udp.status.received", { count: state.datagramsReceived }),
      ].join(" · ");
    case "closing":
      return translate("editorProtocols.udp.status.closing");
    case "closed":
      return state.error !== null ? errorText(state.error) : translate("editorProtocols.udp.status.closed");
  }
}

/** Colour of the state dot. */
function statusTone(state: UdpSessionState): string {
  if (state.status === "open") return "ok";
  if (state.status === "opening" || state.status === "closing") return "busy";
  return state.error !== null ? "bad" : "idle";
}

/** The bytes the composer spells in hex or Base64, or why not; null for text. */
function parseBytes(mode: UdpSendMode, message: string) {
  if (mode === "hex") return parseHex(message);
  if (mode === "base64") return parseBase64(message);
  return null;
}

export interface UdpEditorProps {
  id: RequestId;
}

/**
 * The editor of a UDP request (UDP-01): the address and Open/Close, the
 * socket's state (through a SOCKS5 proxy's UDP ASSOCIATE relay, SOCKS-07,
 * the proxy is named), the socket options (who may answer, the IP version,
 * the largest datagram), the log of the datagrams sent and received with
 * each sender (as text or hex), and a composer that sends text (with
 * {{variables}}), hex or Base64 datagrams. Everything but the log is
 * auto-saved. The socket belongs to the request's tab, not to this
 * component (commands/udpSessions.ts).
 */
export function UdpEditor({ id }: UdpEditorProps) {
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
  return <LoadedUdpEditor id={id} request={draft.request} />;
}

function LoadedUdpEditor({ id, request }: { id: RequestId; request: RequestDTO }) {
  const t = useT();
  const { edit } = useAutoSave(id);
  const notice = useReadOnlyNotice(id);
  const readOnly = notice !== null;
  const { session, state } = useUdpSession(id);
  const [sendError, setSendError] = useState<AppError | null>(null);
  const [sending, setSending] = useState(false);

  const baseId = useId();
  const statusId = `${baseId}-status`;
  const composerErrorId = `${baseId}-composer-error`;

  const mode = sendModeOf(request);
  const options = optionsOf(request);
  const message = request.message;
  const active = state.status === "opening" || state.status === "open" || state.status === "closing";
  const open = state.status === "open";

  // Hex and Base64 are checked while typing; Go checks again when sending.
  const parsed = useMemo(() => parseBytes(mode, message), [mode, message]);
  const parseError = parsed !== null && !parsed.ok ? parsed.message : null;
  const size = parsed !== null && parsed.ok ? parsed.bytes.length : new TextEncoder().encode(message).length;
  // Text with variables may grow or shrink when rendered: Go checks it.
  const sizeKnown = parsed !== null || !message.includes("{{");
  const tooLarge = open && sizeKnown && state.maxPayload > 0 && size > state.maxPayload;
  const canSend = open && !sending && parseError === null && !tooLarge;

  const onAddress = (address: string) => {
    if (!readOnly) edit({ address });
  };

  const openSocket = () => {
    void session.open(request.address.trim(), openOptionsOf(request)).catch(() => {
      // Shown as the session's error.
    });
  };
  const closeSocket = () => void session.close();

  const send = useCallback(() => {
    if (!canSend) return;
    setSending(true);
    setSendError(null);
    session
      .send(mode, message)
      .catch((err: AppError) => setSendError(err))
      .finally(() => setSending(false));
  }, [canSend, session, mode, message]);

  const onComposerKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (matchesShortcut(e, "composer.send")) {
      e.preventDefault();
      send();
    }
  };

  const onAddressKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Enter" && !active) {
      e.preventDefault();
      openSocket();
    }
  };

  const setView = (view: UdpView) => session.setView(view);
  const logSource = state.view === "hex" ? session.hexLog : session.textLog;
  const composerError =
    parseError ??
    (tooLarge ? t("editorProtocols.udp.tooLarge", { size: formatSize(size), max: formatSize(state.maxPayload) }) : null) ??
    (sendError !== null ? errorText(sendError) : null);

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
            UDP
          </span>
          <input
            className={styles.address}
            aria-label={t("editorProtocols.tcp.address")}
            aria-describedby={statusId}
            placeholder={t("editorProtocols.udp.addressPlaceholder")}
            spellCheck={false}
            autoComplete="off"
            value={request.address}
            readOnly={readOnly}
            onChange={(e) => onAddress(e.target.value)}
            onKeyDown={onAddressKeyDown}
          />
        </div>
        {active ? (
          <button type="button" className={styles.disconnect} onClick={closeSocket} disabled={state.status === "closing"}>
            {state.status === "opening" ? t("editorProtocols.cancel") : t("editorProtocols.udp.close")}
          </button>
        ) : (
          <button type="button" className={styles.connect} onClick={openSocket} disabled={request.address.trim() === ""}>
            {t("editorProtocols.udp.open")}
          </button>
        )}
      </div>

      <p className={styles.status} id={statusId} role="status" data-tone={statusTone(state)}>
        <span className={styles.dot} aria-hidden="true" />
        <span className={styles.statusText}>{statusText(state)}</span>
      </p>

      <SocketOptions request={request} options={options} readOnly={readOnly} active={active} edit={edit} />

      <RequestProxyFold
        settings={request.settings}
        readOnly={readOnly}
        edit={edit}
        locked={active ? t("editorProtocols.udp.locked") : undefined}
        udp
      />

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
        <MessageLog key={state.view} source={logSource} label={t("editorProtocols.udp.log")} clearable={false} />
      </div>

      <div className={styles.composer} data-shortcut-scope="composer">
        <textarea
          className={styles.message}
          aria-label={t("editorProtocols.udp.datagram")}
          aria-invalid={parseError !== null || tooLarge}
          aria-describedby={composerError !== null ? composerErrorId : undefined}
          aria-keyshortcuts={ariaShortcut("composer.send")}
          placeholder={
            mode === "hex"
              ? t("editorProtocols.tcp.hexPlaceholder")
              : mode === "base64"
                ? t("editorProtocols.udp.base64Placeholder")
                : t("editorProtocols.udp.textPlaceholder")
          }
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
            {SEND_MODES.map((m) => (
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
                {sendModeLabel(m, t)}
              </button>
            ))}
          </div>
          {sizeKnown && <span className={styles.caption}>{formatSize(size)}</span>}
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
            title={open ? undefined : t("editorProtocols.udp.openToSend")}
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
  return v !== "" && Number.isInteger(n) && n >= 1 && n <= UDP_MAX_DATAGRAM;
}

/**
 * The socket options under the status line: who may answer (any sender,
 * or the target only: a connected socket), the IP version and the largest
 * datagram. They apply when opening, so they are disabled while a socket is
 * active.
 */
function SocketOptions({
  request,
  options,
  readOnly,
  active,
  edit,
}: {
  request: RequestDTO;
  options: UdpOptions;
  readOnly: boolean;
  active: boolean;
  edit: (patch: RequestData) => void;
}) {
  const t = useT();
  const baseId = useId();
  const disabled = readOnly || active;
  const locked = active ? t("editorProtocols.udp.locked") : undefined;
  const setOptions = (patch: Partial<UdpOptions>) => {
    const base = request.udp ?? { connected: false, ipVersion: "", maxDatagramBytes: null, openTimeoutMs: null };
    edit({ udp: { ...base, ...patch } });
  };
  // The text typed in the maximum field while it is not a valid size.
  const [maxText, setMaxText] = useState<string | null>(null);
  const maxValue = maxText ?? (options.maxDatagramBytes === null ? "" : String(options.maxDatagramBytes));
  const maxInvalid = maxText !== null && maxText.trim() !== "" && !validMax(maxText);

  const onMax = (text: string) => {
    setMaxText(text);
    if (text.trim() === "") setOptions({ maxDatagramBytes: null });
    else if (validMax(text)) setOptions({ maxDatagramBytes: Number(text.trim()) });
  };

  return (
    <div className={styles.options} role="group" aria-label={t("editorProtocols.udp.options")}>
      <span className={styles.option}>
        <label className={styles.caption} htmlFor={`${baseId}-from`}>
          {t("editorProtocols.udp.receiveFrom")}
        </label>
        <select
          id={`${baseId}-from`}
          className={styles.select}
          value={options.connected ? "target" : "any"}
          disabled={disabled}
          title={locked ?? t("editorProtocols.udp.receiveFromTitle")}
          onChange={(e) => setOptions({ connected: e.target.value === "target" })}
        >
          <option value="any">{t("editorProtocols.udp.anySender")}</option>
          <option value="target">{t("editorProtocols.udp.targetOnly")}</option>
        </select>
      </span>
      <span className={styles.option}>
        <label className={styles.caption} htmlFor={`${baseId}-ip`}>
          {t("editorProtocols.udp.ipVersion")}
        </label>
        <select
          id={`${baseId}-ip`}
          className={styles.select}
          value={options.ipVersion}
          disabled={disabled}
          title={locked}
          onChange={(e) => setOptions({ ipVersion: e.target.value as UdpIpVersion })}
        >
          {IP_VERSIONS.map((v) => (
            <option key={v.value} value={v.value}>
              {t(v.labelKey)}
            </option>
          ))}
        </select>
      </span>
      <span className={styles.option}>
        <label className={styles.caption} htmlFor={`${baseId}-max`}>
          {t("editorProtocols.udp.max")}
        </label>
        <input
          id={`${baseId}-max`}
          className={styles.number}
          type="number"
          min={1}
          max={UDP_MAX_DATAGRAM}
          step={1}
          placeholder={String(UDP_MAX_DATAGRAM)}
          aria-invalid={maxInvalid}
          value={maxValue}
          disabled={disabled}
          title={locked ?? t("editorProtocols.udp.maxTitle")}
          onChange={(e) => onMax(e.target.value)}
          onBlur={() => setMaxText(null)}
        />
        <span className={styles.caption}>{t("editorProtocols.tcp.bytes")}</span>
      </span>
    </div>
  );
}
