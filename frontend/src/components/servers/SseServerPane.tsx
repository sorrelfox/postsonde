import { useCallback, useEffect, useId, useRef, useState } from "react";
import { disconnectSseSubscriber, eventSourceSnippet, sendSseEvent, sseServerUrl, updateSseConfig } from "../../commands/sseServer";
import { t as translate } from "../../i18n";
import { Trans, useT } from "../../i18n/react";
import { toAppError } from "../../lib/errors";
import {
  selectSseComposer,
  selectSseSubscribers,
  selectSseTargetSubscriber,
  selectSseSelectionGone,
  useServers,
  useStores,
  type LocalServer,
  type SseComposerState,
  type SseServerConfig,
  type SseSubscriber,
  type SseTimer,
} from "../../stores";
import { EmptyState } from "../common/EmptyState";
import { IconPlus, IconServers, IconTrash } from "../common/icons";
import { formatSize } from "../messagelog/format";
import { MessageComposer, type ComposerAction } from "../messagelog/MessageComposer";
import sseStyles from "./SseServer.module.css";
import { Uptime } from "./Uptime";
import styles from "./WsServer.module.css";

/** How long settings edits must pause before they are sent, in ms. */
export const SSE_SETTINGS_DELAY_MS = 400;

/** Limits of the settings (sseserver). */
const MAX_KEEP_ALIVE_SEC = 3600;
const MAX_RETRY_MS = 3_600_000;
const MIN_INTERVAL_MS = 100;
const MAX_INTERVAL_MS = 86_400_000;

function errorText(err: unknown): string {
  const e = toAppError(err);
  return e.hint ? `${e.message} ${e.hint}` : e.message;
}

/** A whole number typed in a field ("" is `empty`), or null when it is not one. */
function wholeNumber(text: string, empty = 0): number | null {
  const t = text.trim();
  if (t === "") return empty;
  return /^\d+$/.test(t) ? Number(t) : null;
}

/**
 * The Subscribers tab of an SSE server (SSRV-04): the URL a client or a
 * browser page subscribes to, the connected subscribers (select one to
 * send to it, disconnect one) with the Last-Event-ID each came back with,
 * and the event composer: type, ID and retry above the data, sent to the
 * selected subscriber or to all. Dynamic values such as {{$timestamp}} are
 * rendered by Go when the event is sent.
 */
export function SseSubscribersPanel({ server }: { server: LocalServer }) {
  const t = useT();
  const stores = useStores();
  const subscribers = useServers((s) => selectSseSubscribers(s, server.id));
  const target = useServers((s) => selectSseTargetSubscriber(s, server.id));
  const selectionGone = useServers((s) => selectSseSelectionGone(s, server.id));
  const composer = useServers((s) => selectSseComposer(s, server.id));
  const [status, setStatus] = useState<string | null>(null);
  const baseId = useId();
  const running = server.state === "running";
  const url = sseServerUrl(server);
  const cors = server.sse?.allowOrigin ?? "";

  const setComposer = useCallback(
    (patch: Partial<SseComposerState>) => {
      const current = selectSseComposer(stores.servers.getState(), server.id);
      stores.servers.dispatch({ type: "servers/sseComposer", id: server.id, composer: { ...current, ...patch } });
    },
    [stores, server.id],
  );
  const onFormatChange = useCallback((format: SseComposerState["format"]) => setComposer({ format }), [setComposer]);
  const onMessageChange = useCallback((message: string) => setComposer({ message }), [setComposer]);

  const eventOf = (data: string) => {
    const retry = wholeNumber(composer.retry);
    if (retry === null || retry > MAX_RETRY_MS) {
      throw new Error(t("servers.sse.badRetry", { max: MAX_RETRY_MS }));
    }
    return { event: composer.event.trim(), data, id: composer.id, retryMs: retry };
  };
  const describe = (sent: number, failed: number, id: string) => {
    if (failed === 0) return id !== "" ? t("servers.sse.sentWithId", { count: sent, id }) : t("servers.sse.sent", { count: sent });
    const total = sent + failed;
    return id !== "" ? t("servers.sse.sentSomeWithId", { count: sent, total, id }) : t("servers.sse.sentSome", { count: sent, total });
  };

  const actions: ComposerAction[] = [
    {
      label: t("servers.sse.sendAll"),
      disabled: subscribers.length === 0,
      title: subscribers.length === 0 ? t("servers.sse.noSubscriber") : undefined,
      send: async (data) => {
        setStatus(null);
        const res = await sendSseEvent(server.id, "", eventOf(data));
        setStatus(describe(res.sent, res.failed, res.id));
      },
    },
    {
      label: target !== null ? t("servers.sse.sendOne", { id: target.id }) : t("servers.sse.sendOneNone"),
      disabled: target === null,
      title: target !== null ? undefined : selectionGone && subscribers.length > 0 ? t("servers.sse.selectionGone") : t("servers.sse.noSubscriber"),
      send: async (data) => {
        if (target === null) return;
        setStatus(null);
        const res = await sendSseEvent(server.id, target.id, eventOf(data));
        setStatus(describe(res.sent, res.failed, res.id));
      },
    },
  ];

  return (
    <div className={styles.clients}>
      <div className={styles.connectBar}>
        {url !== null ? (
          <>
            <p className={styles.connectNote}>
              <Trans
                k={server.sse?.path === "*" ? "servers.sse.subscribeAtAny" : "servers.sse.subscribeAt"}
                values={{ url: <code className={styles.url}>{url}</code> }}
              />
            </p>
            <p className={sseStyles.snippet}>
              <Trans
                k={cors === "" ? "servers.sse.fromBrowserNoCors" : "servers.sse.fromBrowser"}
                values={{ snippet: <code className={styles.url}>{eventSourceSnippet(url, cors !== "")}</code> }}
              />
            </p>
          </>
        ) : (
          <p className={styles.connectNote}>{t("servers.sse.startToAccept")}</p>
        )}
        {status !== null && (
          <p className={styles.status} role="status">
            {status}
          </p>
        )}
      </div>
      <div className={styles.listArea}>
        {subscribers.length === 0 ? (
          <EmptyState placement="top" icon={<IconServers size={24} strokeWidth={1.5} />} title={t("servers.sse.emptyTitle")}>
            {running ? t("servers.sse.emptyRunning") : t("servers.sse.emptyStopped")}
          </EmptyState>
        ) : (
          <ul className={styles.list} aria-label={t("servers.tab.subscribers")}>
            {subscribers.map((s) => (
              <SubscriberRow key={s.id} serverId={server.id} subscriber={s} selected={s.id === target?.id} />
            ))}
          </ul>
        )}
      </div>
      <div className={sseStyles.eventFields}>
        <label className={sseStyles.eventField} htmlFor={`${baseId}-event`}>
          {t("servers.sse.event")}
        </label>
        <input
          id={`${baseId}-event`}
          className={`${styles.input} ${sseStyles.eventInput}`}
          value={composer.event}
          placeholder="message" // i18n-ignore: the default SSE event type
          spellCheck={false}
          autoComplete="off"
          onChange={(e) => setComposer({ event: e.target.value })}
        />
        <label className={sseStyles.eventField} htmlFor={`${baseId}-id`}>
          {t("servers.sse.id")}
        </label>
        <input
          id={`${baseId}-id`}
          className={`${styles.input} ${sseStyles.eventInput}`}
          value={composer.id}
          placeholder={server.sse?.autoId ? t("servers.sse.nextNumber") : t("servers.sse.none")}
          spellCheck={false}
          autoComplete="off"
          onChange={(e) => setComposer({ id: e.target.value })}
        />
        <label className={sseStyles.eventField} htmlFor={`${baseId}-retry`}>
          {t("servers.sse.retry")}
        </label>
        <input
          id={`${baseId}-retry`}
          className={`${styles.input} ${sseStyles.retryInput}`}
          value={composer.retry}
          inputMode="numeric"
          placeholder={t("servers.unit.ms")}
          autoComplete="off"
          onChange={(e) => setComposer({ retry: e.target.value })}
        />
        <span className={styles.help}>
          <Trans k="servers.sse.dataHelp" values={{ timestamp: <code>{"{{$timestamp}}"}</code>, uuid: <code>{"{{$uuid}}"}</code> }} />
        </span>
      </div>
      <MessageComposer
        format={composer.format}
        onFormatChange={onFormatChange}
        message={composer.message}
        onMessageChange={onMessageChange}
        actions={actions}
        ariaLabel={t("servers.sse.eventData")}
      />
    </div>
  );
}

function SubscriberRow({ serverId, subscriber, selected }: { serverId: string; subscriber: SseSubscriber; selected: boolean }) {
  const t = useT();
  const stores = useStores();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const disconnect = () => {
    setBusy(true);
    setError(null);
    disconnectSseSubscriber(serverId, subscriber.id).then(
      () => setBusy(false), // the row goes away with the next servers:subscribers event
      (err: unknown) => {
        setBusy(false);
        setError(errorText(err));
      },
    );
  };

  const details = [
    subscriber.peer,
    subscriber.target,
    // i18n-ignore: a header name and its value
    subscriber.lastEventId !== "" ? `Last-Event-ID ${subscriber.lastEventId}` : "",
    subscriber.origin !== "" ? t("servers.sse.origin", { origin: subscriber.origin }) : "",
  ].filter((x) => x !== "");
  return (
    <li className={styles.row} data-selected={selected || undefined}>
      <div className={styles.rowLine}>
        <button
          type="button"
          className={styles.rowMain}
          aria-pressed={selected}
          aria-label={t("servers.sse.subscriberLabel", { id: subscriber.id, peer: subscriber.peer })}
          title={subscriber.userAgent !== "" ? subscriber.userAgent : undefined}
          onClick={() => stores.servers.dispatch({ type: "servers/sseSelect", id: serverId, subscriberId: subscriber.id })}
        >
          <span className={styles.clientName}>{t("servers.sse.subscriber", { id: subscriber.id })}</span>
          <span className={styles.clientDetails}>{details.join(" · ")}</span>
        </button>
        <span className={styles.counts} title={t("servers.sse.eventsSent", { count: subscriber.eventsSent, size: formatSize(subscriber.bytesSent) })}>
          ↑{subscriber.eventsSent}
        </span>
        {subscriber.connectedAt !== null && (
          <span className={styles.since}>
            <Uptime since={subscriber.connectedAt} />
          </span>
        )}
        <button
          type="button"
          className={`${styles.button} ${styles.danger}`}
          disabled={busy}
          title={t("servers.sse.disconnectTitle")}
          onClick={disconnect}
        >
          {busy ? t("servers.sse.disconnecting") : t("servers.sse.disconnect")}
        </button>
      </div>
      {error !== null && (
        <p className={styles.error} role="alert">
          {error}
        </p>
      )}
    </li>
  );
}

/** A timer as edited; `key` only keeps rows apart. */
interface TimerDraft {
  key: number;
  enabled: boolean;
  event: string;
  data: string;
  id: string;
  /** Seconds, as typed. */
  every: string;
}

type CorsMode = "off" | "any" | "origin";

interface SseSettingsDraft {
  path: string;
  anyPath: boolean;
  cors: CorsMode;
  origin: string;
  keepAliveSec: string;
  retryMs: string;
  autoId: boolean;
  timers: TimerDraft[];
}

/** A problem with the settings; `timerIndex` places it under that timer. */
export interface SseSettingsProblem {
  message: string;
  timerIndex: number | null;
}

function toSseDraft(c: SseServerConfig | null): SseSettingsDraft {
  const path = c?.path ?? "/events";
  const origin = c?.allowOrigin ?? "";
  return {
    path: path === "*" ? "/events" : path,
    anyPath: path === "*",
    cors: origin === "" ? "off" : origin === "*" ? "any" : "origin",
    origin: origin === "*" ? "" : origin,
    keepAliveSec: c && c.keepAliveSec > 0 ? String(c.keepAliveSec) : "",
    retryMs: c && c.retryMs > 0 ? String(c.retryMs) : "",
    autoId: c?.autoId ?? false,
    timers: (c?.timers ?? []).map((t, i) => ({ ...t, key: i, every: String(t.intervalMs / 1000) })),
  };
}

/** The settings a draft stands for, or a problem to show instead of sending it. */
export function sseDraftToConfig(d: SseSettingsDraft): SseServerConfig | SseSettingsProblem {
  const keepAlive = wholeNumber(d.keepAliveSec);
  if (keepAlive === null || keepAlive > MAX_KEEP_ALIVE_SEC) {
    return { message: translate("servers.sse.badKeepAlive", { max: MAX_KEEP_ALIVE_SEC }), timerIndex: null };
  }
  const retry = wholeNumber(d.retryMs);
  if (retry === null || retry > MAX_RETRY_MS) {
    return { message: translate("servers.sse.badRetry", { max: MAX_RETRY_MS }), timerIndex: null };
  }
  if (d.cors === "origin" && d.origin.trim() === "") {
    return { message: translate("servers.sse.needOrigin"), timerIndex: null };
  }
  const timers: SseTimer[] = [];
  for (const [i, t] of d.timers.entries()) {
    const seconds = Number(t.every.trim());
    const ms = Math.round(seconds * 1000);
    if (t.every.trim() === "" || !Number.isFinite(seconds) || ms < MIN_INTERVAL_MS || ms > MAX_INTERVAL_MS) {
      return {
        message: translate("servers.sse.badInterval", { n: i + 1, min: MIN_INTERVAL_MS / 1000, max: MAX_INTERVAL_MS / 1000 }),
        timerIndex: i,
      };
    }
    timers.push({ enabled: t.enabled, event: t.event.trim(), data: t.data, id: t.id, intervalMs: ms });
  }
  return {
    path: d.anyPath ? "*" : d.path.trim(),
    allowOrigin: d.cors === "off" ? "" : d.cors === "any" ? "*" : d.origin.trim(),
    keepAliveSec: keepAlive,
    retryMs: retry,
    autoId: d.autoId,
    timers,
  };
}

function isSseProblem(c: SseServerConfig | SseSettingsProblem): c is SseSettingsProblem {
  return "message" in c;
}

/** A rejected update as a problem, under the timer Go names (details.timerIndex). */
function sseProblemOf(err: unknown): SseSettingsProblem {
  const e = toAppError(err);
  const index = e.details.timerIndex;
  return { message: e.hint ? `${e.message} ${e.hint}` : e.message, timerIndex: index !== undefined && /^\d+$/.test(index) ? Number(index) : null };
}

/**
 * The Settings tab of an SSE server: path (or any path), CORS for browser
 * pages on other origins, the keep-alive comment interval, the retry sent
 * to new subscribers, automatic event IDs, and the timers that send an
 * event to every subscriber at an interval. Edits reach Go once typing
 * pauses.
 */
export function SseSettingsPanel({ server }: { server: LocalServer }) {
  const t = useT();
  const stores = useStores();
  const [draft, setDraft] = useState<SseSettingsDraft>(() => toSseDraft(server.sse));
  const [problem, setProblem] = useState<SseSettingsProblem | null>(null);
  const baseId = useId();
  const nextKey = useRef(draft.timers.length);

  const pending = useRef<SseServerConfig | null>(null);
  const seq = useRef(0);
  const flush = useCallback(() => {
    const config = pending.current;
    if (config === null) return;
    pending.current = null;
    const mine = ++seq.current;
    updateSseConfig(stores, server.id, config).then(
      () => {
        if (mine === seq.current) setProblem(null);
      },
      (err: unknown) => {
        if (mine === seq.current) setProblem(sseProblemOf(err));
      },
    );
  }, [stores, server.id]);

  const edit = (patch: Partial<SseSettingsDraft>) => {
    const next = { ...draft, ...patch };
    setDraft(next);
    const config = sseDraftToConfig(next);
    if (isSseProblem(config)) {
      pending.current = null;
      setProblem(config);
      return;
    }
    pending.current = config;
  };
  const editTimer = (index: number, patch: Partial<TimerDraft>) =>
    edit({ timers: draft.timers.map((t, i) => (i === index ? { ...t, ...patch } : t)) });
  const addTimer = () =>
    edit({
      timers: [
        ...draft.timers,
        { key: nextKey.current++, enabled: true, event: "tick", data: '{"at": {{$timestamp}}}', id: "", every: "1" },
      ],
    });

  useEffect(() => {
    if (pending.current === null) return;
    const timer = setTimeout(flush, SSE_SETTINGS_DELAY_MS);
    return () => clearTimeout(timer);
  }, [draft, flush]);
  // Edits still waiting are sent when the tab closes.
  useEffect(() => flush, [flush]);

  const timerProblem = problem !== null && problem.timerIndex !== null && problem.timerIndex < draft.timers.length ? problem : null;
  const generalProblem = problem !== null && timerProblem === null ? problem : null;

  return (
    <div className={styles.settings}>
      <div className={styles.settingRow}>
        <label className={styles.settingLabel} htmlFor={`${baseId}-path`}>
          {t("servers.sse.path")}
        </label>
        <input
          id={`${baseId}-path`}
          className={`${styles.input} ${styles.pathInput}`}
          value={draft.anyPath ? "" : draft.path}
          placeholder={draft.anyPath ? t("servers.sse.anyPathPlaceholder") : "/events"}
          disabled={draft.anyPath}
          spellCheck={false}
          autoComplete="off"
          onChange={(e) => edit({ path: e.target.value })}
        />
        <label className={styles.check}>
          <input type="checkbox" checked={draft.anyPath} onChange={(e) => edit({ anyPath: e.target.checked })} />
          {t("servers.sse.anyPath")}
        </label>
      </div>
      <div className={styles.settingRow}>
        <label className={styles.settingLabel} htmlFor={`${baseId}-cors`}>
          CORS
        </label>
        <select
          id={`${baseId}-cors`}
          className={styles.input}
          value={draft.cors}
          onChange={(e) => edit({ cors: e.target.value as CorsMode })}
        >
          <option value="off">{t("servers.sse.cors.off")}</option>
          <option value="any">{t("servers.sse.cors.any")}</option>
          <option value="origin">{t("servers.sse.cors.origin")}</option>
        </select>
        {draft.cors === "origin" && (
          <input
            className={`${styles.input} ${styles.pathInput}`}
            aria-label={t("servers.sse.allowedOrigin")}
            value={draft.origin}
            placeholder="scheme://host:port" // i18n-ignore: the form of an origin
            spellCheck={false}
            autoComplete="off"
            onChange={(e) => edit({ origin: e.target.value })}
          />
        )}
      </div>
      <p className={styles.help}>
        <Trans k="servers.sse.corsHelp" values={{ withCredentials: <code>withCredentials</code> }} />
      </p>
      <div className={styles.settingRow}>
        <label className={styles.settingLabel} htmlFor={`${baseId}-keepalive`}>
          {t("servers.sse.keepAlive")}
        </label>
        <input
          id={`${baseId}-keepalive`}
          className={`${styles.input} ${styles.sizeInput}`}
          value={draft.keepAliveSec}
          inputMode="numeric"
          placeholder={t("servers.sse.none")}
          autoComplete="off"
          onChange={(e) => edit({ keepAliveSec: e.target.value })}
        />
        <span className={styles.help}>{t("servers.sse.keepAliveHelp")}</span>
      </div>
      <div className={styles.settingRow}>
        <label className={styles.settingLabel} htmlFor={`${baseId}-retry`}>
          {t("servers.sse.retry")}
        </label>
        <input
          id={`${baseId}-retry`}
          className={`${styles.input} ${styles.sizeInput}`}
          value={draft.retryMs}
          inputMode="numeric"
          placeholder={t("servers.sse.none")}
          autoComplete="off"
          onChange={(e) => edit({ retryMs: e.target.value })}
        />
        <span className={styles.help}>{t("servers.sse.retryHelp")}</span>
      </div>
      <label className={styles.check}>
        <input type="checkbox" checked={draft.autoId} onChange={(e) => edit({ autoId: e.target.checked })} />
        {t("servers.sse.autoId")}
      </label>
      {generalProblem !== null && (
        <p className={styles.error} role="alert">
          {generalProblem.message}
        </p>
      )}

      <h3 className={styles.sectionTitle}>{t("servers.sse.timers")}</h3>
      {draft.timers.length > 0 && (
        <ol className={styles.rules} aria-label={t("servers.sse.timers")}>
          {draft.timers.map((timer, i) => (
            <TimerRow
              key={timer.key}
              index={i}
              timer={timer}
              error={timerProblem !== null && timerProblem.timerIndex === i ? timerProblem.message : null}
              onChange={(patch) => editTimer(i, patch)}
              onRemove={() => edit({ timers: draft.timers.filter((_, j) => j !== i) })}
            />
          ))}
        </ol>
      )}
      <div>
        <button type="button" className={styles.button} onClick={addTimer}>
          <IconPlus size={12} /> {t("servers.sse.addTimer")}
        </button>
      </div>
      <p className={styles.help}>
        <Trans
          k="servers.sse.timersHelp"
          values={{
            timestamp: <code>{"{{$timestamp}}"}</code>,
            isoTimestamp: <code>{"{{$isoTimestamp}}"}</code>,
            randomInt: <code>{"{{$randomInt 1 100}}"}</code>,
          }}
        />
      </p>
    </div>
  );
}

function TimerRow({
  index,
  timer,
  error,
  onChange,
  onRemove,
}: {
  index: number;
  timer: TimerDraft;
  error: string | null;
  onChange: (patch: Partial<TimerDraft>) => void;
  onRemove: () => void;
}) {
  const t = useT();
  const n = index + 1;
  const errorId = useId();
  return (
    <li className={styles.rule} data-disabled={!timer.enabled || undefined}>
      <div className={styles.ruleLine}>
        <span className={styles.ruleNumber}>{n}</span>
        <input
          type="checkbox"
          aria-label={t("servers.sse.timer.enabled", { n })}
          checked={timer.enabled}
          onChange={(e) => onChange({ enabled: e.target.checked })}
        />
        <label className={styles.closeLabel}>
          <Trans
            k="servers.sse.timer.every"
            values={{
              input: (
                <input
                  className={`${styles.input} ${styles.codeInput}`}
                  aria-label={t("servers.sse.timer.interval", { n })}
                  aria-invalid={error !== null || undefined}
                  aria-describedby={error !== null ? errorId : undefined}
                  inputMode="decimal"
                  value={timer.every}
                  onChange={(e) => onChange({ every: e.target.value })}
                />
              ),
            }}
          />
        </label>
        <input
          className={`${styles.input} ${styles.rulePattern}`}
          aria-label={t("servers.sse.timer.event", { n })}
          value={timer.event}
          placeholder={t("servers.sse.timer.eventPlaceholder")}
          spellCheck={false}
          autoComplete="off"
          onChange={(e) => onChange({ event: e.target.value })}
        />
        <input
          className={`${styles.input} ${sseStyles.eventInput}`}
          aria-label={t("servers.sse.timer.id", { n })}
          value={timer.id}
          placeholder={t("servers.sse.timer.idPlaceholder")}
          spellCheck={false}
          autoComplete="off"
          onChange={(e) => onChange({ id: e.target.value })}
        />
        <button
          type="button"
          className={`${styles.iconButton} ${styles.danger}`}
          aria-label={t("servers.sse.timer.remove", { n })}
          title={t("servers.sse.timer.removeTitle")}
          onClick={onRemove}
        >
          <IconTrash size={12} />
        </button>
      </div>
      <div className={styles.ruleLine}>
        <span className={styles.ruleArrow} aria-hidden="true">
          →
        </span>
        <textarea
          className={`${styles.input} ${styles.ruleReply}`}
          aria-label={t("servers.sse.timer.data", { n })}
          value={timer.data}
          rows={2}
          placeholder={t("servers.sse.timer.dataPlaceholder")}
          spellCheck={false}
          onChange={(e) => onChange({ data: e.target.value })}
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
