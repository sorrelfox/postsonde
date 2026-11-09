import { useCallback, useEffect, useId, useRef, useState, type FormEvent } from "react";
import { disconnectSioSocket, emitToSioSockets, ioSnippet, setSioSocketRooms, sioServerUrl, updateSioConfig } from "../../commands/sioServer";
import { t as translate, type MessageKey } from "../../i18n";
import { Trans, useT } from "../../i18n/react";
import { toAppError } from "../../lib/errors";
import {
  selectSioComposer,
  selectSioSelectionGone,
  selectSioSockets,
  selectSioTargetSocket,
  useServers,
  useStores,
  type LocalServer,
  type SioAckMode,
  type SioComposerState,
  type SioRule,
  type SioRuleMatch,
  type SioServerConfig,
  type SioSocket,
} from "../../stores";
import { EmptyState } from "../common/EmptyState";
import { IconChevron, IconPlus, IconServers, IconTrash } from "../common/icons";
import { MessageComposer, type ComposerAction } from "../messagelog/MessageComposer";
import sioStyles from "./SioServer.module.css";
import { Uptime } from "./Uptime";
import styles from "./WsServer.module.css";

/** How long settings edits must pause before they are sent, in ms. */
export const SIO_SETTINGS_DELAY_MS = 400;

/** Limits of the settings (sioserver). */
const MIN_HEARTBEAT_MS = 100;
const MAX_HEARTBEAT_MS = 3_600_000;
const MAX_PAYLOAD = 256 * 1024 * 1024;
const MAX_RULE_DELAY_MS = 60_000;
const DEFAULT_PATH = "/socket.io/";

function errorText(err: unknown): string {
  const e = toAppError(err);
  return e.hint ? `${e.message} ${e.hint}` : e.message;
}

/** A whole number typed in a field ("" is `empty`), or null when it is not one. */
function wholeNumber(text: string, empty: number): number | null {
  const t = text.trim();
  if (t === "") return empty;
  return /^\d+$/.test(t) ? Number(t) : null;
}

/** "3" for a socket in "/", "3 /admin" in another namespace. */
function socketName(s: SioSocket): string {
  return s.namespace === "/" ? s.clientId : `${s.clientId} ${s.namespace}`;
}

/**
 * The Sockets tab of a Socket.IO server (SSRV-03): the URL and a line
 * that connects the official client, the connected sockets (one per
 * client and namespace: select one to emit to it, set its rooms,
 * disconnect it or close its client), and the emit composer: event name,
 * Ack and room above the arguments (JSON), emitted to the selected socket,
 * to every socket or to a room.
 */
export function SioSocketsPanel({ server }: { server: LocalServer }) {
  const t = useT();
  const stores = useStores();
  const sockets = useServers((s) => selectSioSockets(s, server.id));
  const target = useServers((s) => selectSioTargetSocket(s, server.id));
  const selectionGone = useServers((s) => selectSioSelectionGone(s, server.id));
  const composer = useServers((s) => selectSioComposer(s, server.id));
  const [status, setStatus] = useState<string | null>(null);
  const baseId = useId();
  const running = server.state === "running";
  const url = sioServerUrl(server);

  const setComposer = useCallback(
    (patch: Partial<SioComposerState>) => {
      const current = selectSioComposer(stores.servers.getState(), server.id);
      stores.servers.dispatch({ type: "servers/sioComposer", id: server.id, composer: { ...current, ...patch } });
    },
    [stores, server.id],
  );
  const onFormatChange = useCallback((format: SioComposerState["format"]) => setComposer({ format }), [setComposer]);
  const onMessageChange = useCallback((message: string) => setComposer({ message }), [setComposer]);

  const describe = (sent: number, failed: number) =>
    failed === 0 ? t("servers.sio.sent", { count: sent }) : t("servers.sio.sentSome", { count: sent, total: sent + failed });
  const emit = async (socketId: string, room: string, args: string) => {
    setStatus(null);
    const res = await emitToSioSockets(server.id, { socketId, room }, composer.event.trim(), args, composer.ack);
    setStatus(describe(res.sent, res.failed));
  };
  const room = composer.room.trim();
  const actions: ComposerAction[] = [
    {
      label: target !== null ? t("servers.sio.emitOne", { id: socketName(target) }) : t("servers.sio.emitOneNone"),
      disabled: target === null,
      title: target !== null ? undefined : selectionGone && sockets.length > 0 ? t("servers.sio.selectionGone") : t("servers.sio.noSocket"),
      send: async (args) => {
        if (target !== null) await emit(target.id, "", args);
      },
    },
    {
      label: t("servers.sio.emitAll"),
      disabled: sockets.length === 0,
      title: sockets.length === 0 ? t("servers.sio.noSocket") : undefined,
      send: (args) => emit("", "", args),
    },
    {
      label: room !== "" ? t("servers.sio.emitRoom", { room }) : t("servers.sio.emitRoomNone"),
      disabled: room === "" || sockets.length === 0,
      title: room === "" ? t("servers.sio.noRoom") : undefined,
      send: (args) => emit("", room, args),
    },
  ];

  return (
    <div className={styles.clients}>
      <div className={styles.connectBar}>
        {url !== null ? (
          <>
            <p className={styles.connectNote}>
              <Trans k="servers.sio.connectTo" values={{ url: <code className={styles.url}>{url}</code> }} />
            </p>
            <p className={sioStyles.snippet}>
              <Trans
                k="servers.sio.fromClient"
                values={{ snippet: <code className={styles.url}>{ioSnippet(url, server.socketIo?.path ?? DEFAULT_PATH)}</code> }}
              />
            </p>
          </>
        ) : (
          <p className={styles.connectNote}>{t("servers.sio.startToAccept")}</p>
        )}
        {status !== null && (
          <p className={styles.status} role="status">
            {status}
          </p>
        )}
      </div>
      <div className={styles.listArea}>
        {sockets.length === 0 ? (
          <EmptyState placement="top" icon={<IconServers size={24} strokeWidth={1.5} />} title={t("servers.sio.emptyTitle")}>
            {running ? t("servers.sio.emptyRunning") : t("servers.sio.emptyStopped")}
          </EmptyState>
        ) : (
          <ul className={styles.list} aria-label={t("servers.tab.sockets")}>
            {sockets.map((s) => (
              <SocketRow key={s.id} serverId={server.id} socket={s} selected={s.id === target?.id} />
            ))}
          </ul>
        )}
      </div>
      <div className={sioStyles.eventFields}>
        <label className={sioStyles.eventField} htmlFor={`${baseId}-event`}>
          {t("servers.sio.event")}
        </label>
        <input
          id={`${baseId}-event`}
          className={`${styles.input} ${sioStyles.eventInput}`}
          value={composer.event}
          placeholder={t("servers.sio.eventPlaceholder")}
          spellCheck={false}
          autoComplete="off"
          onChange={(e) => setComposer({ event: e.target.value })}
        />
        <label className={styles.check}>
          <input type="checkbox" checked={composer.ack} onChange={(e) => setComposer({ ack: e.target.checked })} />
          {t("servers.sio.ack")}
        </label>
        <label className={sioStyles.eventField} htmlFor={`${baseId}-room`}>
          {t("servers.sio.room")}
        </label>
        <input
          id={`${baseId}-room`}
          className={`${styles.input} ${sioStyles.roomInput}`}
          value={composer.room}
          placeholder={t("servers.sio.roomPlaceholder")}
          spellCheck={false}
          autoComplete="off"
          onChange={(e) => setComposer({ room: e.target.value })}
        />
        <span className={styles.help}>
          <Trans k="servers.sio.argsHelp" values={{ binary: <code>{'{"$base64": "…"}'}</code>, timestamp: <code>{"{{$timestamp}}"}</code> }} />
        </span>
      </div>
      <MessageComposer
        format={composer.format}
        onFormatChange={onFormatChange}
        message={composer.message}
        onMessageChange={onMessageChange}
        actions={actions}
        ariaLabel={t("servers.sio.args")}
      />
    </div>
  );
}

function SocketRow({ serverId, socket, selected }: { serverId: string; socket: SioSocket; selected: boolean }) {
  const t = useT();
  const stores = useStores();
  const [editingRooms, setEditingRooms] = useState(false);
  const [rooms, setRooms] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const baseId = useId();

  const act = (call: () => Promise<void>, after?: () => void) => {
    setBusy(true);
    setError(null);
    call().then(
      () => {
        setBusy(false);
        after?.();
      },
      (err: unknown) => {
        setBusy(false);
        setError(errorText(err));
      },
    );
  };
  const submitRooms = (e: FormEvent) => {
    e.preventDefault();
    const list = rooms
      .split(",")
      .map((r) => r.trim())
      .filter((r) => r !== "");
    act(() => setSioSocketRooms(serverId, socket.id, list), () => setEditingRooms(false));
  };

  const details = [
    socket.peer,
    socket.transport,
    socket.rooms.length > 0 ? t("servers.sio.inRooms", { rooms: socket.rooms.join(", ") }) : "",
    socket.auth !== "" ? t("servers.sio.auth", { auth: socket.auth }) : "",
  ].filter((x) => x !== "");
  return (
    <li className={styles.row} data-selected={selected || undefined}>
      <div className={styles.rowLine}>
        <button
          type="button"
          className={styles.rowMain}
          aria-pressed={selected}
          aria-label={t("servers.sio.socketLabel", { id: socketName(socket), peer: socket.peer })}
          title={socket.userAgent !== "" ? socket.userAgent : undefined}
          onClick={() => stores.servers.dispatch({ type: "servers/sioSelect", id: serverId, socketId: socket.id })}
        >
          <span className={styles.clientName}>
            {socket.namespace === "/"
              ? t("servers.sio.socket", { id: socket.clientId })
              : t("servers.sio.socketIn", { id: socket.clientId, namespace: socket.namespace })}
          </span>
          <span className={styles.clientDetails}>{details.join(" · ")}</span>
        </button>
        <span className={styles.counts} title={t("servers.sio.counts", { received: socket.eventsIn, sent: socket.eventsOut })}>
          ↓{socket.eventsIn} ↑{socket.eventsOut}
        </span>
        {socket.connectedAt !== null && (
          <span className={styles.since}>
            <Uptime since={socket.connectedAt} />
          </span>
        )}
        <button
          type="button"
          className={styles.button}
          aria-expanded={editingRooms}
          aria-controls={editingRooms ? `${baseId}-rooms` : undefined}
          disabled={busy}
          onClick={() => {
            setRooms(socket.rooms.join(", "));
            setEditingRooms((v) => !v);
            setError(null);
          }}
        >
          {t("servers.sio.roomsMenu")}
        </button>
        <button
          type="button"
          className={`${styles.button} ${styles.danger}`}
          disabled={busy}
          title={t("servers.sio.disconnectTitle")}
          onClick={() => act(() => disconnectSioSocket(serverId, socket.id, false))}
        >
          {t("servers.sio.disconnect")}
        </button>
        <button
          type="button"
          className={`${styles.button} ${styles.danger}`}
          disabled={busy}
          title={t("servers.sio.closeClientTitle")}
          onClick={() => act(() => disconnectSioSocket(serverId, socket.id, true))}
        >
          {t("servers.sio.closeClient")}
        </button>
      </div>
      {editingRooms && (
        <form id={`${baseId}-rooms`} className={styles.closeForm} onSubmit={submitRooms} aria-label={t("servers.sio.roomsOf", { id: socketName(socket) })}>
          <input
            className={`${styles.input} ${styles.reasonInput}`}
            aria-label={t("servers.sio.rooms")}
            placeholder={t("servers.sio.roomsPlaceholder")}
            value={rooms}
            disabled={busy}
            spellCheck={false}
            onChange={(e) => setRooms(e.target.value)}
          />
          <button type="submit" className={styles.button} disabled={busy}>
            {t("servers.sio.setRooms")}
          </button>
          <button type="button" className={styles.button} disabled={busy} onClick={() => setEditingRooms(false)}>
            {t("common.cancel")}
          </button>
        </form>
      )}
      {error !== null && (
        <p className={styles.error} role="alert">
          {error}
        </p>
      )}
    </li>
  );
}

/** A rule as edited; `key` only keeps rows apart while they move. */
interface RuleDraft extends Omit<SioRule, "delayMs"> {
  key: number;
  delayMs: string;
}

type CorsMode = "off" | "any" | "origin";

interface SioSettingsDraft {
  path: string;
  namespaces: string;
  auth: string;
  transports: SioServerConfig["transports"];
  cors: CorsMode;
  origin: string;
  pingIntervalMs: string;
  pingTimeoutMs: string;
  maxPayload: string;
  echo: boolean;
  ackMode: SioAckMode;
  rules: RuleDraft[];
}

/** A problem with the settings; `ruleIndex` places it under that rule. */
export interface SioSettingsProblem {
  message: string;
  ruleIndex: number | null;
}

const MATCH_LABELS: Record<SioRuleMatch, MessageKey> = {
  exact: "servers.sio.match.exact",
  contains: "servers.sio.match.contains",
  regex: "servers.sio.match.regex",
};

const ACK_LABELS: Record<SioAckMode, MessageKey> = {
  echo: "servers.sio.ackMode.echo",
  empty: "servers.sio.ackMode.empty",
  none: "servers.sio.ackMode.none",
};

function toSioDraft(c: SioServerConfig | null): SioSettingsDraft {
  const origin = c?.allowOrigin ?? "";
  return {
    path: c?.path ?? DEFAULT_PATH,
    namespaces: (c?.namespaces ?? []).join(", "),
    auth: c?.auth ?? "",
    transports: c?.transports ?? "",
    cors: origin === "" ? "off" : origin === "*" ? "any" : "origin",
    origin: origin === "*" ? "" : origin,
    pingIntervalMs: String(c?.pingIntervalMs ?? 25000),
    pingTimeoutMs: String(c?.pingTimeoutMs ?? 20000),
    maxPayload: String(c?.maxPayload ?? 1000000),
    echo: c?.echo ?? false,
    ackMode: c?.ackMode ?? "echo",
    rules: (c?.rules ?? []).map((r, i) => ({ ...r, key: i, delayMs: r.delayMs > 0 ? String(r.delayMs) : "" })),
  };
}

/** The settings a draft stands for, or a problem to show instead of sending it. */
export function sioDraftToConfig(d: SioSettingsDraft): SioServerConfig | SioSettingsProblem {
  const heartbeat = (text: string) => {
    const n = wholeNumber(text, 0);
    return n === null || (n !== 0 && (n < MIN_HEARTBEAT_MS || n > MAX_HEARTBEAT_MS)) ? null : n;
  };
  const limits = { min: MIN_HEARTBEAT_MS, max: MAX_HEARTBEAT_MS };
  const interval = heartbeat(d.pingIntervalMs);
  if (interval === null) return { message: translate("servers.sio.badPingInterval", limits), ruleIndex: null };
  const timeout = heartbeat(d.pingTimeoutMs);
  if (timeout === null) return { message: translate("servers.sio.badPingTimeout", limits), ruleIndex: null };
  const payload = wholeNumber(d.maxPayload, 0);
  if (payload === null || payload > MAX_PAYLOAD) {
    return { message: translate("servers.sio.badPayload", { max: MAX_PAYLOAD }), ruleIndex: null };
  }
  if (d.cors === "origin" && d.origin.trim() === "") {
    return { message: translate("servers.sio.needOrigin"), ruleIndex: null };
  }
  const rules: SioRule[] = [];
  for (const [i, r] of d.rules.entries()) {
    const ms = wholeNumber(r.delayMs, 0);
    if (ms === null || ms > MAX_RULE_DELAY_MS) {
      return { message: translate("servers.sio.badDelay", { n: i + 1, max: MAX_RULE_DELAY_MS }), ruleIndex: i };
    }
    rules.push({ enabled: r.enabled, match: r.match, pattern: r.pattern, ack: r.ack, emit: r.emit.trim(), emitArgs: r.emitArgs, delayMs: ms });
  }
  return {
    path: d.path.trim(),
    namespaces: d.namespaces
      .split(",")
      .map((n) => n.trim())
      .filter((n) => n !== ""),
    auth: d.auth.trim(),
    transports: d.transports,
    allowOrigin: d.cors === "off" ? "" : d.cors === "any" ? "*" : d.origin.trim(),
    pingIntervalMs: interval,
    pingTimeoutMs: timeout,
    maxPayload: payload,
    echo: d.echo,
    ackMode: d.ackMode,
    rules,
  };
}

function isSioProblem(c: SioServerConfig | SioSettingsProblem): c is SioSettingsProblem {
  return "message" in c;
}

/** A rejected update as a problem, under the rule Go names (details.ruleIndex). */
function sioProblemOf(err: unknown): SioSettingsProblem {
  const e = toAppError(err);
  const index = e.details.ruleIndex;
  return { message: e.hint ? `${e.message} ${e.hint}` : e.message, ruleIndex: index !== undefined && /^\d+$/.test(index) ? Number(index) : null };
}

/**
 * The Settings tab of a Socket.IO server: the Engine.IO path, the
 * namespaces it accepts, the auth a CONNECT must carry, the transports,
 * CORS for browser pages on other origins, the heartbeat and the largest
 * payload; then the answers: how events that ask for an acknowledgement
 * are acknowledged, echo, and the auto-reply rules. Edits reach Go once
 * typing pauses.
 */
export function SioSettingsPanel({ server }: { server: LocalServer }) {
  const t = useT();
  const stores = useStores();
  const [draft, setDraft] = useState<SioSettingsDraft>(() => toSioDraft(server.socketIo));
  const [problem, setProblem] = useState<SioSettingsProblem | null>(null);
  const baseId = useId();
  const nextKey = useRef(draft.rules.length);

  const pending = useRef<SioServerConfig | null>(null);
  const seq = useRef(0);
  const flush = useCallback(() => {
    const config = pending.current;
    if (config === null) return;
    pending.current = null;
    const mine = ++seq.current;
    updateSioConfig(stores, server.id, config).then(
      () => {
        if (mine === seq.current) setProblem(null);
      },
      (err: unknown) => {
        if (mine === seq.current) setProblem(sioProblemOf(err));
      },
    );
  }, [stores, server.id]);

  const edit = (patch: Partial<SioSettingsDraft>) => {
    const next = { ...draft, ...patch };
    setDraft(next);
    const config = sioDraftToConfig(next);
    if (isSioProblem(config)) {
      pending.current = null;
      setProblem(config);
      return;
    }
    pending.current = config;
  };
  const editRule = (index: number, patch: Partial<RuleDraft>) =>
    edit({ rules: draft.rules.map((r, i) => (i === index ? { ...r, ...patch } : r)) });
  const moveRule = (index: number, by: -1 | 1) => {
    const to = index + by;
    if (to < 0 || to >= draft.rules.length) return;
    const rules = [...draft.rules];
    [rules[index], rules[to]] = [rules[to], rules[index]];
    edit({ rules });
  };
  const addRule = () =>
    edit({
      rules: [
        ...draft.rules,
        { key: nextKey.current++, enabled: true, match: "exact", pattern: "", ack: "", emit: "", emitArgs: "", delayMs: "" },
      ],
    });

  useEffect(() => {
    if (pending.current === null) return;
    const timer = setTimeout(flush, SIO_SETTINGS_DELAY_MS);
    return () => clearTimeout(timer);
  }, [draft, flush]);
  // Edits still waiting are sent when the tab closes.
  useEffect(() => flush, [flush]);

  const ruleProblem = problem !== null && problem.ruleIndex !== null && problem.ruleIndex < draft.rules.length ? problem : null;
  const generalProblem = problem !== null && ruleProblem === null ? problem : null;

  return (
    <div className={styles.settings}>
      <div className={styles.settingRow}>
        <label className={styles.settingLabel} htmlFor={`${baseId}-path`}>
          {t("servers.sio.path")}
        </label>
        <input
          id={`${baseId}-path`}
          className={`${styles.input} ${styles.pathInput}`}
          value={draft.path}
          placeholder={DEFAULT_PATH}
          spellCheck={false}
          autoComplete="off"
          onChange={(e) => edit({ path: e.target.value })}
        />
      </div>
      <div className={styles.settingRow}>
        <label className={styles.settingLabel} htmlFor={`${baseId}-namespaces`}>
          {t("servers.sio.namespaces")}
        </label>
        <input
          id={`${baseId}-namespaces`}
          className={`${styles.input} ${styles.pathInput}`}
          value={draft.namespaces}
          placeholder={t("servers.sio.namespacesPlaceholder")}
          spellCheck={false}
          autoComplete="off"
          onChange={(e) => edit({ namespaces: e.target.value })}
        />
      </div>
      <p className={styles.help}>{t("servers.sio.namespacesHelp")}</p>
      <div className={styles.settingRow}>
        <label className={styles.settingLabel} htmlFor={`${baseId}-auth`}>
          {t("servers.sio.requiredAuth")}
        </label>
        <input
          id={`${baseId}-auth`}
          className={`${styles.input} ${styles.pathInput}`}
          value={draft.auth}
          placeholder={t("servers.sio.requiredAuthPlaceholder")}
          spellCheck={false}
          autoComplete="off"
          onChange={(e) => edit({ auth: e.target.value })}
        />
      </div>
      <p className={styles.help}>{t("servers.sio.requiredAuthHelp")}</p>
      <div className={styles.settingRow}>
        <label className={styles.settingLabel} htmlFor={`${baseId}-transports`}>
          {t("servers.sio.transports")}
        </label>
        <select
          id={`${baseId}-transports`}
          className={styles.input}
          value={draft.transports}
          onChange={(e) => edit({ transports: e.target.value as SioSettingsDraft["transports"] })}
        >
          <option value="">{t("servers.sio.transports.both")}</option>
          <option value="websocket">{t("servers.sio.transports.websocket")}</option>
          <option value="polling">{t("servers.sio.transports.polling")}</option>
        </select>
      </div>
      <div className={styles.settingRow}>
        <label className={styles.settingLabel} htmlFor={`${baseId}-cors`}>
          CORS
        </label>
        <select id={`${baseId}-cors`} className={styles.input} value={draft.cors} onChange={(e) => edit({ cors: e.target.value as CorsMode })}>
          <option value="off">{t("servers.sio.cors.off")}</option>
          <option value="any">{t("servers.sio.cors.any")}</option>
          <option value="origin">{t("servers.sio.cors.origin")}</option>
        </select>
        {draft.cors === "origin" && (
          <input
            className={`${styles.input} ${styles.pathInput}`}
            aria-label={t("servers.sio.allowedOrigin")}
            value={draft.origin}
            placeholder="scheme://host:port" // i18n-ignore: the form of an origin
            spellCheck={false}
            autoComplete="off"
            onChange={(e) => edit({ origin: e.target.value })}
          />
        )}
      </div>
      <div className={styles.settingRow}>
        <label className={styles.settingLabel} htmlFor={`${baseId}-interval`}>
          {t("servers.sio.pingInterval")}
        </label>
        <input
          id={`${baseId}-interval`}
          className={`${styles.input} ${styles.sizeInput}`}
          value={draft.pingIntervalMs}
          inputMode="numeric"
          autoComplete="off"
          onChange={(e) => edit({ pingIntervalMs: e.target.value })}
        />
        <label className={styles.settingLabel} htmlFor={`${baseId}-timeout`}>
          {t("servers.sio.pingTimeout")}
        </label>
        <input
          id={`${baseId}-timeout`}
          className={`${styles.input} ${styles.sizeInput}`}
          value={draft.pingTimeoutMs}
          inputMode="numeric"
          autoComplete="off"
          onChange={(e) => edit({ pingTimeoutMs: e.target.value })}
        />
      </div>
      <p className={styles.help}>{t("servers.sio.heartbeatHelp")}</p>
      <div className={styles.settingRow}>
        <label className={styles.settingLabel} htmlFor={`${baseId}-payload`}>
          {t("servers.sio.maxPayload")}
        </label>
        <input
          id={`${baseId}-payload`}
          className={`${styles.input} ${styles.sizeInput}`}
          value={draft.maxPayload}
          inputMode="numeric"
          autoComplete="off"
          onChange={(e) => edit({ maxPayload: e.target.value })}
        />
        <span className={styles.help}>{t("servers.sio.maxPayloadHelp")}</span>
      </div>
      {generalProblem !== null && (
        <p className={styles.error} role="alert">
          {generalProblem.message}
        </p>
      )}

      <h3 className={styles.sectionTitle}>{t("servers.sio.answers")}</h3>
      <div className={styles.settingRow}>
        <label className={styles.settingLabel} htmlFor={`${baseId}-ackmode`}>
          {t("servers.sio.ackMode")}
        </label>
        <select id={`${baseId}-ackmode`} className={styles.input} value={draft.ackMode} onChange={(e) => edit({ ackMode: e.target.value as SioAckMode })}>
          {(Object.keys(ACK_LABELS) as SioAckMode[]).map((m) => (
            <option key={m} value={m}>
              {t(ACK_LABELS[m])}
            </option>
          ))}
        </select>
      </div>
      <label className={styles.check}>
        <input type="checkbox" checked={draft.echo} onChange={(e) => edit({ echo: e.target.checked })} />
        {t("servers.sio.echo")}
      </label>
      {draft.rules.length > 0 && (
        <ol className={styles.rules} aria-label={t("servers.sio.rules")}>
          {draft.rules.map((r, i) => (
            <RuleRow
              key={r.key}
              index={i}
              count={draft.rules.length}
              rule={r}
              error={ruleProblem !== null && ruleProblem.ruleIndex === i ? ruleProblem.message : null}
              onChange={(patch) => editRule(i, patch)}
              onMove={(by) => moveRule(i, by)}
              onRemove={() => edit({ rules: draft.rules.filter((_, j) => j !== i) })}
            />
          ))}
        </ol>
      )}
      <div>
        <button type="button" className={styles.button} onClick={addRule}>
          <IconPlus size={12} /> {t("servers.sio.addRule")}
        </button>
      </div>
      <p className={styles.help}>
        <Trans k="servers.sio.rulesHelp" values={{ timestamp: <code>{"{{$timestamp}}"}</code>, binary: <code>{'{"$base64": "…"}'}</code> }} />
      </p>
    </div>
  );
}

function RuleRow({
  index,
  count,
  rule,
  error,
  onChange,
  onMove,
  onRemove,
}: {
  index: number;
  count: number;
  rule: RuleDraft;
  error: string | null;
  onChange: (patch: Partial<RuleDraft>) => void;
  onMove: (by: -1 | 1) => void;
  onRemove: () => void;
}) {
  const t = useT();
  const n = index + 1;
  const errorId = useId();
  return (
    <li className={styles.rule} data-disabled={!rule.enabled || undefined}>
      <div className={styles.ruleLine}>
        <span className={styles.ruleNumber}>{n}</span>
        <input
          type="checkbox"
          aria-label={t("servers.sio.rule.enabled", { n })}
          checked={rule.enabled}
          onChange={(e) => onChange({ enabled: e.target.checked })}
        />
        <select
          className={styles.input}
          aria-label={t("servers.sio.rule.match", { n })}
          value={rule.match}
          onChange={(e) => onChange({ match: e.target.value as SioRuleMatch })}
        >
          {(Object.keys(MATCH_LABELS) as SioRuleMatch[]).map((m) => (
            <option key={m} value={m}>
              {t(MATCH_LABELS[m])}
            </option>
          ))}
        </select>
        <input
          className={`${styles.input} ${styles.rulePattern}`}
          aria-label={t("servers.sio.rule.pattern", { n })}
          aria-invalid={error !== null || undefined}
          aria-describedby={error !== null ? errorId : undefined}
          value={rule.pattern}
          placeholder={t("servers.sio.rule.patternPlaceholder")}
          spellCheck={false}
          autoComplete="off"
          onChange={(e) => onChange({ pattern: e.target.value })}
        />
        <button
          type="button"
          className={styles.iconButton}
          aria-label={t("servers.sio.rule.moveUp", { n })}
          title={t("servers.sio.rule.moveUpTitle")}
          disabled={index === 0}
          onClick={() => onMove(-1)}
        >
          <IconChevron direction="up" size={12} />
        </button>
        <button
          type="button"
          className={styles.iconButton}
          aria-label={t("servers.sio.rule.moveDown", { n })}
          title={t("servers.sio.rule.moveDownTitle")}
          disabled={index === count - 1}
          onClick={() => onMove(1)}
        >
          <IconChevron direction="down" size={12} />
        </button>
        <button
          type="button"
          className={`${styles.iconButton} ${styles.danger}`}
          aria-label={t("servers.sio.rule.remove", { n })}
          title={t("servers.sio.rule.removeTitle")}
          onClick={onRemove}
        >
          <IconTrash size={12} />
        </button>
      </div>
      <div className={styles.ruleLine}>
        <span className={styles.ruleArrow} aria-hidden="true">
          →
        </span>
        <input
          className={`${styles.input} ${styles.rulePattern}`}
          aria-label={t("servers.sio.rule.ack", { n })}
          value={rule.ack}
          placeholder={t("servers.sio.rule.ackPlaceholder")}
          spellCheck={false}
          autoComplete="off"
          onChange={(e) => onChange({ ack: e.target.value })}
        />
        <label className={styles.closeLabel}>
          <Trans
            k="servers.sio.rule.after"
            values={{
              input: (
                <input
                  className={`${styles.input} ${styles.codeInput}`}
                  aria-label={t("servers.sio.rule.delay", { n })}
                  inputMode="numeric"
                  placeholder="0"
                  value={rule.delayMs}
                  onChange={(e) => onChange({ delayMs: e.target.value })}
                />
              ),
            }}
          />
        </label>
      </div>
      <div className={styles.ruleLine}>
        <span className={styles.ruleArrow} aria-hidden="true">
          →
        </span>
        <input
          className={`${styles.input} ${sioStyles.eventInput}`}
          aria-label={t("servers.sio.rule.emit", { n })}
          value={rule.emit}
          placeholder={t("servers.sio.rule.emitPlaceholder")}
          spellCheck={false}
          autoComplete="off"
          onChange={(e) => onChange({ emit: e.target.value })}
        />
        <input
          className={`${styles.input} ${styles.rulePattern}`}
          aria-label={t("servers.sio.rule.emitArgs", { n })}
          value={rule.emitArgs}
          placeholder={t("servers.sio.rule.emitArgsPlaceholder")}
          spellCheck={false}
          autoComplete="off"
          disabled={rule.emit.trim() === ""}
          onChange={(e) => onChange({ emitArgs: e.target.value })}
        />
      </div>
      {error !== null && (
        <p id={errorId} className={styles.error} role="alert">
          {error}
        </p>
      )}
    </li>
  );
}
