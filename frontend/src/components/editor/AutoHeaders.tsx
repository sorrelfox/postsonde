import { useEffect, useRef, useState } from "react";
import { waitForQueuedEdits } from "../../commands/autosave";
import { useHttpSend } from "../../commands/useHttpSend";
import type { RequestDTO } from "../../lib/collection";
import { isCancelled, type CancellablePromise } from "../../lib/calls";
import { toAppError } from "../../lib/errors";
import { headerPreview, type HeaderPreviewDTO, type PreviewHeaderDTO } from "../../lib/http";
import { t as translate, type MessageKey } from "../../i18n";
import { useT } from "../../i18n/react";
import { useStores, type RequestId } from "../../stores";
import styles from "./AutoHeaders.module.css";
import { ImplicitHeaders } from "./ImplicitHeaders";

/** Wait after an edit before asking Go again. */
export const PREVIEW_DELAY_MS = 200;

export interface HeaderPreviewState {
  /** The latest preview, null before the first one (or when it failed). */
  preview: HeaderPreviewDTO | null;
  /** Why the latest preview failed, "" when it did not. */
  error: string;
}

/**
 * The headers a send of request `id` would write (HTTP-09), from Go
 * (HTTPService.HeaderPreview), asked again after each edit of `request`
 * (once they reached Go) and after each send (the cookie jar may have
 * changed). The first one is asked at once, later ones after
 * PREVIEW_DELAY_MS.
 */
export function useHeaderPreview(id: RequestId, request: RequestDTO): HeaderPreviewState {
  const stores = useStores();
  const { state: send } = useHttpSend(id);
  const [state, setState] = useState<HeaderPreviewState>({ preview: null, error: "" });
  const first = useRef(true);

  useEffect(() => {
    let stopped = false;
    let call: CancellablePromise<HeaderPreviewDTO> | null = null;
    const delay = first.current ? 0 : PREVIEW_DELAY_MS;
    first.current = false;
    const timer = setTimeout(() => {
      void (async () => {
        await waitForQueuedEdits(stores, id);
        if (stopped) return;
        try {
          call = headerPreview(id);
          const preview = await call;
          if (!stopped) setState({ preview, error: "" });
        } catch (err: unknown) {
          if (!stopped && !isCancelled(err)) setState({ preview: null, error: toAppError(err).message });
        }
      })();
    }, delay);
    return () => {
      stopped = true;
      clearTimeout(timer);
      call?.cancel();
    };
  }, [stores, id, request, send.response]);

  return state;
}

/** What a source means, for the row's title. */
const SOURCE_TEXT: Readonly<Record<string, MessageKey>> = {
  url: "editor.autoHeaders.source.url",
  body: "editor.autoHeaders.source.body",
  auth: "editor.autoHeaders.source.auth",
  cookies: "editor.autoHeaders.source.cookies",
  client: "editor.autoHeaders.source.client",
};

/** Automatic headers the Headers table cannot turn off, and why. */
const REQUIRED: Readonly<Record<string, MessageKey>> = {
  host: "editor.autoHeaders.required.host",
  "content-length": "editor.autoHeaders.required.contentLength",
  "transfer-encoding": "editor.autoHeaders.required.transferEncoding",
};

/** One row of the list: a header sent, overridden, turned off, or not sent at all. */
interface AutoRow {
  header: PreviewHeaderDTO;
  /** Sent ("auto"), overridden by a row, turned off, or not sent by default. */
  state: "auto" | "overridden" | "omitted" | "absent";
}

/** The rows to list: what the client adds, then what is not sent. */
export function autoRows(preview: HeaderPreviewDTO): AutoRow[] {
  const typed = new Set(["request", "folder"]);
  const rows: AutoRow[] = (preview.headers ?? [])
    .filter((h) => !typed.has(h.source))
    .map((header) => ({ header, state: "auto" }));
  for (const header of preview.suppressed ?? []) {
    rows.push({ header, state: header.reason === "overridden" ? "overridden" : "omitted" });
  }
  if (preview.digest && !rows.some((r) => r.header.name.toLowerCase() === "authorization")) {
    rows.push({
      header: { name: "Authorization", value: "Digest …", masked: false, source: "auth", reason: "" }, // i18n-ignore: a header value
      state: "auto",
    });
  }
  if (!(preview.headers ?? []).some((h) => h.name.toLowerCase() === "user-agent")) {
    rows.push({
      header: { name: "User-Agent", value: translate("editor.autoHeaders.notSent"), masked: false, source: "client", reason: "" },
      state: "absent",
    });
  }
  return rows;
}

export interface AutoHeadersProps {
  id: RequestId;
  request: RequestDTO;
  readOnly: boolean;
  /** Replaces the request's settings (turning an automatic header off or on). */
  onSettings: (settings: RequestDTO["settings"]) => void;
  /** Adds an enabled header row, to override an automatic header. */
  onOverride: (name: string, value: string) => void;
}

/**
 * The headers PostSonde adds by itself when it sends (HTTP-09), read-only
 * above the Headers table: Host, Content-Length or Transfer-Encoding,
 * Content-Type, the Auth tab's headers, the cookie jar's Cookie, and a
 * note that no User-Agent is sent. Computed in Go by the send's own code
 * (useHeaderPreview), so the list is what the timeline then shows.
 *
 * The body's Content-Type, the auth's headers and the jar's cookies can be
 * turned off with the row's checkbox (settings.omitHeaders, and
 * settings.sendCookies for cookies); Override adds a header row of the
 * same name, which wins. Host and Content-Length cannot be turned off.
 * Until the first preview arrives, or when it fails, the body's
 * Content-Type is shown from the frontend's own rule (ImplicitHeaders).
 */
export function AutoHeaders({ id, request, readOnly, onSettings, onOverride }: AutoHeadersProps) {
  const t = useT();
  const { preview, error } = useHeaderPreview(id, request);
  if (preview === null) {
    return (
      <>
        <ImplicitHeaders body={request.body} headers={request.headers} />
        {error !== "" && (
          <p className={styles.error} role="note">
            {t("editor.autoHeaders.failed", { error })}
          </p>
        )}
      </>
    );
  }
  const settings = request.settings;
  const omitted = settings.omitHeaders ?? [];

  const toggle = (row: AutoRow) => {
    const { name, source } = row.header;
    const on = row.state === "omitted";
    if (source === "cookies") {
      onSettings({ ...settings, sendCookies: on ? null : false });
      return;
    }
    const lower = name.toLowerCase();
    const rest = omitted.filter((n) => n.toLowerCase() !== lower);
    onSettings({ ...settings, omitHeaders: on ? (rest.length > 0 ? rest : null) : [...rest, name] });
  };

  const rows = autoRows(preview);
  const http2 = preview.httpVersion !== "http1";
  return (
    <section className={styles.auto}>
      <p className={styles.caption}>{http2 ? t("editor.autoHeaders.captionHttp2") : t("editor.autoHeaders.caption")}</p>
      <ul className={styles.list} aria-label={t("editor.autoHeaders.label")}>
        {rows.map((row, i) => {
          const { name, value, source, masked } = row.header;
          const lower = name.toLowerCase();
          const requiredKey = REQUIRED[lower];
          const switchable =
            row.state !== "overridden" &&
            row.state !== "absent" &&
            (source === "auth" || source === "cookies" || (source === "body" && lower === "content-type"));
          let why: string;
          if (row.state === "overridden") why = t("editor.autoHeaders.why.overridden");
          else if (row.state === "omitted") why = t("editor.autoHeaders.why.omitted");
          else if (row.state === "absent") why = t("editor.autoHeaders.why.absent");
          else {
            why = t(SOURCE_TEXT[source] ?? "editor.autoHeaders.source.other");
            if (requiredKey !== undefined) why = t("editor.autoHeaders.why.required", { why, required: t(requiredKey) });
            if (masked) why = t("editor.autoHeaders.why.masked", { why });
          }
          const badge =
            row.state === "overridden"
              ? t("editor.autoHeaders.badge.overridden")
              : row.state === "omitted"
                ? t("editor.autoHeaders.badge.off")
                : row.state === "absent"
                  ? t("editor.autoHeaders.badge.none")
                  : t("editor.autoHeaders.badge.auto");
          const struck = row.state === "overridden" || row.state === "omitted";
          const canOverride = !readOnly && row.state !== "overridden" && lower !== "content-length" && lower !== "transfer-encoding";
          return (
            <li key={`${lower}-${row.state}-${i}`} className={styles.row} data-state={row.state} title={why}>
              <span className={styles.toggleCell}>
                {row.state !== "absent" && (
                  <input
                    type="checkbox"
                    className={styles.toggle}
                    checked={row.state === "auto"}
                    disabled={readOnly || !switchable}
                    aria-label={t("editor.autoHeaders.send", { name })}
                    onChange={() => toggle(row)}
                  />
                )}
              </span>
              <span className={styles.name}>{name}</span>
              <span className={styles.value}>{struck ? <s>{value}</s> : value}</span>
              <span className={styles.badge}>{badge}</span>
              <span className={styles.actions}>
                {canOverride && (
                  <button
                    type="button"
                    className={styles.override}
                    aria-label={row.state === "absent" ? t("editor.autoHeaders.addName", { name }) : t("editor.autoHeaders.overrideName", { name })}
                    onClick={() => onOverride(name, row.state === "absent" || masked ? "" : value)}
                  >
                    {row.state === "absent" ? t("editor.autoHeaders.add") : t("editor.autoHeaders.override")}
                  </button>
                )}
              </span>
              <span className={styles.srOnly}>{why}</span>
            </li>
          );
        })}
      </ul>
    </section>
  );
}
