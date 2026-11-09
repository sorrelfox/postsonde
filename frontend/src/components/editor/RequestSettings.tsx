import { useId, useState, type ReactNode } from "react";
import type { RequestDTO } from "../../lib/collection";
import { formatTimeoutMs } from "../../lib/http";
import { t as translate, type MessageKey } from "../../i18n";
import { useT } from "../../i18n/react";
import { MAX_DEFAULT_TIMEOUT_MS, useSettings, type RequestData } from "../../stores";
import { IconWarning } from "../common/icons";
import { RequestProxySettings } from "./RequestProxySettings";
import styles from "./RequestSettings.module.css";

export interface RequestSettingsProps {
  settings: RequestDTO["settings"];
  readOnly: boolean;
  edit: (patch: RequestData) => void;
  /** An sse request: also shows its reconnection policy (SSE-03). */
  stream?: boolean;
  /** Settings of the editor's own kind, shown first (the GraphQL schema auto-refresh, GQL-19). */
  children?: ReactNode;
}

type Settings = RequestDTO["settings"];

/** The redirect limit when a request sets none (httpclient.DefaultMaxRedirects). */
export const DEFAULT_MAX_REDIRECTS = 10;

/** The largest redirect limit the field accepts. */
export const MAX_REDIRECTS_INPUT = 1000;

/** The field's text for a stored number: empty for "use the default". */
function numberText(value: number | null): string {
  return value === null ? "" : String(value);
}

/**
 * Reads the timeout field: empty is "use the default" (null), otherwise
 * whole milliseconds from 0 (no timeout) to 24 hours.
 */
export function parseTimeoutMs(text: string): { ok: true; ms: number | null } | { ok: false; error: string } {
  const trimmed = text.trim();
  if (trimmed === "") return { ok: true, ms: null };
  if (!/^\d+$/.test(trimmed)) return { ok: false, error: translate("editor.settings.timeout.invalid") };
  const ms = Number(trimmed);
  if (ms > MAX_DEFAULT_TIMEOUT_MS) return { ok: false, error: translate("editor.settings.msTooLarge", { max: MAX_DEFAULT_TIMEOUT_MS }) };
  return { ok: true, ms };
}

/**
 * Reads the maximum redirects field (HTTP-22): empty is "use the default"
 * (null, 10 redirects), otherwise a whole number from 0 (follow none) to
 * MAX_REDIRECTS_INPUT.
 */
export function parseMaxRedirects(text: string): { ok: true; max: number | null } | { ok: false; error: string } {
  const trimmed = text.trim();
  if (trimmed === "") return { ok: true, max: null };
  if (!/^\d+$/.test(trimmed)) return { ok: false, error: translate("editor.settings.redirects.invalid") };
  const max = Number(trimmed);
  if (max > MAX_REDIRECTS_INPUT) return { ok: false, error: translate("editor.settings.redirects.tooLarge", { max: MAX_REDIRECTS_INPUT }) };
  return { ok: true, max };
}

/**
 * Where TLS certificate verification is turned off for a request (HTTP-23):
 * "request" by its own verifyTls, "settings" by the app setting it falls
 * back to, or null when certificates are verified.
 */
export function tlsVerificationOff(settings: Settings, appVerifyTls: boolean): "request" | "settings" | null {
  if (settings.verifyTls === false) return "request";
  if (settings.verifyTls === null && !appVerifyTls) return "settings";
  return null;
}

/** tlsVerificationOff with the app setting from the settings store. */
export function useTlsVerificationOff(settings: Settings): "request" | "settings" | null {
  const appVerifyTls = useSettings((s) => s.verifyTls);
  return tlsVerificationOff(settings, appVerifyTls);
}

/**
 * The warning badge of the URL bar while TLS verification is off for the
 * request (HTTP-23); nothing while it is on. Clicking it opens the
 * Settings sub-tab, where the switch is.
 */
export function TlsOffBadge({ settings, className, onOpen }: { settings: Settings; className?: string; onOpen: () => void }) {
  const t = useT();
  const off = useTlsVerificationOff(settings);
  if (off === null) return null;
  const title = off === "request" ? t("editor.settings.tlsBadge.request") : t("editor.settings.tlsBadge.settings");
  return (
    <button type="button" className={className} data-tls-off={off} title={title} onClick={onOpen}>
      <IconWarning size={12} />
      {t("editor.settings.tlsBadge.label")}
    </button>
  );
}

/**
 * The Settings sub-tab of the HTTP request editor, also used by the
 * GraphQL and SSE editors: the request's own timeout (HTTP-21), redirect
 * handling (HTTP-22), TLS certificate verification (HTTP-23), URL encoding
 * (HTTP-24), HTTP version (HTTP-25), cookie behaviour (HTTP-26) and its
 * own proxy, and for an sse request its reconnection policy (SSE-03).
 * Each overrides the default; an empty field or "default" choice keeps
 * it. Stored in the request file's settings.
 */
export function RequestSettings({ settings, readOnly, edit, stream = false, children }: RequestSettingsProps) {
  const set = (patch: Partial<Settings>) => edit({ settings: { ...settings, ...patch } });
  return (
    <div className={styles.settings}>
      {children}
      <TimeoutSetting settings={settings} readOnly={readOnly} set={set} />
      {stream && <ReconnectSettings settings={settings} readOnly={readOnly} set={set} />}
      <RedirectSettings settings={settings} readOnly={readOnly} set={set} />
      <TlsSetting settings={settings} readOnly={readOnly} set={set} />
      <ProtocolSettings settings={settings} readOnly={readOnly} set={set} />
      <CookieSettings settings={settings} readOnly={readOnly} set={set} />
      <RequestProxySettings settings={settings} readOnly={readOnly} edit={edit} />
    </div>
  );
}

/** Values of settings.urlEncoding (HTTP-24); "" (missing) is "auto". */
export type UrlEncoding = "auto" | "rfc3986" | "none";

/** Values of settings.httpVersion (HTTP-25); "" (missing) is "auto". */
export type HttpVersion = "auto" | "http1" | "http2";

/** The encoding mode a stored value means: unknown values read as automatic, as the menu shows them. */
export function urlEncodingOf(settings: Settings): UrlEncoding {
  return settings.urlEncoding === "rfc3986" || settings.urlEncoding === "none" ? settings.urlEncoding : "auto";
}

/** The HTTP version a stored value means. */
export function httpVersionOf(settings: Settings): HttpVersion {
  return settings.httpVersion === "http1" || settings.httpVersion === "http2" ? settings.httpVersion : "auto";
}

const URL_ENCODING_HELP: Record<UrlEncoding, MessageKey> = {
  auto: "editor.settings.urlEncoding.auto.help",
  rfc3986: "editor.settings.urlEncoding.rfc3986.help",
  none: "editor.settings.urlEncoding.none.help",
};

const HTTP_VERSION_HELP: Record<HttpVersion, MessageKey> = {
  auto: "editor.settings.httpVersion.auto.help",
  http1: "editor.settings.httpVersion.http1.help",
  http2: "editor.settings.httpVersion.http2.help",
};

/**
 * URL encoding (HTTP-24) and HTTP version (HTTP-25). "Automatic" is
 * stored as nothing, so the file only names what differs.
 */
function ProtocolSettings({ settings, readOnly, set }: SettingProps) {
  const t = useT();
  const encodingId = useId();
  const versionId = useId();
  const encoding = urlEncodingOf(settings);
  const version = httpVersionOf(settings);
  return (
    <section className={styles.group} aria-label={t("editor.settings.protocol")}>
      <Heading>{t("editor.settings.protocol")}</Heading>
      <div className={styles.row}>
        <label className={styles.label} htmlFor={encodingId}>
          {t("editor.settings.urlEncoding.label")}
        </label>
        <select
          id={encodingId}
          className={styles.select}
          value={encoding}
          disabled={readOnly}
          onChange={(e) => {
            const next = e.currentTarget.value as UrlEncoding;
            set({ urlEncoding: next === "auto" ? "" : next });
          }}
        >
          <option value="auto">{t("editor.settings.urlEncoding.auto")}</option>
          <option value="rfc3986">{t("editor.settings.urlEncoding.rfc3986")}</option>
          <option value="none">{t("editor.settings.urlEncoding.none")}</option>
        </select>
      </div>
      <p className={styles.help}>{t(URL_ENCODING_HELP[encoding])}</p>
      <div className={styles.row}>
        <label className={styles.label} htmlFor={versionId}>
          {t("editor.settings.httpVersion.label")}
        </label>
        <select
          id={versionId}
          className={styles.select}
          value={version}
          disabled={readOnly}
          onChange={(e) => {
            const next = e.currentTarget.value as HttpVersion;
            set({ httpVersion: next === "auto" ? "" : next });
          }}
        >
          <option value="auto">{t("editor.settings.httpVersion.auto")}</option>
          <option value="http1">{t("editor.settings.httpVersion.http1")}</option>
          <option value="http2">{t("editor.settings.httpVersion.http2")}</option>
        </select>
      </div>
      <p className={styles.help}>{t(HTTP_VERSION_HELP[version])}</p>
    </section>
  );
}

/**
 * Cookie behaviour (HTTP-26): send the project jar's cookies, store the
 * response's. Checked is the default and stored as nothing; unchecked is
 * stored as false.
 */
function CookieSettings({ settings, readOnly, set }: SettingProps) {
  const t = useT();
  return (
    <section className={styles.group} aria-label={t("editor.settings.cookies")}>
      <Heading>{t("editor.settings.cookies")}</Heading>
      <label className={styles.check}>
        <input
          type="checkbox"
          checked={settings.sendCookies !== false}
          disabled={readOnly}
          onChange={(e) => set({ sendCookies: e.currentTarget.checked ? null : false })}
        />
        {t("editor.settings.cookies.send")}
        <span className={styles.checkHelp}>{t("editor.settings.cookies.send.help")}</span>
      </label>
      <label className={styles.check}>
        <input
          type="checkbox"
          checked={settings.storeCookies !== false}
          disabled={readOnly}
          onChange={(e) => set({ storeCookies: e.currentTarget.checked ? null : false })}
        />
        {t("editor.settings.cookies.store")}
        <span className={styles.checkHelp}>{t("editor.settings.cookies.store.help")}</span>
      </label>
    </section>
  );
}

interface SettingProps {
  settings: Settings;
  readOnly: boolean;
  set: (patch: Partial<Settings>) => void;
}

function Heading({ children }: { children: ReactNode }) {
  return <h3 className={styles.heading}>{children}</h3>;
}

function TimeoutSetting({ settings, readOnly, set }: SettingProps) {
  const t = useT();
  const defaultMs = useSettings((s) => s.defaultTimeoutMs);
  const [text, setText] = useState(() => numberText(settings.timeoutMs));
  const [error, setError] = useState<string | null>(null);
  const inputId = useId();
  const helpId = useId();
  const errorId = useId();

  const onChange = (next: string) => {
    setText(next);
    const parsed = parseTimeoutMs(next);
    if (!parsed.ok) {
      setError(parsed.error);
      return;
    }
    setError(null);
    if (parsed.ms !== settings.timeoutMs) set({ timeoutMs: parsed.ms });
  };

  const defaultLabel =
    defaultMs > 0 ? t("editor.settings.defaultValue", { value: formatTimeoutMs(defaultMs) }) : t("editor.settings.defaultNone");
  let effective: string;
  if (error !== null) effective = "";
  else if (settings.timeoutMs === null)
    effective =
      defaultMs > 0
        ? t("editor.settings.timeout.default", { timeout: formatTimeoutMs(defaultMs) })
        : t("editor.settings.timeout.defaultNone");
  else if (settings.timeoutMs === 0) effective = t("editor.settings.timeout.none");
  else effective = t("editor.settings.timeout.after", { timeout: formatTimeoutMs(settings.timeoutMs) });

  return (
    <section className={styles.group}>
      <div className={styles.row}>
        <label className={styles.label} htmlFor={inputId}>
          {t("editor.settings.timeout.label")}
        </label>
        <input
          id={inputId}
          className={styles.input}
          inputMode="numeric"
          placeholder={defaultLabel}
          spellCheck={false}
          autoComplete="off"
          value={text}
          readOnly={readOnly}
          aria-invalid={error !== null ? true : undefined}
          aria-describedby={error !== null ? errorId : helpId}
          onChange={(e) => onChange(e.currentTarget.value)}
        />
        <span className={styles.unit}>{t("editor.settings.unit.ms")}</span>
      </div>
      {error !== null ? (
        <p id={errorId} className={styles.error} role="alert">
          {error}
        </p>
      ) : (
        <p id={helpId} className={styles.help}>
          {t("editor.settings.timeout.help", { effective })}
        </p>
      )}
    </section>
  );
}

/**
 * Redirects (HTTP-22): follow or not, the maximum number (empty: 10; 0:
 * none), keep the method on 301/302/303, and forward Authorization to
 * another origin. Unchecked boxes are stored as "default" (null), so the
 * file only names what differs from the safe defaults.
 */
function RedirectSettings({ settings, readOnly, set }: SettingProps) {
  const t = useT();
  const [text, setText] = useState(() => numberText(settings.maxRedirects));
  const [error, setError] = useState<string | null>(null);
  const maxId = useId();
  const helpId = useId();
  const errorId = useId();
  const follow = settings.followRedirects !== false;

  const onMax = (next: string) => {
    setText(next);
    const parsed = parseMaxRedirects(next);
    if (!parsed.ok) {
      setError(parsed.error);
      return;
    }
    setError(null);
    if (parsed.max !== settings.maxRedirects) set({ maxRedirects: parsed.max });
  };

  let effective: string;
  if (!follow) effective = t("editor.settings.redirects.notFollowed");
  else if (settings.maxRedirects === 0) effective = t("editor.settings.redirects.zero");
  else effective = t("editor.settings.redirects.followsUpTo", { count: settings.maxRedirects ?? DEFAULT_MAX_REDIRECTS });

  return (
    <section className={styles.group} aria-label={t("editor.settings.redirects")}>
      <Heading>{t("editor.settings.redirects")}</Heading>
      <label className={styles.check}>
        <input
          type="checkbox"
          checked={follow}
          disabled={readOnly}
          onChange={(e) => set({ followRedirects: e.currentTarget.checked ? null : false })}
        />
        {t("editor.settings.redirects.follow")}
      </label>
      <div className={styles.row}>
        <label className={styles.label} htmlFor={maxId}>
          {t("editor.settings.redirects.maximum")}
        </label>
        <input
          id={maxId}
          className={styles.input}
          inputMode="numeric"
          placeholder={t("editor.settings.defaultValue", { value: DEFAULT_MAX_REDIRECTS })}
          spellCheck={false}
          autoComplete="off"
          value={text}
          readOnly={readOnly}
          disabled={!follow}
          aria-invalid={error !== null ? true : undefined}
          aria-describedby={error !== null ? errorId : helpId}
          onChange={(e) => onMax(e.currentTarget.value)}
        />
        <span className={styles.unit}>{t("editor.settings.unit.redirects")}</span>
      </div>
      {error !== null ? (
        <p id={errorId} className={styles.error} role="alert">
          {error}
        </p>
      ) : (
        <p id={helpId} className={styles.help}>
          {t("editor.settings.redirects.help", { effective, max: DEFAULT_MAX_REDIRECTS })}
        </p>
      )}
      <label className={styles.check}>
        <input
          type="checkbox"
          checked={settings.followOriginalMethod === true}
          disabled={readOnly || !follow}
          onChange={(e) => set({ followOriginalMethod: e.currentTarget.checked ? true : null })}
        />
        {t("editor.settings.redirects.keepMethod")}
        <span className={styles.checkHelp}>{t("editor.settings.redirects.keepMethod.help")}</span>
      </label>
      <label className={styles.check}>
        <input
          type="checkbox"
          checked={settings.forwardAuthorization === true}
          disabled={readOnly || !follow}
          onChange={(e) => set({ forwardAuthorization: e.currentTarget.checked ? true : null })}
        />
        {t("editor.settings.redirects.forwardAuth")}
        <span className={styles.checkHelp}>{t("editor.settings.redirects.forwardAuth.help")}</span>
      </label>
      {follow && settings.forwardAuthorization === true && (
        <p className={styles.warning} role="note">
          <IconWarning size={13} />
          {t("editor.settings.redirects.forwardAuth.warning")}
        </p>
      )}
    </section>
  );
}

/** An sse request's reconnection policy (SSE-03), as stored. */
type Reconnect = NonNullable<Settings["reconnect"]>;

/** Defaults of the reconnection policy (sse.DefaultReconnectAttempts, sse.DefaultReconnectDelay). */
export const DEFAULT_RECONNECT_ATTEMPTS = 5;
export const DEFAULT_RECONNECT_DELAY_MS = 3000;

/** The largest attempt limit the field accepts. */
export const MAX_RECONNECT_ATTEMPTS_INPUT = 1000;

/**
 * Reads the reconnection attempts field: empty is the default (null),
 * otherwise a whole number from 0 (no limit) to MAX_RECONNECT_ATTEMPTS_INPUT.
 */
export function parseReconnectAttempts(text: string): { ok: true; value: number | null } | { ok: false; error: string } {
  const trimmed = text.trim();
  if (trimmed === "") return { ok: true, value: null };
  if (!/^\d+$/.test(trimmed)) return { ok: false, error: translate("editor.settings.reconnect.attempts.invalid") };
  const n = Number(trimmed);
  if (n > MAX_RECONNECT_ATTEMPTS_INPUT) return { ok: false, error: translate("editor.settings.reconnect.attempts.tooLarge", { max: MAX_RECONNECT_ATTEMPTS_INPUT }) };
  return { ok: true, value: n };
}

/** Reads the reconnection delay field: empty is the default (null), otherwise whole milliseconds up to 24 hours. */
export function parseReconnectDelay(text: string): { ok: true; value: number | null } | { ok: false; error: string } {
  const trimmed = text.trim();
  if (trimmed === "") return { ok: true, value: null };
  if (!/^\d+$/.test(trimmed)) return { ok: false, error: translate("editor.settings.reconnect.delay.invalid") };
  const ms = Number(trimmed);
  if (ms > MAX_DEFAULT_TIMEOUT_MS) return { ok: false, error: translate("editor.settings.msTooLarge", { max: MAX_DEFAULT_TIMEOUT_MS }) };
  return { ok: true, value: ms };
}

/** The stored policy after a change: null when nothing is left to store, so the file leaves it out. */
export function reconnectPatch(current: Reconnect | null | undefined, patch: Partial<Reconnect>): Reconnect | null {
  const next: Reconnect = { enabled: null, maxAttempts: null, delayMs: null, extra: "", ...current, ...patch };
  if (next.enabled === null && next.maxAttempts === null && next.delayMs === null && next.extra === "") return null;
  return next;
}

/**
 * Reconnecting (SSE-03): whether a stream that ends or drops is opened
 * again with Last-Event-ID, how many attempts in a row may fail, and the
 * reconnection time until the server sets one with retry. Off is the
 * default and stored as nothing.
 */
function ReconnectSettings({ settings, readOnly, set }: SettingProps) {
  const t = useT();
  const r = settings.reconnect ?? null;
  const enabled = r?.enabled === true;
  const [attemptsText, setAttemptsText] = useState(() => numberText(r?.maxAttempts ?? null));
  const [delayText, setDelayText] = useState(() => numberText(r?.delayMs ?? null));
  const [error, setError] = useState<string | null>(null);
  const attemptsId = useId();
  const delayId = useId();
  const helpId = useId();
  const errorId = useId();
  const update = (patch: Partial<Reconnect>) => set({ reconnect: reconnectPatch(r, patch) });

  const onAttempts = (next: string) => {
    setAttemptsText(next);
    const parsed = parseReconnectAttempts(next);
    if (!parsed.ok) return setError(parsed.error);
    setError(null);
    if (parsed.value !== (r?.maxAttempts ?? null)) update({ maxAttempts: parsed.value });
  };
  const onDelay = (next: string) => {
    setDelayText(next);
    const parsed = parseReconnectDelay(next);
    if (!parsed.ok) return setError(parsed.error);
    setError(null);
    if (parsed.value !== (r?.delayMs ?? null)) update({ delayMs: parsed.value });
  };

  const attempts = r?.maxAttempts ?? DEFAULT_RECONNECT_ATTEMPTS;
  const delay = r?.delayMs ?? DEFAULT_RECONNECT_DELAY_MS;
  const effective = enabled
    ? t("editor.settings.reconnect.on", {
        delay,
        limit: attempts === 0 ? t("editor.settings.reconnect.noLimit") : t("editor.settings.reconnect.limit", { count: attempts }),
      })
    : t("editor.settings.reconnect.off");

  return (
    <section className={styles.group} aria-label={t("editor.settings.reconnect")}>
      <Heading>{t("editor.settings.reconnect")}</Heading>
      <label className={styles.check}>
        <input type="checkbox" checked={enabled} disabled={readOnly} onChange={(e) => update({ enabled: e.currentTarget.checked ? true : null })} />
        {t("editor.settings.reconnect.enable")}
        <span className={styles.checkHelp}>{t("editor.settings.reconnect.enable.help")}</span>
      </label>
      <div className={styles.row}>
        <label className={styles.label} htmlFor={attemptsId}>
          {t("editor.settings.reconnect.attempts")}
        </label>
        <input
          id={attemptsId}
          className={styles.input}
          inputMode="numeric"
          placeholder={t("editor.settings.defaultValue", { value: DEFAULT_RECONNECT_ATTEMPTS })}
          spellCheck={false}
          autoComplete="off"
          value={attemptsText}
          readOnly={readOnly}
          disabled={!enabled}
          aria-invalid={error !== null ? true : undefined}
          aria-describedby={error !== null ? errorId : helpId}
          onChange={(e) => onAttempts(e.currentTarget.value)}
        />
        <span className={styles.unit}>{t("editor.settings.unit.inARow")}</span>
      </div>
      <div className={styles.row}>
        <label className={styles.label} htmlFor={delayId}>
          {t("editor.settings.reconnect.delay")}
        </label>
        <input
          id={delayId}
          className={styles.input}
          inputMode="numeric"
          placeholder={t("editor.settings.defaultValue", { value: DEFAULT_RECONNECT_DELAY_MS })}
          spellCheck={false}
          autoComplete="off"
          value={delayText}
          readOnly={readOnly}
          disabled={!enabled}
          aria-invalid={error !== null ? true : undefined}
          aria-describedby={error !== null ? errorId : helpId}
          onChange={(e) => onDelay(e.currentTarget.value)}
        />
        <span className={styles.unit}>{t("editor.settings.unit.ms")}</span>
      </div>
      {error !== null ? (
        <p id={errorId} className={styles.error} role="alert">
          {error}
        </p>
      ) : (
        <p id={helpId} className={styles.help}>
          {effective}
        </p>
      )}
    </section>
  );
}

/** Values of the TLS verification menu. */
type TlsChoice = "default" | "on" | "off";

function tlsChoice(verifyTls: boolean | null): TlsChoice {
  if (verifyTls === null) return "default";
  return verifyTls ? "on" : "off";
}

/**
 * TLS certificate verification (HTTP-23): the app setting (default), on,
 * or off. Off, from either place, shows a warning here and a badge in the
 * URL bar.
 */
function TlsSetting({ settings, readOnly, set }: SettingProps) {
  const t = useT();
  const appVerifyTls = useSettings((s) => s.verifyTls);
  const off = tlsVerificationOff(settings, appVerifyTls);
  const selectId = useId();

  const onChoice = (choice: TlsChoice) => {
    const verifyTls = choice === "default" ? null : choice === "on";
    if (verifyTls !== settings.verifyTls) set({ verifyTls });
  };

  return (
    <section className={styles.group} aria-label={t("editor.settings.tls")}>
      <Heading>{t("editor.settings.tls")}</Heading>
      <div className={styles.row}>
        <label className={styles.label} htmlFor={selectId}>
          {t("editor.settings.tls.certificates")}
        </label>
        <select
          id={selectId}
          className={styles.select}
          value={tlsChoice(settings.verifyTls)}
          disabled={readOnly}
          onChange={(e) => onChoice(e.currentTarget.value as TlsChoice)}
        >
          <option value="default">{appVerifyTls ? t("editor.settings.tls.defaultVerify") : t("editor.settings.tls.defaultNoVerify")}</option>
          <option value="on">{t("editor.settings.tls.verify")}</option>
          <option value="off">{t("editor.settings.tls.noVerify")}</option>
        </select>
      </div>
      {off !== null ? (
        <p className={styles.warning} role="note" data-tls-off={off}>
          <IconWarning size={13} />
          {off === "request" ? t("editor.settings.tls.offRequest") : t("editor.settings.tls.offSettings")}
        </p>
      ) : (
        <p className={styles.help}>{t("editor.settings.tls.help")}</p>
      )}
    </section>
  );
}
