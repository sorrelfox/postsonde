import { useEffect, useId, useRef, useState, type ReactNode } from "react";
import { formatError, isCancelled, toAppError } from "../../lib/errors";
import { splitBypassList } from "../../lib/proxyScopes";
import {
  getProxy,
  onSettingsChanged,
  setProxy,
  setProxyBypass,
  testProxy,
  type ProxyDTO,
  type ProxyMode,
  type ProxyTestDTO,
} from "../../lib/settings";
import { formatNumber, type MessageKey } from "../../i18n";
import { useT } from "../../i18n/react";
import { IconCheck, IconWarning } from "../common/icons";
import { LockBadge, RowControl, useSettingLocked } from "./Locked";
import styles from "./ProxySettings.module.css";
import screen from "./SettingsScreen.module.css";

/**
 * The proxy every connection goes through (SOCKS-03, SOCKS-04, NET-02;
 * network.proxyMode and network.proxyUrl): the one in the ALL_PROXY
 * environment variable (the default), a manual one, or none. Go checks the
 * URL before saving it and keeps the manual proxy's password in its local
 * secret storage: it never comes back here, only whether one is stored.
 * The rows follow hand edits of the settings file (settings:changed).
 */

const MODES: readonly { value: ProxyMode; labelKey: MessageKey }[] = [
  { value: "system", labelKey: "settings.proxy.mode.system" },
  { value: "env", labelKey: "settings.proxy.mode.env" },
  { value: "manual", labelKey: "settings.proxy.mode.manual" },
  { value: "off", labelKey: "settings.proxy.mode.off" },
];

/** Which row a save error belongs to, from the error's `field` detail. */
type Field = "mode" | "url" | "password" | "bypass";

/**
 * The app's proxy rows. `showTest` adds "Test connection" while the app's
 * proxy is in effect; the settings screen shows it with the project's
 * scopes instead (ProxyScopes.tsx), where the proxy in effect is known.
 */
export function ProxyRows({ showTest = true }: { showTest?: boolean } = {}) {
  const t = useT();
  const [proxy, setProxyState] = useState<ProxyDTO | null>(null);
  const [urlDraft, setUrlDraft] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<{ field: Field; message: string } | null>(null);
  const editingUrl = useRef(false);
  const queue = useRef<Promise<void>>(Promise.resolve());
  const [bypassDraft, setBypassDraft] = useState("");
  const editingBypass = useRef(false);
  const ids = {
    url: useId(),
    password: useId(),
    bypass: useId(),
    modeError: useId(),
    urlError: useId(),
    passwordError: useId(),
    bypassError: useId(),
  };

  const show = (next: ProxyDTO) => {
    setProxyState(next);
    if (!editingUrl.current) setUrlDraft(next.url);
    if (!editingBypass.current) setBypassDraft((next.bypass ?? []).join("\n"));
  };

  useEffect(() => {
    let live = true;
    const refresh = () => {
      getProxy().then(
        (next) => {
          if (live) show(next);
        },
        () => {
          // No Go backend (the browser, tests without it): nothing to show.
        },
      );
    };
    refresh();
    const off = onSettingsChanged(refresh);
    return () => {
      live = false;
      off();
    };
  }, []);

  // Saves run one after the other: a field's blur and a button's click can
  // both save within one render, and the second must see the first's result.
  const save = (mode: ProxyMode, url: string, pass: string | null, field: Field) => {
    setError(null);
    queue.current = queue.current.then(() =>
      setProxy({ mode, url, password: pass }).then(
        (next) => {
          setPassword("");
          setUrlDraft(next.url);
          show(next);
        },
        (err: unknown) => {
          if (isCancelled(err)) return;
          const e = toAppError(err);
          const at = e.details.field;
          setError({ field: at === "url" || at === "password" || at === "mode" ? at : field, message: formatError(e) });
        },
      ),
    );
  };

  const mode = (proxy?.mode ?? "") as ProxyMode | "";
  const current = proxy?.url ?? "";
  const commitUrl = (text: string) => {
    if (mode === "" || text.trim() === current) return;
    save(mode, text, null, "url");
  };
  const commitPassword = (text: string) => {
    if (mode === "" || text === "") return;
    save(mode, urlDraft, text, "password");
  };
  const commitBypass = (text: string) => {
    const entries = splitBypassList(text);
    if (proxy === null || entries.join("\n") === (proxy.bypass ?? []).join("\n")) return;
    setError(null);
    queue.current = queue.current.then(() =>
      setProxyBypass(entries).then(
        (next) => {
          setBypassDraft((next.bypass ?? []).join("\n"));
          show(next);
        },
        (err: unknown) => {
          if (isCancelled(err)) return;
          setError({ field: "bypass", message: formatError(toAppError(err)) });
        },
      ),
    );
  };
  const errorOf = (field: Field) => (error?.field === field ? error.message : null);

  return (
    <>
      <Row
        label={t("settings.proxy.label")}
        setting="network.proxyMode"
        description={proxy ? <ProxyStatus proxy={proxy} /> : t("settings.proxy.description")}
        error={errorOf("mode")}
        errorId={ids.modeError}
      >
        <div
          className={screen.segmented}
          role="group"
          aria-label={t("settings.proxy.group")}
          aria-describedby={errorOf("mode") ? ids.modeError : undefined}
        >
          {MODES.map((m) => (
            <button
              key={m.value}
              type="button"
              className={screen.segment}
              aria-pressed={mode === m.value}
              disabled={proxy === null}
              onClick={() => {
                if (m.value !== mode) save(m.value, urlDraft, null, "mode");
              }}
            >
              {t(m.labelKey)}
            </button>
          ))}
        </div>
      </Row>
      {mode === "manual" && (
        <>
          <Row
            label={t("settings.proxy.url.label")}
            setting="network.proxyUrl"
            controlId={ids.url}
            description={t("settings.proxy.url.description")}
            error={errorOf("url")}
            errorId={ids.urlError}
          >
            <input
              id={ids.url}
              className={`${screen.input} ${styles.url}`}
              placeholder={t("settings.proxy.url.placeholder")}
              spellCheck={false}
              autoComplete="off"
              value={urlDraft}
              aria-invalid={errorOf("url") ? true : undefined}
              aria-describedby={errorOf("url") ? ids.urlError : undefined}
              onFocus={() => {
                editingUrl.current = true;
              }}
              onBlur={(e) => {
                editingUrl.current = false;
                commitUrl(e.currentTarget.value);
              }}
              onKeyDown={(e) => {
                if (e.key === "Enter") commitUrl(e.currentTarget.value);
              }}
              onChange={(e) => setUrlDraft(e.currentTarget.value)}
            />
          </Row>
          <Row
            label={t("settings.proxy.password.label")}
            setting="network.proxyUrl"
            controlId={ids.password}
            description={t("settings.proxy.password.description")}
            error={errorOf("password")}
            errorId={ids.passwordError}
          >
            <input
              id={ids.password}
              type="password"
              className={`${screen.input} ${styles.password}`}
              placeholder={proxy?.hasPassword ? t("settings.proxy.password.stored") : t("settings.proxy.password.none")}
              autoComplete="off"
              value={password}
              aria-invalid={errorOf("password") ? true : undefined}
              aria-describedby={errorOf("password") ? ids.passwordError : undefined}
              onBlur={(e) => commitPassword(e.currentTarget.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") commitPassword(e.currentTarget.value);
              }}
              onChange={(e) => setPassword(e.currentTarget.value)}
            />
            {proxy?.hasPassword && (
              <button
                type="button"
                className={screen.smallButton}
                onClick={() => save("manual", urlDraft, "", "password")}
              >
                {t("settings.proxy.password.remove")}
              </button>
            )}
          </Row>
        </>
      )}
      <Row
        label={t("settings.proxy.bypass.label")}
        setting="network.proxyBypass"
        controlId={ids.bypass}
        description={
          <span className={styles.status}>
            <span>{t("settings.proxy.bypass.description")}</span>
            {mode === "env" && proxy?.noProxyVar && (
              <span>{t("settings.proxy.bypass.noProxy", { name: proxy.noProxyVar, value: proxy.noProxy })}</span>
            )}
          </span>
        }
        error={errorOf("bypass")}
        errorId={ids.bypassError}
      >
        <textarea
          id={ids.bypass}
          className={`${screen.input} ${styles.bypass}`}
          placeholder={t("settings.proxy.bypass.placeholder")}
          spellCheck={false}
          autoComplete="off"
          rows={3}
          value={bypassDraft}
          disabled={proxy === null}
          aria-invalid={errorOf("bypass") ? true : undefined}
          aria-describedby={errorOf("bypass") ? ids.bypassError : undefined}
          onFocus={() => {
            editingBypass.current = true;
          }}
          onBlur={(e) => {
            editingBypass.current = false;
            commitBypass(e.currentTarget.value);
          }}
          onChange={(e) => setBypassDraft(e.currentTarget.value)}
        />
      </Row>
      {showTest && proxy !== null && (proxy.effective !== "" || (proxy.effectiveHttp ?? "") !== "") && <ProxyTestRow />}
    </>
  );
}

/** Where a failed test stopped (ProxyTestDTO.step), as the end of a sentence. */
const STEP_KEYS: Readonly<Record<string, MessageKey>> = {
  setup: "settings.proxy.test.step.setup",
  dns: "settings.proxy.test.step.dns",
  connect: "settings.proxy.test.step.connect",
  tls: "settings.proxy.test.step.tls",
  handshake: "settings.proxy.test.step.handshake",
  auth: "settings.proxy.test.step.auth",
  target: "settings.proxy.test.step.target",
  timeout: "settings.proxy.test.step.timeout",
  cancelled: "settings.proxy.test.step.cancelled",
};

/** The proxy's authentication methods (netx.Auth*), by Go name. */
const AUTH_KEYS: Readonly<Record<string, MessageKey>> = {
  none: "settings.proxy.test.auth.none",
  "username/password": "settings.proxy.test.auth.userpass",
  basic: "settings.proxy.test.auth.basic",
};

/**
 * "Test connection" (SOCKS-11): Go connects to a host:port the user
 * chooses through the proxy in effect, then says how the hop went (the
 * proxy's address, protocol, authentication, DNS mode, reply and timings)
 * or at which step it failed and why.
 */
export function ProxyTestRow() {
  const t = useT();
  const resultId = useId();
  const [target, setTarget] = useState("example.com:443");
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<ProxyTestDTO | null>(null);
  const [failure, setFailure] = useState<string | null>(null);
  const live = useRef(true);
  useEffect(
    () => () => {
      live.current = false;
    },
    [],
  );

  const run = () => {
    if (busy) return;
    setBusy(true);
    setFailure(null);
    testProxy(target).then(
      (res) => {
        if (!live.current) return;
        setBusy(false);
        setResult(res);
      },
      (err: unknown) => {
        if (!live.current) return;
        setBusy(false);
        setResult(null);
        if (!isCancelled(err)) setFailure(formatError(toAppError(err)));
      },
    );
  };

  const ms = (value: number) => t("settings.proxy.test.ms", { ms: formatNumber(value, { maximumFractionDigits: value < 10 ? 2 : 0 }) });
  return (
    <div className={screen.row}>
      <div className={screen.rowText}>
        <span className={screen.rowLabel}>{t("settings.proxy.test.label")}</span>
        <span className={screen.rowDescription}>{t("settings.proxy.test.description")}</span>
        <div id={resultId} className={styles.testResult} role="status" aria-live="polite">
          {failure !== null && (
            <span className={screen.rowError}>
              <IconWarning size={13} />
              {failure}
            </span>
          )}
          {result !== null && result.ok && (
            <span className={styles.testOk} data-proxy-test="ok">
              <IconCheck size={13} />
              {t("settings.proxy.test.ok", { target: result.target, proxy: result.proxy, time: ms(result.durationMs) })}
            </span>
          )}
          {result !== null && !result.ok && (
            <span className={screen.rowError} data-proxy-test={result.step}>
              <IconWarning size={13} />
              {t("settings.proxy.test.failed", {
                step: STEP_KEYS[result.step] !== undefined ? t(STEP_KEYS[result.step]) : t("settings.proxy.test.step.other"),
              })}
            </span>
          )}
          {result !== null && result.error !== null && <span className={styles.testError}>{formatError(result.error)}</span>}
          {result?.bypassed && <span className={styles.testError}>{t("settings.proxy.test.bypassed", { target: result.target })}</span>}
          {result?.hop && (
            <dl className={styles.testHop} aria-label={t("settings.proxy.test.hop")}>
              <dt>{t("settings.proxy.test.hop.proxy")}</dt>
              <dd>{result.hop.proxyAddr !== "" && result.hop.proxyAddr !== result.hop.proxy ? `${result.hop.proxy} (${result.hop.proxyAddr})` : result.hop.proxy}</dd>
              <dt>{t("settings.proxy.test.hop.version")}</dt>
              <dd>{result.hop.version}</dd>
              <dt>{t("settings.proxy.test.hop.auth")}</dt>
              <dd>{AUTH_KEYS[result.hop.auth] !== undefined ? t(AUTH_KEYS[result.hop.auth]) : result.hop.auth || t("settings.proxy.test.auth.notChosen")}</dd>
              <dt>{t("settings.proxy.test.hop.dns")}</dt>
              <dd>{result.hop.remoteDns ? t("settings.proxy.test.dns.remote") : t("settings.proxy.test.dns.local")}</dd>
              <dt>{t("settings.proxy.test.hop.reply")}</dt>
              <dd>{result.hop.reply === null ? t("settings.proxy.test.reply.none") : replyText(result.hop.reply, result.hop.replyText)}</dd>
              <dt>{t("settings.proxy.test.hop.timings")}</dt>
              <dd>{hopTimings(result.hop, ms, t).join(", ")}</dd>
            </dl>
          )}
        </div>
      </div>
      <div className={screen.rowControl}>
        <input
          className={`${screen.input} ${styles.target}`}
          aria-label={t("settings.proxy.test.target")}
          placeholder={t("settings.proxy.test.placeholder")}
          spellCheck={false}
          autoComplete="off"
          value={target}
          aria-describedby={resultId}
          onChange={(e) => setTarget(e.currentTarget.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") run();
          }}
        />
        <button type="button" className={screen.smallButton} disabled={busy} aria-busy={busy || undefined} onClick={run}>
          {busy ? t("settings.proxy.test.running") : t("settings.proxy.test.run")}
        </button>
      </div>
    </div>
  );
}

/** "0 succeeded" (SOCKS), "200 Connection established" (HTTP, whose text starts with the code). */
function replyText(reply: number, text: string): string {
  const code = String(reply);
  if (text === "" || text === code) return code;
  return text.startsWith(`${code} `) ? text : `${code} ${text}`;
}

/** The hop's timed steps: "lookup 1.2 ms", "connect 3 ms", "TLS 5 ms", "handshake 0.8 ms". */
function hopTimings(hop: NonNullable<ProxyTestDTO["hop"]>, ms: (value: number) => string, t: ReturnType<typeof useT>): string[] {
  const out: string[] = [];
  if (hop.lookupMs > 0) out.push(t("settings.proxy.test.timing.lookup", { time: ms(hop.lookupMs) }));
  if (hop.connectMs > 0) out.push(t("settings.proxy.test.timing.connect", { time: ms(hop.connectMs) }));
  if (hop.tlsMs > 0) out.push(t("settings.proxy.test.timing.tls", { time: ms(hop.tlsMs) }));
  if (hop.handshakeMs > 0) out.push(t("settings.proxy.test.timing.handshake", { time: ms(hop.handshakeMs) }));
  return out;
}

/** What connections do now: the proxy and its DNS mode, direct, or why they fail. */
function ProxyStatus({ proxy }: { proxy: ProxyDTO }) {
  const t = useT();
  if (proxy.mode === "system") return <SystemProxyStatus proxy={proxy} />;
  let status: string;
  if (proxy.effective) {
    status =
      proxy.mode === "env"
        ? t("settings.proxy.status.fromEnv", { name: proxy.envVar, proxy: proxy.effective })
        : t("settings.proxy.status.manual", { proxy: proxy.effective });
  } else if (proxy.mode === "off") {
    status = proxy.envVar ? t("settings.proxy.status.offIgnoresEnv", { name: proxy.envVar }) : t("settings.proxy.status.off");
  } else if (proxy.mode === "env" && !proxy.envVar) {
    status = t("settings.proxy.status.envUnset");
  } else {
    status = t("settings.proxy.status.failing");
  }
  return (
    <span className={styles.status}>
      <span>{status}</span>
      {proxy.effective && <span>{t(dnsKey(proxy))}</span>}
      {proxy.effective && <span>{t("settings.proxy.loopback")}</span>}
      {proxy.error && (
        <span className={screen.rowWarning} role="note">
          <IconWarning size={13} />
          {formatError(proxy.error)}
        </span>
      )}
    </span>
  );
}

/** How host names are resolved through the proxy in effect: by an HTTP proxy, by a socks5h or socks4a one, or here. */
function dnsKey(proxy: ProxyDTO): MessageKey {
  return dnsKeyOf(proxy.effective || (proxy.effectiveHttp ?? ""), proxy.remoteDns);
}

/** Where the OS proxy settings were read (SystemProxyDTO.source). */
const SYSTEM_SOURCE_KEYS: Readonly<Record<string, MessageKey>> = {
  macos: "settings.proxy.system.source.macos",
  windows: "settings.proxy.system.source.windows",
  gnome: "settings.proxy.system.source.gnome",
  env: "settings.proxy.system.source.env",
};

/**
 * The "system" mode (NET-01): which OS proxy each kind of connection uses
 * (http:// requests the web proxy, everything else the secure web or SOCKS
 * one), the OS's bypass list, and what PostSonde cannot use (a PAC file,
 * WPAD, bypass entries it does not understand).
 */
function SystemProxyStatus({ proxy }: { proxy: ProxyDTO }) {
  const t = useT();
  const sys = proxy.system;
  const source = sys && SYSTEM_SOURCE_KEYS[sys.source] !== undefined ? t(SYSTEM_SOURCE_KEYS[sys.source]) : t("settings.proxy.system.source.unknown");
  const all = proxy.effective;
  const http = proxy.effectiveHttp ?? "";
  let status: string;
  if (proxy.error) {
    status = t("settings.proxy.status.failing");
  } else if (all !== "" && (http === "" || http === all)) {
    status =
      http === ""
        ? t("settings.proxy.system.allButHttp", { source, proxy: all })
        : t("settings.proxy.system.all", { source, proxy: all });
  } else if (all !== "") {
    status = t("settings.proxy.system.split", { source, proxy: all, http });
  } else if (http !== "") {
    status = t("settings.proxy.system.httpOnly", { source, http });
  } else {
    status = t("settings.proxy.system.none", { source });
  }
  const bypass = sys?.bypass ?? [];
  const skipped = sys?.skipped ?? [];
  const inUse = !proxy.error && (all !== "" || http !== "");
  return (
    <span className={styles.status}>
      <span>{status}</span>
      {inUse && <span>{t(dnsKey(proxy))}</span>}
      {inUse && bypass.length > 0 && <span>{t("settings.proxy.system.bypass", { list: bypass.join(", ") })}</span>}
      {inUse && <span>{t("settings.proxy.loopback")}</span>}
      {sys?.autoDiscovery && <span>{t("settings.proxy.system.wpad")}</span>}
      {skipped.length > 0 && <span>{t("settings.proxy.system.skipped", { list: skipped.join(", ") })}</span>}
      {proxy.error && (
        <span className={screen.rowWarning} role="note">
          <IconWarning size={13} />
          {formatError(proxy.error)}
        </span>
      )}
    </span>
  );
}

/** How host names are resolved through a proxy, from its URL and whether it resolves them itself. */
export function dnsKeyOf(proxyUrl: string, remoteDns: boolean): MessageKey {
  if (/^https?:/i.test(proxyUrl)) return "settings.proxy.dns.http";
  if (/^socks4a?:/i.test(proxyUrl)) return remoteDns ? "settings.proxy.dns.remote4" : "settings.proxy.dns.local4";
  return remoteDns ? "settings.proxy.dns.remote" : "settings.proxy.dns.local";
}

/** A settings row, as SettingsScreen's (kept local to avoid an import cycle). */
export function Row({
  label,
  controlId,
  description,
  error,
  errorId,
  setting,
  children,
}: {
  label: string;
  controlId?: string;
  description?: ReactNode;
  error?: string | null;
  errorId?: string;
  /** The settings.json key; locked by the admin policy (SET-02), the row shows a lock and its controls are disabled. */
  setting: string;
  children: ReactNode;
}) {
  const locked = useSettingLocked(setting);
  return (
    <div className={screen.row} data-invalid={error ? "true" : undefined} data-locked={locked ? "true" : undefined}>
      <div className={screen.rowText}>
        {controlId ? (
          <label className={screen.rowLabel} htmlFor={controlId}>
            {label}
            {locked && <LockBadge />}
          </label>
        ) : (
          <span className={screen.rowLabel}>
            {label}
            {locked && <LockBadge />}
          </span>
        )}
        {description && <span className={screen.rowDescription}>{description}</span>}
        {error && (
          <span id={errorId} className={screen.rowError} role="alert">
            <IconWarning size={13} />
            {error}
          </span>
        )}
      </div>
      <RowControl locked={locked}>{children}</RowControl>
    </div>
  );
}
