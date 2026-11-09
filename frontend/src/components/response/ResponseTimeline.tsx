import { Fragment, useState, type KeyboardEvent } from "react";
import type { CertificateDTO, ConnInfoDTO, HopTimingDTO, SentRequestDTO, TimelineDTO, TimelineEventDTO, TimelineHopDTO } from "../../lib/http";
import type { MessageKey } from "../../i18n";
import { useT } from "../../i18n/react";
import { EntryMenuButton, SaveLogButton, useEntryMenu, type LogEntryExport } from "../common/LogActions";
import styles from "./ResponseView.module.css";
import {
  certNotes,
  eventJson,
  eventLabel,
  eventText,
  formatOffsetMs,
  hopCopyLabel,
  hopJson,
  hopMeta,
  hopText,
  hopTitle,
  phaseLabel,
  proxyRows,
  reuseText,
  sentRequestNotes,
  timelineDocument,
  tlsText,
  wireText,
} from "./timelineFormat";

export { formatOffsetMs };

/**
 * The response's Timeline tab (DBG-01): for each hop, OAuth 2.0 token
 * requests (AUTH-33, with the token response's body), redirects and answered
 * auth challenges first, a waterfall of its phases (DBG-03), the connection
 * it went over (DBG-04, DBG-05), the request exactly as it went over the
 * wire (request line, every header field, the body) and the head of the
 * response it got; then the trace of the send. Go masks secret values
 * before they get here. Timelines saved before the phases were recorded
 * have no timing or conn, and a stored response's (an earlier response or
 * an example, HTTP-38/39) has neither: those parts are left out.
 *
 * Each hop has a copy button and each trace event a menu (DBG-07); "Save
 * timeline…" saves it all as text or JSON. Copies and saves are masked in
 * Go again (commands/logExport).
 */
export function ResponseTimeline({ timeline, requestProxy = "" }: { timeline: TimelineDTO | undefined; requestProxy?: string }) {
  const t = useT();
  const hops = timeline?.hops ?? [];
  const events = timeline?.events ?? [];
  if (timeline === undefined || hops.length === 0) return <p className={styles.empty}>{t("response.timeline.empty")}</p>;
  return (
    <div className={styles.timeline}>
      <div className={styles.timelineBar}>
        {timeline.masked === true ? (
          <p className={styles.timelineNote} role="note">
            {t("response.timeline.masked")}
          </p>
        ) : (
          <span />
        )}
        <SaveLogButton
          title={t("response.timeline.title")}
          fileName="timeline"
          build={(format) => timelineDocument(timeline, format)}
          className={styles.timelineButton}
          label={t("response.timeline.saveLabel")}
        >
          {t("response.timeline.save")}
        </SaveLogButton>
      </div>
      {requestProxy !== "" && (
        <p className={styles.timelineNote} role="note" data-request-proxy={requestProxy}>
          {requestProxy === "none"
            ? t("response.timeline.requestNoProxy")
            : t("response.timeline.requestProxy", { proxy: requestProxy })}
        </p>
      )}
      {hops.map((hop, i) => (
        <Hop key={i} hop={hop} index={i} count={hops.length} />
      ))}
      {events.length > 0 && <Events events={events} />}
    </div>
  );
}

/** What a phase that did not happen shows, and why (PhaseDTO.skipped). */
const SKIP_LABELS: Record<string, { text: MessageKey | "—"; title: MessageKey }> = {
  reused: { text: "response.timeline.skip.reused", title: "response.timeline.skip.reusedTitle" },
  no_lookup: { text: "response.timeline.skip.noLookup", title: "response.timeline.skip.noLookupTitle" },
  no_tls: { text: "—", title: "response.timeline.skip.noTlsTitle" },
  not_final: { text: "—", title: "response.timeline.skip.notFinalTitle" },
};

/** Where a hop's time went (DBG-03): one row per phase, bars placed on the hop's span. */
export function HopTiming({ timing }: { timing: HopTimingDTO }) {
  const t = useT();
  const phases = timing.phases ?? [];
  const end = phases.reduce((m, p) => (p.observed ? Math.max(m, p.startMs + p.durationMs) : m), 0);
  const span = Math.max(timing.totalMs, end);
  const pct = (ms: number) => (span > 0 ? `${Math.min(100, (ms / span) * 100)}%` : "0%");
  return (
    <table className={styles.waterfall} aria-label={t("response.timeline.phases")}>
      <thead className={styles.srOnly}>
        <tr>
          <th scope="col">{t("response.timeline.column.phase")}</th>
          <th scope="col">{t("response.timeline.column.waterfall")}</th>
          <th scope="col">{t("response.timeline.column.duration")}</th>
        </tr>
      </thead>
      <tbody>
        {phases.map((p) => {
          const skip = p.observed ? undefined : (SKIP_LABELS[p.skipped] ?? { text: "—", title: "response.timeline.notRecorded" });
          return (
            <tr key={p.phase} data-phase={p.phase} data-observed={p.observed}>
              <th scope="row" className={styles.phaseName}>
                {phaseLabel(p.phase)}
              </th>
              <td className={styles.phaseTrack} aria-hidden="true">
                {p.observed && (
                  <span
                    className={styles.phaseBar}
                    data-phase={p.phase}
                    style={{ left: pct(p.startMs), width: pct(p.durationMs) }}
                  />
                )}
              </td>
              <td className={styles.phaseValue} title={skip && t(skip.title)}>
                {skip ? (skip.text === "—" ? skip.text : t(skip.text)) : formatOffsetMs(p.durationMs)}
              </td>
            </tr>
          );
        })}
        <tr className={styles.phaseTotal}>
          <th scope="row" className={styles.phaseName}>
            {t("response.timeline.total")}
          </th>
          <td className={styles.phaseTrack} aria-hidden="true" />
          <td className={styles.phaseValue}>{formatOffsetMs(timing.totalMs)}</td>
        </tr>
      </tbody>
    </table>
  );
}

/** "2026-01-01" from an RFC 3339 time, as it came when it does not parse. */
function day(rfc3339: string): string {
  return /^\d{4}-\d{2}-\d{2}/.test(rfc3339) ? rfc3339.slice(0, 10) : rfc3339;
}

function Certificate({ cert, index }: { cert: CertificateDTO; index: number }) {
  const t = useT();
  const sans = [...(cert.dnsNames ?? []), ...(cert.ipAddresses ?? [])];
  // A self-signed root is expected in a chain; only a self-signed server
  // certificate is flagged as a problem.
  const notes = certNotes(cert, Date.now());
  const title = index === 0 ? t("response.cert.server") : t("response.cert.chain", { number: index + 1 });
  return (
    <div className={styles.cert} role="group" aria-label={title}>
      <div className={styles.certTitle}>{title}</div>
      <dl className={styles.connInfo}>
        <dt>{t("response.cert.subject")}</dt>
        <dd>{cert.subject || "—"}</dd>
        <dt>{t("response.cert.issuer")}</dt>
        <dd>{cert.issuer || "—"}</dd>
        <dt>{t("response.cert.valid")}</dt>
        <dd>
          {t("response.cert.validRange", { from: day(cert.notBefore), to: day(cert.notAfter) })}
          {notes.map((n) => (
            <span key={n} className={n !== "response.cert.selfSigned" || index === 0 ? styles.certBad : undefined}>
              {" "}
              {t("response.cert.note", { note: t(n) })}
            </span>
          ))}
        </dd>
        {index === 0 && (
          <>
            <dt>{t("response.cert.names")}</dt>
            <dd>{sans.length > 0 ? sans.join(", ") : t("response.conn.none")}</dd>
          </>
        )}
      </dl>
    </div>
  );
}

/** The connection a hop went over (DBG-04) and its TLS session (DBG-05). */
export function ConnectionInfo({ conn }: { conn: ConnInfoDTO }) {
  const t = useT();
  if (conn.protocol === "" && conn.remoteAddr === "" && conn.localAddr === "") return null;
  const tls = conn.tls;
  const certs = tls?.certificates ?? [];
  return (
    <div className={styles.conn}>
      <dl className={styles.connInfo} aria-label={t("response.conn.label")}>
        <dt>{t("response.conn.connection")}</dt>
        <dd>{reuseText(conn)}</dd>
        <dt>{t("response.conn.protocol")}</dt>
        {/* i18n-ignore: a protocol name and its ALPN token */}
        <dd>{conn.protocol === "h2" ? "HTTP/2 (h2)" : conn.protocol || "—"}</dd>
        <dt>{t("response.conn.remote")}</dt>
        <dd>{conn.remoteAddr || "—"}</dd>
        <dt>{t("response.conn.local")}</dt>
        <dd>{conn.localAddr || "—"}</dd>
        {(conn.via ?? "") !== "" && conn.proxy == null && (
          <>
            <dt>{t("response.conn.via")}</dt>
            <dd data-proxy-via="">{conn.via}</dd>
          </>
        )}
        {conn.proxy != null &&
          proxyRows(conn.proxy).map((r) => (
            <Fragment key={r.label}>
              <dt>{r.label}</dt>
              <dd data-proxy-hop="">{r.value}</dd>
            </Fragment>
          ))}
        {tls && (
          <>
            <dt>TLS</dt>
            <dd>{tlsText(tls)}</dd>
            <dt>ALPN</dt>
            <dd>{tls.alpn || t("response.conn.none")}</dd>
            <dt>{t("response.conn.sni")}</dt>
            <dd>{tls.serverName || t("response.conn.sniNone")}</dd>
          </>
        )}
      </dl>
      {certs.length > 0 && (
        <details className={styles.certs} open={!conn.reused}>
          <summary>{t("response.conn.certificates", { count: certs.length })}</summary>
          {certs.map((c, i) => (
            <Certificate key={i} cert={c} index={i} />
          ))}
        </details>
      )}
    </div>
  );
}

function Hop({ hop, index, count }: { hop: TimelineHopDTO; index: number; count: number }) {
  const t = useT();
  const final = index === count - 1;
  const title = hopTitle(hop, index, count);
  const entry: LogEntryExport = { text: () => hopText(hop, index, count), json: () => hopJson(hop, index, count) };
  return (
    <section className={styles.hop} aria-label={title}>
      <h3 className={styles.hopTitle}>
        <span>{title}</span>
        <span className={styles.hopMeta}>{hopMeta(hop)}</span>
        <EntryMenuButton entry={entry} label={hopCopyLabel(hop, index, count)} className={styles.hopCopy} />
      </h3>
      {hop.timing != null && (hop.timing.phases ?? []).length > 0 && <HopTiming timing={hop.timing} />}
      {hop.conn != null && (hop.conn.protocol !== "" || hop.conn.remoteAddr !== "") && <ConnectionInfo conn={hop.conn} />}
      <SentRequest request={hop.request} />
      <div className={styles.wireLabel}>{t("response.hop.response")}</div>
      <pre className={styles.wire} data-wire="response">
        {wireText(hop.responseHead)}
        {(hop.responseBody ?? "") !== "" && `\n\n${hop.responseBody ?? ""}`}
      </pre>
      {hop.kind === "token" && <p className={styles.timelineNote}>{t("response.hop.tokenNote")}</p>}
      {final && <p className={styles.timelineNote}>{t("response.hop.bodyInBodyTab")}</p>}
    </section>
  );
}

function SentRequest({ request }: { request: SentRequestDTO }) {
  const t = useT();
  const notes = sentRequestNotes(request);
  const head = wireText(request.head);
  return (
    <>
      <div className={styles.wireLabel}>{t("response.hop.request")}</div>
      <pre className={styles.wire} data-wire="request">
        {head}
        {request.body !== "" && `\n\n${request.body}`}
      </pre>
      {notes.map((n) => (
        <p key={n} className={styles.timelineNote}>
          {n}
        </p>
      ))}
    </>
  );
}

/**
 * The trace of the send. One row is in the Tab order; Up/Down, Home/End
 * move. On a focused event ⌘C / Ctrl+C copies it, Shift+F10 or the Menu key
 * (or a right-click) opens its menu (DBG-07).
 */
function Events({ events }: { events: TimelineEventDTO[] }) {
  const t = useT();
  const [focused, setFocused] = useState(0);
  const menu = useEntryMenu(t("response.timeline.event"));
  const entryOf = (i: number): LogEntryExport | undefined => {
    const e = events[i];
    return e === undefined ? undefined : { text: () => eventText(e), json: () => eventJson(e) };
  };
  const onKeyDown = (e: KeyboardEvent<HTMLTableSectionElement>) => {
    const row = e.target instanceof Element ? e.target.closest("tr") : null;
    const i = Number(row?.dataset.index ?? -1);
    if (i < 0) return;
    if (menu.onKeyDown(e, entryOf(i))) return;
    if (e.altKey || e.metaKey || e.ctrlKey || e.shiftKey) return;
    let next = -1;
    if (e.key === "ArrowDown") next = i + 1;
    else if (e.key === "ArrowUp") next = i - 1;
    else if (e.key === "Home") next = 0;
    else if (e.key === "End") next = events.length - 1;
    if (next < 0 || next >= events.length) return;
    e.preventDefault();
    setFocused(next);
    e.currentTarget.querySelector<HTMLElement>(`tr[data-index="${next}"]`)?.focus();
  };
  const tabbable = Math.min(focused, events.length - 1);
  return (
    <section className={styles.hop} aria-label={t("response.timeline.events")}>
      <h3 className={styles.hopTitle}>{t("response.timeline.events")}</h3>
      <table className={styles.events} aria-label={t("response.timeline.eventsTable")}>
        <thead className={styles.srOnly}>
          <tr>
            <th scope="col">{t("response.timeline.column.time")}</th>
            <th scope="col">{t("response.timeline.column.event")}</th>
            <th scope="col">{t("response.timeline.column.detail")}</th>
          </tr>
        </thead>
        <tbody onKeyDown={onKeyDown} data-shortcut-scope="log">
          {events.map((e, i) => (
            <tr
              key={i}
              data-index={i}
              tabIndex={i === tabbable ? 0 : -1}
              className={styles.eventRow}
              onFocus={() => setFocused(i)}
              onContextMenu={(ev) => {
                const entry = entryOf(i);
                if (entry !== undefined) menu.onContextMenu(ev, entry);
              }}
            >
              <td className={styles.eventTime}>{formatOffsetMs(e.atMs)}</td>
              <td className={styles.eventName} title={e.name}>
                {eventLabel(e)}
              </td>
              <td className={styles.eventDetail}>{e.detail}</td>
            </tr>
          ))}
        </tbody>
      </table>
      {menu.menu}
    </section>
  );
}
