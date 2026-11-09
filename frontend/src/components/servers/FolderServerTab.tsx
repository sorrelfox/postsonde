import { useState, type FormEvent, type KeyboardEvent } from "react";
import {
  createFolderServer,
  keepTls,
  removeServer,
  startServer,
  stopServer,
  updateServerSettings,
} from "../../commands/servers";
import { useT } from "../../i18n/react";
import { formatError, toAppError } from "../../lib/errors";
import { GRAPHQL_MOCK_TYPE, GRPC_MOCK_TYPE, MOCK_HTTP_TYPE, SOCKETIO_TYPE, SSE_TYPE, UDP_TYPE, WEBHOOK_TYPE, WEBSOCKET_TYPE } from "../../lib/servers";
import {
  isServerActive,
  selectFolderServer,
  selectFolderServerProblem,
  useCollection,
  useServers,
  useStores,
  type LocalServer,
} from "../../stores";
import { IconPlay, IconServers, IconStop, IconWarning } from "../common/icons";
import { CopyAddress } from "./CopyAddress";
import styles from "./FolderServer.module.css";
import { NEW_SERVER_TYPES, newServerNote, serverTypeLabel, STATE_LABELS } from "./format";
import { GraphQLMockPanel } from "./GraphQLMockPane";
import { GrpcMockMethodsPanel } from "./GrpcMockPane";
import { MockRoutesEditor } from "./MockRoutesEditor";
import serverStyles from "./Servers.module.css";
import { TlsBadge } from "./TlsBadge";
import { TlsSection } from "./TlsSettings";
import { SioSettingsPanel } from "./SioServerPane";
import { SseSettingsPanel } from "./SseServerPane";
import { UdpSettingsPanel } from "./UdpServerPane";
import { Uptime } from "./Uptime";
import { WebhookResponsePanel } from "./WebhookServerPane";
import { WsSettingsPanel } from "./WsServerPane";

/** Hosts that bind only this computer (SRV-02). */
const LOOPBACK_HOSTS: ReadonlySet<string> = new Set(["", "localhost", "127.0.0.1", "::1", "[::1]"]);

/** Whether `host` binds beyond this computer: anything but loopback. */
export function isBeyondLocalhost(host: string): boolean {
  return !LOOPBACK_HOSTS.has(host.trim().toLowerCase());
}

function parsePort(text: string): number | null {
  const t = text.trim();
  if (t === "") return 0;
  if (!/^\d+$/.test(t)) return null;
  const n = Number(t);
  return n <= 65535 ? n : null;
}

export interface FolderServerTabProps {
  folderId: string;
  /** The folder cannot be written (a newer version's file, a read-only project). */
  readOnly: boolean;
}

/**
 * The Server tab of the folder settings view (SRV-04, as in the mockup):
 * the one local server a folder can define, saved in its `_folder.json`.
 * Without one it offers the kinds of server; with one it shows its state
 * with Start/Stop, where it listens (name, bind address, port, LAN access,
 * with a warning when it binds beyond localhost), TLS (SRV-05: the
 * generated certificate or the user's files; not for UDP), the
 * configuration of its kind (mock routes, WebSocket settings, webhook
 * response, SSE settings and timers, UDP replies, gRPC mock files and
 * responses, Socket.IO settings and rules, GraphQL mock schema and
 * overrides) and Remove.
 * Every change is saved in the folder at once. Opening the project never
 * starts the server; its traffic shows in the response pane while this tab
 * is open (App.tsx).
 */
export function FolderServerTab({ folderId, readOnly }: FolderServerTabProps) {
  const t = useT();
  const workspaceId = useCollection((s) => s.workspaceId);
  const server = useServers((s) => selectFolderServer(s, workspaceId, folderId));
  const problem = useServers((s) => selectFolderServerProblem(s, folderId));

  if (problem !== null && server === null) {
    return (
      <div className={styles.tab}>
        <div className={serverStyles.failure} role="alert">
          <p>{t("servers.folder.loadFailed", { message: problem.message })}</p>
          <p className={serverStyles.hint}>{t("servers.folder.loadFailedHint")}</p>
        </div>
      </div>
    );
  }
  if (server === null) {
    return workspaceId === null ? null : <NewFolderServer workspaceId={workspaceId} folderId={folderId} readOnly={readOnly} />;
  }
  return <FolderServerView key={server.id} server={server} readOnly={readOnly} />;
}

/** No server yet: the kinds of server, and Add. */
function NewFolderServer({ workspaceId, folderId, readOnly }: { workspaceId: string; folderId: string; readOnly: boolean }) {
  const t = useT();
  const stores = useStores();
  const [type, setType] = useState(MOCK_HTTP_TYPE);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (busy || readOnly) return;
    setBusy(true);
    setError(null);
    createFolderServer(stores, { workspaceId, folderId, type }).then(
      () => setBusy(false),
      (err: unknown) => {
        setBusy(false);
        const appErr = toAppError(err);
        setError(appErr.hint ? `${appErr.message} ${appErr.hint}` : appErr.message);
      },
    );
  };

  const note = newServerNote(type);
  return (
    <form className={styles.tab} onSubmit={submit} aria-label={t("servers.folder.add")}>
      <div className={styles.empty}>
        <IconServers size={22} strokeWidth={1.5} />
        <p className={styles.emptyTitle}>{t("servers.folder.emptyTitle")}</p>
        <p className={serverStyles.formNote}>{t("servers.folder.emptyBody")}</p>
      </div>
      <div className={serverStyles.typeChoice} role="radiogroup" aria-label={t("servers.form.kind")}>
        {NEW_SERVER_TYPES.map((k) => (
          <label key={k.type} className={serverStyles.typeOption}>
            <input
              type="radio"
              name={`folder-server-type-${folderId}`}
              value={k.type}
              checked={type === k.type}
              disabled={busy || readOnly}
              onChange={() => setType(k.type)}
            />
            {t(k.labelKey)}
          </label>
        ))}
      </div>
      <p className={serverStyles.formNote}>{note !== undefined && t(note)}</p>
      {error !== null && (
        <p className={serverStyles.error} role="alert">
          {error}
        </p>
      )}
      <div className={serverStyles.formButtons}>
        <button type="submit" className={`${serverStyles.smallButton} ${serverStyles.primary}`} disabled={busy || readOnly}>
          {t("servers.folder.addButton")}
        </button>
      </div>
    </form>
  );
}

/** The folder's server: state and Start/Stop, where it listens, its configuration, Remove. */
function FolderServerView({ server, readOnly }: { server: LocalServer; readOnly: boolean }) {
  const t = useT();
  const stores = useStores();
  const [busy, setBusy] = useState(false);
  const [confirmRemove, setConfirmRemove] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const active = isServerActive(server.state);

  const act = (call: () => Promise<unknown>) => {
    setBusy(true);
    setError(null);
    call()
      .catch((err: unknown) => {
        const appErr = toAppError(err);
        // A failed start is shown from the server's own error.
        if (server.state !== "failed") setError(appErr.hint ? `${appErr.message} ${appErr.hint}` : appErr.message);
        console.warn(`servers: ${formatError(err)}`);
      })
      .finally(() => setBusy(false));
  };

  return (
    <div className={styles.tab} data-testid="folder-server">
      <div className={styles.status}>
        <span className={serverStyles.dot} data-state={server.state} aria-hidden="true" />
        <span className={styles.kind}>{serverTypeLabel(server)}</span>
        <span className={styles.state}>{t(STATE_LABELS[server.state])}</span>
        <CopyAddress server={server} />
        <TlsBadge server={server} />
        {server.state === "running" && server.startedAt !== null && (
          <span className={serverStyles.uptime}>
            <Uptime since={server.startedAt} />
          </span>
        )}
        <span className={serverStyles.spacer} />
        {active ? (
          <button
            type="button"
            className={serverStyles.smallButton}
            disabled={busy || server.state !== "running"}
            onClick={() => act(() => stopServer(stores, server.id))}
          >
            <IconStop size={10} /> {t("servers.info.stop")}
          </button>
        ) : (
          <button
            type="button"
            className={`${serverStyles.smallButton} ${serverStyles.primary}`}
            disabled={busy}
            onClick={() => act(() => startServer(stores, server.id))}
          >
            <IconPlay size={10} /> {t("servers.info.start")}
          </button>
        )}
      </div>

      {server.warning !== null && server.state === "running" && (
        <p className={serverStyles.warning} role="note">
          <IconWarning size={13} />
          <span>{server.warning.message}</span>
        </p>
      )}
      {server.state === "failed" && server.error !== null && (
        <div className={serverStyles.failure} role="alert">
          <p>{server.error.message}</p>
          {server.error.hint !== "" && <p className={serverStyles.hint}>{server.error.hint}</p>}
        </div>
      )}
      {error !== null && (
        <p className={serverStyles.error} role="alert">
          {error}
        </p>
      )}

      <ListenSettings server={server} readOnly={readOnly} />

      {server.type !== UDP_TYPE && (
        <section className={styles.config} aria-label={t("servers.folder.tlsSettings")}>
          <h3 className={styles.sectionTitle}>TLS</h3>
          <TlsSection server={server} readOnly={readOnly} />
        </section>
      )}

      {server.type === MOCK_HTTP_TYPE && (
        <section className={styles.config} aria-label={t("servers.tab.routes")}>
          <h3 className={styles.sectionTitle}>{t("servers.tab.routes")}</h3>
          <div className={styles.configBody}>
            <MockRoutesEditor server={server} />
          </div>
        </section>
      )}
      {server.type === WEBSOCKET_TYPE && (
        <section className={styles.config} aria-label={t("servers.folder.wsSettings")}>
          <h3 className={styles.sectionTitle}>{t("servers.type.websocket")}</h3>
          <WsSettingsPanel server={server} />
        </section>
      )}
      {server.type === WEBHOOK_TYPE && (
        <section className={styles.config} aria-label={t("servers.tab.response")}>
          <h3 className={styles.sectionTitle}>{t("servers.tab.response")}</h3>
          <WebhookResponsePanel server={server} />
        </section>
      )}
      {server.type === SSE_TYPE && (
        <section className={styles.config} aria-label={t("servers.folder.sseSettings")}>
          <h3 className={styles.sectionTitle}>{t("servers.folder.events")}</h3>
          <SseSettingsPanel server={server} />
        </section>
      )}
      {server.type === UDP_TYPE && (
        <section className={styles.config} aria-label={t("servers.tab.replies")}>
          <h3 className={styles.sectionTitle}>{t("servers.tab.replies")}</h3>
          <UdpSettingsPanel server={server} />
        </section>
      )}
      {server.type === SOCKETIO_TYPE && (
        <section className={styles.config} aria-label={t("servers.folder.sioSettings")}>
          <h3 className={styles.sectionTitle}>{t("servers.type.socketio")}</h3>
          <SioSettingsPanel server={server} />
        </section>
      )}
      {server.type === GRPC_MOCK_TYPE && (
        <section className={styles.config} aria-label={t("servers.folder.grpcMethods")}>
          <h3 className={styles.sectionTitle}>{t("servers.tab.methods")}</h3>
          <GrpcMockMethodsPanel server={server} readOnly={readOnly} />
        </section>
      )}
      {server.type === GRAPHQL_MOCK_TYPE && (
        <section className={styles.config} aria-label={t("servers.folder.graphqlSettings")}>
          <h3 className={styles.sectionTitle}>{t("servers.type.graphql")}</h3>
          <GraphQLMockPanel server={server} readOnly={readOnly} />
        </section>
      )}

      <div className={styles.footer}>
        {confirmRemove ? (
          <>
            <span className={styles.confirmText}>{t("servers.folder.confirmRemove")}</span>
            <button
              type="button"
              className={`${serverStyles.smallButton} ${serverStyles.danger}`}
              disabled={busy}
              onClick={() => act(() => removeServer(stores, server.id))}
            >
              {t("servers.info.remove")}
            </button>
            <button type="button" className={serverStyles.smallButton} onClick={() => setConfirmRemove(false)}>
              {t("common.cancel")}
            </button>
          </>
        ) : (
          <button
            type="button"
            className={`${serverStyles.smallButton} ${serverStyles.danger}`}
            disabled={busy || readOnly}
            onClick={() => setConfirmRemove(true)}
          >
            {t("servers.folder.removeServer")}
          </button>
        )}
      </div>
    </div>
  );
}

/**
 * Name, bind address, port and LAN access. Each change is saved when the
 * field is left (or on Enter); the address cannot change while the server
 * holds its port.
 */
function ListenSettings({ server, readOnly }: { server: LocalServer; readOnly: boolean }) {
  const t = useT();
  const stores = useStores();
  const [name, setName] = useState(server.name === server.typeLabel ? "" : server.name);
  const [host, setHost] = useState(server.host === "" ? "localhost" : server.host);
  const [port, setPort] = useState(server.port === 0 ? "" : String(server.port));
  const [error, setError] = useState<string | null>(null);
  const active = isServerActive(server.state);
  const addressLocked = readOnly || active;

  const save = (next: { name?: string; host?: string; port?: string; allowLan?: boolean }) => {
    const n = next.name ?? name;
    const h = (next.host ?? host).trim();
    const p = parsePort(next.port ?? port);
    if (p === null) {
      setError(t("servers.folder.badPort"));
      return;
    }
    const allowLan = next.allowLan ?? server.allowLan;
    const wantName = n.trim() === "" ? server.typeLabel : n.trim();
    if (wantName === server.name && h === (server.host === "" ? "localhost" : server.host) && p === server.port && allowLan === server.allowLan) {
      setError(null);
      return;
    }
    setError(null);
    updateServerSettings(stores, server.id, { name: n.trim(), host: h, port: p, allowLan, tls: keepTls(server.tls) }).catch((err: unknown) => {
      const appErr = toAppError(err);
      setError(appErr.hint ? `${appErr.message} ${appErr.hint}` : appErr.message);
    });
  };
  const onEnter = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Enter") e.currentTarget.blur();
  };

  const beyond = isBeyondLocalhost(host);
  return (
    <section className={styles.settings} aria-label={t("servers.folder.listen")}>
      <div className={serverStyles.fields}>
        <label className={serverStyles.field}>
          <span>{t("servers.folder.name")}</span>
          <input
            className={serverStyles.input}
            value={name}
            placeholder={server.typeLabel}
            autoComplete="off"
            disabled={readOnly}
            onChange={(e) => setName(e.target.value)}
            onBlur={() => save({})}
            onKeyDown={onEnter}
          />
        </label>
        <label className={serverStyles.field}>
          <span>{t("servers.folder.bindAddress")}</span>
          <input
            className={serverStyles.input}
            value={host}
            spellCheck={false}
            autoComplete="off"
            disabled={addressLocked}
            onChange={(e) => {
              setHost(e.target.value);
              setError(null);
            }}
            onBlur={() => save({})}
            onKeyDown={onEnter}
          />
        </label>
        <label className={`${serverStyles.field} ${serverStyles.portField}`}>
          <span>{t("servers.form.port")}</span>
          <input
            className={serverStyles.input}
            value={port}
            inputMode="numeric"
            placeholder={t("servers.form.anyPort")}
            autoComplete="off"
            disabled={addressLocked}
            aria-invalid={parsePort(port) === null || undefined}
            onChange={(e) => {
              setPort(e.target.value);
              setError(null);
            }}
            onBlur={() => save({})}
            onKeyDown={onEnter}
          />
        </label>
      </div>
      <label className={serverStyles.check}>
        <input
          type="checkbox"
          checked={server.allowLan}
          disabled={addressLocked}
          onChange={(e) => save({ allowLan: e.target.checked })}
        />
        <span>{t("servers.form.allowLan")}</span>
      </label>
      {(beyond || server.allowLan) && (
        <p className={serverStyles.lanNote} role="note">
          <IconWarning size={12} />
          <span>
            {beyond && !server.allowLan
              ? t("servers.folder.notLocalOnly", { host: host.trim() })
              : beyond
                ? t("servers.folder.binds", { host: host.trim(), note: t("servers.lanNote") })
                : t("servers.folder.lanButLocalhost")}
          </span>
        </p>
      )}
      {active && !readOnly && <p className={serverStyles.formNote}>{t("servers.folder.stopToChange")}</p>}
      {error !== null && (
        <p className={serverStyles.error} role="alert">
          {error}
        </p>
      )}
    </section>
  );
}
