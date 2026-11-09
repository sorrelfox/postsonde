import { useState, type KeyboardEvent } from "react";
import { clearServerTraffic, removeServer, startServer, stopServer, trafficSources } from "../../commands/servers";
import { formatDate, formatNumber, type MessageKey } from "../../i18n";
import { useT } from "../../i18n/react";
import { formatError } from "../../lib/errors";
import { GRAPHQL_MOCK_TYPE, GRPC_MOCK_TYPE, MOCK_HTTP_TYPE, SOCKETIO_TYPE, SSE_TYPE, TCP_TYPE, UDP_TYPE, WEBHOOK_TYPE, WEBSOCKET_TYPE } from "../../lib/servers";
import type { TrafficSources } from "../../lib/serverTraffic";
import {
  isServerActive,
  selectMockConfig,
  selectSioSockets,
  selectSseSubscribers,
  selectTcpConnections,
  selectTcpView,
  selectUdpPeers,
  selectWebhookCaptures,
  selectWsClients,
  useCollection,
  useServers,
  useStores,
  type LocalServer,
} from "../../stores";
import { IconClose, IconPlay, IconStop, IconWarning } from "../common/icons";
import { MessageLog } from "../messagelog/MessageLog";
import { CopyAddress } from "./CopyAddress";
import { GraphQLMockPanel } from "./GraphQLMockPane";
import { GrpcMockMethodsPanel } from "./GrpcMockPane";
import { serverTypeLabel, STATE_LABELS } from "./format";
import { MockRoutesEditor } from "./MockRoutesEditor";
import mockStyles from "./MockRoutes.module.css";
import styles from "./Servers.module.css";
import { TlsBadge } from "./TlsBadge";
import { describeTls, TlsSection } from "./TlsSettings";
import { Uptime } from "./Uptime";
import { SioSettingsPanel, SioSocketsPanel } from "./SioServerPane";
import { SseSettingsPanel, SseSubscribersPanel } from "./SseServerPane";
import { TcpConnectionsPanel } from "./TcpServerPane";
import { UdpPeersPanel, UdpSettingsPanel } from "./UdpServerPane";
import { WebhookRequestsPanel, WebhookResponsePanel } from "./WebhookServerPane";
import { WsClientsPanel, WsSettingsPanel } from "./WsServerPane";

const SCOPE_LABELS: Readonly<Record<string, MessageKey>> = {
  loopback: "servers.scope.loopback",
  lan: "servers.scope.lan",
  all_interfaces: "servers.scope.allInterfaces",
};

type PaneTab =
  | "routes"
  | "clients"
  | "connections"
  | "subscribers"
  | "peers"
  | "replies"
  | "settings"
  | "requests"
  | "response"
  | "methods"
  | "sockets"
  | "schema"
  | "responses"
  | "server";

interface PaneTabDef {
  readonly id: PaneTab;
  readonly label: string;
  readonly count?: number;
}

/**
 * The selected server, for the request pane: its details, and in tabs next
 * to them the Routes editor of a mock server (MOCK-01), the Clients and
 * Settings of a WebSocket server (SSRV-01), the Connections of a TCP
 * server (SSRV-05), the Requests and Response of a webhook receiver
 * (MOCK-08), the Subscribers and Settings of an SSE server (SSRV-04), the
 * Peers and Replies of a UDP server (SSRV-06), the Methods of a gRPC mock
 * server (SSRV-07), the Sockets and Settings of a Socket.IO server
 * (SSRV-03) or the Schema and Responses of a GraphQL mock server (SSRV-08). The traffic stays in the
 * response pane, so it shows up while the server is being worked with.
 */
export function ServerDetails({ server }: { server: LocalServer }) {
  const t = useT();
  const routeCount = useServers((s) => selectMockConfig(s, server.id)?.routes.length ?? 0);
  const clientCount = useServers((s) => selectWsClients(s, server.id).length);
  const connectionCount = useServers((s) => selectTcpConnections(s, server.id).length);
  const captureCount = useServers((s) => selectWebhookCaptures(s, server.id).length);
  const subscriberCount = useServers((s) => selectSseSubscribers(s, server.id).length);
  const peerCount = useServers((s) => selectUdpPeers(s, server.id).length);
  const socketCount = useServers((s) => selectSioSockets(s, server.id).length);
  const mockedCount = server.grpcMock?.methods.length ?? 0;
  const overrideCount = server.graphqlMock?.overrides.length ?? 0;
  const workspaceId = useCollection((s) => s.workspaceId);
  const isOpenProject = server.folder === null || server.folder.workspaceId === workspaceId;
  if (server.type === MOCK_HTTP_TYPE) {
    return (
      <ServerTabs
        server={server}
        label={t("servers.view.mock")}
        tabs={[
          { id: "routes", label: t("servers.tab.routes"), count: routeCount },
          { id: "server", label: t("servers.tab.server") },
        ]}
        render={(tab) => (tab === "routes" ? <MockRoutesEditor server={server} /> : <ServerInfo server={server} />)}
      />
    );
  }
  if (server.type === WEBSOCKET_TYPE) {
    return (
      <ServerTabs
        server={server}
        label={t("servers.view.websocket")}
        tabs={[
          { id: "clients", label: t("servers.tab.clients"), count: clientCount },
          { id: "settings", label: t("servers.tab.settings") },
          { id: "server", label: t("servers.tab.server") },
        ]}
        render={(tab) =>
          tab === "clients" ? (
            <WsClientsPanel server={server} />
          ) : tab === "settings" ? (
            <WsSettingsPanel key={server.id} server={server} />
          ) : (
            <ServerInfo server={server} />
          )
        }
      />
    );
  }
  if (server.type === TCP_TYPE) {
    return (
      <ServerTabs
        server={server}
        label={t("servers.view.tcp")}
        tabs={[
          { id: "connections", label: t("servers.tab.connections"), count: connectionCount },
          { id: "server", label: t("servers.tab.server") },
        ]}
        render={(tab) => (tab === "connections" ? <TcpConnectionsPanel server={server} /> : <ServerInfo server={server} />)}
      />
    );
  }
  if (server.type === WEBHOOK_TYPE) {
    return (
      <ServerTabs
        server={server}
        label={t("servers.view.webhook")}
        tabs={[
          { id: "requests", label: t("servers.tab.requests"), count: captureCount },
          { id: "response", label: t("servers.tab.response") },
          { id: "server", label: t("servers.tab.server") },
        ]}
        render={(tab) =>
          tab === "requests" ? (
            <WebhookRequestsPanel server={server} />
          ) : tab === "response" ? (
            <WebhookResponsePanel key={server.id} server={server} />
          ) : (
            <ServerInfo server={server} />
          )
        }
      />
    );
  }
  if (server.type === SSE_TYPE) {
    return (
      <ServerTabs
        server={server}
        label={t("servers.view.sse")}
        tabs={[
          { id: "subscribers", label: t("servers.tab.subscribers"), count: subscriberCount },
          { id: "settings", label: t("servers.tab.settings") },
          { id: "server", label: t("servers.tab.server") },
        ]}
        render={(tab) =>
          tab === "subscribers" ? (
            <SseSubscribersPanel server={server} />
          ) : tab === "settings" ? (
            <SseSettingsPanel key={server.id} server={server} />
          ) : (
            <ServerInfo server={server} />
          )
        }
      />
    );
  }
  if (server.type === UDP_TYPE) {
    return (
      <ServerTabs
        server={server}
        label={t("servers.view.udp")}
        tabs={[
          { id: "peers", label: t("servers.tab.peers"), count: peerCount },
          { id: "replies", label: t("servers.tab.replies") },
          { id: "server", label: t("servers.tab.server") },
        ]}
        render={(tab) =>
          tab === "peers" ? (
            <UdpPeersPanel server={server} />
          ) : tab === "replies" ? (
            <UdpSettingsPanel key={server.id} server={server} />
          ) : (
            <ServerInfo server={server} />
          )
        }
      />
    );
  }
  if (server.type === SOCKETIO_TYPE) {
    return (
      <ServerTabs
        server={server}
        label={t("servers.view.sio")}
        tabs={[
          { id: "sockets", label: t("servers.tab.sockets"), count: socketCount },
          { id: "settings", label: t("servers.tab.settings") },
          { id: "server", label: t("servers.tab.server") },
        ]}
        render={(tab) =>
          tab === "sockets" ? (
            <SioSocketsPanel server={server} />
          ) : tab === "settings" ? (
            <SioSettingsPanel key={server.id} server={server} />
          ) : (
            <ServerInfo server={server} />
          )
        }
      />
    );
  }
  if (server.type === GRPC_MOCK_TYPE) {
    return (
      <ServerTabs
        server={server}
        label={t("servers.view.grpc")}
        tabs={[
          { id: "methods", label: t("servers.tab.methods"), count: mockedCount },
          { id: "server", label: t("servers.tab.server") },
        ]}
        render={(tab) =>
          tab === "methods" ? <GrpcMockMethodsPanel key={server.id} server={server} readOnly={!isOpenProject} /> : <ServerInfo server={server} />
        }
      />
    );
  }
  if (server.type === GRAPHQL_MOCK_TYPE) {
    return (
      <ServerTabs
        server={server}
        label={t("servers.view.graphql")}
        tabs={[
          { id: "schema", label: t("servers.tab.schema") },
          { id: "responses", label: t("servers.tab.responses"), count: overrideCount },
          { id: "server", label: t("servers.tab.server") },
        ]}
        render={(tab) =>
          tab === "schema" || tab === "responses" ? (
            <GraphQLMockPanel key={`${server.id}-${tab}`} server={server} view={tab} readOnly={!isOpenProject} />
          ) : (
            <ServerInfo server={server} />
          )
        }
      />
    );
  }
  return <ServerInfo server={server} />;
}

/** The tab bar of a server with tabs, with its name and Close, and the selected tab. */
function ServerTabs({
  server,
  label,
  tabs,
  render,
}: {
  server: LocalServer;
  label: string;
  tabs: readonly PaneTabDef[];
  render: (tab: PaneTab) => React.ReactNode;
}) {
  const t = useT();
  const stores = useStores();
  const [chosen, setTab] = useState<PaneTab>(tabs[0].id);
  const tab = tabs.some((d) => d.id === chosen) ? chosen : tabs[0].id;
  const tabId = (id: PaneTab) => `server-pane-${server.id}-${id}`;
  const onKeyDown = (e: KeyboardEvent<HTMLButtonElement>) => {
    if (e.key !== "ArrowLeft" && e.key !== "ArrowRight") return;
    e.preventDefault();
    const at = tabs.findIndex((d) => d.id === tab);
    const next = tabs[(at + (e.key === "ArrowRight" ? 1 : tabs.length - 1)) % tabs.length].id;
    setTab(next);
    document.getElementById(tabId(next))?.focus();
  };
  return (
    <div className={mockStyles.pane}>
      <div className={mockStyles.tabs}>
        <div className={mockStyles.tabList} role="tablist" aria-label={label}>
          {tabs.map((d) => (
            <button
              key={d.id}
              id={tabId(d.id)}
              type="button"
              role="tab"
              className={mockStyles.tab}
              aria-selected={d.id === tab}
              tabIndex={d.id === tab ? 0 : -1}
              onClick={() => setTab(d.id)}
              onKeyDown={onKeyDown}
            >
              {d.label}
              {d.count !== undefined && d.count > 0 && <span className={mockStyles.count}>{d.count}</span>}
            </button>
          ))}
        </div>
        <span className={mockStyles.spacer} />
        <span className={styles.dot} data-state={server.state} title={t(STATE_LABELS[server.state])} aria-hidden="true" />
        <span className={mockStyles.paneName}>{server.name}</span>
        <button
          type="button"
          className={styles.iconButton}
          aria-label={t("servers.view.close")}
          title={t("servers.view.closeTitle")}
          onClick={() => stores.servers.dispatch({ type: "servers/select", id: null })}
        >
          <IconClose size={13} />
        </button>
      </div>
      <div className={mockStyles.tabPanel} role="tabpanel" aria-labelledby={tabId(tab)}>
        {render(tab)}
      </div>
    </div>
  );
}

/** A server's status, addresses and Start/Stop/Remove buttons. */
function ServerInfo({ server }: { server: LocalServer }) {
  const t = useT();
  const stores = useStores();
  // A saved server of a project that is not open cannot be changed: its
  // folder file is not there to write.
  const workspaceId = useCollection((s) => s.workspaceId);
  const isOpenProject = server.folder === null || server.folder.workspaceId === workspaceId;
  const [busy, setBusy] = useState(false);
  const active = isServerActive(server.state);

  const act = (call: () => Promise<unknown>) => {
    setBusy(true);
    call()
      .catch((err: unknown) => console.warn(`servers: ${formatError(err)}`))
      .finally(() => setBusy(false));
  };

  const scope = SCOPE_LABELS[server.scope];
  const rows: [string, React.ReactNode][] = [
    [t("servers.info.type"), serverTypeLabel(server)],
    [t("servers.info.status"), t(STATE_LABELS[server.state])],
    [t("servers.info.address"), <CopyAddress key="address" server={server} />],
  ];
  if (server.addresses.length > 1) rows.push([t("servers.info.listeningOn"), server.addresses.join(", ")]);
  if (server.scope !== "") rows.push([t("servers.info.reachableFrom"), scope === undefined ? server.scope : t(scope)]);
  rows.push(["TLS", server.type === UDP_TYPE ? t("servers.info.tlsNotForUdp") : describeTls(server.tls)]);
  const cert = server.certificate;
  if (cert !== null) {
    rows.push([t("servers.info.certificate"), cert.subject]);
    rows.push([t("servers.info.issuedBy"), cert.issuer]);
    if (cert.names.length > 0) rows.push([t("servers.info.validFor"), cert.names.join(", ")]);
    if (cert.notAfter !== null) rows.push([t("servers.info.validUntil"), formatDate(new Date(cert.notAfter), { dateStyle: "medium", timeZone: "UTC" })]);
    // i18n-ignore: an algorithm name
    rows.push(["SHA-256", <code key="sha" className={styles.fingerprint}>{cert.sha256}</code>]);
  }
  if (server.state === "running" && server.startedAt !== null) {
    rows.push([t("servers.info.uptime"), <Uptime key="uptime" since={server.startedAt} />]);
  }
  rows.push([t("servers.info.events"), formatNumber(server.events)]);

  return (
    <div className={styles.details} data-testid="server-details">
      <div className={styles.detailsHeader}>
        <span className={styles.dot} data-state={server.state} aria-hidden="true" />
        <h2 className={styles.detailsTitle}>{server.name}</h2>
        <TlsBadge server={server} />
        <button
          type="button"
          className={styles.iconButton}
          aria-label={t("servers.view.close")}
          title={t("servers.view.closeTitle")}
          onClick={() => stores.servers.dispatch({ type: "servers/select", id: null })}
        >
          <IconClose size={13} />
        </button>
      </div>

      <div className={styles.detailsButtons}>
        {active ? (
          <button
            type="button"
            className={styles.smallButton}
            disabled={busy || server.state !== "running"}
            onClick={() => act(() => stopServer(stores, server.id))}
          >
            <IconStop size={10} /> {t("servers.info.stop")}
          </button>
        ) : (
          <button
            type="button"
            className={`${styles.smallButton} ${styles.primary}`}
            disabled={busy}
            onClick={() => act(() => startServer(stores, server.id))}
          >
            <IconPlay size={10} /> {t("servers.info.start")}
          </button>
        )}
        <button
          type="button"
          className={`${styles.smallButton} ${styles.danger}`}
          disabled={busy}
          onClick={() => act(() => removeServer(stores, server.id))}
        >
          {t("servers.info.remove")}
        </button>
      </div>

      {server.warning !== null && server.state === "running" && (
        <p className={styles.warning} role="note">
          <IconWarning size={13} />
          <span>{server.warning.message}</span>
        </p>
      )}
      {server.state === "failed" && server.error !== null && (
        <div className={styles.failure} role="alert">
          <p>{server.error.message}</p>
          {server.error.hint !== "" && <p className={styles.hint}>{server.error.hint}</p>}
        </div>
      )}

      <dl className={styles.facts}>
        {rows.map(([k, v]) => (
          <div key={k} className={styles.fact}>
            <dt>{k}</dt>
            <dd>{v}</dd>
          </div>
        ))}
      </dl>
      {server.type !== UDP_TYPE && (
        <>
          <h3 className={styles.detailsSubtitle}>TLS</h3>
          <TlsSection key={server.id} server={server} readOnly={!isOpenProject} />
        </>
      )}
      <p className={styles.formNote}>
        {server.folder !== null
          ? t("servers.info.savedNote", { folder: server.folder.folderName })
          : t("servers.info.sessionNote")}
      </p>
    </div>
  );
}

/**
 * The selected server's traffic log, for the response pane. The bytes of a
 * TCP server (SSRV-05) or a UDP server (SSRV-06) can be shown as text or
 * as hex.
 */
export function ServerTraffic({ serverId, sources = trafficSources }: { serverId: string; sources?: TrafficSources }) {
  const t = useT();
  const stores = useStores();
  const name = useServers((s) => s.byId[serverId]?.name) ?? t("servers.traffic.unnamed");
  const byteStream = useServers((s) => s.byId[serverId]?.type === TCP_TYPE || s.byId[serverId]?.type === UDP_TYPE);
  const view = useServers((s) => selectTcpView(s, serverId));
  const hex = byteStream && view === "hex";
  return (
    <div className={styles.traffic}>
      {byteStream && (
        <div className={styles.trafficBar}>
          <span className={styles.trafficCaption} id={`server-traffic-view-${serverId}`}>
            {t("servers.traffic.showAs")}
          </span>
          <div className={styles.segmented} role="group" aria-labelledby={`server-traffic-view-${serverId}`}>
            {(["text", "hex"] as const).map((v) => (
              <button
                key={v}
                type="button"
                className={styles.segment}
                aria-pressed={view === v}
                onClick={() => stores.servers.dispatch({ type: "servers/tcpView", id: serverId, view: v })}
              >
                {v === "text" ? t("servers.traffic.text") : t("servers.traffic.hex")}
              </button>
            ))}
          </div>
        </div>
      )}
      <MessageLog
        key={`${serverId}-${hex ? "hex" : "text"}`}
        source={hex ? sources.hex(serverId) : sources.get(serverId)}
        label={t("servers.traffic.label", { name })}
        onClear={() => {
          clearServerTraffic(serverId, sources).catch((err: unknown) => console.warn(`servers: ${formatError(err)}`));
        }}
      />
    </div>
  );
}
