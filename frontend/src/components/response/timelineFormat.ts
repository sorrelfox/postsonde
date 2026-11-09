// The response timeline (DBG-01) as text and JSON, for copying one entry
// (a hop or a trace event) or saving the whole timeline (DBG-07). Go has
// masked the timeline's secrets before it reached the UI; the copy and save
// commands mask again in Go.
//
// The labels are the Timeline tab's, in the language in use; a copied or
// saved timeline is in that language too, while the JSON keys, the Go names
// of events and phases and the wire text stay as they are (docs/frontend.md,
// Copying and saving logs).
import { formatNumber, t, type MessageKey } from "../../i18n";
import type { CertificateDTO, ConnInfoDTO, HopTimingDTO, SentRequestDTO, TimelineDTO, TimelineEventDTO, TimelineHopDTO } from "../../lib/http";
import { jsonLogDocument, textLogDocument } from "../../lib/logExport";
import { formatSize } from "../messagelog/format";

/** The labels of the trace events Go records (docs/http-client.md), by Go name. */
export const EVENT_KEYS: Readonly<Record<string, MessageKey>> = {
  get_conn: "response.event.getConn",
  dial_start: "response.event.dialStart",
  dns_start: "response.event.dnsStart",
  dns_done: "response.event.dnsDone",
  connect_start: "response.event.connectStart",
  connect_done: "response.event.connectDone",
  dial_done: "response.event.dialDone",
  tls_start: "response.event.tlsStart",
  tls_done: "response.event.tlsDone",
  client_cert: "response.event.clientCert",
  got_conn: "response.event.gotConn",
  wrote_headers: "response.event.wroteHeaders",
  wrote_request: "response.event.wroteRequest",
  got_1xx: "response.event.got1xx",
  first_byte: "response.event.firstByte",
  redirect: "response.event.redirect",
  auth_challenge: "response.event.authChallenge",
  auth_unanswered: "response.event.authUnanswered",
  body_done: "response.event.bodyDone",
  events_dropped: "response.event.eventsDropped",
  cookies_sent: "response.event.cookiesSent",
  cookies_stored: "response.event.cookiesStored",
  request_options: "response.event.requestOptions",
  proxy: "response.event.proxy",
  proxy_hop: "response.event.proxyHop",
  oauth2_token: "response.event.oauth2Token",
  oauth2_token_stored: "response.event.oauth2TokenStored",
  oauth2_token_dropped: "response.event.oauth2TokenDropped",
};

/** The row labels of a hop's phases (DBG-03), in wire order, by Go name. */
export const PHASE_KEYS: Readonly<Record<string, MessageKey>> = {
  dns: "response.phase.dns",
  connect: "response.phase.connect",
  proxy: "response.phase.proxy",
  tls: "response.phase.tls",
  request_write: "response.phase.requestWrite",
  wait: "response.phase.wait",
  download: "response.phase.download",
};

/** The label of a phase: its name in the language in use, or its Go name. */
export function phaseLabel(phase: string): string {
  const key = PHASE_KEYS[phase];
  return key === undefined ? phase : t(key);
}

const fixed = (digits: number): Intl.NumberFormatOptions => ({ minimumFractionDigits: digits, maximumFractionDigits: digits, useGrouping: false });

/** "1.234 ms", "12.3 ms", "1.24 s": an offset from the start of the send, in the language in use. */
export function formatOffsetMs(ms: number): string {
  if (ms >= 1000) return t("response.duration.s", { s: formatNumber(ms / 1000, fixed(2)) });
  return t("response.duration.ms", { ms: formatNumber(ms, fixed(ms >= 100 ? 0 : ms >= 10 ? 1 : 3)) });
}

/** Wire text for display: CRLF line ends as plain line breaks, the trailing blank line dropped. */
export function wireText(head: string): string {
  return head.replace(/\r\n/g, "\n").replace(/\n+$/, "");
}

/** The heading of hop `index` of `count`: "Exchange", "Token request 1 of 2", "Redirect 1 of 3", "Final request (3 of 3)". */
export function hopTitle(hop: TimelineHopDTO, index: number, count: number): string {
  if (count === 1) return t("response.hop.exchange");
  const n = { number: index + 1, count };
  if (index === count - 1) return t("response.hop.final", n);
  if (hop.kind === "token") return t("response.hop.token", n);
  return hop.kind === "auth" ? t("response.hop.auth", n) : t("response.hop.redirect", n);
}

/** The accessible name of a hop's copy button: "Copy exchange", "Copy redirect 1 of 3". */
export function hopCopyLabel(hop: TimelineHopDTO, index: number, count: number): string {
  if (count === 1) return t("response.hop.copy.exchange");
  const n = { number: index + 1, count };
  if (index === count - 1) return t("response.hop.copy.final", n);
  if (hop.kind === "token") return t("response.hop.copy.token", n);
  return hop.kind === "auth" ? t("response.hop.copy.auth", n) : t("response.hop.copy.redirect", n);
}

/** "at 1.000 ms, took 12.5 ms": when a hop started and how long it took. */
export function hopMeta(hop: TimelineHopDTO): string {
  const start = formatOffsetMs(hop.startMs);
  return hop.timing != null ? t("response.hop.atTook", { start, total: formatOffsetMs(hop.timing.totalMs) }) : t("response.hop.at", { start });
}

/** The label of a trace event: its readable name, or its Go name. */
export function eventLabel(e: TimelineEventDTO): string {
  const key = EVENT_KEYS[e.name];
  return key === undefined ? e.name : t(key);
}

/** "Subject: CN=api.test": one labelled line of a copied hop. */
function field(label: string, value: string): string {
  return t("response.text.field", { label, value });
}

/** "reused", "Reused, idle 1.2 s", "New". */
export function reuseText(conn: ConnInfoDTO): string {
  if (!conn.reused) return t("response.conn.new");
  return conn.wasIdle ? t("response.conn.reusedIdle", { idle: formatOffsetMs(conn.idleMs) }) : t("response.conn.reused");
}

/** The TLS version, cipher suite and whether the session was resumed. */
export function tlsText(tls: NonNullable<ConnInfoDTO["tls"]>): string {
  const session = tls.cipherSuite !== "" ? `${tls.version}, ${tls.cipherSuite}` : tls.version;
  return tls.resumed ? t("response.conn.tlsResumed", { tls: session }) : session;
}

/** The proxy's authentication methods (netx.Auth*), by Go name. */
const PROXY_AUTH_KEYS: Readonly<Record<string, MessageKey>> = {
  none: "response.conn.proxyAuthNone",
  "username/password": "response.conn.proxyAuthUserPass",
  basic: "response.conn.proxyAuthBasic",
};

type ProxyHop = NonNullable<ConnInfoDTO["proxy"]>;

/**
 * The hop through a proxy (SOCKS-12) as labelled rows: the proxy (and the
 * address connected to), its protocol version, authentication, DNS mode,
 * reply and timings.
 */
export function proxyRows(hop: ProxyHop): { label: string; value: string }[] {
  const address = hop.proxyAddr !== "" && hop.proxyAddr !== hop.proxy ? `${hop.proxy} (${hop.proxyAddr})` : hop.proxy;
  const proxy = `${hop.version} ${address}`;
  const auth = PROXY_AUTH_KEYS[hop.auth];
  const timings: string[] = [];
  if (hop.lookupMs > 0) timings.push(t("response.conn.proxyLookup", { time: formatOffsetMs(hop.lookupMs) }));
  if (hop.connectMs > 0) timings.push(t("response.conn.proxyConnect", { time: formatOffsetMs(hop.connectMs) }));
  if (hop.tlsMs > 0) timings.push(t("response.conn.proxyTls", { time: formatOffsetMs(hop.tlsMs) }));
  if (hop.handshakeMs > 0) timings.push(t("response.conn.proxyHandshake", { time: formatOffsetMs(hop.handshakeMs) }));
  const rows = [
    { label: t("response.conn.proxy"), value: hop.forward ? t("response.conn.proxyForward", { proxy }) : proxy },
    {
      label: t("response.conn.proxyAuth"),
      value: auth !== undefined ? t(auth) : hop.auth !== "" ? hop.auth : t("response.conn.proxyAuthNotChosen"),
    },
    { label: t("response.conn.proxyDns"), value: hop.remoteDns ? t("response.conn.proxyDnsRemote") : t("response.conn.proxyDnsLocal") },
  ];
  if (!hop.forward) {
    rows.push({ label: t("response.conn.proxyReply"), value: hop.reply == null ? t("response.conn.proxyNoReply") : proxyReplyText(hop.reply, hop.replyText) });
  }
  if (timings.length > 0) rows.push({ label: t("response.conn.proxyTimings"), value: timings.join(", ") });
  return rows;
}

/** "0 succeeded" (SOCKS), "200 Connection established" (HTTP, whose text starts with the code). */
function proxyReplyText(reply: number, text: string): string {
  const code = String(reply);
  if (text === "" || text === code) return code;
  return text.startsWith(`${code} `) ? text : `${code} ${text}`;
}

/** The notes under a request as sent: HTTP/2, chunked, hex or clipped body. */
export function sentRequestNotes(r: SentRequestDTO): string[] {
  const notes: string[] = [];
  if (r.proto === "HTTP/2.0") notes.push(t("response.sent.http2"));
  if (r.bodyChunked) notes.push(t("response.sent.chunked"));
  if (r.bodyHex) notes.push(t("response.sent.hex"));
  if (r.bodyClipped) notes.push(t("response.sent.clipped", { shown: formatSize(r.bodyShown), size: formatSize(r.bodySize) }));
  return notes;
}

function timingLines(timing: HopTimingDTO): string[] {
  const phases = timing.phases ?? [];
  const total = t("response.timeline.total");
  const width = Math.max(18, total.length, ...phases.map((p) => phaseLabel(p.phase).length));
  const lines = [t("response.text.timing")];
  for (const p of phases) {
    const label = phaseLabel(p.phase).padEnd(width);
    lines.push(
      p.observed
        ? `  ${label} ${t("response.text.phaseTimed", { duration: formatOffsetMs(p.durationMs), start: formatOffsetMs(p.startMs) })}`
        : `  ${label} ${p.skipped !== "" ? t("response.text.notTimedWhy", { reason: p.skipped }) : t("response.text.notTimed")}`,
    );
  }
  lines.push(`  ${total.padEnd(width)} ${formatOffsetMs(timing.totalMs)}`);
  return lines;
}

function connLines(conn: ConnInfoDTO): string[] {
  if (conn.protocol === "" && conn.remoteAddr === "" && conn.localAddr === "") return [];
  const none = t("response.conn.none");
  const lines = [
    t("response.conn.label"),
    `  ${field(t("response.conn.connection"), reuseText(conn))}`,
    `  ${field(t("response.conn.protocol"), conn.protocol || "—")}`,
    `  ${field(t("response.conn.remote"), conn.remoteAddr || "—")}`,
    `  ${field(t("response.conn.local"), conn.localAddr || "—")}`,
  ];
  if (conn.proxy != null) for (const r of proxyRows(conn.proxy)) lines.push(`  ${field(r.label, r.value)}`);
  else if ((conn.via ?? "") !== "") lines.push(`  ${field(t("response.conn.via"), conn.via)}`);
  const tls = conn.tls;
  if (tls) {
    lines.push(
      `  ${field("TLS", tlsText(tls))}`,
      `  ${field("ALPN", tls.alpn || none)}`,
      `  ${field(t("response.conn.sni"), tls.serverName || t("response.conn.sniNone"))}`,
    );
    (tls.certificates ?? []).forEach((c, i) => {
      const sans = [...(c.dnsNames ?? []), ...(c.ipAddresses ?? [])];
      lines.push(
        `  ${i === 0 ? t("response.cert.server") : t("response.cert.chain", { number: i + 1 })}`,
        `    ${field(t("response.cert.subject"), c.subject || "—")}`,
        `    ${field(t("response.cert.issuer"), c.issuer || "—")}`,
        `    ${field(t("response.cert.valid"), [t("response.cert.validRange", { from: c.notBefore, to: c.notAfter }), ...certNotes(c, Date.now()).map((n) => t("response.cert.note", { note: t(n) }))].join(" "))}`,
      );
      if (i === 0) lines.push(`    ${field(t("response.cert.names"), sans.length > 0 ? sans.join(", ") : none)}`);
    });
  }
  return lines;
}

/**
 * What is wrong with a certificate, for a note after its validity: expired
 * or not yet valid at the time of the request (Go's validity), and
 * self-signed. A timeline saved before Go recorded the validity has it
 * judged at `now` from the dates instead.
 */
export function certNotes(cert: CertificateDTO, now: number): MessageKey[] {
  const notes: MessageKey[] = [];
  let validity = cert.validity ?? "";
  if (validity === "") {
    const from = Date.parse(cert.notBefore);
    const to = Date.parse(cert.notAfter);
    if (!Number.isNaN(to) && now > to) validity = "expired";
    else if (!Number.isNaN(from) && now < from) validity = "not_yet_valid";
  }
  if (validity === "expired") notes.push("response.cert.expired");
  else if (validity === "not_yet_valid") notes.push("response.cert.notYetValid");
  if (cert.selfSigned) notes.push("response.cert.selfSigned");
  return notes;
}

function requestLines(r: SentRequestDTO): string[] {
  const lines = [t("response.hop.request"), wireText(r.head)];
  if (r.body !== "") lines.push("", r.body);
  for (const note of sentRequestNotes(r)) lines.push(t("response.text.note", { note }));
  return lines;
}

/** One hop in full: timing, connection and TLS, the request as sent and the response head. */
export function hopText(hop: TimelineHopDTO, index: number, count: number): string {
  const lines = [t("response.text.hopHeading", { title: hopTitle(hop, index, count), meta: hopMeta(hop) })];
  if (hop.timing != null && (hop.timing.phases ?? []).length > 0) lines.push(...timingLines(hop.timing));
  if (hop.conn != null) lines.push(...connLines(hop.conn));
  lines.push(...requestLines(hop.request), t("response.hop.response"), wireText(hop.responseHead));
  // A token response's body (AUTH-33), masked by Go.
  if ((hop.responseBody ?? "") !== "") lines.push("", hop.responseBody ?? "");
  return lines.join("\n");
}

/** One trace event: offset, label, Go name and detail. */
export function eventText(e: TimelineEventDTO): string {
  const label = eventLabel(e);
  const name = label === e.name ? "" : ` (${e.name})`;
  return `+${formatOffsetMs(e.atMs)}  ${label}${name}${e.detail !== "" ? `  ${e.detail}` : ""}`;
}

/** A hop as JSON: its position and title, then the DTO as Go sent it. */
export function hopJson(hop: TimelineHopDTO, index: number, count: number): Record<string, unknown> {
  return { entry: "hop", index, title: hopTitle(hop, index, count), ...hop };
}

/** A trace event as JSON, with its readable label. */
export function eventJson(e: TimelineEventDTO): Record<string, unknown> {
  return { entry: "event", ...e, label: eventLabel(e) };
}

/** The whole timeline in `format`: every hop, then the trace. */
export function timelineDocument(timeline: TimelineDTO, format: "text" | "json", savedAt?: Date): string {
  const hops = timeline.hops ?? [];
  const events = timeline.events ?? [];
  const info = { kind: "timeline", title: t("response.timeline.title"), savedAt };
  if (format === "json") {
    return jsonLogDocument(info, [...hops.map((h, i) => hopJson(h, i, hops.length)), ...events.map(eventJson)]);
  }
  const parts = hops.map((h, i) => hopText(h, i, hops.length));
  if (events.length > 0) parts.push([t("response.timeline.events"), ...events.map(eventText)].join("\n"));
  return textLogDocument(info, parts);
}
