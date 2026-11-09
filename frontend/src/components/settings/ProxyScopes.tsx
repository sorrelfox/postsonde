import { useEffect, useId, useRef, useState, type ReactNode } from "react";
import { formatError, isCancelled, toAppError } from "../../lib/errors";
import {
  getProxyScopes,
  onProxyScopesInvalidated,
  setProxyScope,
  splitBypassList,
  type ScopeDTO,
  type ScopeMode,
  type ScopesDTO,
} from "../../lib/proxyScopes";
import type { MessageKey } from "../../i18n";
import { useT } from "../../i18n/react";
import { IconWarning } from "../common/icons";
import { dnsKeyOf, ProxyRows, ProxyTestRow } from "./ProxySettings";
import styles from "./ProxySettings.module.css";
import screen from "./SettingsScreen.module.css";

/**
 * The proxy section of the settings screen: the app's proxy (ProxyRows),
 * then the proxy in effect now with the scope it comes from, the
 * connection test, and the proxies of the open project and of each of its
 * environments (SOCKS-08). The most specific scope wins: the active
 * environment, then the project, then the app. Scopes are kept on this
 * computer, never in project files; their passwords stay in Go.
 */
export function ProxySection() {
  return (
    <>
      <ProxyRows showTest={false} />
      <ProxyScopeRows />
    </>
  );
}

/** The proxy in effect, the test and the scope editors. */
export function ProxyScopeRows() {
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
          // No Go backend (the browser, tests without it): nothing to show.
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

  if (scopes === null) return null;
  const environments = scopes.environments ?? [];
  const active = environments.find((e) => e.active);
  return (
    <>
      <EffectiveRow scopes={scopes} activeName={active?.name ?? ""} />
      {(scopes.effective.proxy !== "" || (scopes.effective.httpProxy ?? "") !== "") && <ProxyTestRow />}
      {!scopes.open ? (
        <div className={screen.row}>
          <div className={screen.rowText}>
            <span className={screen.rowLabel}>{t("settings.proxyScopes.label")}</span>
            <span className={screen.rowDescription}>{t("settings.proxyScopes.noProject")}</span>
          </div>
        </div>
      ) : (
        <>
          {scopes.error && (
            <div className={screen.row}>
              <span className={screen.rowWarning} role="note">
                <IconWarning size={13} />
                {formatError(scopes.error)}
              </span>
            </div>
          )}
          <ScopeEditor
            label={t("settings.proxyScopes.project.label")}
            description={t("settings.proxyScopes.project.description")}
            inheritKey="settings.proxyScopes.mode.inheritApp"
            scope={scopes.project}
            save={(input) => setProxyScope({ level: "project", environmentId: "", ...input })}
            onSaved={setScopes}
          />
          {environments.map((env) => (
            <ScopeEditor
              key={env.id}
              label={env.active ? t("settings.proxyScopes.environment.activeLabel", { name: env.name }) : env.name}
              description={t("settings.proxyScopes.environment.description")}
              inheritKey="settings.proxyScopes.mode.inheritProject"
              scope={env.scope}
              save={(input) => setProxyScope({ level: "environment", environmentId: env.id, ...input })}
              onSaved={setScopes}
            />
          ))}
        </>
      )}
    </>
  );
}

/** What connections do now, and which scope decided it. */
function EffectiveRow({ scopes, activeName }: { scopes: ScopesDTO; activeName: string }) {
  const t = useT();
  const eff = { ...scopes.effective, httpProxy: scopes.effective.httpProxy ?? "" };
  const bypass = eff.bypass ?? [];
  const inUse = !eff.error && (eff.proxy !== "" || eff.httpProxy !== "");
  let status: string;
  if (eff.error) {
    status = t("settings.proxy.status.failing");
  } else if (eff.httpSplit && eff.proxy !== eff.httpProxy && eff.httpProxy !== "") {
    status = t("settings.proxyScopes.effective.system", { proxy: eff.proxy || t("settings.proxyScopes.effective.direct"), http: eff.httpProxy });
  } else if (eff.proxy !== "") {
    status =
      eff.level === "environment"
        ? t("settings.proxyScopes.effective.environment", { proxy: eff.proxy, name: activeName })
        : eff.level === "project"
          ? t("settings.proxyScopes.effective.project", { proxy: eff.proxy })
          : t("settings.proxyScopes.effective.app", { proxy: eff.proxy });
  } else {
    status =
      eff.level === "environment"
        ? t("settings.proxyScopes.effective.environmentOff", { name: activeName })
        : eff.level === "project"
          ? t("settings.proxyScopes.effective.projectOff")
          : t("settings.proxyScopes.effective.appOff");
  }
  return (
    <div className={screen.row} data-proxy-level={eff.level}>
      <div className={screen.rowText}>
        <span className={screen.rowLabel}>{t("settings.proxyScopes.effective.label")}</span>
        <span className={screen.rowDescription}>
          <span className={styles.status}>
            <span>{status}</span>
            {inUse && <span>{t(dnsKeyOf(eff.proxy || eff.httpProxy, eff.remoteDns))}</span>}
            {inUse && bypass.length > 0 && (
              <span>{t("settings.proxyScopes.effective.bypass", { list: bypass.join(", ") })}</span>
            )}
            {eff.error && (
              <span className={screen.rowWarning} role="note">
                <IconWarning size={13} />
                {formatError(eff.error)}
              </span>
            )}
          </span>
        </span>
      </div>
    </div>
  );
}

const SCOPE_MODES: readonly ScopeMode[] = ["inherit", "manual", "off"];

type ScopeFieldName = "mode" | "url" | "password" | "bypass";

type ScopeChange = { mode: ScopeMode; url: string; password: string | null; bypass: string[] };

/**
 * One scope: inherit, a proxy of its own (URL, password, bypass list) or
 * no proxy. Saves go through `save` one after the other; Go's reason is
 * shown under the field it is about.
 */
function ScopeEditor({
  label,
  description,
  inheritKey,
  scope,
  save,
  onSaved,
}: {
  label: string;
  description: string;
  inheritKey: MessageKey;
  scope: ScopeDTO;
  save: (input: ScopeChange) => Promise<ScopesDTO>;
  onSaved: (next: ScopesDTO) => void;
}) {
  const t = useT();
  const scopeBypass = (scope.bypass ?? []).join("\n");
  const [urlDraft, setUrlDraft] = useState(scope.url);
  const [bypassDraft, setBypassDraft] = useState(scopeBypass);
  const [password, setPassword] = useState("");
  const [error, setError] = useState<{ field: ScopeFieldName; message: string } | null>(null);
  const editing = useRef<"url" | "bypass" | null>(null);
  const queue = useRef<Promise<void>>(Promise.resolve());
  const ids = {
    group: useId(),
    url: useId(),
    password: useId(),
    bypass: useId(),
    error: useId(),
    urlError: useId(),
    passwordError: useId(),
    bypassError: useId(),
  };

  useEffect(() => {
    if (editing.current !== "url") setUrlDraft(scope.url);
    if (editing.current !== "bypass") setBypassDraft(scopeBypass);
  }, [scope.url, scopeBypass]);

  const mode = (SCOPE_MODES.includes(scope.mode as ScopeMode) ? scope.mode : "inherit") as ScopeMode;
  const commit = (change: Partial<ScopeChange>, field: ScopeFieldName) => {
    setError(null);
    const input: ScopeChange = { mode, url: urlDraft, password: null, bypass: splitBypassList(bypassDraft), ...change };
    queue.current = queue.current.then(() =>
      save(input).then(
        (next) => {
          setPassword("");
          onSaved(next);
        },
        (err: unknown) => {
          if (isCancelled(err)) return;
          const e = toAppError(err);
          const at = e.details.field;
          setError({
            field: at === "url" || at === "password" || at === "bypass" || at === "mode" ? at : field,
            message: formatError(e),
          });
        },
      ),
    );
  };
  const errorOf = (field: ScopeFieldName) => (error?.field === field ? error.message : null);
  const modeLabel = (m: ScopeMode) =>
    m === "inherit" ? t(inheritKey) : m === "manual" ? t("settings.proxyScopes.mode.manual") : t("settings.proxyScopes.mode.off");

  return (
    <div className={`${screen.row} ${styles.scope}`} data-invalid={error ? "true" : undefined}>
      <div className={screen.rowText}>
        <span className={screen.rowLabel} id={ids.group}>
          {label}
        </span>
        <span className={screen.rowDescription}>{description}</span>
        {errorOf("mode") && (
          <span id={ids.error} className={screen.rowError} role="alert">
            <IconWarning size={13} />
            {errorOf("mode")}
          </span>
        )}
      </div>
      <div className={screen.rowControl}>
        <div
          className={screen.segmented}
          role="group"
          aria-labelledby={ids.group}
          aria-describedby={errorOf("mode") ? ids.error : undefined}
        >
          {SCOPE_MODES.map((m) => (
            <button
              key={m}
              type="button"
              className={screen.segment}
              aria-pressed={mode === m}
              onClick={() => {
                if (m !== mode) commit({ mode: m }, "mode");
              }}
            >
              {modeLabel(m)}
            </button>
          ))}
        </div>
      </div>
      {mode === "manual" && (
        <div className={styles.scopeFields}>
          <ScopeField label={t("settings.proxy.url.label")} id={ids.url} error={errorOf("url")} errorId={ids.urlError}>
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
                editing.current = "url";
              }}
              onBlur={(e) => {
                editing.current = null;
                if (e.currentTarget.value.trim() !== scope.url) commit({ url: e.currentTarget.value }, "url");
              }}
              onKeyDown={(e) => {
                if (e.key === "Enter" && e.currentTarget.value.trim() !== scope.url) commit({ url: e.currentTarget.value }, "url");
              }}
              onChange={(e) => setUrlDraft(e.currentTarget.value)}
            />
          </ScopeField>
          <ScopeField label={t("settings.proxy.password.label")} id={ids.password} error={errorOf("password")} errorId={ids.passwordError}>
            <input
              id={ids.password}
              type="password"
              className={`${screen.input} ${styles.password}`}
              placeholder={scope.hasPassword ? t("settings.proxy.password.stored") : t("settings.proxy.password.none")}
              autoComplete="off"
              value={password}
              aria-invalid={errorOf("password") ? true : undefined}
              aria-describedby={errorOf("password") ? ids.passwordError : undefined}
              onBlur={(e) => {
                if (e.currentTarget.value !== "") commit({ password: e.currentTarget.value }, "password");
              }}
              onKeyDown={(e) => {
                if (e.key === "Enter" && e.currentTarget.value !== "") commit({ password: e.currentTarget.value }, "password");
              }}
              onChange={(e) => setPassword(e.currentTarget.value)}
            />
            {scope.hasPassword && (
              <button type="button" className={screen.smallButton} onClick={() => commit({ password: "" }, "password")}>
                {t("settings.proxy.password.remove")}
              </button>
            )}
          </ScopeField>
          <ScopeField label={t("settings.proxyScopes.bypass.label")} id={ids.bypass} error={errorOf("bypass")} errorId={ids.bypassError}>
            <textarea
              id={ids.bypass}
              className={`${screen.input} ${styles.bypass}`}
              placeholder={t("settings.proxy.bypass.placeholder")}
              spellCheck={false}
              autoComplete="off"
              rows={2}
              value={bypassDraft}
              aria-invalid={errorOf("bypass") ? true : undefined}
              aria-describedby={errorOf("bypass") ? ids.bypassError : undefined}
              onFocus={() => {
                editing.current = "bypass";
              }}
              onBlur={(e) => {
                editing.current = null;
                const entries = splitBypassList(e.currentTarget.value);
                if (entries.join("\n") !== scopeBypass) commit({ bypass: entries }, "bypass");
              }}
              onChange={(e) => setBypassDraft(e.currentTarget.value)}
            />
          </ScopeField>
        </div>
      )}
    </div>
  );
}

/** A labelled field of a scope, with Go's reason under it. */
function ScopeField({
  label,
  id,
  error,
  errorId,
  children,
}: {
  label: string;
  id: string;
  error: string | null;
  errorId: string;
  children: ReactNode;
}) {
  return (
    <div className={styles.scopeField}>
      <label className={styles.scopeFieldLabel} htmlFor={id}>
        {label}
      </label>
      <div className={styles.scopeFieldControl}>{children}</div>
      {error && (
        <span id={errorId} className={screen.rowError} role="alert">
          <IconWarning size={13} />
          {error}
        </span>
      )}
    </div>
  );
}
