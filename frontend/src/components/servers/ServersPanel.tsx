import { useState, type FormEvent } from "react";
import { createAndStartServer, startServer, stopServer } from "../../commands/servers";
import { Trans, useT } from "../../i18n/react";
import { formatError, toAppError } from "../../lib/errors";
import { ECHO_HTTP_TYPE, GRAPHQL_MOCK_TYPE, GRPC_MOCK_TYPE, MOCK_HTTP_TYPE, SOCKETIO_TYPE, SSE_TYPE, TCP_TYPE, UDP_TYPE, WEBHOOK_TYPE, WEBSOCKET_TYPE } from "../../lib/servers";
import { isServerActive, shallowEqual, useCollection, useServers, useStores, type LocalServer } from "../../stores";
import { EmptyState } from "../common/EmptyState";
import { IconClose, IconFolder, IconPlay, IconPlus, IconServers, IconStop, IconWarning } from "../common/icons";
import { CopyAddress } from "./CopyAddress";
import { NEW_SERVER_TYPES, newServerNote, serverTypeLabel, STATE_LABELS } from "./format";
import styles from "./Servers.module.css";
import { TlsBadge } from "./TlsBadge";
import { TlsFields, tlsDraftOf, tlsInputOf } from "./TlsSettings";
import { Uptime } from "./Uptime";

export interface ServersPanelProps {
  /** The sidebar heading. */
  label: string;
}

function parsePort(text: string): number | null {
  const t = text.trim();
  if (t === "") return 0;
  if (!/^\d+$/.test(t)) return null;
  const n = Number(t);
  return n <= 65535 ? n : null;
}

/** The inline "New server" form: the kind of server (mock, echo, WebSocket, TCP, webhook receiver, SSE, UDP, gRPC mock, Socket.IO or GraphQL mock), host, port, the LAN opt-in and TLS (SRV-05; not for UDP). */
function NewServerForm({ initialType, onDone }: { initialType: string; onDone: () => void }) {
  const t = useT();
  const stores = useStores();
  const [type, setType] = useState(initialType);
  const [host, setHost] = useState("localhost");
  const [port, setPort] = useState("");
  const [allowLan, setAllowLan] = useState(false);
  const [tls, setTls] = useState(() => tlsDraftOf(null));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (busy) return;
    const p = parsePort(port);
    if (p === null) {
      setError(t("servers.form.badPort"));
      return;
    }
    setBusy(true);
    setError(null);
    createAndStartServer(stores, { type, host: host.trim(), port: p, allowLan, tls: type === UDP_TYPE ? null : tlsInputOf(tls) }).then(
      // A server that failed to start is in the list, selected, with its error.
      () => onDone(),
      (err: unknown) => {
        setBusy(false);
        const appErr = toAppError(err);
        setError(appErr.hint ? `${appErr.message} ${appErr.hint}` : appErr.message);
      },
    );
  };

  const note = newServerNote(type);
  return (
    <form className={styles.form} onSubmit={submit} aria-label={t("servers.form.label")}>
      <p className={styles.formTitle}>{t("servers.form.label")}</p>
      <div className={styles.typeChoice} role="radiogroup" aria-label={t("servers.form.kind")}>
        {NEW_SERVER_TYPES.map((k) => (
          <label key={k.type} className={styles.typeOption}>
            <input
              type="radio"
              name="new-server-type"
              value={k.type}
              checked={type === k.type}
              disabled={busy}
              onChange={() => setType(k.type)}
            />
            {t(k.labelKey)}
          </label>
        ))}
      </div>
      <p className={styles.formNote}>{note !== undefined && t(note)}</p>
      <div className={styles.fields}>
        <label className={styles.field}>
          <span>{t("servers.form.host")}</span>
          <input
            className={styles.input}
            value={host}
            spellCheck={false}
            autoComplete="off"
            disabled={busy}
            onChange={(e) => {
              setHost(e.target.value);
              setError(null);
            }}
          />
        </label>
        <label className={`${styles.field} ${styles.portField}`}>
          <span>{t("servers.form.port")}</span>
          <input
            className={styles.input}
            value={port}
            inputMode="numeric"
            placeholder={t("servers.form.anyPort")}
            autoComplete="off"
            disabled={busy}
            aria-invalid={parsePort(port) === null || undefined}
            onChange={(e) => {
              setPort(e.target.value);
              setError(null);
            }}
          />
        </label>
      </div>
      <label className={styles.check}>
        <input type="checkbox" checked={allowLan} disabled={busy} onChange={(e) => setAllowLan(e.target.checked)} />
        <span>{t("servers.form.allowLan")}</span>
      </label>
      {allowLan && (
        <p className={styles.lanNote}>
          <IconWarning size={12} />
          <span>{t("servers.lanNote")}</span>
        </p>
      )}
      {type !== UDP_TYPE && (
        <TlsFields
          serverType={type}
          draft={tls}
          onChange={(d) => {
            setTls(d);
            setError(null);
          }}
          disabled={busy}
          hasPassphrase={false}
          idPrefix="new-server"
        />
      )}
      {error !== null && (
        <p className={styles.error} role="alert">
          {error}
        </p>
      )}
      <div className={styles.formButtons}>
        <button type="submit" className={`${styles.smallButton} ${styles.primary}`} disabled={busy}>
          {busy ? t("servers.form.starting") : t("servers.form.start")}
        </button>
        <button type="button" className={styles.smallButton} onClick={onDone} disabled={busy}>
          {t("common.cancel")}
        </button>
      </div>
    </form>
  );
}

/**
 * Where a saved server is defined (SRV-04): its folder's name in the open
 * project, or the name it had in a project that is not open.
 */
function FolderLabel({ server }: { server: LocalServer }) {
  const t = useT();
  const workspaceId = useCollection((s) => s.workspaceId);
  const folder = server.folder;
  const current = useCollection((s) => (folder !== null ? s.nodes[folder.folderId]?.name : undefined));
  if (folder === null) return null;
  const here = folder.workspaceId === workspaceId;
  const name = here ? (current ?? folder.folderName) : folder.folderName;
  return (
    <span
      className={styles.rowFolder}
      title={here ? t("servers.row.savedIn", { name }) : t("servers.row.savedInOther", { name })}
    >
      <IconFolder size={11} />
      {here ? name : t("servers.row.otherProject", { name })}
    </span>
  );
}

function ServerRow({ server, selected }: { server: LocalServer; selected: boolean }) {
  const t = useT();
  const stores = useStores();
  const [busy, setBusy] = useState(false);
  const active = isServerActive(server.state);
  const running = server.state === "running";

  const toggle = () => {
    setBusy(true);
    const call = active ? stopServer(stores, server.id) : startServer(stores, server.id);
    call
      .catch((err: unknown) => console.warn(`servers: ${formatError(err)}`))
      .finally(() => setBusy(false));
  };

  return (
    <li className={selected ? `${styles.row} ${styles.rowSelected}` : styles.row}>
      <button
        type="button"
        className={styles.rowMain}
        aria-current={selected || undefined}
        title={t("servers.row.show", { name: server.name, type: serverTypeLabel(server) })}
        onClick={() => stores.servers.dispatch({ type: "servers/select", id: server.id })}
      >
        <span className={styles.dot} data-state={server.state} aria-hidden="true" />
        <span className={styles.rowName}>{server.name}</span>
        <span className={styles.rowState}>{t(STATE_LABELS[server.state])}</span>
      </button>
      {server.folder !== null && <FolderLabel server={server} />}
      <div className={styles.rowMeta}>
        <CopyAddress server={server} />
        <TlsBadge server={server} />
        {server.warning !== null && running && (
          <span className={styles.warnIcon} title={server.warning.message} aria-label={server.warning.message} role="img">
            <IconWarning size={12} />
          </span>
        )}
        <span className={styles.spacer} />
        {running && server.startedAt !== null && (
          <span className={styles.uptime}>
            <Uptime since={server.startedAt} />
          </span>
        )}
        <button
          type="button"
          className={styles.iconButton}
          disabled={busy || server.state === "starting" || server.state === "stopping"}
          aria-label={active ? t("servers.row.stopName", { name: server.name }) : t("servers.row.startName", { name: server.name })}
          title={active ? t("servers.row.stop") : t("servers.row.start")}
          onClick={toggle}
        >
          {active ? <IconStop size={11} /> : <IconPlay size={11} />}
        </button>
      </div>
      {server.state === "failed" && server.error !== null && (
        <p className={styles.rowError} title={server.error.hint || undefined}>
          {server.error.message}
        </p>
      )}
    </li>
  );
}

/**
 * The Servers sidebar view (SRV-01): the local servers with their
 * state, address, uptime and a start/stop button, and the inline form for
 * a new Mock HTTP (MOCK-01), Echo HTTP, WebSocket (SSRV-01) or TCP (SSRV-05) server. Selecting a server shows its traffic in the work
 * area. The servers saved in the open project's folders (SRV-04) are
 * listed first, under "In this project", with their folder; the servers
 * made here (this session only) and those of another project still
 * running follow under "This session".
 */
export function ServersPanel({ label }: ServersPanelProps) {
  const t = useT();
  const workspaceId = useCollection((s) => s.workspaceId);
  const all = useServers((s) => s.ids.map((id) => s.byId[id]), shallowEqual);
  const inProject = all.filter((s) => s.folder !== null && s.folder.workspaceId === workspaceId);
  const session = all.filter((s) => !inProject.includes(s));
  const servers = all;
  const selectedId = useServers((s) => s.selectedId);
  const status = useServers((s) => s.status);
  const loadError = useServers((s) => s.error);
  // The form's kind of server, or null when it is closed.
  const [creating, setCreating] = useState<string | null>(null);

  return (
    <>
      <div className={styles.header}>
        <h2 className={styles.title}>{label}</h2>
        <div className={styles.headerActions}>
          <button
            type="button"
            className={styles.headerButton}
            aria-label={creating !== null ? t("servers.panel.cancelNew") : t("servers.form.label")}
            title={creating !== null ? t("common.cancel") : t("servers.panel.newTitle")}
            aria-expanded={creating !== null}
            onClick={() => setCreating((c) => (c === null ? MOCK_HTTP_TYPE : null))}
          >
            {creating !== null ? <IconClose size={14} /> : <IconPlus size={15} />}
          </button>
        </div>
      </div>
      <div className={styles.content}>
        {status === "error" && (
          <p className={styles.error} role="alert">
            {loadError ?? t("servers.panel.loadFailed")}
          </p>
        )}
        {creating !== null && <NewServerForm initialType={creating} onDone={() => setCreating(null)} />}
        {servers.length === 0 && creating === null && (
          <EmptyState placement="top" icon={<IconServers size={28} strokeWidth={1.5} />} title={t("servers.panel.empty.title")}>
            <Trans
              k="servers.panel.empty.body"
              values={{
                mock: (
                  <button type="button" className={styles.linkButton} onClick={() => setCreating(MOCK_HTTP_TYPE)}>{t("servers.panel.empty.mock")}</button>
                ),
                echo: (
                  <button type="button" className={styles.linkButton} onClick={() => setCreating(ECHO_HTTP_TYPE)}>{t("servers.panel.empty.echo")}</button>
                ),
                websocket: (
                  <button type="button" className={styles.linkButton} onClick={() => setCreating(WEBSOCKET_TYPE)}>{t("servers.panel.empty.websocket")}</button>
                ),
                tcp: (
                  <button type="button" className={styles.linkButton} onClick={() => setCreating(TCP_TYPE)}>{t("servers.panel.empty.tcp")}</button>
                ),
                webhook: (
                  <button type="button" className={styles.linkButton} onClick={() => setCreating(WEBHOOK_TYPE)}>{t("servers.panel.empty.webhook")}</button>
                ),
                sse: (
                  <button type="button" className={styles.linkButton} onClick={() => setCreating(SSE_TYPE)}>{t("servers.panel.empty.sse")}</button>
                ),
                udp: (
                  <button type="button" className={styles.linkButton} onClick={() => setCreating(UDP_TYPE)}>{t("servers.panel.empty.udp")}</button>
                ),
                grpc: (
                  <button type="button" className={styles.linkButton} onClick={() => setCreating(GRPC_MOCK_TYPE)}>{t("servers.panel.empty.grpc")}</button>
                ),
                sio: (
                  <button type="button" className={styles.linkButton} onClick={() => setCreating(SOCKETIO_TYPE)}>{t("servers.panel.empty.sio")}</button>
                ),
                graphql: (
                  <button type="button" className={styles.linkButton} onClick={() => setCreating(GRAPHQL_MOCK_TYPE)}>{t("servers.panel.empty.graphql")}</button>
                ),
              }}
            />
          </EmptyState>
        )}
        {inProject.length > 0 && (
          <>
            <p className={styles.groupTitle} id="servers-in-project">
              {t("servers.panel.inProject")}
            </p>
            <ul className={styles.list} aria-labelledby="servers-in-project">
              {inProject.map((s) => (
                <ServerRow key={s.id} server={s} selected={s.id === selectedId} />
              ))}
            </ul>
          </>
        )}
        {session.length > 0 && (
          <>
            {inProject.length > 0 && (
              <p className={styles.groupTitle} id="servers-in-session">
                {t("servers.panel.inSession")}
              </p>
            )}
            <ul className={styles.list} aria-label={inProject.length > 0 ? undefined : t("servers.panel.list")} aria-labelledby={inProject.length > 0 ? "servers-in-session" : undefined}>
              {session.map((s) => (
                <ServerRow key={s.id} server={s} selected={s.id === selectedId} />
              ))}
            </ul>
          </>
        )}
      </div>
    </>
  );
}
