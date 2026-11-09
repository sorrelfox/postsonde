import { useCallback, useState } from "react";
import { closeTcpConnection, netcatCommand, sendToTcpConnection, tcpServerAddress } from "../../commands/tcpServer";
import type { MessageKey } from "../../i18n";
import { Trans, useT } from "../../i18n/react";
import { toAppError } from "../../lib/errors";
import {
  selectTcpComposer,
  selectTcpConnections,
  selectTcpTargetConnection,
  selectTcpSelectionGone,
  useServers,
  useStores,
  type LocalServer,
  type TcpComposerState,
  type TcpConnection,
} from "../../stores";
import { EmptyState } from "../common/EmptyState";
import { IconServers } from "../common/icons";
import { formatSize } from "../messagelog/format";
import { MessageComposer, type ComposerAction } from "../messagelog/MessageComposer";
import { Uptime } from "./Uptime";
import styles from "./WsServer.module.css";

const TCP_FORMATS: readonly TcpComposerState["format"][] = ["text", "hex"];

const LINE_ENDINGS: readonly { value: TcpComposerState["lineEnding"]; labelKey: MessageKey }[] = [
  { value: "none", labelKey: "servers.tcp.lineEnding.none" },
  { value: "lf", labelKey: "servers.tcp.lineEnding.lf" },
  { value: "crlf", labelKey: "servers.tcp.lineEnding.crlf" },
];

function errorText(err: unknown): string {
  const e = toAppError(err);
  return e.hint ? `${e.message} ${e.hint}` : e.message;
}

/**
 * The Connections tab of a TCP server (SSRV-05): the address clients
 * connect to, the open connections (select one to send to it, close one)
 * and the composer, which sends text with a line ending or hex bytes to
 * the selected connection. What the connections send shows up in the
 * traffic, as text or hex.
 */
export function TcpConnectionsPanel({ server }: { server: LocalServer }) {
  const t = useT();
  const stores = useStores();
  const connections = useServers((s) => selectTcpConnections(s, server.id));
  const target = useServers((s) => selectTcpTargetConnection(s, server.id));
  const selectionGone = useServers((s) => selectTcpSelectionGone(s, server.id));
  const composer = useServers((s) => selectTcpComposer(s, server.id));
  const [status, setStatus] = useState<string | null>(null);
  const running = server.state === "running";
  const address = tcpServerAddress(server);

  const setComposer = useCallback(
    (patch: Partial<TcpComposerState>) => {
      const current = selectTcpComposer(stores.servers.getState(), server.id);
      stores.servers.dispatch({ type: "servers/tcpComposer", id: server.id, composer: { ...current, ...patch } });
    },
    [stores, server.id],
  );
  const onFormatChange = useCallback((format: TcpComposerState["format"]) => setComposer({ format }), [setComposer]);
  const onMessageChange = useCallback((message: string) => setComposer({ message }), [setComposer]);

  const actions: ComposerAction[] = [
    {
      label: target !== null ? t("servers.tcp.sendOne", { id: target.id }) : t("servers.tcp.sendOneNone"),
      disabled: target === null,
      title: target !== null ? undefined : selectionGone && connections.length > 0 ? t("servers.tcp.selectionGone") : t("servers.tcp.noConnection"),
      send: async (text) => {
        if (target === null) return;
        setStatus(null);
        const bytes = await sendToTcpConnection(server.id, target.id, composer.format, text, composer.lineEnding);
        setStatus(t("servers.tcp.sent", { count: bytes, id: target.id }));
      },
    },
  ];

  const lineEnding = (
    <select
      className={styles.input}
      aria-label={t("servers.tcp.lineEnding")}
      value={composer.lineEnding}
      disabled={composer.format === "hex"}
      title={composer.format === "hex" ? t("servers.tcp.hexExact") : undefined}
      onChange={(e) => setComposer({ lineEnding: e.target.value as TcpComposerState["lineEnding"] })}
    >
      {LINE_ENDINGS.map((le) => (
        <option key={le.value} value={le.value}>
          {t(le.labelKey)}
        </option>
      ))}
    </select>
  );

  return (
    <div className={styles.clients}>
      <div className={styles.connectBar}>
        {address !== null ? (
          <p className={styles.connectNote}>
            <Trans
              k="servers.tcp.connectTo"
              values={{
                address: <code className={styles.url}>{address}</code>,
                command: <code className={styles.url}>{netcatCommand(address, server.tls !== null)}</code>,
              }}
            />
          </p>
        ) : (
          <p className={styles.connectNote}>{t("servers.tcp.startToAccept")}</p>
        )}
        {status !== null && (
          <p className={styles.status} role="status">
            {status}
          </p>
        )}
      </div>
      <div className={styles.listArea}>
        {connections.length === 0 ? (
          <EmptyState placement="top" icon={<IconServers size={24} strokeWidth={1.5} />} title={t("servers.tcp.emptyTitle")}>
            {running ? t("servers.tcp.emptyRunning") : t("servers.tcp.emptyStopped")}
          </EmptyState>
        ) : (
          <ul className={styles.list} aria-label={t("servers.tab.connections")}>
            {connections.map((c) => (
              <ConnectionRow key={c.id} serverId={server.id} connection={c} selected={c.id === target?.id} />
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
        formats={TCP_FORMATS}
        extra={lineEnding}
      />
    </div>
  );
}

function ConnectionRow({ serverId, connection, selected }: { serverId: string; connection: TcpConnection; selected: boolean }) {
  const t = useT();
  const stores = useStores();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const close = () => {
    setBusy(true);
    setError(null);
    closeTcpConnection(serverId, connection.id).then(
      () => setBusy(false), // the row goes away with the next servers:connections event
      (err: unknown) => {
        setBusy(false);
        setError(errorText(err));
      },
    );
  };

  return (
    <li className={styles.row} data-selected={selected || undefined}>
      <div className={styles.rowLine}>
        <button
          type="button"
          className={styles.rowMain}
          aria-pressed={selected}
          aria-label={t("servers.tcp.connectionLabel", { id: connection.id, peer: connection.peer })}
          onClick={() => stores.servers.dispatch({ type: "servers/tcpSelectConnection", id: serverId, connectionId: connection.id })}
        >
          <span className={styles.clientName}>{t("servers.tcp.connection", { id: connection.id })}</span>
          <span className={styles.clientDetails}>{connection.peer}</span>
        </button>
        <span
          className={styles.counts}
          title={t("servers.tcp.counts", {
            received: t("servers.tcp.receivedIn", { count: connection.messagesIn, size: formatSize(connection.bytesIn) }),
            sent: t("servers.tcp.sentIn", { count: connection.messagesOut, size: formatSize(connection.bytesOut) }),
          })}
        >
          ↓{formatSize(connection.bytesIn)} ↑{formatSize(connection.bytesOut)}
        </span>
        {connection.connectedAt !== null && (
          <span className={styles.since}>
            <Uptime since={connection.connectedAt} />
          </span>
        )}
        <button type="button" className={`${styles.button} ${styles.danger}`} disabled={busy} onClick={close}>
          {busy ? t("servers.tcp.closing") : t("servers.tcp.close")}
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
