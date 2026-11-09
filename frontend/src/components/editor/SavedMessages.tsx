import { useEffect, useRef, useState } from "react";
import { t as translate, type MessageKey } from "../../i18n";
import { useT } from "../../i18n/react";
import type { AppError } from "../../lib/errors";
import { toAppError } from "../../lib/errors";
import { wsMessageFormat, type SavedMessageDTO, type WsMessageFormat } from "../../lib/websocket";
import { IconChevron, IconPlay, IconPlus, IconTrash, IconWarning } from "../common/icons";
import styles from "./SavedMessages.module.css";

/** A saved message's format as the composer knows it ("" and unknown formats read as text). */
export function savedFormat(m: SavedMessageDTO): WsMessageFormat {
  return wsMessageFormat(m.format);
}

/** The badge after a saved message's name; none for text. */
const FORMAT_BADGES: Readonly<Record<WsMessageFormat, string | null>> = { text: null, json: "JSON", hex: "HEX", base64: "B64" };

/** The tooltip of a saved message's badge (message keys). */
const FORMAT_TITLES: Readonly<Record<WsMessageFormat, MessageKey>> = {
  text: "editorProtocols.saved.format.text",
  json: "editorProtocols.saved.format.json",
  hex: "editorProtocols.saved.format.hex",
  base64: "editorProtocols.saved.format.base64",
};

/** "Message 3": the first "Message N" (or "Emit N") name not in use. */
export function nextMessageName(messages: readonly SavedMessageDTO[], kind: "message" | "emit" = "message"): string {
  const used = new Set(messages.map((m) => m.name));
  for (let n = messages.length + 1; ; n++) {
    const name =
      kind === "emit"
        ? translate("editorProtocols.saved.emitName", { n })
        : translate("editorProtocols.saved.messageName", { n });
    if (!used.has(name)) return name;
  }
}

/** `list` with the item at `from` moved to `to`. */
export function moveItem<T>(list: readonly T[], from: number, to: number): T[] {
  const out = [...list];
  const [item] = out.splice(from, 1);
  if (item !== undefined) out.splice(to, 0, item);
  return out;
}

function errorText(err: AppError): string {
  return err.hint ? `${err.message} ${err.hint}` : err.message;
}

export interface SavedMessagesProps {
  /** The request's saved messages, in order. */
  readonly messages: readonly SavedMessageDTO[];
  /** Saves the new list (auto-saved with the request). */
  readonly onChange: (messages: SavedMessageDTO[]) => void;
  /**
   * The composer's message and format: what Save and Update store. With
   * `event` set the list holds Socket.IO emits (SIO-03): the event name and
   * ack switch are stored too, and `text` is the arguments' JSON.
   */
  readonly composer: { readonly text: string; readonly format: WsMessageFormat; readonly event?: string; readonly ack?: boolean };
  /** Puts a saved message into the composer. */
  readonly onLoad: (message: SavedMessageDTO) => void;
  /** Sends a saved message; a rejection (an AppError) is shown under the list. */
  readonly onSend: (message: SavedMessageDTO) => Promise<unknown>;
  /** False while not connected: the send buttons are disabled. */
  readonly canSend: boolean;
  /** Nothing can be changed; sending still works. */
  readonly readOnly: boolean;
}

/**
 * The saved messages of a WebSocket request (WS-07): named messages kept in
 * the request file and sent with one click (▶). Save stores the composer's
 * message under a new name; clicking a name's field renames it; Load puts a
 * message into the composer, and Update writes the composer's text back to
 * the message loaded last. The arrows reorder, the bin deletes. Their
 * {{variables}} are resolved by Go when they are sent (WS-11), in text and
 * JSON messages only: hex and Base64 ones (HEX, B64) are binary messages
 * sent as written (WS-04). The Socket.IO editor keeps its saved emits here
 * too (composer.event set): each shows its event name, and ACK when it
 * asks for an acknowledgement.
 */
export function SavedMessages({ messages, onChange, composer, onLoad, onSend, canSend, readOnly }: SavedMessagesProps) {
  const t = useT();
  const [error, setError] = useState<string | null>(null);
  const [sending, setSending] = useState<number | null>(null);
  /** The message last loaded into the composer, which Update overwrites. */
  const [loaded, setLoaded] = useState<number | null>(null);
  const inputs = useRef<(HTMLInputElement | null)[]>([]);
  const [focusIndex, setFocusIndex] = useState<number | null>(null);
  useEffect(() => {
    if (focusIndex === null) return;
    const input = inputs.current[focusIndex];
    input?.focus();
    input?.select();
    setFocusIndex(null);
  }, [focusIndex]);

  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  const emits = composer.event !== undefined;
  const loadedMessage = loaded !== null ? messages[loaded] : undefined;
  const canUpdate =
    !readOnly &&
    loadedMessage !== undefined &&
    (loadedMessage.text !== composer.text ||
      (emits ? (loadedMessage.event ?? "") !== composer.event || (loadedMessage.ack ?? false) !== (composer.ack ?? false) : savedFormat(loadedMessage) !== composer.format));
  /** The composer's content as stored: an emit keeps its event and ack, a message its format. */
  const fromComposer = (): Pick<SavedMessageDTO, "text" | "format" | "event" | "ack"> =>
    emits ? { text: composer.text, format: "", event: composer.event, ack: composer.ack ?? false } : { text: composer.text, format: composer.format };
  const canSave = emits ? (composer.event ?? "").trim() !== "" : composer.text !== "";

  const save = () => {
    const next: SavedMessageDTO = { name: nextMessageName(messages, emits ? "emit" : "message"), ...fromComposer(), extra: "" };
    onChange([...messages, next]);
    setLoaded(messages.length);
    setFocusIndex(messages.length);
  };

  const update = () => {
    if (loaded === null || loadedMessage === undefined) return;
    onChange(messages.map((m, i) => (i === loaded ? { ...m, ...fromComposer() } : m)));
  };

  const rename = (index: number, name: string) => onChange(messages.map((m, i) => (i === index ? { ...m, name } : m)));

  const move = (index: number, to: number) => {
    onChange(moveItem(messages, index, to));
    if (loaded === index) setLoaded(to);
    else if (loaded === to) setLoaded(index);
  };

  const remove = (index: number) => {
    onChange(messages.filter((_, i) => i !== index));
    if (loaded === index) setLoaded(null);
    else if (loaded !== null && loaded > index) setLoaded(loaded - 1);
  };

  const load = (index: number) => {
    const m = messages[index];
    if (m === undefined) return;
    onLoad(m);
    setLoaded(index);
  };

  const send = (index: number) => {
    const m = messages[index];
    if (m === undefined || !canSend || sending !== null) return;
    setError(null);
    setSending(index);
    onSend(m)
      .catch((err: unknown) => {
        if (mounted.current) setError(t("editorProtocols.saved.sendFailed", { name: m.name, error: errorText(toAppError(err)) }));
      })
      .finally(() => {
        if (mounted.current) setSending(null);
      });
  };

  return (
    <section className={styles.saved} aria-label={emits ? t("editorProtocols.saved.emitsLabel") : t("editorProtocols.saved.messagesLabel")}>
      <div className={styles.header}>
        <h3 className={styles.title}>{t("editorProtocols.saved.title")}</h3>
        {canUpdate && (
          <button
            type="button"
            className={styles.textButton}
            title={
              emits
                ? t("editorProtocols.saved.updateEmitTitle", { name: loadedMessage.name })
                : t("editorProtocols.saved.updateMessageTitle", { name: loadedMessage.name })
            }
            onClick={update}
          >
            {t("editorProtocols.saved.update")}
          </button>
        )}
        {!readOnly && (
          <button
            type="button"
            className={styles.textButton}
            title={emits ? t("editorProtocols.saved.saveEmitTitle") : t("editorProtocols.saved.saveMessageTitle")}
            disabled={!canSave}
            onClick={save}
          >
            <IconPlus size={11} />
            {t("editorProtocols.saved.save")}
          </button>
        )}
      </div>
      {messages.length === 0 ? (
        <p className={styles.empty}>
          {readOnly
            ? emits
              ? t("editorProtocols.saved.noEmits")
              : t("editorProtocols.saved.noMessages")
            : emits
              ? t("editorProtocols.saved.emptyEmits")
              : t("editorProtocols.saved.emptyMessages")}
        </p>
      ) : (
        <ul className={styles.list}>
          {messages.map((m, i) => {
            const label =
              m.name.trim() !== ""
                ? m.name
                : emits
                  ? t("editorProtocols.saved.unnamedEmit", { n: i + 1 })
                  : t("editorProtocols.saved.unnamedMessage", { n: i + 1 });
            return (
              <li key={i} className={styles.row} data-loaded={loaded === i || undefined}>
                <button
                  type="button"
                  className={styles.send}
                  aria-label={t("editorProtocols.saved.send", { name: label })}
                  title={canSend ? t("editorProtocols.saved.send", { name: label }) : t("editorProtocols.saved.connectToSend")}
                  disabled={!canSend || sending !== null}
                  onClick={() => send(i)}
                >
                  <IconPlay size={10} />
                </button>
                <input
                  ref={(el) => {
                    inputs.current[i] = el;
                  }}
                  className={styles.name}
                  aria-label={
                    emits
                      ? t("editorProtocols.saved.emitNameLabel", { n: i + 1 })
                      : t("editorProtocols.saved.messageNameLabel", { n: i + 1 })
                  }
                  title={emits ? `${m.event ?? ""} ${m.text}`.trim() : m.text}
                  spellCheck={false}
                  autoComplete="off"
                  value={m.name}
                  readOnly={readOnly}
                  onChange={(e) => rename(i, e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
                      e.preventDefault();
                      send(i);
                    } else if (e.key === "Enter" || e.key === "Escape") {
                      e.currentTarget.blur();
                    }
                  }}
                />
                {emits && (
                  <span
                    className={styles.format}
                    title={m.ack ? t("editorProtocols.saved.ackTitle") : t("editorProtocols.saved.eventTitle")}
                  >
                    {m.event ?? ""}
                    {/* i18n-ignore: ACK is the Socket.IO protocol badge, like HEX and B64 */}
                    {m.ack ? " · ACK" : ""}
                  </span>
                )}
                {!emits && FORMAT_BADGES[savedFormat(m)] !== null && (
                  <span className={styles.format} title={t(FORMAT_TITLES[savedFormat(m)])}>
                    {FORMAT_BADGES[savedFormat(m)]}
                  </span>
                )}
                {!readOnly && (
                  <>
                    <button
                      type="button"
                      className={styles.textButton}
                      title={emits ? t("editorProtocols.saved.loadEmitTitle") : t("editorProtocols.saved.loadMessageTitle")}
                      aria-label={t("editorProtocols.saved.loadLabel", { name: label })}
                      onClick={() => load(i)}
                    >
                      {t("editorProtocols.saved.load")}
                    </button>
                    <button
                      type="button"
                      className={styles.iconButton}
                      aria-label={t("editorProtocols.saved.moveUp", { name: label })}
                      disabled={i === 0}
                      onClick={() => move(i, i - 1)}
                    >
                      <IconChevron direction="up" size={11} />
                    </button>
                    <button
                      type="button"
                      className={styles.iconButton}
                      aria-label={t("editorProtocols.saved.moveDown", { name: label })}
                      disabled={i === messages.length - 1}
                      onClick={() => move(i, i + 1)}
                    >
                      <IconChevron direction="down" size={11} />
                    </button>
                    <button type="button" className={styles.iconButton} aria-label={t("editorProtocols.saved.delete", { name: label })} onClick={() => remove(i)}>
                      <IconTrash size={11} />
                    </button>
                  </>
                )}
              </li>
            );
          })}
        </ul>
      )}
      {error !== null && (
        <p className={styles.error} role="alert">
          <IconWarning size={12} />
          <span>{error}</span>
        </p>
      )}
    </section>
  );
}
