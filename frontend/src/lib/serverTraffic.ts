// The traffic of the local servers as message logs (SRV-03): one
// MessageSource per server, fed by servers:traffic batches and by the
// recent traffic Go keeps. Framework-free; commands/servers.ts owns the
// app's instance. See docs/servers.md.
import { t } from "../i18n";
import { methodKindLabel } from "./grpc";
import { formatUnit } from "./units";
import { MessageSource, type LogMessageInput, type MessageDirection, type MessageMetaValue, type MessageSourceOptions } from "./messageSource";
import type { TrafficDTO } from "./servers";

const DIRECTIONS: Readonly<Record<string, MessageDirection>> = {
  in: "received",
  out: "sent",
  system: "system",
};

/** Log kinds for the Go event kinds; the message log colours connected, closed and error. */
const KINDS: Readonly<Record<string, string>> = {
  connection_open: "connected",
  connection_close: "closed",
};

function decodeBase64(b64: string): Uint8Array {
  const bin = atob(b64);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

/**
 * One traffic event as a log message: `in` is received, `out` sent,
 * `system` system. Requests carry the method as badge and responses the
 * status. The payload is the content, or the summary line when there is
 * none; the summary, peer, connection, the WebSocket client with its
 * subprotocol and close code, how a TCP connection closed, the SSE
 * subscriber with the event's type, ID and recipients and the
 * Last-Event-ID, the UDP socket and reply kind, the mock route with its
 * parameters and delay, the HTTP headers, and a gRPC mock call's method,
 * message type and status with its trailers, and a GraphQL mock's
 * operation go into `meta` (a gRPC call is
 * badged CALL and its end with the status name).
 */
export function trafficToMessage(dto: TrafficDTO): LogMessageInput {
  const time = Date.parse(dto.time);
  const hasContent = dto.binary ? dto.dataBase64 !== "" : dto.text !== "";
  const meta: Record<string, MessageMetaValue> = {};
  if (hasContent && dto.summary !== "") meta.summary = dto.summary;
  if (dto.peer !== "") meta.peer = dto.peer;
  if (dto.connId > 0) meta.connection = dto.connId;
  // The WebSocket server's client (SSRV-01).
  const ws = dto.ws;
  if (ws) {
    if (ws.clientId !== "") meta.client = ws.clientId;
    if (ws.subprotocol !== "") meta.subprotocol = ws.subprotocol;
    if (dto.kind === "connection_close" && ws.closeCode > 0) {
      meta["close code"] = ws.closeCode;
      if (ws.closeReason !== "") meta["close reason"] = ws.closeReason;
      meta["closed by"] = ws.initiatedBy === "" ? t("lib.serverTraffic.closedByNobody") : ws.initiatedBy;
    }
  }
  // The TCP server's connection totals (SSRV-05).
  const tcp = dto.tcp;
  if (tcp && dto.kind === "connection_close") {
    meta["closed by"] = tcp.initiatedBy === "" ? t("lib.serverTraffic.closedByNobody") : tcp.initiatedBy;
    meta["bytes received"] = tcp.bytesIn;
    meta["bytes sent"] = tcp.bytesOut;
  }
  // The SSE server's subscriber and event fields (SSRV-04).
  const sse = dto.sse;
  if (sse) {
    if (sse.subscriberId !== "") meta.subscriber = sse.subscriberId;
    if (dto.kind === "message") {
      meta.event = sse.event === "" ? "message" : sse.event;
      if (sse.id !== "") meta.id = sse.id;
      if (sse.retryMs > 0) meta.retry = formatUnit("ms", sse.retryMs);
      meta.recipients = sse.recipients;
      if (sse.source !== "") meta["sent by"] = sse.source;
    }
    if (sse.lastEventId !== "") meta["last event ID"] = sse.lastEventId;
    if (dto.kind === "connection_close" && sse.initiatedBy !== "") meta["closed by"] = sse.initiatedBy;
  }
  // The UDP server's socket and automatic replies (SSRV-06).
  const udp = dto.udp;
  if (udp) {
    if (udp.local !== "") meta.local = udp.local;
    if (udp.reply !== "") meta.reply = udp.reply;
  }
  if (dto.truncated) {
    meta.truncated = t("lib.serverTraffic.truncated", { shown: dto.binary ? decodedLength(dto.dataBase64) : utf8Length(dto.text), count: dto.size });
  }
  let badge: string | undefined;
  const http = dto.http;
  if (http) {
    badge = http.status > 0 ? String(http.status) : http.method;
    if (http.status > 0) meta.request = `${http.method} ${http.target}`;
    meta.protocol = http.proto;
    // The HTTP mock's route (MOCK-01) and delay (MOCK-04).
    if (http.route) {
      meta.route = `${http.route.method} ${http.route.pattern}`;
      for (const p of http.route.params ?? []) meta[`params.${p.name}`] = p.value;
      if (http.route.wildcard !== "" && !(http.route.params ?? []).some((p) => p.value === http.route?.wildcard)) {
        meta.wildcard = http.route.wildcard;
      }
      // Which of the route's responses answered (MOCK-05, MOCK-07).
      if (http.route.response) meta.response = http.route.response;
    }
    if (http.delayMs > 0) meta.delay = formatUnit("ms", http.delayMs);
    for (const h of http.headers ?? []) {
      const prev = meta[h.name];
      meta[h.name] = prev === undefined ? h.value : `${String(prev)}, ${h.value}`;
    }
  }
  // The gRPC mock server's call, message type and status (SSRV-07): a call
  // is badged CALL and its end with the status name, not HTTP's POST and 200.
  const grpc = dto.grpc;
  if (grpc) {
    meta.call = grpc.call;
    meta.method = grpc.method;
    if (grpc.kind !== "") meta["call type"] = methodKindLabel(grpc.kind);
    if (grpc.web) meta.transport = "gRPC-Web";
    if (grpc.type !== "") meta.type = grpc.type;
    if (dto.kind === "request") badge = "CALL";
    if (dto.kind === "response") {
      badge = grpc.codeName;
      meta.status = `${grpc.code} ${grpc.codeName}`;
      if (grpc.message !== "") meta["status message"] = grpc.message;
      for (const trailer of grpc.trailers ?? []) {
        const key = `trailer ${trailer.name}`;
        const prev = meta[key];
        meta[key] = prev === undefined ? trailer.value : `${String(prev)}, ${trailer.value}`;
      }
    }
  }

  // The GraphQL mock server's operation (SSRV-08): a request is badged with
  // its operation type, its response keeps the HTTP status.
  const gql = dto.graphql;
  if (gql) {
    meta["graphql request"] = gql.request;
    if (gql.operationType !== "") meta["operation type"] = gql.operationType;
    if (gql.operation !== "") meta.operation = gql.operation;
    if (gql.variables !== "") meta.variables = gql.variables;
    if (gql.introspection) meta.introspection = true;
    if (dto.kind === "request" && gql.operationType !== "") badge = gql.operationType.toUpperCase();
    if (dto.kind === "response") {
      meta.data = gql.hasData;
      meta.errors = gql.errors;
    }
  }

  // The Socket.IO server's client, socket and packet (SSRV-03): an event is
  // badged with its name, other packets with their type.
  const sio = dto.sio;
  if (sio) {
    if (sio.clientId !== "") meta.client = sio.clientId;
    if (sio.socketId !== "") meta.socket = sio.socketId;
    if (sio.namespace !== "") meta.namespace = sio.namespace;
    if (sio.transport !== "") meta.transport = sio.transport;
    if (sio.packet !== "") meta.packet = sio.packet;
    if (sio.hasAck) meta["ack ID"] = sio.ackId;
    if (sio.attachments > 0) meta.attachments = sio.attachments;
    if (sio.rttMs > 0) meta["round trip"] = formatUnit("ms", sio.rttMs);
    if (sio.source !== "") meta["sent by"] = sio.source;
    if (sio.reason !== "") meta.reason = sio.reason;
    if (sio.initiatedBy !== "") meta["closed by"] = sio.initiatedBy;
    if (dto.kind === "message" && sio.packet !== "") {
      const isEvent = sio.packet === "EVENT" || sio.packet === "BINARY_EVENT";
      badge = isEvent && sio.event !== "" ? sio.event : sio.packet === "BINARY_ACK" ? "ACK" : sio.packet;
    }
  }

  const message: LogMessageInput = {
    direction: DIRECTIONS[dto.direction] ?? "system",
    kind: KINDS[dto.kind] ?? (dto.kind === "message" ? (dto.binary ? "binary" : "text") : dto.kind),
    size: hasContent || dto.size > 0 ? dto.size : 0,
  };
  if (Number.isFinite(time)) message.time = time;
  if (badge !== undefined) message.badge = badge;
  if (dto.binary && hasContent) message.data = decodeBase64(dto.dataBase64);
  else message.text = hasContent ? dto.text : dto.summary;
  if (Object.keys(meta).length > 0) message.meta = meta;
  return message;
}

/**
 * A message with its text payload as bytes, for a hex view of a byte
 * stream (the TCP server's traffic). Messages without a text payload of
 * their own (connected, closed, errors) stay as they are.
 */
export function toHexMessage(message: LogMessageInput): LogMessageInput {
  if (message.direction === "system" || message.kind !== "text" || message.text === undefined || message.data !== undefined) {
    return message;
  }
  const { text, ...rest } = message;
  return { ...rest, kind: "binary", data: new TextEncoder().encode(text) };
}

function utf8Length(s: string): number {
  return new TextEncoder().encode(s).length;
}

function decodedLength(b64: string): number {
  const pad = b64.endsWith("==") ? 2 : b64.endsWith("=") ? 1 : 0;
  return (b64.length / 4) * 3 - pad;
}

/**
 * The message sources of the servers, created on first use. Events are
 * applied in seq order per server and each seq once, so the recent traffic
 * loaded from Go and live batches can overlap. A server whose log is also
 * shown as hex (a TCP server) gets a second source once `hex` is called:
 * filled from the first, then kept in step with it.
 */
export class TrafficSources {
  private readonly sources = new Map<string, MessageSource>();
  private readonly hexSources = new Map<string, MessageSource>();
  private readonly lastSeq = new Map<string, number>();

  constructor(private readonly options: MessageSourceOptions = {}) {}

  /** The source of a server, created empty if needed. */
  get(serverId: string): MessageSource {
    let source = this.sources.get(serverId);
    if (!source) {
      source = new MessageSource(this.options);
      this.sources.set(serverId, source);
    }
    return source;
  }

  /** The log of a server with text payloads as bytes (toHexMessage), created from the log on first use. */
  hex(serverId: string): MessageSource {
    let source = this.hexSources.get(serverId);
    if (!source) {
      source = new MessageSource(this.options);
      const base = this.get(serverId);
      base.flush();
      const { firstSeq, nextSeq } = base.getSnapshot();
      const list: LogMessageInput[] = [];
      for (let seq = firstSeq; seq < nextSeq; seq++) {
        const m = base.get(seq);
        if (!m) continue;
        const input: LogMessageInput = { time: m.time, direction: m.direction, kind: m.kind };
        if (m.badge !== undefined) input.badge = m.badge;
        if (m.text !== undefined) input.text = m.text;
        if (m.data !== undefined) input.data = m.data;
        if (m.size !== undefined) input.size = m.size;
        if (m.meta !== undefined) input.meta = m.meta;
        list.push(toHexMessage(input));
      }
      source.append(list);
      this.hexSources.set(serverId, source);
    }
    return source;
  }

  /** The last seq applied for a server, 0 before any. */
  last(serverId: string): number {
    return this.lastSeq.get(serverId) ?? 0;
  }

  /** Appends events to their servers' sources, skipping seqs already applied. */
  append(events: readonly TrafficDTO[] | null | undefined): void {
    if (!events || events.length === 0) return;
    const byServer = new Map<string, LogMessageInput[]>();
    for (const ev of events) {
      const last = this.lastSeq.get(ev.serverId) ?? 0;
      if (ev.seq <= last) continue;
      this.lastSeq.set(ev.serverId, ev.seq);
      let list = byServer.get(ev.serverId);
      if (!list) {
        list = [];
        byServer.set(ev.serverId, list);
      }
      list.push(trafficToMessage(ev));
    }
    for (const [id, list] of byServer) {
      this.get(id).append(list);
      this.hexSources.get(id)?.append(list.map(toHexMessage));
    }
  }

  /** Empties a server's log; later events keep their seq order. */
  clear(serverId: string): void {
    this.sources.get(serverId)?.clear();
    this.hexSources.get(serverId)?.clear();
  }

  /** Forgets servers that no longer exist. */
  retain(serverIds: readonly string[]): void {
    const keep = new Set(serverIds);
    for (const id of [...this.sources.keys(), ...this.hexSources.keys(), ...this.lastSeq.keys()]) {
      if (!keep.has(id)) this.remove(id);
    }
  }

  remove(serverId: string): void {
    this.sources.get(serverId)?.dispose();
    this.sources.delete(serverId);
    this.hexSources.get(serverId)?.dispose();
    this.hexSources.delete(serverId);
    this.lastSeq.delete(serverId);
  }
}
