import { useEffect, useMemo, useRef, useState, type KeyboardEvent, type ReactNode } from "react";
import { copyLogText } from "../../commands/logExport";
import type { MessageKey } from "../../i18n";
import { useT } from "../../i18n/react";
import type { LogMessage } from "../../lib/messageSource";
import { IconClose } from "../common/icons";
import { copyLogEntry, type LogEntryExport } from "../common/LogActions";
import {
  base64,
  BASE64_BYTES,
  base64Head,
  DIRECTION_KEYS,
  formatRelative,
  formatSize,
  formatTimestamp,
  HEXDUMP_BYTES,
  hexDump,
  hexString,
  messageSize,
  prettyJson,
  type BinaryView,
} from "./format";
import styles from "./MessageLog.module.css";

export interface MessageDetailProps {
  seq: number;
  /** The message, or undefined once it was dropped from the log. */
  message: LogMessage | undefined;
  startTime: number | null;
  onClose: () => void;
  /** How a binary payload is shown at first; the pane's own switch changes it for this message. Default hex. */
  binaryView?: BinaryView;
  /** Shows a text payload in a protocol's own way; null keeps the default (MessageLogProps.renderPayload). */
  renderPayload?: (message: LogMessage) => ReactNode | null;
  /** The message as an entry: adds "Copy entry" (full details, secrets masked; DBG-07). */
  entry?: LogEntryExport;
}

/** The last copy: its "Copied …" message, and whether it worked. */
type CopyState = { done: MessageKey; ok: boolean } | null;

async function copyText(text: string): Promise<void> {
  if (!navigator.clipboard) throw new Error("clipboard unavailable");
  await navigator.clipboard.writeText(text);
}

/**
 * The selected message's full payload: pretty JSON when it parses; for
 * binary a hex dump of the first HEXDUMP_BYTES or the Base64 of the first
 * BASE64_BYTES (WS-06), switched here. Copy takes the whole payload; a
 * text payload and "Copy entry" (the message with its details) are masked
 * in Go first (DBG-07). Hex and Base64 copies are the bytes as received.
 */
export function MessageDetail({ seq, message, startTime, onClose, binaryView = "hex", renderPayload, entry }: MessageDetailProps) {
  const t = useT();
  const [view, setView] = useState<BinaryView>(binaryView);
  const [copied, setCopied] = useState<CopyState>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(
    () => () => {
      if (timer.current !== null) clearTimeout(timer.current);
    },
    [],
  );

  const body = useMemo(() => {
    if (message === undefined) return null;
    if (message.data !== undefined) {
      if (view === "base64") {
        return { text: base64Head(message.data), format: "Base64", truncated: message.data.length > BASE64_BYTES, cap: BASE64_BYTES };
      }
      // i18n-ignore: the name of the encoding (not shown for binary payloads)
      return { text: hexDump(message.data), format: "Hex", truncated: message.data.length > HEXDUMP_BYTES, cap: HEXDUMP_BYTES };
    }
    const text = message.text ?? "";
    const pretty = prettyJson(text);
    return pretty !== null ? { text: pretty, format: "JSON", truncated: false, cap: 0 } : { text, format: t("messagelog.detail.format.text"), truncated: false, cap: 0 };
  }, [message, view, t]);

  const custom = useMemo(
    () => (message !== undefined && message.data === undefined && renderPayload !== undefined ? renderPayload(message) : null),
    [message, renderPayload],
  );

  const copy = (done: MessageKey, text: string) => {
    copyText(text).then(
      () => setCopied({ done, ok: true }),
      () => setCopied({ done, ok: false }),
    );
    if (timer.current !== null) clearTimeout(timer.current);
    timer.current = setTimeout(() => setCopied(null), 1500);
  };

  const onKeyDown = (e: KeyboardEvent<HTMLElement>) => {
    if (e.key === "Escape") {
      e.stopPropagation();
      onClose();
    }
  };

  return (
    <section className={styles.detail} aria-label={t("messagelog.detail.label", { number: seq + 1 })} onKeyDown={onKeyDown}>
      <header className={styles.detailHeader}>
        {message === undefined ? (
          <span className={styles.detailTitle}>{t("messagelog.detail.label", { number: seq + 1 })}</span>
        ) : (
          <>
            <span className={styles.detailTitle}>
              <span className={styles.cDir} data-direction={message.direction}>
                {t(DIRECTION_KEYS[message.direction])}
              </span>
              <span>{message.kind}</span>
              {message.badge !== undefined && <span className={styles.badge}>{message.badge}</span>}
            </span>
            <span className={styles.detailMeta} title={formatTimestamp(message.time)}>
              {formatTimestamp(message.time)} ({formatRelative(message.time, startTime)})
            </span>
            {messageSize(message) !== null && <span className={styles.detailMeta}>{formatSize(messageSize(message))}</span>}
            {body !== null && message.data === undefined && <span className={styles.detailMeta}>{body.format}</span>}
            {message.data !== undefined && (
              <div role="group" aria-label={t("messagelog.detail.showAs")} className={styles.group}>
                {(["hex", "base64"] as const).map((v) => (
                  <button key={v} type="button" className={styles.toggle} aria-pressed={view === v} onClick={() => setView(v)}>
                    {v === "hex" ? t("messagelog.detail.hexdump") : "Base64"}
                  </button>
                ))}
              </div>
            )}
          </>
        )}
        <span className={styles.spacer} />
        {copied !== null && (
          <span className={styles.copied} role="status">
            {copied.ok ? t(copied.done) : t("messagelog.detail.copyFailed")}
          </span>
        )}
        {message !== undefined && entry !== undefined && (
          <button type="button" className={styles.button} title={t("messagelog.detail.copyEntryTitle")} onClick={() => void copyLogEntry(entry)}>
            {t("messagelog.detail.copyEntry")}
          </button>
        )}
        {message !== undefined && message.data !== undefined && (
          <>
            <button type="button" className={styles.button} onClick={() => copy("messagelog.detail.copied.hex", hexString(message.data!))}>
              {t("messagelog.detail.copyHex")}
            </button>
            <button type="button" className={styles.button} onClick={() => copy("messagelog.detail.copied.base64", base64(message.data!))}>
              {t("messagelog.detail.copyBase64")}
            </button>
          </>
        )}
        {message !== undefined && message.data === undefined && (
          <button type="button" className={styles.button} onClick={() => void copyLogText(message.text ?? "", "text", "payload")}>
            {t("messagelog.detail.copyPayload")}
          </button>
        )}
        <button type="button" className={styles.iconButton} aria-label={t("messagelog.detail.close")} title={t("messagelog.detail.closeTitle")} onClick={onClose}>
          <IconClose size={13} />
        </button>
      </header>
      {message === undefined ? (
        <p className={styles.detailEmpty}>{t("messagelog.detail.gone")}</p>
      ) : (
        <div className={styles.detailBody}>
          {message.meta !== undefined && Object.keys(message.meta).length > 0 && (
            <dl className={styles.meta}>
              {Object.entries(message.meta).map(([k, v]) => (
                <div key={k} className={styles.metaItem}>
                  <dt>{k}</dt>
                  <dd>{String(v)}</dd>
                </div>
              ))}
            </dl>
          )}
          {custom !== null ? (
            custom
          ) : body !== null && body.text !== "" ? (
            <pre className={styles.payload} tabIndex={0} aria-label={t("messagelog.detail.payload")} data-wrap={body.format === "Base64" || undefined}>
              {body.text}
              {body.truncated && `\n${t("messagelog.detail.truncated", { size: formatSize(body.cap) })}`}
            </pre>
          ) : (
            <p className={styles.detailEmpty}>{t("messagelog.detail.noPayload")}</p>
          )}
        </div>
      )}
    </section>
  );
}
