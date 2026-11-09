import { useCallback, useEffect, useId, useMemo, useRef, useState, type ChangeEvent, type KeyboardEvent } from "react";
import { wsSnippetSource } from "../../commands/snippets";
import { useAutoSave } from "../../commands/useAutoSave";
import { useStreamTests } from "../../commands/streamTests";
import { countAssertions } from "../../lib/assertions";
import { StreamTests } from "../response/StreamTests";
import { useRequestDraft } from "../../commands/useRequestDraft";
import { useWsSession } from "../../commands/useWsSession";
import type { WsSession, WsSessionState } from "../../commands/wsSessions";
import { closeCodeText, formatRtt, reconnectText, WS_PING_KINDS, WS_RECONNECT_KINDS } from "../../commands/wsSessions";
import { t as translate, type MessageKey } from "../../i18n";
import { Trans, useT } from "../../i18n/react";
import type { KeyValueDTO, RequestDTO, SavedMessageDTO } from "../../lib/collection";
import type { AppError } from "../../lib/errors";
import { toAppError } from "../../lib/errors";
import {
  connectInput,
  WS_DEFAULT_PONG_TIMEOUT_MS,
  WS_DEFAULT_RECONNECT_ATTEMPTS,
  WS_DEFAULT_RECONNECT_DELAY_MS,
  WS_DEFAULT_RECONNECT_MAX_DELAY_MS,
  WS_FILE_MAX_BYTES,
  WS_MESSAGE_FORMATS,
  WS_NORMAL_CLOSURE,
  wsMessageFormat,
  type ConnectInputDTO,
  type WsMessageFormat,
} from "../../lib/websocket";
import { useCollection, useDrafts, useStores, type RequestData, type RequestId } from "../../stores";
import { EmptyState } from "../common/EmptyState";
import { IconClose, IconFile, IconLock, IconPlus, IconWarning } from "../common/icons";
import { KeyValueTable } from "../common/KeyValueTable";
import { base64, formatSize } from "../messagelog/format";
import { MessageComposer } from "../messagelog/MessageComposer";
import { MessageLog, type KindToggle } from "../messagelog/MessageLog";
import { AssertionsEditor } from "./AssertionsEditor";
import { AuthEditor } from "./AuthEditor";
import { CodeEditor } from "./CodeEditor";
import { HEADER_SUGGEST, HEADER_SUGGEST_LABELS } from "./headerSuggest";
import { useKeyValueRows } from "./keyValueRows";
import { NotesPanel } from "./NotesPanel";
import { SavedMessages, savedFormat } from "./SavedMessages";
import { SnippetPanel } from "./SnippetPanel";
import { useUrlParamsSync } from "./useUrlParamsSync";
import styles from "./WebSocketEditor.module.css";

const READ_ONLY: Readonly<Record<string, MessageKey>> = {
  newer_version: "editorProtocols.readOnly.newerVersion",
  symlink: "editorProtocols.readOnly.symlink",
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

export type WsSubTab = "params" | "headers" | "auth" | "subprotocols" | "tests" | "settings" | "notes" | "code";

const SUB_TABS: readonly { id: WsSubTab; labelKey: MessageKey }[] = [
  { id: "params", labelKey: "editorProtocols.tab.params" },
  { id: "headers", labelKey: "editorProtocols.tab.headers" },
  { id: "auth", labelKey: "editorProtocols.tab.auth" },
  { id: "subprotocols", labelKey: "editorProtocols.tab.subprotocols" },
  // Assertions on the received messages (TEST-08), and their results.
  { id: "tests", labelKey: "editorProtocols.tab.tests" },
  { id: "settings", labelKey: "editorProtocols.tab.settings" },
  { id: "notes", labelKey: "editorProtocols.tab.notes" },
  // The handshake as a websocat or wscat command (GEN-11, SnippetPanel).
  { id: "code", labelKey: "editorProtocols.tab.code" },
];

function isSubTab(value: string | undefined): value is WsSubTab {
  return SUB_TABS.some((t) => t.id === value);
}

/** The Code tab (GEN-11): the handshake Connect would make, as a websocat or wscat command. */
function WsCode({ id, request }: { id: RequestId; request: RequestDTO }) {
  const key = JSON.stringify(connectInput(request));
  // A new source only when the handshake changed: the panel asks again then.
  const source = useMemo(() => wsSnippetSource(JSON.parse(key) as ConnectInputDTO), [key]);
  return <SnippetPanel id={id} request={key} source={source} />;
}

function countRows(rows: readonly KeyValueDTO[] | null): number {
  return (rows ?? []).filter((r) => r.enabled && r.name !== "").length;
}

function formatOf(request: RequestDTO): WsMessageFormat {
  return wsMessageFormat(request.messageFormat);
}

function errorText(err: AppError): string {
  return err.hint ? `${err.message} ${err.hint}` : err.message;
}

/** A hint when the URL's scheme is not ws:// or wss://, or null. */
export function schemeHint(url: string): string | null {
  const trimmed = url.trim();
  if (trimmed === "" || trimmed.startsWith("{{")) return null;
  const match = /^([a-z][a-z0-9+.-]*):\/\//i.exec(trimmed);
  const scheme = match?.[1]?.toLowerCase();
  if (scheme === "ws" || scheme === "wss") return null;
  // No text right after "://": the bundle check (scripts/check-dist.mjs) reads that as a remote URL.
  if (scheme === "http") return translate("editorProtocols.ws.scheme.http");
  if (scheme === "https") return translate("editorProtocols.ws.scheme.https");
  return translate("editorProtocols.ws.scheme.other");
}

/** The status line under the URL bar. */
function statusText(state: WsSessionState): string {
  switch (state.status) {
    case "idle":
      return state.error !== null ? errorText(state.error) : translate("editorProtocols.stream.notConnected");
    case "connecting":
      return translate("editorProtocols.stream.connecting", { url: state.url });
    case "open": {
      const parts = [translate("editorProtocols.stream.connected", { url: state.url })];
      if (state.handshake?.subprotocol) {
        parts.push(translate("editorProtocols.ws.status.subprotocol", { name: state.handshake.subprotocol }));
      }
      parts.push(translate("editorProtocols.stream.sent", { count: state.messagesSent }));
      parts.push(translate("editorProtocols.stream.received", { count: state.messagesReceived }));
      if (state.lastRttMs !== null) parts.push(translate("editorProtocols.ws.status.lastPong", { rtt: formatRtt(state.lastRttMs) }));
      return parts.join(" · ");
    }
    case "closing":
      return translate("editorProtocols.stream.closing");
    case "reconnecting": {
      if (state.reconnect !== null) {
        const plan = reconnectText(state.reconnect);
        return state.error !== null
          ? translate("editorProtocols.stream.reconnectPlanError", { error: state.error.message, plan })
          : translate("editorProtocols.stream.reconnectPlan", { plan });
      }
      return state.error !== null
        ? translate("editorProtocols.stream.reconnectingError", { error: state.error.message })
        : translate("editorProtocols.stream.reconnecting");
    }
    case "closed": {
      if (state.error !== null) {
        const status = state.handshake?.response?.status;
        return status && state.handshake?.response?.statusCode !== 101
          ? translate("editorProtocols.ws.status.errorStatus", { error: errorText(state.error), status })
          : errorText(state.error);
      }
      const close = state.close;
      if (state.reconnect !== null && state.reconnect.stopped === "manual" && state.close?.initiatedBy !== "local") {
        return translate("editorProtocols.stream.reconnectCancelled");
      }
      if (close === null) return translate("editorProtocols.stream.disconnected");
      return close.initiatedBy === "remote"
        ? translate("editorProtocols.ws.status.closedByServer", { reason: closeCodeText(close.code, close.reason) })
        : translate("editorProtocols.ws.status.disconnectedReason", {
            reason: closeCodeText(close.sent?.code ?? close.code, close.sent?.reason ?? close.reason),
          });
    }
  }
}

function statusTone(state: WsSessionState): string {
  if (state.status === "open") return "ok";
  if (state.status === "connecting" || state.status === "closing" || state.status === "reconnecting") return "busy";
  return state.error !== null ? "bad" : "idle";
}

/** The log's toggles for pings and pongs (WS-09) and reconnect notes (WS-10), in the language in use. */
function useLogKindToggles(): readonly KindToggle[] {
  const t = useT();
  return useMemo(
    () => [
      { label: t("editorProtocols.ws.log.pings"), kinds: WS_PING_KINDS, title: t("editorProtocols.ws.log.pingsTitle") },
      { label: t("editorProtocols.ws.log.reconnects"), kinds: WS_RECONNECT_KINDS, title: t("editorProtocols.ws.log.reconnectsTitle") },
    ],
    [t],
  );
}

export interface WebSocketEditorProps {
  id: RequestId;
}

/**
 * The editor of a WebSocket request (WS-01…WS-04, WS-06…WS-11): the
 * URL with Connect/Disconnect and the connection state, a Ping button, the
 * handshake options (query params synced with the URL, headers,
 * subprotocols, settings with keepalive and auto-reconnect), the message
 * log (binary messages as a hexdump or Base64; pings, pongs and reconnect
 * notes can be hidden), a composer for text or JSON with validation and beautify or for binary
 * messages as hex, Base64 or a file, and the saved messages beside it.
 * Everything but the close code is auto-saved. {{variables}} are drawn as
 * chips (the TabPanel's TemplateVariablesProvider) and resolved by Go on
 * connect and send. The connection belongs to the request's tab, not to
 * this component (commands/wsSessions.ts).
 */
export function WebSocketEditor({ id }: WebSocketEditorProps) {
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
  return <LoadedWebSocketEditor id={id} request={draft.request} />;
}

function LoadedWebSocketEditor({ id, request }: { id: RequestId; request: RequestDTO }) {
  const t = useT();
  const logKindToggles = useLogKindToggles();
  const stores = useStores();
  const { edit } = useAutoSave(id);
  const streamTests = useStreamTests(id);
  const notice = useReadOnlyNotice(id);
  const readOnly = notice !== null;
  const { session, state } = useWsSession(id);

  const baseId = useId();
  const statusId = `${baseId}-status`;

  const stored = useDrafts((s) => s.subTabs[id]);
  const subTab: WsSubTab = isSubTab(stored) ? stored : "params";
  const selectSubTab = (next: WsSubTab) => stores.drafts.dispatch({ type: "drafts/setSubTab", id, subTab: next });
  const tabRefs = useRef<Partial<Record<WsSubTab, HTMLButtonElement | null>>>({});

  const { onUrlChange, onParamsChange } = useUrlParamsSync(id, edit);
  const onHeaders = useCallback((headers: KeyValueDTO[]) => edit({ headers }), [edit]);
  const [paramRows, setParamRows] = useKeyValueRows(request.params, onParamsChange);
  const [headerRows, setHeaderRows] = useKeyValueRows(request.headers, onHeaders);

  const [closeCode, setCloseCode] = useState(String(WS_NORMAL_CLOSURE));
  const [closeReason, setCloseReason] = useState("");
  const [closeError, setCloseError] = useState<string | null>(null);
  const [pingPayload, setPingPayload] = useState("");
  // A ping or close error is about the connection as it was: a new state replaces it.
  useEffect(() => setCloseError(null), [state.status]);

  const active = state.status === "connecting" || state.status === "open" || state.status === "closing" || state.status === "reconnecting";
  const hint = schemeHint(request.url);

  const connect = () => {
    const current = (stores.drafts.getState().byId[id]?.request as unknown as RequestDTO | undefined) ?? request;
    setCloseError(null);
    void session.connect(connectInput(current)).catch(() => {
      // Shown as the session's error.
    });
  };

  const disconnect = () => {
    setCloseError(null);
    const code = closeCode.trim() === "" ? WS_NORMAL_CLOSURE : Number(closeCode);
    if (!Number.isInteger(code)) {
      setCloseError(t("editorProtocols.ws.closeCodeInvalid", { example: WS_NORMAL_CLOSURE }));
      return;
    }
    session.disconnect(code, closeReason).catch((err: unknown) => setCloseError(toAppError(err).message));
  };

  const ping = () => {
    setCloseError(null);
    session.ping(pingPayload).catch((err: unknown) => setCloseError(toAppError(err).message));
  };

  const onUrlKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if (e.key === "Enter" && !e.metaKey && !e.ctrlKey && !e.shiftKey && !e.altKey && !active && request.url.trim() !== "") {
      connect();
    }
  };

  const counts: Partial<Record<WsSubTab, number>> = {
    params: countRows(request.params),
    headers: countRows(request.headers),
    subprotocols: (request.subprotocols ?? []).filter((p) => p.trim() !== "").length,
    tests: countAssertions(request.assertions),
  };

  const onTabKeyDown = (e: KeyboardEvent<HTMLButtonElement>) => {
    const at = SUB_TABS.findIndex((tab) => tab.id === subTab);
    let next: number;
    if (e.key === "ArrowRight") next = (at + 1) % SUB_TABS.length;
    else if (e.key === "ArrowLeft") next = (at - 1 + SUB_TABS.length) % SUB_TABS.length;
    else if (e.key === "Home") next = 0;
    else if (e.key === "End") next = SUB_TABS.length - 1;
    else return;
    e.preventDefault();
    const target = SUB_TABS[next];
    if (target === undefined) return;
    selectSubTab(target.id);
    tabRefs.current[target.id]?.focus();
  };

  return (
    <div className={styles.editor} data-request-id={id}>
      {notice !== null && (
        <p className={styles.notice} role="note">
          <IconLock size={13} />
          {notice}
        </p>
      )}

      <div className={styles.bar}>
        <div className={styles.urlGroup} onKeyDown={onUrlKeyDown}>
          <span className={styles.protocol} aria-hidden="true">
            WS
          </span>
          <CodeEditor
            className={styles.url}
            singleLine
            ariaLabel="URL"
            placeholder={t("editorProtocols.ws.urlPlaceholder")}
            value={request.url}
            readOnly={readOnly}
            onChange={readOnly ? undefined : onUrlChange}
          />
        </div>
        {active ? (
          <button type="button" className={styles.disconnect} onClick={disconnect} disabled={state.status === "closing"}>
            {state.status === "connecting" || state.status === "reconnecting"
              ? t("editorProtocols.cancel")
              : t("editorProtocols.stream.disconnect")}
          </button>
        ) : (
          <button type="button" className={styles.connect} onClick={connect} disabled={request.url.trim() === ""}>
            {t("editorProtocols.stream.connect")}
          </button>
        )}
      </div>

      <div className={styles.statusRow}>
        <p className={styles.status} id={statusId} role="status" data-tone={statusTone(state)}>
          <span className={styles.dot} aria-hidden="true" />
          <span className={styles.statusText}>{closeError ?? hint ?? statusText(state)}</span>
        </p>
        <div className={styles.closeOptions} role="group" aria-label={t("editorProtocols.ws.ping")}>
          <input
            className={styles.closeReason}
            aria-label={t("editorProtocols.ws.pingPayload")}
            placeholder={t("editorProtocols.ws.pingPayloadPlaceholder")}
            value={pingPayload}
            onChange={(e) => setPingPayload(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" && state.status === "open") ping();
            }}
          />
          <button
            type="button"
            className={styles.smallButton}
            onClick={ping}
            disabled={state.status !== "open"}
            title={state.status === "open" ? t("editorProtocols.ws.pingTitle") : t("editorProtocols.ws.pingConnect")}
          >
            {t("editorProtocols.ws.ping")}
          </button>
        </div>
        <div className={styles.closeOptions} role="group" aria-label={t("editorProtocols.ws.closeOptions")}>
          <label className={styles.closeLabel}>
            {t("editorProtocols.ws.closeCode")}
            <input
              className={styles.closeCode}
              inputMode="numeric"
              value={closeCode}
              onChange={(e) => setCloseCode(e.target.value)}
              aria-invalid={closeError !== null}
            />
          </label>
          <input
            className={styles.closeReason}
            aria-label={t("editorProtocols.ws.closeReason")}
            placeholder={t("editorProtocols.ws.closeReasonPlaceholder")}
            value={closeReason}
            maxLength={123}
            onChange={(e) => setCloseReason(e.target.value)}
          />
        </div>
      </div>

      <div className={styles.subTabs} role="tablist" aria-label={t("editorProtocols.tab.handshake")}>
        {SUB_TABS.map((tab) => {
          const selected = tab.id === subTab;
          const count = counts[tab.id] ?? 0;
          return (
            <button
              key={tab.id}
              ref={(el) => {
                tabRefs.current[tab.id] = el;
              }}
              type="button"
              role="tab"
              id={`${baseId}-tab-${tab.id}`}
              className={styles.subTab}
              aria-selected={selected}
              aria-controls={selected ? `${baseId}-panel` : undefined}
              tabIndex={selected ? 0 : -1}
              onClick={() => selectSubTab(tab.id)}
              onKeyDown={onTabKeyDown}
            >
              {t(tab.labelKey)}
              {count > 0 && <span className={styles.count}>{count}</span>}
            </button>
          );
        })}
      </div>

      <div className={styles.section} role="tabpanel" id={`${baseId}-panel`} aria-labelledby={`${baseId}-tab-${subTab}`}>
        {subTab === "params" && (
          <KeyValueTable
            label={t("editorProtocols.queryParams")}
            rows={paramRows}
            onChange={readOnly ? undefined : setParamRows}
            readOnly={readOnly}
            bulkEdit
          />
        )}
        {subTab === "headers" && (
          <KeyValueTable
            label={t("editorProtocols.tab.headers")}
            bulkEdit
            rows={headerRows}
            onChange={readOnly ? undefined : setHeaderRows}
            readOnly={readOnly}
            placeholders={{ key: "header" }}
            suggest={HEADER_SUGGEST}
            suggestLabels={HEADER_SUGGEST_LABELS}
          />
        )}
        {subTab === "auth" && <AuthEditor id={id} auth={request.auth} readOnly={readOnly} protocol="websocket" />}
        {subTab === "subprotocols" && (
          <SubprotocolList subprotocols={request.subprotocols ?? []} readOnly={readOnly} onChange={(subprotocols) => edit({ subprotocols })} />
        )}
        {subTab === "tests" && (
          <>
            <AssertionsEditor id={id} assertions={request.assertions} readOnly={readOnly} edit={edit} stream results={streamTests.tests} />
            <StreamTests id={id} />
          </>
        )}
        {subTab === "settings" && <WsSettings request={request} readOnly={readOnly} edit={edit} />}
        {subTab === "notes" && (
          <NotesPanel
            value={request.description ?? ""}
            subject="request"
            readOnly={readOnly}
            onChange={(description) => edit({ description })}
          />
        )}
        {subTab === "code" && <WsCode id={id} request={request} />}
      </div>

      <div className={styles.logArea}>
        <MessageLog source={session.log} label={t("editorProtocols.ws.log.label")} binaryToggle kindToggles={logKindToggles} />
      </div>

      <div className={styles.composerArea}>
        <div className={styles.composerMain}>
          <Composer session={session} request={request} readOnly={readOnly} open={state.status === "open"} edit={edit} />
        </div>
        <Saved session={session} request={request} readOnly={readOnly} open={state.status === "open"} edit={edit} />
      </div>
    </div>
  );
}

/** The saved messages (WS-07), saved with the request; the composer's message is what Save stores. */
function Saved({
  session,
  request,
  readOnly,
  open,
  edit,
}: {
  session: WsSession;
  request: RequestDTO;
  readOnly: boolean;
  open: boolean;
  edit: (patch: RequestData) => void;
}) {
  const onChange = useCallback((savedMessages: SavedMessageDTO[]) => edit({ savedMessages }), [edit]);
  const onLoad = useCallback(
    (m: SavedMessageDTO) => edit({ message: m.text, messageFormat: savedFormat(m) }),
    [edit],
  );
  const onSend = useCallback((m: SavedMessageDTO) => session.send(m.text, savedFormat(m)), [session]);
  return (
    <SavedMessages
      messages={request.savedMessages ?? []}
      onChange={onChange}
      composer={{ text: request.message, format: formatOf(request) }}
      onLoad={onLoad}
      onSend={onSend}
      canSend={open}
      readOnly={readOnly}
    />
  );
}

/** A subprotocol is an HTTP token: no spaces, commas or quotes. */
const TOKEN = /^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$/;

/** Why subprotocol `value` at `index` would be refused, or null. */
export function subprotocolProblem(list: readonly string[], index: number): string | null {
  const value = (list[index] ?? "").trim();
  if (value === "") return null;
  if (!TOKEN.test(value)) return translate("editorProtocols.ws.subprotocol.invalid", { chars: "!#$%&'*+-.^_`|~" });
  if (list.findIndex((p) => p.trim() === value) !== index) return translate("editorProtocols.ws.subprotocol.twice");
  return null;
}

function SubprotocolList({
  subprotocols,
  readOnly,
  onChange,
}: {
  subprotocols: readonly string[];
  readOnly: boolean;
  onChange: (next: string[]) => void;
}) {
  const t = useT();
  const inputs = useRef<(HTMLInputElement | null)[]>([]);
  const [focusIndex, setFocusIndex] = useState<number | null>(null);
  useEffect(() => {
    if (focusIndex === null) return;
    inputs.current[focusIndex]?.focus();
    setFocusIndex(null);
  }, [focusIndex]);

  const add = () => {
    onChange([...subprotocols, ""]);
    setFocusIndex(subprotocols.length);
  };

  return (
    <div className={styles.subprotocols}>
      <p className={styles.note}>
        <Trans k="editorProtocols.ws.subprotocol.note" values={{ header: <code>Sec-WebSocket-Protocol</code> }} />
      </p>
      {subprotocols.length > 0 && (
        <ul className={styles.subprotocolList} aria-label={t("editorProtocols.tab.subprotocols")}>
          {subprotocols.map((p, i) => {
            const problem = subprotocolProblem(subprotocols, i);
            return (
              <li key={i} className={styles.subprotocolRow}>
                <input
                  ref={(el) => {
                    inputs.current[i] = el;
                  }}
                  className={styles.subprotocolInput}
                  aria-label={t("editorProtocols.ws.subprotocol.label", { n: i + 1 })}
                  aria-invalid={problem !== null}
                  placeholder={t("editorProtocols.ws.subprotocol.placeholder", { example: "graphql-transport-ws" })}
                  spellCheck={false}
                  autoComplete="off"
                  value={p}
                  readOnly={readOnly}
                  onChange={(e) => onChange(subprotocols.map((old, j) => (j === i ? e.target.value : old)))}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" && !readOnly) {
                      e.preventDefault();
                      add();
                    }
                  }}
                />
                {problem !== null && (
                  <span className={styles.rowProblem}>
                    <IconWarning size={12} />
                    {problem}
                  </span>
                )}
                {!readOnly && (
                  <button
                    type="button"
                    className={styles.iconButton}
                    aria-label={t("editorProtocols.ws.subprotocol.remove", { n: i + 1 })}
                    onClick={() => onChange(subprotocols.filter((_, j) => j !== i))}
                  >
                    <IconClose size={12} />
                  </button>
                )}
              </li>
            );
          })}
        </ul>
      )}
      {!readOnly && (
        <button type="button" className={styles.smallButton} onClick={add}>
          <IconPlus size={12} />
          {t("editorProtocols.ws.subprotocol.add")}
        </button>
      )}
    </div>
  );
}

type NumberSetting = "pingIntervalMs" | "pongTimeoutMs" | "reconnectMaxAttempts" | "reconnectDelayMs" | "reconnectMaxDelayMs";

/** A whole-number setting: empty means null (Go's default). */
function NumberSettingRow({
  id,
  label,
  help,
  value,
  placeholder,
  step,
  readOnly,
  disabled,
  onChange,
}: {
  id: string;
  label: string;
  help: string;
  value: number | null | undefined;
  placeholder: string;
  step: number;
  readOnly: boolean;
  disabled?: boolean;
  onChange: (value: number | null) => void;
}) {
  return (
    <label className={styles.settingRow} htmlFor={id}>
      <span className={styles.settingLabel}>{label}</span>
      <input
        id={id}
        className={styles.settingInput}
        type="number"
        min={0}
        step={step}
        placeholder={placeholder}
        value={value ?? ""}
        readOnly={readOnly}
        disabled={disabled}
        onChange={(e) => {
          const v = e.target.value.trim();
          const n = Number(v);
          if (v === "") onChange(null);
          else if (Number.isInteger(n) && n >= 0) onChange(n);
        }}
      />
      <span className={styles.settingHelp}>{help}</span>
    </label>
  );
}

function WsSettings({ request, readOnly, edit }: { request: RequestDTO; readOnly: boolean; edit: (patch: RequestData) => void }) {
  const t = useT();
  const settings = request.settings;
  const setSettings = (patch: Partial<RequestDTO["settings"]>) => edit({ settings: { ...settings, ...patch } });
  const setNumber = (key: NumberSetting) => (value: number | null) => setSettings({ [key]: value });
  const baseId = useId();
  const reconnect = settings.autoReconnect === true;
  return (
    <div className={styles.settings}>
      <label className={styles.settingRow} htmlFor={`${baseId}-timeout`}>
        <span className={styles.settingLabel}>{t("editorProtocols.stream.settings.connectTimeout")}</span>
        <input
          id={`${baseId}-timeout`}
          className={styles.settingInput}
          type="number"
          min={0}
          step={1000}
          placeholder="30000"
          value={settings.connectTimeoutMs ?? ""}
          readOnly={readOnly}
          onChange={(e) => {
            const v = e.target.value.trim();
            const n = Number(v);
            if (v === "") setSettings({ connectTimeoutMs: null });
            else if (Number.isInteger(n) && n >= 0) setSettings({ connectTimeoutMs: n });
          }}
        />
        <span className={styles.settingHelp}>{t("editorProtocols.ws.settings.connectTimeoutHelp")}</span>
      </label>
      <label className={styles.settingRow}>
        <input
          type="checkbox"
          checked={settings.verifyTls !== false}
          disabled={readOnly}
          onChange={(e) => setSettings({ verifyTls: e.target.checked ? null : false })}
        />
        <span className={styles.settingLabel}>{t("editorProtocols.stream.settings.verifyTls")}</span>
        <span className={styles.settingHelp}>{t("editorProtocols.ws.settings.verifyTlsHelp")}</span>
      </label>
      <label className={styles.settingRow}>
        <input
          type="checkbox"
          checked={settings.sendCookies !== false}
          disabled={readOnly}
          onChange={(e) => setSettings({ sendCookies: e.target.checked ? null : false })}
        />
        <span className={styles.settingLabel}>{t("editorProtocols.ws.settings.cookies")}</span>
        <span className={styles.settingHelp}>{t("editorProtocols.ws.settings.cookiesHelp")}</span>
      </label>
      <NumberSettingRow
        id={`${baseId}-ping`}
        label={t("editorProtocols.ws.settings.ping")}
        help={t("editorProtocols.ws.settings.pingHelp")}
        value={settings.pingIntervalMs}
        placeholder={t("editorProtocols.ws.settings.pingPlaceholder")}
        step={1000}
        readOnly={readOnly}
        onChange={setNumber("pingIntervalMs")}
      />
      <NumberSettingRow
        id={`${baseId}-pong`}
        label={t("editorProtocols.ws.settings.pongTimeout")}
        help={t("editorProtocols.ws.settings.pongTimeoutHelp", { seconds: WS_DEFAULT_PONG_TIMEOUT_MS / 1000 })}
        value={settings.pongTimeoutMs}
        placeholder={String(WS_DEFAULT_PONG_TIMEOUT_MS)}
        step={1000}
        readOnly={readOnly}
        disabled={!settings.pingIntervalMs}
        onChange={setNumber("pongTimeoutMs")}
      />
      <label className={styles.settingRow}>
        <input
          type="checkbox"
          checked={reconnect}
          disabled={readOnly}
          onChange={(e) => setSettings({ autoReconnect: e.target.checked ? true : null })}
        />
        <span className={styles.settingLabel}>{t("editorProtocols.ws.settings.reconnect")}</span>
        <span className={styles.settingHelp}>{t("editorProtocols.ws.settings.reconnectHelp")}</span>
      </label>
      <NumberSettingRow
        id={`${baseId}-attempts`}
        label={t("editorProtocols.ws.settings.attempts")}
        help={t("editorProtocols.ws.settings.attemptsHelp", { attempts: WS_DEFAULT_RECONNECT_ATTEMPTS })}
        value={settings.reconnectMaxAttempts}
        placeholder={String(WS_DEFAULT_RECONNECT_ATTEMPTS)}
        step={1}
        readOnly={readOnly}
        disabled={!reconnect}
        onChange={setNumber("reconnectMaxAttempts")}
      />
      <NumberSettingRow
        id={`${baseId}-delay`}
        label={t("editorProtocols.ws.settings.delay")}
        help={t("editorProtocols.ws.settings.delayHelp")}
        value={settings.reconnectDelayMs}
        placeholder={String(WS_DEFAULT_RECONNECT_DELAY_MS)}
        step={500}
        readOnly={readOnly}
        disabled={!reconnect}
        onChange={setNumber("reconnectDelayMs")}
      />
      <NumberSettingRow
        id={`${baseId}-max-delay`}
        label={t("editorProtocols.ws.settings.maxDelay")}
        help={t("editorProtocols.ws.settings.maxDelayHelp")}
        value={settings.reconnectMaxDelayMs}
        placeholder={String(WS_DEFAULT_RECONNECT_MAX_DELAY_MS)}
        step={1000}
        readOnly={readOnly}
        disabled={!reconnect}
        onChange={setNumber("reconnectMaxDelayMs")}
      />
    </div>
  );
}

/**
 * The message composer (WS-03, WS-04): the shared MessageComposer with
 * text, JSON, hex and Base64, saved with the request. "Load file…" puts a
 * file of up to WS_FILE_MAX_BYTES into it as Base64, to send as a binary
 * message.
 */
function Composer({
  session,
  request,
  readOnly,
  open,
  edit,
}: {
  session: WsSession;
  request: RequestDTO;
  readOnly: boolean;
  open: boolean;
  edit: (patch: RequestData) => void;
}) {
  const t = useT();
  const format = formatOf(request);
  const onFormatChange = useCallback((f: WsMessageFormat) => edit({ messageFormat: f }), [edit]);
  const onMessageChange = useCallback((text: string) => edit({ message: text }), [edit]);
  const onFile = useCallback((text: string) => edit({ message: text, messageFormat: "base64" }), [edit]);
  return (
    <MessageComposer
      format={format}
      formats={WS_MESSAGE_FORMATS}
      onFormatChange={onFormatChange}
      message={request.message}
      onMessageChange={onMessageChange}
      readOnly={readOnly}
      extra={<LoadFileButton readOnly={readOnly} onLoad={onFile} />}
      actions={[
        {
          label: t("editorProtocols.send"),
          disabled: !open,
          title: open ? undefined : t("editorProtocols.saved.connectToSend"),
          send: (text) => session.send(text, format),
        },
      ]}
    />
  );
}

/**
 * Reads a file the user picks (WS-04) and hands its bytes on as Base64.
 * Files over WS_FILE_MAX_BYTES are refused: the composer holds the text,
 * and the file is also written into the request file with it.
 */
function LoadFileButton({ readOnly, onLoad }: { readOnly: boolean; onLoad: (base64Text: string) => void }) {
  const t = useT();
  const input = useRef<HTMLInputElement>(null);
  const [error, setError] = useState<string | null>(null);
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  const onChange = (e: ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    e.target.value = "";
    if (file === undefined) return;
    if (file.size > WS_FILE_MAX_BYTES) {
      setError(
        t("editorProtocols.ws.file.tooBig", { name: file.name, size: formatSize(file.size), max: formatSize(WS_FILE_MAX_BYTES) }),
      );
      return;
    }
    setError(null);
    file.arrayBuffer().then(
      (buffer) => {
        if (mounted.current) onLoad(base64(new Uint8Array(buffer)));
      },
      () => {
        if (mounted.current) setError(t("editorProtocols.ws.file.unreadable", { name: file.name }));
      },
    );
  };

  if (readOnly) return null;
  return (
    <>
      <button
        type="button"
        className={styles.smallButton}
        title={t("editorProtocols.ws.file.loadTitle", { max: formatSize(WS_FILE_MAX_BYTES) })}
        onClick={() => {
          setError(null);
          input.current?.click();
        }}
      >
        <IconFile size={12} />
        {t("editorProtocols.ws.file.load")}
      </button>
      <input
        ref={input}
        type="file"
        className={styles.fileInput}
        aria-label={t("editorProtocols.ws.file.label")}
        tabIndex={-1}
        onChange={onChange}
      />
      {error !== null && (
        <span className={styles.fileError} role="alert">
          <IconWarning size={12} />
          {error}
        </span>
      )}
    </>
  );
}
