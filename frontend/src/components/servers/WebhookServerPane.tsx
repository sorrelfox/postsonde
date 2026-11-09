import { useCallback, useEffect, useId, useRef, useState } from "react";
import { useOperation } from "../../commands/useOperation";
import {
  defaultReplayUrl,
  loadWebhookCapture,
  startWebhookReplay,
  updateWebhookConfig,
  webhookUrl,
} from "../../commands/webhookServer";
import { formatDate, formatNumber, t as translate, type MessageKey } from "../../i18n";
import { Trans, useT } from "../../i18n/react";
import { toAppError } from "../../lib/errors";
import { HTTP_DOMAIN, type ResponseDTO } from "../../lib/http";
import type { CaptureDTO } from "../../lib/servers";
import {
  selectWebhookCapture,
  selectWebhookCaptures,
  selectWebhookReplayUrl,
  useServers,
  useStores,
  type LocalServer,
  type MockHeader,
  type WebhookCapture,
  type WebhookConfig,
} from "../../stores";
import { EmptyState } from "../common/EmptyState";
import { IconServers } from "../common/icons";
import { onTabListKeyDown } from "../common/tabList";
import { formatSize } from "../messagelog/format";
import { hasPrettyView, initialBodyView, ResponseBody, type BodyView } from "../response/ResponseBody";
import { ResponseHeaders } from "../response/ResponseHeaders";
import { ResponseView, Segmented } from "../response/ResponseView";
import responseStyles from "../response/ResponseView.module.css";
import styles from "./Webhook.module.css";
import wsStyles from "./WsServer.module.css";

/** How long response edits wait before they are sent to Go. */
export const WEBHOOK_CONFIG_DELAY_MS = 400;

function errorText(err: unknown): string {
  const e = toAppError(err);
  return e.hint ? `${e.message} ${e.hint}` : e.message;
}

function clockTime(ms: number | null): string {
  if (ms === null) return "";
  return formatDate(ms, { hour: "2-digit", minute: "2-digit", second: "2-digit", hourCycle: "h23" });
}

/**
 * The Requests tab of a webhook receiver (MOCK-08): the URL senders post
 * to, the requests it keeps (newest first) and the selected one in full
 * (body, headers, query), with Replay, which sends it unchanged to a URL
 * through the HTTP send pipeline and shows the response.
 */
export function WebhookRequestsPanel({ server }: { server: LocalServer }) {
  const t = useT();
  const stores = useStores();
  const captures = useServers((s) => selectWebhookCaptures(s, server.id));
  const selected = useServers((s) => selectWebhookCapture(s, server.id));
  const url = webhookUrl(server);
  const newestFirst = [...captures].reverse();

  return (
    <div className={wsStyles.clients}>
      <div className={wsStyles.connectBar}>
        {url !== null ? (
          <p className={wsStyles.connectNote}>
            <Trans k="servers.webhook.sendTo" values={{ url: <code className={wsStyles.url}>{url}</code> }} />
          </p>
        ) : (
          <p className={wsStyles.connectNote}>{t("servers.webhook.startToAccept")}</p>
        )}
      </div>
      {captures.length === 0 ? (
        <div className={wsStyles.listArea}>
          <EmptyState placement="top" icon={<IconServers size={24} strokeWidth={1.5} />} title={t("servers.webhook.emptyTitle")}>
            {t("servers.webhook.emptyBody")}
          </EmptyState>
        </div>
      ) : (
        <>
          <div className={styles.listArea}>
            <ul className={wsStyles.list} aria-label={t("servers.webhook.received")}>
              {newestFirst.map((c) => (
                <CaptureRow
                  key={c.seq}
                  capture={c}
                  selected={c.seq === selected?.seq}
                  onSelect={() => stores.servers.dispatch({ type: "servers/webhookSelect", id: server.id, seq: c.seq })}
                />
              ))}
            </ul>
          </div>
          {selected !== null && <CaptureInspector key={`${server.id}-${selected.seq}`} serverId={server.id} capture={selected} />}
        </>
      )}
    </div>
  );
}

function CaptureRow({ capture, selected, onSelect }: { capture: WebhookCapture; selected: boolean; onSelect: () => void }) {
  const t = useT();
  return (
    <li className={wsStyles.row} data-selected={selected || undefined}>
      <div className={wsStyles.rowLine}>
        <button
          type="button"
          className={wsStyles.rowMain}
          aria-pressed={selected}
          aria-label={t("servers.webhook.rowLabel", { method: capture.method, target: capture.target, seq: capture.seq })}
          onClick={onSelect}
        >
          <span className={styles.method} data-method={capture.method.toUpperCase()}>
            {capture.method}
          </span>
          <span className={wsStyles.clientDetails}>{capture.target}</span>
        </button>
        <span
          className={wsStyles.counts}
          title={capture.truncated ? t("servers.webhook.truncatedTitle") : t("servers.webhook.bytes", { count: capture.size, n: formatNumber(capture.size) })}
        >
          {capture.truncated ? t("servers.webhook.sizeCut", { size: formatSize(capture.size) }) : formatSize(capture.size)}
        </span>
        <span className={wsStyles.since}>{clockTime(capture.time)}</span>
      </div>
    </li>
  );
}

type InspectorTab = "body" | "headers" | "query" | "replay";

type Loaded = { status: "loading" } | { status: "error"; message: string } | { status: "ok"; capture: CaptureDTO };

/** One kept request in full, loaded from Go, with its Replay tab. */
function CaptureInspector({ serverId, capture }: { serverId: string; capture: WebhookCapture }) {
  const t = useT();
  const [loaded, setLoaded] = useState<Loaded>({ status: "loading" });
  const [tab, setTab] = useState<InspectorTab>("body");
  const [view, setView] = useState<BodyView | null>(null);
  const baseId = useId();

  useEffect(() => {
    let live = true;
    loadWebhookCapture(serverId, capture.seq).then(
      (c) => live && setLoaded({ status: "ok", capture: c }),
      (err: unknown) => live && setLoaded({ status: "error", message: errorText(err) }),
    );
    return () => {
      live = false;
    };
  }, [serverId, capture.seq]);

  const full = loaded.status === "ok" ? loaded.capture : null;
  const headers = full?.headers ?? [];
  const query = full?.query ?? [];
  const tabs: { id: InspectorTab; labelKey: MessageKey; count: number }[] = [
    { id: "body", labelKey: "servers.webhook.tab.body", count: 0 },
    { id: "headers", labelKey: "servers.webhook.tab.headers", count: headers.length },
    { id: "query", labelKey: "servers.webhook.tab.query", count: query.length },
    { id: "replay", labelKey: "servers.webhook.tab.replay", count: 0 },
  ];
  const bodyView = full === null ? "raw" : (view ?? initialBodyView(full.body));

  return (
    <section className={styles.inspector} aria-label={t("servers.webhook.request", { seq: capture.seq })}>
      <div className={styles.requestLine}>
        <span className={styles.method} data-method={capture.method.toUpperCase()}>
          {capture.method}
        </span>
        <code className={styles.target}>{capture.target}</code>
        {full !== null && <span className={styles.meta}>{full.proto}</span>}
        <span className={styles.meta} title={t("servers.webhook.sentFrom")}>
          {capture.peer}
        </span>
        <span className={styles.meta}>{formatSize(capture.size)}</span>
      </div>
      <div className={responseStyles.tabs}>
        <div className={responseStyles.tabList} role="tablist" aria-label={t("servers.webhook.sections")} onKeyDown={onTabListKeyDown}>
          {tabs.map((d) => (
            <button
              key={d.id}
              type="button"
              role="tab"
              id={`${baseId}-tab-${d.id}`}
              className={responseStyles.tab}
              aria-selected={d.id === tab}
              aria-controls={d.id === tab ? `${baseId}-panel` : undefined}
              tabIndex={d.id === tab ? 0 : -1}
              onClick={() => setTab(d.id)}
            >
              {t(d.labelKey)}
              {d.count > 0 && <span className={responseStyles.count}>{d.count}</span>}
            </button>
          ))}
        </div>
        {tab === "body" && full !== null && hasPrettyView(full.body) && (
          <div className={responseStyles.tools}>
            <Segmented<BodyView>
              label={t("servers.webhook.bodyView")}
              value={bodyView}
              options={[
                { value: "pretty", label: t("servers.webhook.pretty") },
                { value: "raw", label: t("servers.webhook.raw") },
              ]}
              onChange={setView}
            />
          </div>
        )}
      </div>
      <div
        className={tab === "body" ? `${responseStyles.section} ${responseStyles.bodySection}` : responseStyles.section}
        role="tabpanel"
        id={`${baseId}-panel`}
        aria-labelledby={`${baseId}-tab-${tab}`}
      >
        {tab === "replay" ? (
          <ReplayPanel serverId={serverId} capture={capture} />
        ) : loaded.status === "loading" ? (
          <p className={responseStyles.empty}>{t("servers.webhook.loading")}</p>
        ) : loaded.status === "error" ? (
          <p className={wsStyles.error} role="alert">
            {loaded.message}
          </p>
        ) : tab === "body" ? (
          <ResponseBody response={loaded.capture.body} view={bodyView} subject="request" />
        ) : tab === "headers" ? (
          <ResponseHeaders headers={headers} subject="request" />
        ) : (
          <ResponseHeaders headers={query} subject="request" label={t("servers.webhook.queryParams")} />
        )}
      </div>
    </section>
  );
}

/**
 * Replays the request to a URL: an HTTP send (an http operation, so the
 * dial is the user's), with the response shown in a ResponseView.
 */
function ReplayPanel({ serverId, capture }: { serverId: string; capture: WebhookCapture }) {
  const t = useT();
  const stores = useStores();
  const typed = useServers((s) => selectWebhookReplayUrl(s, serverId));
  const url = typed !== "" ? typed : defaultReplayUrl(capture.target);
  const op = useOperation<ResponseDTO, [string, number, string]>(HTTP_DOMAIN, startWebhookReplay, { keep: 1 });
  const inputId = useId();
  const response = op.status === "ok" ? (op.items[0] ?? null) : null;

  const replay = () => {
    if (op.running || capture.truncated) return;
    op.start(serverId, capture.seq, url);
  };

  return (
    <div className={styles.replay}>
      <form
        className={styles.replayBar}
        onSubmit={(e) => {
          e.preventDefault();
          replay();
        }}
      >
        <label className={styles.replayLabel} htmlFor={inputId}>
          {t("servers.webhook.replayTo")}
        </label>
        <input
          id={inputId}
          className={`${wsStyles.input} ${styles.urlInput}`}
          value={url}
          spellCheck={false}
          autoComplete="off"
          onChange={(e) => stores.servers.dispatch({ type: "servers/webhookReplayUrl", id: serverId, url: e.target.value })}
        />
        {op.running ? (
          <button type="button" className={wsStyles.button} onClick={op.cancel}>
            {t("common.cancel")}
          </button>
        ) : (
          <button
            type="submit"
            className={`${wsStyles.button} ${styles.primary}`}
            disabled={capture.truncated}
            title={capture.truncated ? t("servers.webhook.replayTruncatedTitle") : undefined}
          >
            {t("servers.webhook.replay")}
          </button>
        )}
      </form>
      <p className={wsStyles.help}>
        {capture.truncated ? t("servers.webhook.replayTruncated") : t("servers.webhook.replayHelp")}
      </p>
      {op.running && (
        <p className={wsStyles.status} role="status">
          {op.progress?.message ?? t("servers.webhook.starting")}
        </p>
      )}
      {op.status === "error" && op.error !== null && (
        <p className={wsStyles.error} role="alert">
          {op.error.hint ? `${op.error.message} ${op.error.hint}` : op.error.message}
        </p>
      )}
      {op.status === "cancelled" && (
        <p className={wsStyles.status} role="status">
          {t("servers.webhook.cancelled")}
        </p>
      )}
      {response !== null && (
        <div className={styles.replayResponse} aria-label={t("servers.webhook.replayResponse")}>
          <ResponseView response={response} />
        </div>
      )}
    </div>
  );
}

interface ResponseDraft {
  status: string;
  headers: MockHeader[];
  body: string;
}

function toDraft(config: WebhookConfig | null): ResponseDraft {
  return {
    status: String(config?.status ?? 200),
    headers: [...(config?.headers ?? [])],
    body: config?.body ?? "",
  };
}

/** The config of a draft, or why it cannot be sent. */
function draftToConfig(d: ResponseDraft): WebhookConfig | string {
  const status = Number(d.status.trim());
  if (!/^\d+$/.test(d.status.trim()) || status < 200 || status > 599) return translate("servers.webhook.badStatus", { min: 200, max: 599 });
  return { status, headers: d.headers, body: d.body };
}

/**
 * The Response tab of a webhook receiver: the status, headers and body it
 * answers every request with. Edits are sent after a short pause; a
 * running receiver uses them for the next request.
 */
export function WebhookResponsePanel({ server }: { server: LocalServer }) {
  const t = useT();
  const stores = useStores();
  const [draft, setDraft] = useState<ResponseDraft>(() => toDraft(server.webhook));
  const [error, setError] = useState<string | null>(null);
  const baseId = useId();

  const pending = useRef<WebhookConfig | null>(null);
  const seq = useRef(0);
  const flush = useCallback(() => {
    const config = pending.current;
    if (config === null) return;
    pending.current = null;
    const mine = ++seq.current;
    updateWebhookConfig(stores, server.id, config).then(
      () => {
        if (mine === seq.current) setError(null);
      },
      (err: unknown) => {
        if (mine === seq.current) setError(errorText(err));
      },
    );
  }, [stores, server.id]);

  const edit = (patch: Partial<ResponseDraft>) => {
    const next = { ...draft, ...patch };
    setDraft(next);
    const config = draftToConfig(next);
    if (typeof config === "string") {
      pending.current = null;
      setError(config);
      return;
    }
    setError(null);
    pending.current = config;
  };
  const editHeader = (i: number, patch: Partial<MockHeader>) =>
    edit({ headers: draft.headers.map((h, j) => (j === i ? { ...h, ...patch } : h)) });

  useEffect(() => {
    if (pending.current === null) return;
    const timer = setTimeout(flush, WEBHOOK_CONFIG_DELAY_MS);
    return () => clearTimeout(timer);
  }, [draft, flush]);
  // Edits still waiting are sent when the tab closes.
  useEffect(() => flush, [flush]);

  return (
    <div className={wsStyles.settings}>
      <div className={wsStyles.settingRow}>
        <label className={wsStyles.settingLabel} htmlFor={`${baseId}-status`}>
          {t("servers.webhook.status")}
        </label>
        <input
          id={`${baseId}-status`}
          className={`${wsStyles.input} ${wsStyles.sizeInput}`}
          value={draft.status}
          inputMode="numeric"
          autoComplete="off"
          aria-invalid={typeof draftToConfig(draft) === "string" || undefined}
          onChange={(e) => edit({ status: e.target.value })}
        />
      </div>
      <div className={styles.headerRows} role="group" aria-label={t("servers.webhook.responseHeaders")}>
        <span className={wsStyles.settingLabel}>{t("servers.webhook.headers")}</span>
        {draft.headers.map((h, i) => (
          <div key={i} className={styles.headerRow}>
            <input
              type="checkbox"
              aria-label={t("servers.webhook.sendHeader", { n: i + 1 })}
              checked={h.enabled}
              onChange={(e) => editHeader(i, { enabled: e.target.checked })}
            />
            <input
              className={`${wsStyles.input} ${styles.headerName}`}
              aria-label={t("servers.webhook.headerName", { n: i + 1 })}
              placeholder={t("servers.webhook.namePlaceholder")}
              value={h.name}
              spellCheck={false}
              autoComplete="off"
              onChange={(e) => editHeader(i, { name: e.target.value })}
            />
            <input
              className={`${wsStyles.input} ${styles.headerValue}`}
              aria-label={t("servers.webhook.headerValue", { n: i + 1 })}
              placeholder={t("servers.webhook.valuePlaceholder")}
              value={h.value}
              spellCheck={false}
              autoComplete="off"
              onChange={(e) => editHeader(i, { value: e.target.value })}
            />
            <button
              type="button"
              className={`${wsStyles.button} ${wsStyles.danger}`}
              aria-label={t("servers.webhook.removeHeader", { n: i + 1 })}
              onClick={() => edit({ headers: draft.headers.filter((_, j) => j !== i) })}
            >
              {t("servers.webhook.remove")}
            </button>
          </div>
        ))}
        <div>
          <button
            type="button"
            className={wsStyles.button}
            onClick={() => edit({ headers: [...draft.headers, { enabled: true, name: "", value: "" }] })}
          >
            {t("servers.webhook.addHeader")}
          </button>
        </div>
      </div>
      <label className={wsStyles.settingLabel} htmlFor={`${baseId}-body`}>
        {t("servers.webhook.body")}
      </label>
      <textarea
        id={`${baseId}-body`}
        className={`${wsStyles.input} ${styles.bodyInput}`}
        value={draft.body}
        spellCheck={false}
        placeholder={t("servers.webhook.bodyPlaceholder")}
        onChange={(e) => edit({ body: e.target.value })}
      />
      {error !== null && (
        <p className={wsStyles.error} role="alert">
          {error}
        </p>
      )}
      <p className={wsStyles.help}>{t("servers.webhook.responseHelp")}</p>
    </div>
  );
}
