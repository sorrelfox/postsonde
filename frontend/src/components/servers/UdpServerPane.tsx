import { useCallback, useEffect, useId, useRef, useState } from "react";
import { netcatUdpCommand, sendUdpDatagram, udpServerAddress, updateUdpConfig } from "../../commands/udpServer";
import { formatNumber, t as translate, type MessageKey } from "../../i18n";
import { Trans, useT } from "../../i18n/react";
import { toAppError } from "../../lib/errors";
import { parseHex } from "../../lib/hexBytes";
import {
  selectUdpComposer,
  selectUdpPeers,
  selectUdpTargetPeer,
  selectUdpSelectionGone,
  useServers,
  useStores,
  type LocalServer,
  type TcpComposerState,
  type UdpPeer,
  type UdpServerConfig,
} from "../../stores";
import { EmptyState } from "../common/EmptyState";
import { IconServers } from "../common/icons";
import { formatSize } from "../messagelog/format";
import { MessageComposer, type ComposerAction } from "../messagelog/MessageComposer";
import { Uptime } from "./Uptime";
import styles from "./WsServer.module.css";

/** How long reply edits must pause before they are sent, in ms. */
export const UDP_SETTINGS_DELAY_MS = 400;

const UDP_FORMATS: readonly TcpComposerState["format"][] = ["text", "hex"];

const LINE_ENDINGS: readonly { value: TcpComposerState["lineEnding"]; labelKey: MessageKey }[] = [
  { value: "none", labelKey: "servers.tcp.lineEnding.none" },
  { value: "lf", labelKey: "servers.tcp.lineEnding.lf" },
  { value: "crlf", labelKey: "servers.tcp.lineEnding.crlf" },
];

/** The largest UDP datagram over IPv4, in bytes. */
const MAX_DATAGRAM = 65_507;

function errorText(err: unknown): string {
  const e = toAppError(err);
  return e.hint ? `${e.message} ${e.hint}` : e.message;
}

/**
 * The Peers tab of a UDP server (SSRV-06): the address datagrams go to,
 * the peers heard from recently (the most recent first; select one to
 * send to it) and the composer, which sends text with a line ending or
 * hex bytes as one datagram. What arrives shows up in the traffic, as
 * text or hex.
 */
export function UdpPeersPanel({ server }: { server: LocalServer }) {
  const t = useT();
  const stores = useStores();
  const peers = useServers((s) => selectUdpPeers(s, server.id));
  const target = useServers((s) => selectUdpTargetPeer(s, server.id));
  const selectionGone = useServers((s) => selectUdpSelectionGone(s, server.id));
  const composer = useServers((s) => selectUdpComposer(s, server.id));
  const [status, setStatus] = useState<string | null>(null);
  const running = server.state === "running";
  const address = udpServerAddress(server);

  const setComposer = useCallback(
    (patch: Partial<TcpComposerState>) => {
      const current = selectUdpComposer(stores.servers.getState(), server.id);
      stores.servers.dispatch({ type: "servers/udpComposer", id: server.id, composer: { ...current, ...patch } });
    },
    [stores, server.id],
  );
  const onFormatChange = useCallback((format: TcpComposerState["format"]) => setComposer({ format }), [setComposer]);
  const onMessageChange = useCallback((message: string) => setComposer({ message }), [setComposer]);

  const actions: ComposerAction[] = [
    {
      label: target !== null ? t("servers.udp.sendTo", { addr: target.addr }) : t("servers.udp.sendToNone"),
      disabled: target === null,
      title: target !== null ? undefined : selectionGone && peers.length > 0 ? t("servers.udp.selectionGone") : t("servers.udp.noPeer"),
      send: async (text) => {
        if (target === null) return;
        setStatus(null);
        const bytes = await sendUdpDatagram(server.id, target.addr, composer.format, text, composer.lineEnding);
        setStatus(t("servers.udp.sent", { count: bytes, addr: target.addr }));
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
              k="servers.udp.sendDatagrams"
              values={{
                address: <code className={styles.url}>{address}</code>,
                command: <code className={styles.url}>{netcatUdpCommand(address)}</code>,
              }}
            />
          </p>
        ) : (
          <p className={styles.connectNote}>{t("servers.udp.startToReceive")}</p>
        )}
        {status !== null && (
          <p className={styles.status} role="status">
            {status}
          </p>
        )}
      </div>
      <div className={styles.listArea}>
        {peers.length === 0 ? (
          <EmptyState placement="top" icon={<IconServers size={24} strokeWidth={1.5} />} title={t("servers.udp.emptyTitle")}>
            {running ? t("servers.udp.emptyRunning") : t("servers.udp.emptyStopped")}
          </EmptyState>
        ) : (
          <ul className={styles.list} aria-label={t("servers.tab.peers")}>
            {peers.map((p) => (
              <PeerRow key={p.addr} serverId={server.id} peer={p} selected={p.addr === target?.addr} />
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
        formats={UDP_FORMATS}
        extra={lineEnding}
      />
    </div>
  );
}

function PeerRow({ serverId, peer, selected }: { serverId: string; peer: UdpPeer; selected: boolean }) {
  const t = useT();
  const stores = useStores();
  return (
    <li className={styles.row} data-selected={selected || undefined}>
      <div className={styles.rowLine}>
        <button
          type="button"
          className={styles.rowMain}
          aria-pressed={selected}
          aria-label={t("servers.udp.peer", { addr: peer.addr })}
          onClick={() => stores.servers.dispatch({ type: "servers/udpSelect", id: serverId, peer: peer.addr })}
        >
          <span className={styles.clientName}>{peer.addr}</span>
          <span className={styles.clientDetails}>{t("servers.udp.toLocal", { local: peer.local })}</span>
        </button>
        <span
          className={styles.counts}
          title={t("servers.udp.counts", {
            received: peer.datagramsIn,
            inSize: formatSize(peer.bytesIn),
            sent: peer.datagramsOut,
            outSize: formatSize(peer.bytesOut),
          })}
        >
          ↓{peer.datagramsIn} ↑{peer.datagramsOut}
        </span>
        {peer.lastSeen !== null && (
          <span className={styles.since} title={t("servers.udp.sinceLast")}>
            <Uptime since={peer.lastSeen} />
          </span>
        )}
      </div>
    </li>
  );
}

interface UdpDraft {
  echo: boolean;
  fixedReply: boolean;
  reply: string;
  replyFormat: "text" | "hex";
}

/** The replies a draft stands for, or the problem to show instead. */
export function udpDraftToConfig(d: UdpDraft): UdpServerConfig | string {
  if (d.replyFormat === "hex") {
    const parsed = parseHex(d.reply);
    if (!parsed.ok) return translate("servers.udp.badHex", { message: parsed.message });
  }
  return { ...d };
}

/**
 * The Replies tab of a UDP server: echo every datagram back, and/or send
 * a fixed reply (text or hex bytes) to the sender of every datagram. Edits
 * reach Go once typing pauses and apply from the next datagram.
 */
export function UdpSettingsPanel({ server }: { server: LocalServer }) {
  const t = useT();
  const stores = useStores();
  const [draft, setDraft] = useState<UdpDraft>(() => ({ ...(server.udp ?? { echo: true, fixedReply: false, reply: "", replyFormat: "text" }) }));
  const [problem, setProblem] = useState<string | null>(null);
  const baseId = useId();

  const pending = useRef<UdpServerConfig | null>(null);
  const seq = useRef(0);
  const flush = useCallback(() => {
    const config = pending.current;
    if (config === null) return;
    pending.current = null;
    const mine = ++seq.current;
    updateUdpConfig(stores, server.id, config).then(
      () => {
        if (mine === seq.current) setProblem(null);
      },
      (err: unknown) => {
        if (mine === seq.current) setProblem(errorText(err));
      },
    );
  }, [stores, server.id]);

  const edit = (patch: Partial<UdpDraft>) => {
    const next = { ...draft, ...patch };
    setDraft(next);
    const config = udpDraftToConfig(next);
    if (typeof config === "string") {
      pending.current = null;
      setProblem(config);
      return;
    }
    pending.current = config;
  };

  useEffect(() => {
    if (pending.current === null) return;
    const timer = setTimeout(flush, UDP_SETTINGS_DELAY_MS);
    return () => clearTimeout(timer);
  }, [draft, flush]);
  useEffect(() => flush, [flush]);

  return (
    <div className={styles.settings}>
      <label className={styles.check}>
        <input type="checkbox" checked={draft.echo} onChange={(e) => edit({ echo: e.target.checked })} />
        {t("servers.udp.echo")}
      </label>
      <label className={styles.check}>
        <input type="checkbox" checked={draft.fixedReply} onChange={(e) => edit({ fixedReply: e.target.checked })} />
        {t("servers.udp.fixedReply")}
      </label>
      <div className={styles.settingRow}>
        <label className={styles.settingLabel} htmlFor={`${baseId}-format`}>
          {t("servers.udp.replyAs")}
        </label>
        <select
          id={`${baseId}-format`}
          className={styles.input}
          value={draft.replyFormat}
          onChange={(e) => edit({ replyFormat: e.target.value === "hex" ? "hex" : "text" })}
        >
          <option value="text">{t("servers.udp.replyText")}</option>
          <option value="hex">{t("servers.udp.replyHex")}</option>
        </select>
      </div>
      <textarea
        className={`${styles.input} ${styles.ruleReply}`}
        aria-label={t("servers.udp.fixedReplyLabel")}
        aria-invalid={problem !== null || undefined}
        value={draft.reply}
        rows={3}
        placeholder={draft.replyFormat === "hex" ? t("servers.udp.hexPlaceholder") : t("servers.udp.textPlaceholder")}
        spellCheck={false}
        onChange={(e) => edit({ reply: e.target.value })}
      />
      {problem !== null && (
        <p className={styles.error} role="alert">
          {problem}
        </p>
      )}
      <p className={styles.help}>{t("servers.udp.help", { max: formatNumber(MAX_DATAGRAM) })}</p>
    </div>
  );
}
