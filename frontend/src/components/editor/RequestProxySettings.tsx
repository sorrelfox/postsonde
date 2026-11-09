import { useEffect, useId, useRef, useState } from "react";
import { Trans, useT, type Translate } from "../../i18n/react";
import type { MessageKey } from "../../i18n";
import type { RequestSettingsDTO } from "../../lib/collection";
import { getProxyScopes, onProxyScopesInvalidated, type ScopesDTO } from "../../lib/proxyScopes";
import {
  composeProxyUrl,
  DEFAULT_PROXY_FIELDS,
  defaultProxyPort,
  parseProxyUrl,
  PROXY_TYPES,
  proxyPasswordStorage,
  proxyProblems,
  proxyScheme,
  REQUEST_PROXY_MODES,
  requestProxyModeOf,
  requestProxyPatch,
  withProxyPassword,
  type ProxyFields,
  type ProxyProblem,
  type ProxyType,
  type RequestProxyDTO,
  type RequestProxyMode,
} from "../../lib/requestProxy";
import type { RequestData } from "../../stores";
import { IconEye, IconEyeOff, IconInfo, IconLock, IconWarning } from "../common/icons";
import settingsStyles from "./RequestSettings.module.css";
import styles from "./RequestProxySettings.module.css";

// A request's own proxy (docs/file-format.md#proxy), the same section in
// the Settings tab of every request editor: inherit the proxy in effect
// (the active environment's, the project's, else the app's), a custom
// proxy, or none. A custom proxy is edited as fields (SOCKS5 first) and
// stored as one URL; its password is separate and, when typed rather than
// a {{variable}}, kept on this computer only, as an auth secret is. What
// is typed is saved even when it would not work: sending refuses it with
// Go's own error.

export interface RequestProxySettingsProps {
  settings: RequestSettingsDTO;
  readOnly: boolean;
  edit: (patch: RequestData) => void;
  /** Why the proxy cannot be changed now (a TCP or UDP connection is open): disables the controls, shown as their title. */
  locked?: string;
  /** A UDP request: only a SOCKS5 proxy relays its datagrams. */
  udp?: boolean;
  /** Leave out the heading, when the fold around the section names it. */
  noHeading?: boolean;
}

const MODE_LABELS: Readonly<Record<RequestProxyMode, MessageKey>> = {
  inherit: "editor.settings.proxy.mode.inherit",
  custom: "editor.settings.proxy.mode.custom",
  none: "editor.settings.proxy.mode.none",
};

const TYPE_LABELS: Readonly<Record<ProxyType, string>> = {
  socks5: "SOCKS5", // i18n-ignore: a protocol name
  socks4: "SOCKS4", // i18n-ignore: a protocol name
  http: "HTTP", // i18n-ignore: a protocol name
  https: "HTTPS", // i18n-ignore: a protocol name
};

/** The message of a problem of the custom proxy's fields. */
function problemText(t: Translate, problem: ProxyProblem, type: ProxyType): string {
  switch (problem) {
    case "hostRequired":
      return t("editor.settings.proxy.error.hostRequired");
    case "hostInvalid":
      return t("editor.settings.proxy.error.hostInvalid");
    case "portInvalid":
      return t("editor.settings.proxy.error.portInvalid", { variable: "{{variable}}", port: defaultProxyPort(type) });
    case "passwordNeedsUser":
      return t("editor.settings.proxy.error.passwordNeedsUser");
    case "socks4Password":
      return t("editor.settings.proxy.error.socks4Password");
    case "httpUserColon":
      return t("editor.settings.proxy.error.httpUserColon");
  }
}

export function RequestProxySettings({ settings, readOnly, edit, locked, udp = false, noHeading = false }: RequestProxySettingsProps) {
  const t = useT();
  const proxy = settings.proxy ?? null;
  const mode = requestProxyModeOf(proxy);
  const set = (patch: Partial<RequestProxyDTO>) => edit({ settings: { ...settings, proxy: requestProxyPatch(proxy, patch) } });
  const setProxy = (next: RequestProxyDTO | null) => edit({ settings: { ...settings, proxy: next } });
  const off = readOnly || locked !== undefined;

  return (
    <section
      className={`${settingsStyles.group} ${styles.section}`}
      aria-label={t("editor.settings.proxy")}
      data-proxy-mode={mode ?? proxy?.mode ?? ""}
    >
      {!noHeading && <h3 className={settingsStyles.heading}>{t("editor.settings.proxy")}</h3>}
      <div className={styles.segmented} role="group" aria-label={t("editor.settings.proxy.mode")} title={locked}>
        {REQUEST_PROXY_MODES.map((m) => (
          <button
            key={m}
            type="button"
            className={styles.segment}
            aria-pressed={mode === m}
            disabled={off}
            onClick={() => {
              if (m !== mode) set({ mode: m });
            }}
          >
            {t(MODE_LABELS[m])}
          </button>
        ))}
      </div>
      {mode === null && (
        <p className={settingsStyles.warning} role="note">
          <IconWarning size={13} />
          {t("editor.settings.proxy.unknownMode", { mode: proxy?.mode ?? "" })}
        </p>
      )}
      {mode === "inherit" && <InheritedProxy />}
      {mode === "none" && <p className={settingsStyles.help}>{t("editor.settings.proxy.none.help")}</p>}
      {mode === "custom" && (
        <CustomProxy proxy={proxy} readOnly={readOnly} locked={locked} udp={udp} set={set} setProxy={setProxy} />
      )}
    </section>
  );
}

/**
 * What inheriting means now: the proxy in effect and the level it comes
 * from (ProxyScopesService), loaded while shown and again after the
 * events that change it. Without it, the rule.
 */
function InheritedProxy() {
  const t = useT();
  const [scopes, setScopes] = useState<ScopesDTO | null>(null);

  useEffect(() => {
    let live = true;
    const refresh = () => {
      getProxyScopes().then(
        (next) => {
          if (live) setScopes(next);
        },
        () => {
          // No Go backend, or no answer: the rule is shown instead.
          if (live) setScopes(null);
        },
      );
    };
    refresh();
    const off = onProxyScopesInvalidated(refresh);
    return () => {
      live = false;
      off();
    };
  }, []);

  if (scopes === null) {
    return <p className={settingsStyles.help}>{t("editor.settings.proxy.inherit.unknown")}</p>;
  }
  const eff = scopes.effective;
  const httpProxy = eff.httpProxy ?? "";
  const name = (scopes.environments ?? []).find((e) => e.active)?.name ?? "";
  let status: string;
  if (eff.error) status = t("editor.settings.proxy.inherit.failing");
  else if (eff.httpSplit && httpProxy !== "" && eff.proxy !== httpProxy)
    status = t("editor.settings.proxy.inherit.system", { proxy: eff.proxy || t("editor.settings.proxy.inherit.direct"), http: httpProxy });
  else if (eff.proxy !== "")
    status =
      eff.level === "environment"
        ? t("editor.settings.proxy.inherit.environment", { proxy: eff.proxy, name })
        : eff.level === "project"
          ? t("editor.settings.proxy.inherit.project", { proxy: eff.proxy })
          : t("editor.settings.proxy.inherit.app", { proxy: eff.proxy });
  else
    status =
      eff.level === "environment"
        ? t("editor.settings.proxy.inherit.environmentOff", { name })
        : eff.level === "project"
          ? t("editor.settings.proxy.inherit.projectOff")
          : t("editor.settings.proxy.inherit.appOff");
  return (
    <>
      <p className={settingsStyles.help} data-proxy-level={eff.level} data-proxy-inherited="">
        {status}
      </p>
      <p className={settingsStyles.help}>{t("editor.settings.proxy.inherit.help")}</p>
    </>
  );
}

interface CustomProxyProps {
  proxy: RequestProxyDTO | null;
  readOnly: boolean;
  locked: string | undefined;
  udp: boolean;
  set: (patch: Partial<RequestProxyDTO>) => void;
  setProxy: (next: RequestProxyDTO | null) => void;
}

/**
 * The custom proxy: its URL as fields, or as one text field when the
 * stored URL cannot be split without losing something; then the password.
 */
function CustomProxy({ proxy, readOnly, locked, udp, set, setProxy }: CustomProxyProps) {
  const t = useT();
  const url = proxy?.url ?? "";
  const password = proxy?.password ?? "";
  const [fields, setFields] = useState<ProxyFields>(() => parseProxyUrl(url) ?? DEFAULT_PROXY_FIELDS);
  const [raw, setRaw] = useState(() => parseProxyUrl(url) === null);
  const [hostTouched, setHostTouched] = useState(false);
  // The URL the fields (or the raw field) stand for: another one came from
  // outside (undo, another window) and is read again.
  const shown = useRef(url);
  const ids = {
    type: useId(),
    host: useId(),
    port: useId(),
    user: useId(),
    password: useId(),
    url: useId(),
    hostError: useId(),
    portError: useId(),
    userError: useId(),
    passwordHint: useId(),
    passwordError: useId(),
  };

  useEffect(() => {
    if (url === shown.current) return;
    shown.current = url;
    const parsed = parseProxyUrl(url);
    if (parsed === null) {
      setRaw(true);
    } else {
      setFields(parsed);
      setRaw(false);
    }
  }, [url]);

  const change = (patch: Partial<ProxyFields>) => {
    const next = { ...fields, ...patch };
    setFields(next);
    const nextUrl = composeProxyUrl(next);
    shown.current = nextUrl;
    if (nextUrl !== url) set({ url: nextUrl });
  };

  const disabled = locked !== undefined;
  const problems = raw ? {} : proxyProblems(fields, password);
  const anything = fields.port.trim() !== "" || fields.user !== "" || password !== "";
  const hostProblem = problems.host === "hostRequired" && !hostTouched && !anything ? undefined : problems.host;
  const socks4 = fields.type === "socks4";
  const http = fields.type === "http" || fields.type === "https";
  const defaultPort = defaultProxyPort(fields.type);

  return (
    <>
      <p className={settingsStyles.help}>{t("editor.settings.proxy.custom.help")}</p>
      <div className={styles.fields} title={locked}>
        {raw ? (
          <>
            <label className={styles.label} htmlFor={ids.url}>
              {t("editor.settings.proxy.url")}
            </label>
            <div className={styles.control}>
              <input
                id={ids.url}
                className={styles.input}
                placeholder={t("editor.settings.proxy.url.placeholder")}
                spellCheck={false}
                autoComplete="off"
                value={url}
                readOnly={readOnly}
                disabled={disabled}
                onChange={(e) => {
                  shown.current = e.currentTarget.value;
                  set({ url: e.currentTarget.value });
                }}
                onBlur={(e) => {
                  // Once it can be split, it is shown as fields again.
                  const parsed = parseProxyUrl(e.currentTarget.value);
                  if (parsed !== null) {
                    setFields(parsed);
                    setRaw(false);
                  }
                }}
              />
            </div>
            <p className={settingsStyles.help}>{t("editor.settings.proxy.url.help")}</p>
          </>
        ) : (
          <>
            <label className={styles.label} htmlFor={ids.type}>
              {t("editor.settings.proxy.type")}
            </label>
            <div className={styles.control}>
              <select
                id={ids.type}
                className={settingsStyles.select}
                value={fields.type}
                disabled={readOnly || disabled}
                onChange={(e) => change({ type: e.currentTarget.value as ProxyType })}
              >
                {PROXY_TYPES.map((type) => (
                  <option key={type} value={type}>
                    {TYPE_LABELS[type]}
                  </option>
                ))}
              </select>
            </div>
            {udp && fields.type !== "socks5" && (
              <p className={settingsStyles.warning} role="note">
                <IconWarning size={13} />
                {t("editor.settings.proxy.udp")}
              </p>
            )}

            <label className={styles.label} htmlFor={ids.host}>
              {t("editor.settings.proxy.host")}
            </label>
            <div className={styles.control}>
              <input
                id={ids.host}
                className={styles.input}
                placeholder={t("editor.settings.proxy.host.placeholder")}
                spellCheck={false}
                autoComplete="off"
                value={fields.host}
                readOnly={readOnly}
                disabled={disabled}
                aria-invalid={hostProblem !== undefined ? true : undefined}
                aria-describedby={hostProblem !== undefined ? ids.hostError : undefined}
                onChange={(e) => change({ host: e.currentTarget.value })}
                onBlur={() => setHostTouched(true)}
              />
              <label className={styles.label} htmlFor={ids.port}>
                {t("editor.settings.proxy.port")}
              </label>
              <input
                id={ids.port}
                className={`${styles.input} ${styles.port}`}
                inputMode="numeric"
                placeholder={String(defaultPort)}
                spellCheck={false}
                autoComplete="off"
                value={fields.port}
                readOnly={readOnly}
                disabled={disabled}
                aria-invalid={problems.port !== undefined ? true : undefined}
                aria-describedby={problems.port !== undefined ? ids.portError : undefined}
                onChange={(e) => change({ port: e.currentTarget.value })}
              />
            </div>
            {hostProblem !== undefined && (
              <p id={ids.hostError} className={settingsStyles.error} role="alert">
                {problemText(t, hostProblem, fields.type)}
              </p>
            )}
            {problems.port !== undefined && (
              <p id={ids.portError} className={settingsStyles.error} role="alert">
                {problemText(t, problems.port, fields.type)}
              </p>
            )}

            {http ? (
              <p className={`${settingsStyles.help} ${styles.wide}`}>{t("editor.settings.proxy.httpDns")}</p>
            ) : (
              <label className={`${settingsStyles.check} ${styles.wide}`}>
                <input
                  type="checkbox"
                  checked={fields.remoteDns}
                  disabled={readOnly || disabled}
                  onChange={(e) => change({ remoteDns: e.currentTarget.checked })}
                />
                {t("editor.settings.proxy.remoteDns")}
                <span className={settingsStyles.checkHelp}>{t(remoteDnsHelp(fields))}</span>
              </label>
            )}

            <label className={styles.label} htmlFor={ids.user}>
              {socks4 ? t("editor.settings.proxy.userId") : t("editor.settings.proxy.user")}
            </label>
            <div className={styles.control}>
              <input
                id={ids.user}
                className={styles.input}
                placeholder={t("editor.settings.proxy.optional")}
                spellCheck={false}
                autoComplete="off"
                value={fields.user}
                readOnly={readOnly}
                disabled={disabled}
                aria-invalid={problems.user !== undefined ? true : undefined}
                aria-describedby={problems.user !== undefined ? ids.userError : undefined}
                onChange={(e) => change({ user: e.currentTarget.value })}
              />
            </div>
            {problems.user !== undefined && (
              <p id={ids.userError} className={settingsStyles.error} role="alert">
                {problemText(t, problems.user, fields.type)}
              </p>
            )}
          </>
        )}
        <PasswordField
          id={ids.password}
          hintId={ids.passwordHint}
          errorId={ids.passwordError}
          proxy={proxy}
          readOnly={readOnly}
          disabled={disabled}
          problem={problems.password === undefined ? undefined : problemText(t, problems.password, fields.type)}
          onChange={(value) => setProxy(withProxyPassword(proxy, value))}
        />
      </div>
    </>
  );
}

function remoteDnsHelp(fields: ProxyFields): MessageKey {
  switch (proxyScheme(fields)) {
    case "socks5h":
      return "editor.settings.proxy.remoteDns.socks5h";
    case "socks4a":
      return "editor.settings.proxy.remoteDns.socks4a";
    case "socks4":
      return "editor.settings.proxy.remoteDns.socks4";
    default:
      return "editor.settings.proxy.remoteDns.socks5";
  }
}

/**
 * The proxy's password: masked, or shown with the eye. A typed value
 * starts hidden; an empty one or a {{variable}} starts shown. The hint
 * says where it is kept, as the auth editor's secrets do.
 */
function PasswordField({
  id,
  hintId,
  errorId,
  proxy,
  readOnly,
  disabled,
  problem,
  onChange,
}: {
  id: string;
  hintId: string;
  errorId: string;
  proxy: RequestProxyDTO | null;
  readOnly: boolean;
  disabled: boolean;
  /** The message of a problem with the password, if any. */
  problem: string | undefined;
  onChange: (value: string) => void;
}) {
  const t = useT();
  const value = proxy?.password ?? "";
  const storage = proxyPasswordStorage(proxy);
  const [shown, setShown] = useState(storage !== "local");
  const toggle = shown ? t("editor.settings.proxy.password.hide") : t("editor.settings.proxy.password.show");
  const variable = <code>{"{{variable}}"}</code>;
  return (
    <>
      <label className={styles.label} htmlFor={id}>
        {t("editor.settings.proxy.password")}
      </label>
      <div className={styles.control}>
        <input
          id={id}
          className={styles.input}
          type={shown ? "text" : "password"}
          placeholder={t("editor.settings.proxy.optional")}
          spellCheck={false}
          autoComplete="off"
          value={value}
          readOnly={readOnly}
          disabled={disabled}
          aria-invalid={problem !== undefined ? true : undefined}
          aria-describedby={problem !== undefined ? `${errorId} ${hintId}` : hintId}
          onChange={(e) => onChange(e.currentTarget.value)}
        />
        <button
          type="button"
          className={styles.reveal}
          aria-label={toggle}
          aria-pressed={shown}
          title={toggle}
          onClick={() => setShown((s) => !s)}
        >
          {shown ? <IconEyeOff size={13} /> : <IconEye size={13} />}
        </button>
      </div>
      {problem !== undefined && (
        <p id={errorId} className={settingsStyles.error} role="alert">
          {problem}
        </p>
      )}
      <p id={hintId} className={styles.hint} data-storage={storage}>
        {storage === "missing" ? <IconWarning size={12} /> : storage === "local" ? <IconLock size={12} /> : <IconInfo size={12} />}
        <span>
          {storage === "missing" && <Trans k="editor.settings.proxy.password.missing" values={{ variable }} />}
          {storage === "local" && <Trans k="editor.settings.proxy.password.local" values={{ variable }} />}
          {storage === "reference" && t("editor.settings.proxy.password.reference")}
          {storage === "empty" && <Trans k="editor.settings.proxy.password.empty" values={{ variable }} />}
        </span>
      </p>
    </>
  );
}

/** A short form of the request's proxy for the fold's summary: the mode, or the custom URL. */
export function proxySummary(settings: RequestSettingsDTO, t: Translate): string {
  const proxy = settings.proxy ?? null;
  const mode = requestProxyModeOf(proxy);
  if (mode === "custom" && (proxy?.url ?? "") !== "") return proxy?.url ?? "";
  if (mode === null) return proxy?.mode ?? "";
  return t(MODE_LABELS[mode]);
}

/**
 * The section folded under the options of the TCP and UDP editors, which
 * have no Settings tab, with the proxy in use in its summary.
 */
export function RequestProxyFold(props: Omit<RequestProxySettingsProps, "noHeading">) {
  const t = useT();
  return (
    <details className={styles.fold} data-proxy-fold="">
      <summary className={styles.foldSummary}>
        {t("editor.settings.proxy")}
        <span className={styles.foldValue}>{proxySummary(props.settings, t)}</span>
      </summary>
      <div className={styles.foldBody}>
        <RequestProxySettings {...props} noHeading />
      </div>
    </details>
  );
}
