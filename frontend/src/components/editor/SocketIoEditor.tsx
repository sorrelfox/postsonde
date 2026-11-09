import { useCallback, useEffect, useId, useRef, useState, type KeyboardEvent } from "react";
import type { EditorView } from "@codemirror/view";
import { useAutoSave } from "../../commands/useAutoSave";
import { useStreamTests } from "../../commands/streamTests";
import { countAssertions } from "../../lib/assertions";
import { StreamTests } from "../response/StreamTests";
import { useJsonCheck } from "../../commands/useJsonCheck";
import { useRequestDraft } from "../../commands/useRequestDraft";
import { useSioSession } from "../../commands/useSioSession";
import { listenNames, reasonText, transportName, type SioSession, type SioSessionState } from "../../commands/sioSessions";
import { t as translate, type MessageKey } from "../../i18n";
import { Trans, useT } from "../../i18n/react";
import type { KeyValueDTO, RequestDTO, SavedMessageDTO } from "../../lib/collection";
import type { AppError } from "../../lib/errors";
import { SIO_DEFAULT_PATH, SIO_TRANSPORTS, sioConnectInput, sioTransport, sioVersion, type SioTransport, type SioVersion } from "../../lib/socketio";
import { useDrafts, useStores, type RequestData, type RequestId } from "../../stores";
import { EmptyState } from "../common/EmptyState";
import { IconLock, IconWarning } from "../common/icons";
import { KeyValueTable } from "../common/KeyValueTable";
import { MessageComposer } from "../messagelog/MessageComposer";
import { MessageLog } from "../messagelog/MessageLog";
import { AssertionsEditor } from "./AssertionsEditor";
import { AuthEditor } from "./AuthEditor";
import { CodeEditor } from "./CodeEditor";
import { installErrorMarker, setErrorMarker } from "./errorMarker";
import { HEADER_SUGGEST, HEADER_SUGGEST_LABELS } from "./headerSuggest";
import { useKeyValueRows } from "./keyValueRows";
import { NotesPanel } from "./NotesPanel";
import { RequestProxySettings } from "./RequestProxySettings";
import { useReadOnlyNotice } from "./RequestEditor";
import { SavedMessages } from "./SavedMessages";
import sioStyles from "./SocketIoEditor.module.css";
import { useUrlParamsSync } from "./useUrlParamsSync";
import styles from "./WebSocketEditor.module.css";

export type SioSubTab = "params" | "headers" | "auth" | "options" | "tests" | "settings" | "notes";

const SUB_TABS: readonly { id: SioSubTab; labelKey: MessageKey }[] = [
  { id: "params", labelKey: "editorProtocols.tab.params" },
  { id: "headers", labelKey: "editorProtocols.tab.headers" },
  { id: "auth", labelKey: "editorProtocols.tab.auth" },
  { id: "options", labelKey: "editorProtocols.tab.options" },
  // Assertions on the received events (TEST-08), and their results.
  { id: "tests", labelKey: "editorProtocols.tab.tests" },
  { id: "settings", labelKey: "editorProtocols.tab.settings" },
  { id: "notes", labelKey: "editorProtocols.tab.notes" },
];

function isSubTab(value: string | undefined): value is SioSubTab {
  return SUB_TABS.some((t) => t.id === value);
}

function countRows(rows: readonly KeyValueDTO[] | null): number {
  return (rows ?? []).filter((r) => r.enabled && r.name !== "").length;
}

function errorText(err: AppError): string {
  return err.hint ? `${err.message} ${err.hint}` : err.message;
}

/** A hint when the URL's scheme cannot be used for Socket.IO, or null. */
export function sioSchemeHint(url: string): string | null {
  const trimmed = url.trim();
  if (trimmed === "" || trimmed.startsWith("{{")) return null;
  const scheme = /^([a-z][a-z0-9+.-]*):\/\//i.exec(trimmed)?.[1]?.toLowerCase();
  if (scheme === "http" || scheme === "https" || scheme === "ws" || scheme === "wss") return null;
  // No text right after "://": the bundle check (scripts/check-dist.mjs) reads that as a remote URL.
  return translate("editorProtocols.sio.scheme");
}

/** The status line under the URL bar. */
function statusText(state: SioSessionState): string {
  switch (state.status) {
    case "idle":
      return translate("editorProtocols.stream.notConnected");
    case "connecting":
      return translate("editorProtocols.stream.connecting", { url: state.url });
    case "open":
      return [
        translate("editorProtocols.sio.status.connected", { namespace: state.namespace, transport: transportName(state.transport) }),
        translate("editorProtocols.sio.status.socket", { id: state.socketId }),
        translate("editorProtocols.sio.status.emitted", { count: state.emitted }),
        state.ignored > 0
          ? translate("editorProtocols.sio.status.receivedIgnored", { count: state.received, ignored: state.ignored })
          : translate("editorProtocols.stream.received", { count: state.received }),
      ].join(" · ");
    case "closed":
      return state.error !== null ? errorText(state.error) : reasonText(state.reason);
  }
}

function statusTone(state: SioSessionState): string {
  if (state.status === "open") return "ok";
  if (state.status === "connecting") return "busy";
  return state.error !== null ? "bad" : "idle";
}

export interface SocketIoEditorProps {
  id: RequestId;
}

/**
 * The editor of a Socket.IO request (SIO-01, SIO-03…SIO-07): the server
 * URL with Connect/Disconnect and the connection state, the handshake
 * options (query params synced with the URL, headers, the auth payload,
 * the path, namespace and transport, the events the log shows, settings),
 * the log of what was sent and received (MessageLog), the emit composer
 * (event name, JSON arguments with {"$base64": …} binaries, ack switch)
 * and the saved emits beside it. Everything is auto-saved; {{variables}}
 * are drawn as chips and rendered by Go on connect and emit. The
 * connection belongs to the request's tab (commands/sioSessions.ts).
 */
export function SocketIoEditor({ id }: SocketIoEditorProps) {
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
  return <LoadedSocketIoEditor id={id} request={draft.request} />;
}

function LoadedSocketIoEditor({ id, request }: { id: RequestId; request: RequestDTO }) {
  const t = useT();
  const stores = useStores();
  const { edit } = useAutoSave(id);
  const streamTests = useStreamTests(id);
  const notice = useReadOnlyNotice(id);
  const readOnly = notice !== null;
  const { session, state } = useSioSession(id);

  const baseId = useId();
  const stored = useDrafts((s) => s.subTabs[id]);
  const subTab: SioSubTab = isSubTab(stored) ? stored : "params";
  const selectSubTab = (next: SioSubTab) => stores.drafts.dispatch({ type: "drafts/setSubTab", id, subTab: next });
  const tabRefs = useRef<Partial<Record<SioSubTab, HTMLButtonElement | null>>>({});

  const { onUrlChange, onParamsChange } = useUrlParamsSync(id, edit);
  const onHeaders = useCallback((headers: KeyValueDTO[]) => edit({ headers }), [edit]);
  const [paramRows, setParamRows] = useKeyValueRows(request.params, onParamsChange);
  const [headerRows, setHeaderRows] = useKeyValueRows(request.headers, onHeaders);

  // The listen filter applies to what arrives from now on (SIO-04).
  const listen = request.listen;
  useEffect(() => session.setListen(listen), [session, listen]);

  const active = state.status === "connecting" || state.status === "open";
  const hint = sioSchemeHint(request.url);

  const connect = () => {
    const current = (stores.drafts.getState().byId[id]?.request as unknown as RequestDTO | undefined) ?? request;
    session.setListen(current.listen);
    session.connect(sioConnectInput(current));
  };

  const onUrlKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if (e.key === "Enter" && !e.metaKey && !e.ctrlKey && !e.shiftKey && !e.altKey && !active && request.url.trim() !== "") {
      connect();
    }
  };

  const counts: Partial<Record<SioSubTab, number>> = {
    params: countRows(request.params),
    headers: countRows(request.headers),
    auth: (request.authPayload ?? "").trim() !== "" || !["", "none", "inherit"].includes(request.auth.type) ? 1 : 0,
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
            SIO
          </span>
          <CodeEditor
            className={styles.url}
            singleLine
            ariaLabel="URL"
            placeholder={t("editorProtocols.sio.urlPlaceholder")}
            value={request.url}
            readOnly={readOnly}
            onChange={readOnly ? undefined : onUrlChange}
          />
        </div>
        {active ? (
          <button type="button" className={styles.disconnect} onClick={() => session.disconnect()}>
            {state.status === "connecting" ? t("editorProtocols.cancel") : t("editorProtocols.stream.disconnect")}
          </button>
        ) : (
          <button type="button" className={styles.connect} onClick={connect} disabled={request.url.trim() === ""}>
            {t("editorProtocols.stream.connect")}
          </button>
        )}
      </div>

      <div className={styles.statusRow}>
        <p className={styles.status} role="status" data-tone={statusTone(state)}>
          <span className={styles.dot} aria-hidden="true" />
          <span className={styles.statusText}>{hint ?? statusText(state)}</span>
        </p>
      </div>

      <div className={styles.subTabs} role="tablist" aria-label={t("editorProtocols.tab.connectionOptions")}>
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
        {subTab === "auth" && (
          <>
            <AuthEditor id={id} auth={request.auth} readOnly={readOnly} protocol="socketio" />
            <AuthTarget request={request} readOnly={readOnly} edit={edit} />
            <AuthPayload request={request} readOnly={readOnly} edit={edit} />
          </>
        )}
        {subTab === "options" && <SioOptions request={request} readOnly={readOnly} edit={edit} />}
        {subTab === "tests" && (
          <>
            <AssertionsEditor id={id} assertions={request.assertions} readOnly={readOnly} edit={edit} stream results={streamTests.tests} />
            <StreamTests id={id} />
          </>
        )}
        {subTab === "settings" && <SioSettings request={request} readOnly={readOnly} edit={edit} />}
        {subTab === "notes" && (
          <NotesPanel
            value={request.description ?? ""}
            subject="request"
            readOnly={readOnly}
            onChange={(description) => edit({ description })}
          />
        )}
      </div>

      <div className={styles.logArea}>
        <MessageLog source={session.log} label={t("editorProtocols.sio.log")} />
      </div>

      <div className={styles.composerArea}>
        <div className={styles.composerMain}>
          <Composer session={session} request={request} readOnly={readOnly} open={state.status === "open"} edit={edit} />
        </div>
        <SavedEmits session={session} request={request} readOnly={readOnly} open={state.status === "open"} edit={edit} />
      </div>
    </div>
  );
}

/**
 * Where the Auth tab's credentials go (AUTH-31): the headers and query of
 * every transport request, or members of the auth payload below (a token
 * as "token", a username and password, an API key by its name), which a
 * browser client can send where it cannot set headers.
 */
function AuthTarget({ request, readOnly, edit }: { request: RequestDTO; readOnly: boolean; edit: (patch: RequestData) => void }) {
  const t = useT();
  const baseId = useId();
  const value = (request.authIn ?? "") === "" ? "handshake" : (request.authIn as string);
  const options: { value: string; label: string }[] = [
    { value: "handshake", label: t("editorProtocols.sio.authIn.handshake") },
    {
      value: "payload",
      // Socket.IO v2 has no auth payload: Go sends the same members in the URL's query.
      label: request.socketIoVersion === "v2" ? t("editorProtocols.sio.authIn.payloadV2") : t("editorProtocols.sio.authIn.payload"),
    },
  ];
  if (value !== "handshake" && value !== "payload") options.push({ value, label: t("editorProtocols.sio.authIn.unknown", { value }) });
  const type = request.auth.type === "" ? "none" : request.auth.type;
  return (
    <div className={sioStyles.authTarget} role="radiogroup" aria-labelledby={`${baseId}-label`}>
      <span id={`${baseId}-label`} className={styles.note}>
        {t("editorProtocols.sio.authIn.label")}
      </span>
      {options.map((o) => (
        <label key={o.value}>
          <input
            type="radio"
            name={`${baseId}-authIn`}
            value={o.value}
            checked={value === o.value}
            disabled={readOnly || type === "none"}
            onChange={() => edit({ authIn: o.value === "handshake" ? "" : o.value })}
          />{" "}
          {o.label}
        </label>
      ))}
    </div>
  );
}

/** Whether the auth payload looks like a JSON object (or is empty). */
export function authLooksLikeObject(text: string): boolean {
  const t = text.trim();
  return t === "" || t.startsWith("{");
}

/**
 * The Auth tab (SIO-07): the JSON object sent as the CONNECT packet's
 * `auth`, as socket.io-client's `auth` option. Checked while typing; its
 * {{variables}} are rendered on connect.
 */
function AuthPayload({ request, readOnly, edit }: { request: RequestDTO; readOnly: boolean; edit: (patch: RequestData) => void }) {
  const t = useT();
  const text = request.authPayload ?? "";
  const [view, setView] = useState<EditorView | null>(null);
  const onReady = useCallback((v: EditorView) => {
    installErrorMarker(v);
    setView(v);
  }, []);
  const onChange = useCallback((authPayload: string) => edit({ authPayload }), [edit]);
  const check = useJsonCheck(text, true);
  const error = check.text === text ? check.error : null;
  useEffect(() => {
    if (view === null) return;
    const marker = error !== null && text === view.state.doc.toString() ? { offset: error.offset, message: error.message } : null;
    view.dispatch({ effects: setErrorMarker.of(marker) });
  }, [view, text, error]);
  return (
    <div className={sioStyles.auth}>
      <p className={styles.note}>
        <Trans
          k="editorProtocols.sio.auth.note"
          values={{ option: <code>auth</code>, example: <code>{'{"token": "{{token}}"}'}</code> }}
        />
      </p>
      <div className={sioStyles.authFrame}>
        <CodeEditor
          value={text}
          language="json"
          ariaLabel={t("editorProtocols.sio.auth.label")}
          // i18n-ignore: a JSON example
          placeholder='{"token": "…"}'
          readOnly={readOnly}
          onChange={readOnly ? undefined : onChange}
          onReady={onReady}
        />
      </div>
      {error !== null && (
        <p className={sioStyles.authStatus} role="status">
          <IconWarning size={12} />
          <span>
            {t("editorProtocols.positionMessage", { line: error.line, column: error.column, message: error.message })}
          </span>
        </p>
      )}
      {error === null && !authLooksLikeObject(text) && (
        <p className={sioStyles.authStatus} role="status">
          <IconWarning size={12} />
          {/* i18n-ignore: a JSON example */}
          <span>{t("editorProtocols.sio.auth.notObject", { example: '{"token": "…"}' })}</span>
        </p>
      )}
    </div>
  );
}

/** The Options tab (SIO-01, SIO-02, SIO-04, SIO-06): the server's version, path, namespace, transport and the events shown. */
function SioOptions({ request, readOnly, edit }: { request: RequestDTO; readOnly: boolean; edit: (patch: RequestData) => void }) {
  const t = useT();
  const baseId = useId();
  const transport = sioTransport(request.transport);
  const version = sioVersion(request.socketIoVersion);
  const help = SIO_TRANSPORTS.find((tr) => tr.value === transport)?.help ?? "";
  const [listenText, setListenText] = useState(() => (request.listen ?? []).join(", "));
  const stored = (request.listen ?? []).join(", ");
  const lastStored = useRef(stored);
  useEffect(() => {
    // Another edit (undo, a reload) changed the list: show it.
    if (stored !== lastStored.current) {
      lastStored.current = stored;
      setListenText(stored);
    }
  }, [stored]);
  const onListen = (text: string) => {
    setListenText(text);
    const names = listenNames(text.split(","));
    lastStored.current = names.join(", ");
    edit({ listen: names });
  };
  return (
    <div className={sioStyles.fields}>
      <label className={sioStyles.fieldLabel} htmlFor={`${baseId}-version`}>
        {t("editorProtocols.sio.options.version")}
      </label>
      <select
        id={`${baseId}-version`}
        className={sioStyles.select}
        value={version}
        disabled={readOnly}
        onChange={(e) => edit({ socketIoVersion: e.target.value as SioVersion })}
      >
        <option value="">{t("editorProtocols.sio.options.versionV4")}</option>
        <option value="v2">{t("editorProtocols.sio.options.versionV2")}</option>
      </select>
      <p className={sioStyles.fieldHelp}>
        {version === "v2" ? t("editorProtocols.sio.options.versionV2Help") : t("editorProtocols.sio.options.versionV4Help")}
      </p>

      <label className={sioStyles.fieldLabel} htmlFor={`${baseId}-path`}>
        {t("editorProtocols.sio.options.path")}
      </label>
      <input
        id={`${baseId}-path`}
        className={sioStyles.textInput}
        placeholder={SIO_DEFAULT_PATH}
        spellCheck={false}
        autoComplete="off"
        value={request.path ?? ""}
        readOnly={readOnly}
        onChange={(e) => edit({ path: e.target.value })}
      />
      <p className={sioStyles.fieldHelp}>{t("editorProtocols.sio.options.pathHelp")}</p>

      <label className={sioStyles.fieldLabel} htmlFor={`${baseId}-namespace`}>
        {t("editorProtocols.sio.options.namespace")}
      </label>
      <input
        id={`${baseId}-namespace`}
        className={sioStyles.textInput}
        placeholder={t("editorProtocols.sio.options.namespacePlaceholder")}
        spellCheck={false}
        autoComplete="off"
        value={request.namespace ?? ""}
        readOnly={readOnly}
        onChange={(e) => edit({ namespace: e.target.value })}
      />
      <p className={sioStyles.fieldHelp}>{t("editorProtocols.sio.options.namespaceHelp")}</p>

      <label className={sioStyles.fieldLabel} htmlFor={`${baseId}-transport`}>
        {t("editorProtocols.sio.options.transport")}
      </label>
      <select
        id={`${baseId}-transport`}
        className={sioStyles.select}
        value={transport}
        disabled={readOnly}
        onChange={(e) => edit({ transport: e.target.value as SioTransport })}
      >
        {SIO_TRANSPORTS.map((tr) => (
          <option key={tr.value} value={tr.value}>
            {tr.label}
          </option>
        ))}
      </select>
      <p className={sioStyles.fieldHelp}>{t("editorProtocols.sio.options.transportHelp", { help })}</p>

      <label className={sioStyles.fieldLabel} htmlFor={`${baseId}-listen`}>
        {t("editorProtocols.sio.options.listen")}
      </label>
      <input
        id={`${baseId}-listen`}
        className={sioStyles.textInput}
        placeholder={t("editorProtocols.sio.options.listenPlaceholder")}
        spellCheck={false}
        autoComplete="off"
        value={listenText}
        readOnly={readOnly}
        onChange={(e) => onListen(e.target.value)}
      />
      <p className={sioStyles.fieldHelp}>{t("editorProtocols.sio.options.listenHelp")}</p>
    </div>
  );
}

function SioSettings({ request, readOnly, edit }: { request: RequestDTO; readOnly: boolean; edit: (patch: RequestData) => void }) {
  const t = useT();
  const settings = request.settings;
  const setSettings = (patch: Partial<RequestDTO["settings"]>) => edit({ settings: { ...settings, ...patch } });
  const baseId = useId();
  const numberInput = (key: "connectTimeoutMs" | "ackTimeoutMs", value: string, min: number) => {
    const v = value.trim();
    const n = Number(v);
    if (v === "") setSettings({ [key]: null });
    else if (Number.isInteger(n) && n >= min) setSettings({ [key]: n });
  };
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
          placeholder="20000"
          value={settings.connectTimeoutMs ?? ""}
          readOnly={readOnly}
          onChange={(e) => numberInput("connectTimeoutMs", e.target.value, 0)}
        />
        <span className={styles.settingHelp}>{t("editorProtocols.sio.settings.connectTimeoutHelp")}</span>
      </label>
      <label className={styles.settingRow} htmlFor={`${baseId}-ack`}>
        <span className={styles.settingLabel}>{t("editorProtocols.sio.settings.ackTimeout")}</span>
        <input
          id={`${baseId}-ack`}
          className={styles.settingInput}
          type="number"
          min={1}
          step={1000}
          placeholder="10000"
          value={settings.ackTimeoutMs ?? ""}
          readOnly={readOnly}
          onChange={(e) => numberInput("ackTimeoutMs", e.target.value, 1)}
        />
        <span className={styles.settingHelp}>{t("editorProtocols.sio.settings.ackTimeoutHelp")}</span>
      </label>
      <label className={styles.settingRow}>
        <input
          type="checkbox"
          checked={settings.verifyTls !== false}
          disabled={readOnly}
          onChange={(e) => setSettings({ verifyTls: e.target.checked ? null : false })}
        />
        <span className={styles.settingLabel}>{t("editorProtocols.stream.settings.verifyTls")}</span>
        <span className={styles.settingHelp}>{t("editorProtocols.sio.settings.verifyTlsHelp")}</span>
      </label>
      <label className={styles.settingRow}>
        <input
          type="checkbox"
          checked={settings.sendCookies !== false}
          disabled={readOnly}
          onChange={(e) => setSettings({ sendCookies: e.target.checked ? null : false })}
        />
        <span className={styles.settingLabel}>{t("editorProtocols.ws.settings.cookies")}</span>
        <span className={styles.settingHelp}>{t("editorProtocols.sio.settings.cookiesHelp")}</span>
      </label>
      <RequestProxySettings settings={settings} readOnly={readOnly} edit={edit} />
    </div>
  );
}

/**
 * The emit composer (SIO-03, SIO-05): the event name, the ack switch and
 * the arguments as JSON (a JSON array holds one argument per element;
 * {"$base64": "…"} or {"$hex": "…"} anywhere is a binary argument).
 */
function Composer({
  session,
  request,
  readOnly,
  open,
  edit,
}: {
  session: SioSession;
  request: RequestDTO;
  readOnly: boolean;
  open: boolean;
  edit: (patch: RequestData) => void;
}) {
  const t = useT();
  const baseId = useId();
  const event = request.event ?? "";
  const ack = request.ack ?? false;
  const onArgs = useCallback((message: string) => edit({ message }), [edit]);
  const noFormat = useCallback(() => {}, []);
  const disabledTitle = !open
    ? t("editorProtocols.sio.emit.connect")
    : event.trim() === ""
      ? t("editorProtocols.sio.emit.noEvent")
      : undefined;
  return (
    <>
      <div className={sioStyles.eventRow}>
        <label className={sioStyles.eventLabel} htmlFor={`${baseId}-event`}>
          {t("editorProtocols.sio.emit.event")}
        </label>
        <input
          id={`${baseId}-event`}
          className={sioStyles.eventInput}
          placeholder={t("editorProtocols.sio.emit.eventPlaceholder")}
          spellCheck={false}
          autoComplete="off"
          value={event}
          readOnly={readOnly}
          onChange={(e) => edit({ event: e.target.value })}
        />
        <label className={sioStyles.ackLabel}>
          <input type="checkbox" checked={ack} disabled={readOnly} onChange={(e) => edit({ ack: e.target.checked })} />
          {t("editorProtocols.sio.emit.ack")}
        </label>
        {/* i18n-ignore: a JSON example */}
        <span className={sioStyles.argsHint}>{t("editorProtocols.sio.emit.argsHint", { example: '{"$base64": "…"}' })}</span>
      </div>
      <MessageComposer
        format="json"
        formats={["json"]}
        onFormatChange={noFormat}
        message={request.message}
        onMessageChange={onArgs}
        ariaLabel={t("editorProtocols.sio.emit.args")}
        readOnly={readOnly}
        actions={[
          {
            label: t("editorProtocols.sio.emit.emit"),
            disabled: disabledTitle !== undefined,
            title: disabledTitle,
            send: (text) => session.emit(event, text, ack),
          },
        ]}
      />
    </>
  );
}

/** The saved emits, kept with the request; the composer's event, arguments and ack are what Save stores. */
function SavedEmits({
  session,
  request,
  readOnly,
  open,
  edit,
}: {
  session: SioSession;
  request: RequestDTO;
  readOnly: boolean;
  open: boolean;
  edit: (patch: RequestData) => void;
}) {
  const onChange = useCallback((savedMessages: SavedMessageDTO[]) => edit({ savedMessages }), [edit]);
  const onLoad = useCallback((m: SavedMessageDTO) => edit({ event: m.event ?? "", message: m.text, ack: m.ack ?? false }), [edit]);
  const onSend = useCallback((m: SavedMessageDTO) => session.emit(m.event ?? "", m.text, m.ack ?? false), [session]);
  return (
    <SavedMessages
      messages={request.savedMessages ?? []}
      onChange={onChange}
      composer={{ text: request.message, format: "json", event: request.event ?? "", ack: request.ack ?? false }}
      onLoad={onLoad}
      onSend={onSend}
      canSend={open}
      readOnly={readOnly}
    />
  );
}
