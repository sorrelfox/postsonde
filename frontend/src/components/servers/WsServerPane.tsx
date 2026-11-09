import { useCallback, useEffect, useId, useRef, useState, type FormEvent } from "react";
import {
  broadcastToWsClients,
  disconnectWsClient,
  sendToWsClient,
  updateWebSocketConfig,
  WS_SERVER_NORMAL_CLOSURE,
  wsServerUrl,
} from "../../commands/wsServer";
import { formatNumber, t as translate, type MessageKey } from "../../i18n";
import { Trans, useT } from "../../i18n/react";
import { toAppError } from "../../lib/errors";
import {
  selectWsClients,
  selectWsComposer,
  selectWsTargetClient,
  selectWsSelectionGone,
  useServers,
  useStores,
  type LocalServer,
  type WsClient,
  type WsComposerState,
  type WsAutoReplyRule,
  type WsRuleMatch,
  type WsServerConfig,
} from "../../stores";
import { EmptyState } from "../common/EmptyState";
import { IconChevron, IconPlus, IconServers, IconTrash } from "../common/icons";
import { MessageComposer, type ComposerAction } from "../messagelog/MessageComposer";
import { Uptime } from "./Uptime";
import styles from "./WsServer.module.css";

/** How long settings edits must pause before they are sent, in ms. */
export const WS_SETTINGS_DELAY_MS = 400;

/** The largest message the settings accept (wsserver.MaxMessageSizeLimit), in bytes. */
const MAX_MESSAGE_SIZE = 256 * 1024 * 1024;

function errorText(err: unknown): string {
  const e = toAppError(err);
  return e.hint ? `${e.message} ${e.hint}` : e.message;
}

function formatBytes(n: number): string {
  const one = (v: number) => formatNumber(v, { minimumFractionDigits: 1, maximumFractionDigits: 1 });
  if (n < 1024) return translate("servers.size.bytes", { n: formatNumber(n) });
  if (n < 1024 * 1024) return translate("servers.size.kib", { n: one(n / 1024) });
  return translate("servers.size.mib", { n: one(n / (1024 * 1024)) });
}

/**
 * The Clients tab of a WebSocket server (SSRV-01): the URL clients connect
 * to, the connected clients (select one to send to it, disconnect one with
 * a close code and reason) and the composer, which sends to the selected
 * client or to all.
 */
export function WsClientsPanel({ server }: { server: LocalServer }) {
  const t = useT();
  const stores = useStores();
  const clients = useServers((s) => selectWsClients(s, server.id));
  const target = useServers((s) => selectWsTargetClient(s, server.id));
  const selectionGone = useServers((s) => selectWsSelectionGone(s, server.id));
  const composer = useServers((s) => selectWsComposer(s, server.id));
  const [status, setStatus] = useState<string | null>(null);
  const running = server.state === "running";
  const url = wsServerUrl(server);

  const setComposer = useCallback(
    (patch: Partial<WsComposerState>) => {
      const current = selectWsComposer(stores.servers.getState(), server.id);
      stores.servers.dispatch({ type: "servers/wsComposer", id: server.id, composer: { ...current, ...patch } });
    },
    [stores, server.id],
  );
  const onFormatChange = useCallback((format: WsComposerState["format"]) => setComposer({ format }), [setComposer]);
  const onMessageChange = useCallback((message: string) => setComposer({ message }), [setComposer]);

  const actions: ComposerAction[] = [
    {
      label: target !== null ? t("servers.ws.sendOne", { id: target.id }) : t("servers.ws.sendOneNone"),
      disabled: target === null,
      title: target !== null ? undefined : selectionGone && clients.length > 0 ? t("servers.ws.selectionGone") : t("servers.ws.noClient"),
      send: async (text) => {
        if (target === null) return;
        setStatus(null);
        await sendToWsClient(server.id, target.id, text);
      },
    },
    {
      label: t("servers.ws.sendAll"),
      disabled: clients.length === 0,
      title: clients.length === 0 ? t("servers.ws.noClient") : undefined,
      send: async (text) => {
        setStatus(null);
        const res = await broadcastToWsClients(server.id, text);
        const total = res.sent + res.failed;
        setStatus(
          res.failed === 0
            ? t("servers.ws.sent", { count: res.sent })
            : t("servers.ws.sentSome", { count: res.sent, total }),
        );
      },
    },
  ];

  return (
    <div className={styles.clients}>
      <div className={styles.connectBar}>
        {url !== null ? (
          <p className={styles.connectNote}>
            <Trans
              k={server.webSocket?.path === "*" ? "servers.ws.connectToAny" : "servers.ws.connectTo"}
              values={{ url: <code className={styles.url}>{url}</code> }}
            />
          </p>
        ) : (
          <p className={styles.connectNote}>{t("servers.ws.startToAccept")}</p>
        )}
        {status !== null && (
          <p className={styles.status} role="status">
            {status}
          </p>
        )}
      </div>
      <div className={styles.listArea}>
        {clients.length === 0 ? (
          <EmptyState placement="top" icon={<IconServers size={24} strokeWidth={1.5} />} title={t("servers.ws.emptyTitle")}>
            {running ? t("servers.ws.emptyRunning") : t("servers.ws.emptyStopped")}
          </EmptyState>
        ) : (
          <ul className={styles.list} aria-label={t("servers.tab.clients")}>
            {clients.map((c) => (
              <ClientRow key={c.id} serverId={server.id} client={c} selected={c.id === target?.id} />
            ))}
          </ul>
        )}
      </div>
      <MessageComposer
        format={composer.format}
        onFormatChange={onFormatChange}
        message={composer.message}
        onMessageChange={onMessageChange}
        actions={actions}
      />
    </div>
  );
}

function ClientRow({ serverId, client, selected }: { serverId: string; client: WsClient; selected: boolean }) {
  const t = useT();
  const stores = useStores();
  const [closing, setClosing] = useState(false);
  const [code, setCode] = useState(String(WS_SERVER_NORMAL_CLOSURE));
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const baseId = useId();

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const n = code.trim() === "" ? WS_SERVER_NORMAL_CLOSURE : Number(code);
    if (!Number.isInteger(n)) {
      setError(t("servers.ws.badCode", { example: WS_SERVER_NORMAL_CLOSURE }));
      return;
    }
    setBusy(true);
    setError(null);
    disconnectWsClient(serverId, client.id, n, reason).then(
      () => {
        // The row goes away with the next servers:clients event.
        setBusy(false);
        setClosing(false);
      },
      (err: unknown) => {
        setBusy(false);
        setError(errorText(err));
      },
    );
  };

  const details = [client.peer, client.target, client.subprotocol !== "" ? t("servers.ws.subprotocol", { name: client.subprotocol }) : ""].filter((x) => x !== "");
  return (
    <li className={styles.row} data-selected={selected || undefined}>
      <div className={styles.rowLine}>
        <button
          type="button"
          className={styles.rowMain}
          aria-pressed={selected}
          aria-label={t("servers.ws.clientLabel", { id: client.id, peer: client.peer })}
          title={client.userAgent !== "" ? client.userAgent : undefined}
          onClick={() => stores.servers.dispatch({ type: "servers/wsSelectClient", id: serverId, clientId: client.id })}
        >
          <span className={styles.clientName}>{t("servers.ws.client", { id: client.id })}</span>
          <span className={styles.clientDetails}>{details.join(" · ")}</span>
        </button>
        <span className={styles.counts} title={t("servers.ws.counts", {
            received: client.messagesIn,
            inSize: formatBytes(client.bytesIn),
            sent: client.messagesOut,
            outSize: formatBytes(client.bytesOut),
          })}>
          ↓{client.messagesIn} ↑{client.messagesOut}
        </span>
        {client.connectedAt !== null && (
          <span className={styles.since}>
            <Uptime since={client.connectedAt} />
          </span>
        )}
        <button
          type="button"
          className={styles.button}
          aria-expanded={closing}
          aria-controls={closing ? `${baseId}-close` : undefined}
          onClick={() => {
            setClosing((v) => !v);
            setError(null);
          }}
        >
          {t("servers.ws.disconnectMenu")}
        </button>
      </div>
      {closing && (
        <form id={`${baseId}-close`} className={styles.closeForm} onSubmit={submit} aria-label={t("servers.ws.disconnectClient", { id: client.id })}>
          <label className={styles.closeLabel}>
            {t("servers.ws.closeCode")}
            <input
              className={`${styles.input} ${styles.codeInput}`}
              inputMode="numeric"
              value={code}
              disabled={busy}
              aria-invalid={error !== null || undefined}
              onChange={(e) => setCode(e.target.value)}
            />
          </label>
          <input
            className={`${styles.input} ${styles.reasonInput}`}
            aria-label={t("servers.ws.closeReason")}
            placeholder={t("servers.ws.reasonPlaceholder")}
            value={reason}
            maxLength={123}
            disabled={busy}
            onChange={(e) => setReason(e.target.value)}
          />
          <button type="submit" className={`${styles.button} ${styles.danger}`} disabled={busy}>
            {busy ? t("servers.ws.disconnecting") : t("servers.ws.disconnect")}
          </button>
          <button type="button" className={styles.button} disabled={busy} onClick={() => setClosing(false)}>
            {t("common.cancel")}
          </button>
          {error !== null && (
            <p className={styles.error} role="alert">
              {error}
            </p>
          )}
        </form>
      )}
    </li>
  );
}

/** The longest delay of an auto-reply rule (wsserver.MaxRuleDelayMs), in ms. */
const MAX_RULE_DELAY_MS = 60_000;

/** An auto-reply rule as edited; `key` only keeps rows apart while they move. */
interface RuleDraft {
  key: number;
  enabled: boolean;
  match: WsRuleMatch;
  pattern: string;
  reply: string;
  delayMs: string;
}

interface SettingsDraft {
  path: string;
  anyPath: boolean;
  subprotocols: string;
  echo: boolean;
  maxMessageSize: string;
  rules: RuleDraft[];
}

/** A problem with the settings; `ruleIndex` places it under that rule. */
export interface SettingsProblem {
  message: string;
  ruleIndex: number | null;
}

const MATCH_LABELS: Record<WsRuleMatch, MessageKey> = {
  exact: "servers.ws.match.exact",
  contains: "servers.ws.match.contains",
  regex: "servers.ws.match.regex",
};

const PATTERN_PLACEHOLDERS: Record<WsRuleMatch, MessageKey> = {
  exact: "servers.ws.pattern.exact",
  contains: "servers.ws.pattern.contains",
  regex: "servers.ws.pattern.regex",
};

function toDraft(c: WsServerConfig | null): SettingsDraft {
  const path = c?.path ?? "/";
  return {
    path: path === "*" ? "/" : path,
    anyPath: path === "*",
    subprotocols: (c?.subprotocols ?? []).join(", "),
    echo: c?.echo ?? false,
    maxMessageSize: c && c.maxMessageSize > 0 ? String(c.maxMessageSize) : "",
    rules: (c?.rules ?? []).map((r, i) => ({ ...r, key: i, delayMs: r.delayMs > 0 ? String(r.delayMs) : "" })),
  };
}

/** The settings a draft stands for, or a problem to show instead of sending it. */
export function draftToConfig(d: SettingsDraft): WsServerConfig | SettingsProblem {
  const size = d.maxMessageSize.trim();
  const n = size === "" ? 0 : Number(size);
  if (!Number.isInteger(n) || n < 0 || n > MAX_MESSAGE_SIZE) {
    return {
      message: translate("servers.ws.badSize", { max: MAX_MESSAGE_SIZE }),
      ruleIndex: null,
    };
  }
  const rules: WsAutoReplyRule[] = [];
  for (const [i, r] of d.rules.entries()) {
    const delay = r.delayMs.trim();
    const ms = delay === "" ? 0 : Number(delay);
    if (!Number.isInteger(ms) || ms < 0 || ms > MAX_RULE_DELAY_MS) {
      return { message: translate("servers.ws.badDelay", { n: i + 1, max: MAX_RULE_DELAY_MS }), ruleIndex: i };
    }
    rules.push({ enabled: r.enabled, match: r.match, pattern: r.pattern, reply: r.reply, delayMs: ms });
  }
  return {
    path: d.anyPath ? "*" : d.path.trim(),
    subprotocols: d.subprotocols
      .split(",")
      .map((p) => p.trim())
      .filter((p) => p !== ""),
    echo: d.echo,
    maxMessageSize: n,
    rules,
  };
}

function isProblem(c: WsServerConfig | SettingsProblem): c is SettingsProblem {
  return "message" in c;
}

/** A rejected update as a problem, under the rule Go names (details.ruleIndex). */
function problemOf(err: unknown): SettingsProblem {
  const e = toAppError(err);
  const index = e.details.ruleIndex;
  return { message: e.hint ? `${e.message} ${e.hint}` : e.message, ruleIndex: index !== undefined && /^\d+$/.test(index) ? Number(index) : null };
}

/**
 * The Settings tab of a WebSocket server: path (or any path), accepted
 * subprotocols, the largest message, echo and the auto-reply rules
 * (SSRV-02). Edits reach Go once typing pauses; a running server applies
 * them to new connections (echo and the rules at once).
 */
export function WsSettingsPanel({ server }: { server: LocalServer }) {
  const t = useT();
  const stores = useStores();
  const [draft, setDraft] = useState<SettingsDraft>(() => toDraft(server.webSocket));
  const [problem, setProblem] = useState<SettingsProblem | null>(null);
  const baseId = useId();
  const nextKey = useRef(draft.rules.length);

  const pending = useRef<WsServerConfig | null>(null);
  const seq = useRef(0);
  const flush = useCallback(() => {
    const config = pending.current;
    if (config === null) return;
    pending.current = null;
    const mine = ++seq.current;
    updateWebSocketConfig(stores, server.id, config).then(
      () => {
        if (mine === seq.current) setProblem(null);
      },
      (err: unknown) => {
        if (mine === seq.current) setProblem(problemOf(err));
      },
    );
  }, [stores, server.id]);

  const edit = (patch: Partial<SettingsDraft>) => {
    const next = { ...draft, ...patch };
    setDraft(next);
    const config = draftToConfig(next);
    if (isProblem(config)) {
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
      rules: [...draft.rules, { key: nextKey.current++, enabled: true, match: "exact", pattern: "", reply: "", delayMs: "" }],
    });

  useEffect(() => {
    if (pending.current === null) return;
    const timer = setTimeout(flush, WS_SETTINGS_DELAY_MS);
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
          {t("servers.ws.path")}
        </label>
        <input
          id={`${baseId}-path`}
          className={`${styles.input} ${styles.pathInput}`}
          value={draft.anyPath ? "" : draft.path}
          placeholder={draft.anyPath ? t("servers.ws.anyPathPlaceholder") : "/"}
          disabled={draft.anyPath}
          spellCheck={false}
          autoComplete="off"
          onChange={(e) => edit({ path: e.target.value })}
        />
        <label className={styles.check}>
          <input type="checkbox" checked={draft.anyPath} onChange={(e) => edit({ anyPath: e.target.checked })} />
          {t("servers.ws.anyPath")}
        </label>
      </div>
      <div className={styles.settingRow}>
        <label className={styles.settingLabel} htmlFor={`${baseId}-subprotocols`}>
          {t("servers.ws.subprotocols")}
        </label>
        <input
          id={`${baseId}-subprotocols`}
          className={`${styles.input} ${styles.pathInput}`}
          value={draft.subprotocols}
          placeholder={t("servers.ws.subprotocolsPlaceholder")}
          spellCheck={false}
          autoComplete="off"
          onChange={(e) => edit({ subprotocols: e.target.value })}
        />
      </div>
      <p className={styles.help}>{t("servers.ws.subprotocolsHelp")}</p>
      <div className={styles.settingRow}>
        <label className={styles.settingLabel} htmlFor={`${baseId}-size`}>
          {t("servers.ws.largest")}
        </label>
        <input
          id={`${baseId}-size`}
          className={`${styles.input} ${styles.sizeInput}`}
          value={draft.maxMessageSize}
          inputMode="numeric"
          placeholder="16777216"
          autoComplete="off"
          onChange={(e) => edit({ maxMessageSize: e.target.value })}
        />
        <span className={styles.help}>{t("servers.ws.largestHelp")}</span>
      </div>
      {generalProblem !== null && (
        <p className={styles.error} role="alert">
          {generalProblem.message}
        </p>
      )}

      <h3 className={styles.sectionTitle}>{t("servers.ws.autoReply")}</h3>
      <label className={styles.check}>
        <input type="checkbox" checked={draft.echo} onChange={(e) => edit({ echo: e.target.checked })} />
        {t("servers.ws.echo")}
      </label>
      {draft.rules.length > 0 && (
        <ol className={styles.rules} aria-label={t("servers.ws.rules")}>
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
          <IconPlus size={12} /> {t("servers.ws.addRule")}
        </button>
      </div>
      <p className={styles.help}>
        <Trans
          k="servers.ws.rulesHelp"
          values={{
            group: <code>$1</code>,
            groupBraces: <code>{"${1}"}</code>,
            named: <code>{"${name}"}</code>,
            dollar: <code>$$</code>,
          }}
        />
      </p>
      <p className={styles.help}>{t("servers.ws.applyHelp")}</p>
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
          aria-label={t("servers.ws.rule.enabled", { n })}
          checked={rule.enabled}
          onChange={(e) => onChange({ enabled: e.target.checked })}
        />
        <select
          className={styles.input}
          aria-label={t("servers.ws.rule.match", { n })}
          value={rule.match}
          onChange={(e) => onChange({ match: e.target.value as WsRuleMatch })}
        >
          {(Object.keys(MATCH_LABELS) as WsRuleMatch[]).map((m) => (
            <option key={m} value={m}>
              {t(MATCH_LABELS[m])}
            </option>
          ))}
        </select>
        <input
          className={`${styles.input} ${styles.rulePattern}`}
          aria-label={t("servers.ws.rule.pattern", { n })}
          aria-invalid={error !== null || undefined}
          aria-describedby={error !== null ? errorId : undefined}
          value={rule.pattern}
          placeholder={t(PATTERN_PLACEHOLDERS[rule.match])}
          spellCheck={false}
          autoComplete="off"
          onChange={(e) => onChange({ pattern: e.target.value })}
        />
        <button
          type="button"
          className={styles.iconButton}
          aria-label={t("servers.ws.rule.moveUp", { n })}
          title={t("servers.ws.rule.moveUpTitle")}
          disabled={index === 0}
          onClick={() => onMove(-1)}
        >
          <IconChevron direction="up" size={12} />
        </button>
        <button
          type="button"
          className={styles.iconButton}
          aria-label={t("servers.ws.rule.moveDown", { n })}
          title={t("servers.ws.rule.moveDownTitle")}
          disabled={index === count - 1}
          onClick={() => onMove(1)}
        >
          <IconChevron direction="down" size={12} />
        </button>
        <button
          type="button"
          className={`${styles.iconButton} ${styles.danger}`}
          aria-label={t("servers.ws.rule.remove", { n })}
          title={t("servers.ws.rule.removeTitle")}
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
          aria-label={t("servers.ws.rule.reply", { n })}
          value={rule.reply}
          rows={2}
          placeholder={rule.match === "regex" ? t("servers.ws.rule.replyRegex") : t("servers.ws.rule.replyPlaceholder")}
          spellCheck={false}
          onChange={(e) => onChange({ reply: e.target.value })}
        />
        <label className={styles.closeLabel}>
          <Trans
            k="servers.ws.rule.after"
            values={{
              input: (
                <input
                  className={`${styles.input} ${styles.codeInput}`}
                  aria-label={t("servers.ws.rule.delay", { n })}
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
      {error !== null && (
        <p id={errorId} className={styles.error} role="alert">
          {error}
        </p>
      )}
    </li>
  );
}
