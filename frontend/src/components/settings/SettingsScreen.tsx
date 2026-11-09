import {
  useCallback,
  useEffect,
  useId,
  useRef,
  useState,
  useSyncExternalStore,
  type ComponentType,
  type ReactNode,
} from "react";
import {
  applySetting,
  getSettingsProblems,
  loadSettingsLocation,
  openSettingsFile,
  revealSettingsFile,
  subscribeSettingsProblems,
  type SettingsLocation,
} from "../../commands/settings";
import {
  clearRecentWorkspaces,
  forgetRecentWorkspace,
  openWorkspaceAt,
  refreshRecentWorkspaces,
  revealWorkspace,
} from "../../commands/workspace";
import { formatError, isCancelled, toAppError, type AppError } from "../../lib/errors";
import { LOCALES, resolveLocale, t as translate, type MessageKey } from "../../i18n";
import { useT } from "../../i18n/react";
import {
  addCABundle,
  listCABundles,
  onSettingsChanged,
  removeCABundle,
  settingFileKey,
  type CABundleDTO,
} from "../../lib/settings";
import { folderName } from "../../lib/workspace";
import {
  shallowEqual,
  useSettings,
  useStores,
  useWorkspace,
  LOCALE_SYSTEM,
  type ResponsePosition,
  type SettingsState,
  type Theme,
} from "../../stores";
import {
  IconCheck,
  IconCode,
  IconGlobe,
  IconKeyboard,
  IconLock,
  IconShield,
  IconSliders,
  IconWarning,
  type IconProps,
} from "../common/icons";
import { CodeEditor } from "../editor/CodeEditor";
import { AllowedDirRows } from "./AllowedDirectories";
import { LocalDataRows } from "./LocalData";
import { ClientCertRows } from "./ClientCertificates";
import { CustomThemeRows } from "./CustomThemes";
import { DataDirectoryRows } from "./DataDirectory";
import { fontOptionsFor, parseFontSize, tabSizeOptionsFor } from "./editorFields";
import { LockBadge, RowControl, useSettingLocked, useSettingsPolicy } from "./Locked";
import { ProxySection } from "./ProxyScopes";
import { SecretEncryptionRows } from "./SecretEncryption";
import styles from "./SettingsScreen.module.css";
import { ShortcutSettings } from "./ShortcutSettings";
import { formatTimeoutSeconds, parseMaxResponseBody, parseTimeoutSeconds } from "./timeout";

export type SettingsPage = "general" | "editor" | "network" | "shortcuts" | "privacy";

const PAGES: readonly { id: SettingsPage; labelKey: MessageKey; Icon: ComponentType<IconProps> }[] = [
  { id: "general", labelKey: "settings.page.general", Icon: IconSliders },
  { id: "editor", labelKey: "settings.page.editor", Icon: IconCode },
  { id: "network", labelKey: "settings.page.network", Icon: IconGlobe },
  { id: "shortcuts", labelKey: "settings.page.shortcuts", Icon: IconKeyboard },
  { id: "privacy", labelKey: "settings.page.privacy", Icon: IconShield },
];

/** Quiet time after the last keystroke before a valid number field is saved. */
export const FIELD_SAVE_DELAY_MS = 400;

export interface SettingsScreenProps {
  /** The page shown first. Default: General. */
  initialPage?: SettingsPage;
}

/**
 * The settings screen (SET-01), shown instead of the sidebar and the work
 * area while the rail's gear is active. Every control writes the settings
 * file as it changes (no Save button) through applySetting; a value Go
 * rejects shows its error next to the field and is not saved. Hand edits
 * of the file show up here as they arrive, through the settings store.
 */
export function SettingsScreen({ initialPage = "general" }: SettingsScreenProps) {
  const t = useT();
  const [page, setPage] = useState<SettingsPage>(initialPage);
  const [location, setLocation] = useState<SettingsLocation | null>(null);
  const [fileError, setFileError] = useState<AppError | null>(null);
  const current = PAGES.find((p) => p.id === page) ?? PAGES[0]!;
  const titleId = useId();
  const navRef = useRef<HTMLElement>(null);

  // Opened with ⌘, while focus was in the work area, which is now hidden:
  // keyboard focus starts on the current page in the settings nav (UI-11).
  useEffect(() => {
    const active = document.activeElement;
    if (active === null || active === document.body || active.closest("[hidden]") !== null) {
      navRef.current?.querySelector<HTMLElement>("[aria-current='page']")?.focus({ preventScroll: true });
    }
  }, []);

  useEffect(() => {
    let live = true;
    void loadSettingsLocation().then((loc) => {
      if (live) setLocation(loc);
    });
    return () => {
      live = false;
    };
  }, []);

  const handOver = useCallback((action: () => Promise<void>) => {
    setFileError(null);
    action().then(
      () => void loadSettingsLocation().then((loc) => loc && setLocation(loc)),
      (err: unknown) => setFileError(toAppError(err)),
    );
  }, []);

  return (
    <div className={styles.screen}>
      <nav ref={navRef} className={styles.nav} aria-label={t("settings.screen.label")}>
        <div className={styles.navHeader}>
          <h2 className={styles.navTitle}>{t("settings.screen.title")}</h2>
        </div>
        <ul className={styles.navList}>
          {PAGES.map(({ id, labelKey, Icon }) => (
            <li key={id}>
              <button
                type="button"
                className={styles.navItem}
                aria-current={id === page ? "page" : undefined}
                onClick={() => setPage(id)}
              >
                <Icon size={15} />
                {t(labelKey)}
              </button>
            </li>
          ))}
        </ul>
      </nav>

      <section className={styles.main} aria-labelledby={titleId}>
        <header className={styles.header}>
          <h1 id={titleId} className={styles.title}>
            {t(current.labelKey)}
          </h1>
          <span className={styles.saved}>{t("settings.screen.savedNote")}</span>
          <button type="button" className={styles.button} onClick={() => handOver(openSettingsFile)}>
            {t("settings.screen.openFile")}
          </button>
        </header>
        {fileError && (
          <p className={styles.fileError} role="alert">
            <IconWarning size={13} />
            <span>
              {fileError.message}
              {fileError.hint ? ` ${fileError.hint}` : ""}
            </span>
          </p>
        )}

        <div className={styles.scroll}>
          <div className={styles.content}>
            <PolicyNotice />
            <FileProblems />
            {page === "general" && <GeneralPage location={location} onReveal={() => handOver(revealSettingsFile)} />}
            {page === "editor" && <EditorPage />}
            {page === "network" && <NetworkPage />}
            {page === "shortcuts" && <ShortcutSettings />}
            {page === "privacy" && <PrivacyPage />}
          </div>
        </div>
      </section>
    </div>
  );
}

/** Saves `patch` now; returns the error to show next to the field, or null. */
function useApplySetting(): (patch: Partial<SettingsState>) => Promise<AppError | null> {
  const stores = useStores();
  return useCallback(
    (patch) =>
      applySetting(stores, patch).then(
        () => null,
        (err: unknown) => toAppError(err),
      ),
    [stores],
  );
}

function useFileProblems(): readonly AppError[] {
  return useSyncExternalStore(subscribeSettingsProblems, getSettingsProblems);
}

/** What is wrong with settings.json as last read: shown on every page until the file is fixed. */
function FileProblems() {
  const t = useT();
  const problems = useFileProblems();
  if (problems.length === 0) return null;
  return (
    <div className={styles.problems} role="status">
      <span className={styles.problemsTitle}>
        <IconWarning size={14} />
        {t("settings.problems.title", { count: problems.length })}
      </span>
      <ul className={styles.problemList}>
        {problems.map((p, i) => (
          <li key={i}>
            {p.message}
            {p.hint && <span className={styles.problemHint}> {p.hint}</span>}
          </li>
        ))}
      </ul>
    </div>
  );
}

/**
 * The admin policy (SET-02), on every page while one is in effect or its
 * file has problems: which settings it locks, where the file is, and what
 * is wrong with it.
 */
function PolicyNotice() {
  const t = useT();
  const policy = useSettingsPolicy();
  if (!policy.active && policy.problems.length === 0) return null;
  const count = policy.locked.length;
  return (
    <div className={styles.policy} role="status" aria-label={t("settings.policy.label")}>
      <span className={styles.problemsTitle}>
        <IconLock size={14} />
        {policy.active ? t("settings.policy.managed") : t("settings.policy.notApplied")}
      </span>
      {policy.active && (
        <span className={styles.problemHint}>
          {count === 0 ? t("settings.policy.noneLocked") : t("settings.policy.locked", { count })}
          {policy.requireTlsVerification ? t("settings.policy.tlsRequired") : ""}
          {policy.path ? t("settings.policy.file", { path: policy.path }) : ""}
        </span>
      )}
      {policy.problems.length > 0 && (
        <ul className={styles.problemList}>
          {policy.problems.map((p, i) => (
            <li key={i}>
              {p.message}
              {p.hint && <span className={styles.problemHint}> {p.hint}</span>}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

function Group({ title, children }: { title: string; children: ReactNode }) {
  const id = useId();
  return (
    <section aria-labelledby={id}>
      <h3 id={id} className={styles.groupTitle}>
        {title}
      </h3>
      <div className={styles.group}>{children}</div>
    </section>
  );
}

interface RowProps {
  label: string;
  /** id of the control, so the label names it. */
  controlId?: string;
  description?: ReactNode;
  /** Shown under the description, in the error colour. */
  error?: string | null;
  errorId?: string;
  /**
   * The settings.json key of the setting (`network.verifyTls`). When the
   * admin policy locks it (SET-02), the row shows a lock and its controls
   * are disabled.
   */
  setting?: string;
  children: ReactNode;
}

function Row({ label, controlId, description, error, errorId, setting, children }: RowProps) {
  const locked = useSettingLocked(setting);
  return (
    <div className={styles.row} data-invalid={error ? "true" : undefined} data-locked={locked ? "true" : undefined}>
      <div className={styles.rowText}>
        {controlId ? (
          <label className={styles.rowLabel} htmlFor={controlId}>
            {label}
            {locked && <LockBadge />}
          </label>
        ) : (
          <span className={styles.rowLabel}>
            {label}
            {locked && <LockBadge />}
          </span>
        )}
        {description && <span className={styles.rowDescription}>{description}</span>}
        {error && (
          <span id={errorId} className={styles.rowError} role="alert">
            <IconWarning size={13} />
            {error}
          </span>
        )}
      </div>
      <RowControl locked={locked}>{children}</RowControl>
    </div>
  );
}

const THEME_OPTIONS: readonly { value: Theme; labelKey: MessageKey }[] = [
  { value: "system", labelKey: "common.theme.system" },
  { value: "dark", labelKey: "common.theme.dark" },
  { value: "light", labelKey: "common.theme.light" },
];

function GeneralPage({ location, onReveal }: { location: SettingsLocation | null; onReveal: () => void }) {
  const t = useT();
  const theme = useSettings((s) => s.theme);
  const apply = useApplySetting();
  const [themeError, setThemeError] = useState<string | null>(null);
  const errorId = useId();

  const setTheme = (value: Theme) => {
    if (value === theme) return;
    void apply({ theme: value }).then((err) => setThemeError(err ? err.message : null));
  };

  return (
    <>
      <Group title={t("settings.group.appearance")}>
        <Row
          label={t("settings.theme.label")}
          setting="ui.theme"
          description={t("settings.theme.description")}
          error={themeError}
          errorId={errorId}
        >
          <div className={styles.segmented} role="group" aria-label={t("settings.theme.label")} aria-describedby={themeError ? errorId : undefined}>
            {THEME_OPTIONS.map(({ value, labelKey }) => (
              <button
                key={value}
                type="button"
                className={styles.segment}
                aria-pressed={theme === value}
                onClick={() => setTheme(value)}
              >
                {t(labelKey)}
              </button>
            ))}
          </div>
        </Row>
        <CustomThemeRows />
        <LanguageRow />
        <ResponsePositionRow />
      </Group>

      <RecentProjectsGroup />

      <Group title={t("settings.group.data")}>
        <DataDirectoryRows fallbackPath={location?.dataDir ?? ""} shorten={shortenPath} />
        <Row label={t("settings.file.label")} description={t("settings.file.description")}>
          <span className={styles.path} title={location?.path || undefined}>
            {location?.path ? shortenPath(location.path, 2) : "settings.json"}
          </span>
          <button type="button" className={styles.smallButton} onClick={onReveal}>
            {t("settings.reveal")}
          </button>
        </Row>
      </Group>
    </>
  );
}

/**
 * ui.locale (UI-10): the UI language. "System" follows the OS language when
 * the app has it (its name in brackets), else English. The pseudo-locale
 * (en-XA) is listed only in dev builds, or while it is the setting (set by
 * hand in settings.json). A tag the app has no messages for, set by hand or
 * by a newer build, is listed as it is and English is shown.
 */
function LanguageRow() {
  const t = useT();
  const locale = useSettings((s) => s.locale);
  const [error, save] = useRowSave();
  const selectId = useId();
  const errorId = useId();
  const systemTag = resolveLocale(LOCALE_SYSTEM);
  const systemName = LOCALES.find((l) => l.tag === systemTag)?.name ?? systemTag;
  const listed = LOCALES.filter((l) => !l.pseudo || import.meta.env.DEV || l.tag === locale);
  const known = locale === LOCALE_SYSTEM || listed.some((l) => l.tag === locale);

  return (
    <Row
      label={t("settings.language.label")}
      setting="ui.locale"
      controlId={selectId}
      description={t("settings.language.description")}
      error={error}
      errorId={errorId}
    >
      <select
        id={selectId}
        className={styles.select}
        value={locale}
        aria-describedby={error ? errorId : undefined}
        onChange={(e) => save({ locale: e.currentTarget.value })}
      >
        <option value={LOCALE_SYSTEM}>{t("settings.language.system", { language: systemName })}</option>
        {listed.map((l) => (
          <option key={l.tag} value={l.tag} lang={l.pseudo ? undefined : l.tag}>
            {l.name}
          </option>
        ))}
        {!known && <option value={locale}>{t("settings.language.unavailable", { tag: locale })}</option>}
      </select>
    </Row>
  );
}

/**
 * The recent projects (STO-06), kept in the app data directory (not in
 * settings.json): open one (switching to it), show its folder, remove it
 * from the list, or clear the list. The open project cannot be removed, so
 * it still reopens on the next start; clearing keeps it too. Folders and
 * their local data are never touched.
 */
function RecentProjectsGroup() {
  const t = useT();
  const stores = useStores();
  const recent = useWorkspace((s) => s.recent, shallowEqual);
  const currentPath = useWorkspace((s) => s.current?.path ?? null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    void refreshRecentWorkspaces(stores);
  }, [stores]);

  const act = (action: () => Promise<unknown>) => {
    if (busy) return;
    setBusy(true);
    setError(null);
    action()
      .catch((err: unknown) => {
        if (!isCancelled(err)) setError(toAppError(err).message);
      })
      .finally(() => setBusy(false));
  };

  const others = recent.filter((r) => r.path !== currentPath);

  return (
    <Group title={t("settings.recent.title")}>
      {recent.length === 0 && (
        <Row label={t("settings.recent.none")} description={t("settings.recent.noneDescription")}>
          {null}
        </Row>
      )}
      {recent.map((r) => {
        const name = r.name || folderName(r.path);
        const isCurrent = r.path === currentPath;
        return (
          <Row
            key={r.path}
            label={name}
            description={
              <span className={styles.path} title={r.path}>
                {shortenPath(r.path)}
                {isCurrent ? t("settings.recent.openNow") : r.available ? "" : t("settings.recent.notFound")}
              </span>
            }
          >
            {!isCurrent && (
              <button
                type="button"
                className={styles.smallButton}
                disabled={busy || !r.available}
                aria-label={t("settings.recent.openLabel", { name })}
                onClick={() => act(() => openWorkspaceAt(stores, r.path))}
              >
                {t("settings.recent.open")}
              </button>
            )}
            <button
              type="button"
              className={styles.smallButton}
              disabled={busy || !r.available}
              aria-label={t("settings.recent.revealLabel", { name })}
              onClick={() => act(() => revealWorkspace(r.path))}
            >
              {t("settings.reveal")}
            </button>
            {!isCurrent && (
              <button
                type="button"
                className={styles.smallButton}
                disabled={busy}
                aria-label={t("settings.recent.removeLabel", { name })}
                title={t("settings.recent.removeTitle")}
                onClick={() => act(() => forgetRecentWorkspace(stores, r.path))}
              >
                {t("settings.remove")}
              </button>
            )}
          </Row>
        );
      })}
      {others.length > 0 && (
        <Row label={t("settings.recent.clear")} description={t("settings.recent.clearDescription")} error={error}>
          <button
            type="button"
            className={styles.smallButton}
            disabled={busy}
            onClick={() => act(() => clearRecentWorkspaces(stores))}
          >
            {t("settings.recent.clearButton")}
          </button>
        </Row>
      )}
      {others.length === 0 && error !== null && (
        <Row label={t("settings.recent.title")} error={error}>
          {null}
        </Row>
      )}
    </Group>
  );
}

/**
 * The last `keep` segments of path behind "…/" when it has more, so a long
 * path fits the row; the full path is the tooltip. The home folder is
 * shown as ~ on macOS and Linux.
 */
export function shortenPath(path: string, keep?: number): string {
  const sep = path.includes("\\") && !path.includes("/") ? "\\" : "/";
  const home = /^(\/Users\/[^/]+|\/home\/[^/]+)(?=\/|$)/.exec(path);
  const shown = home ? `~${path.slice(home[1]!.length)}` : path;
  if (keep === undefined) return shown;
  const parts = shown.split(sep).filter((p) => p !== "");
  return parts.length > keep ? `…${sep}${parts.slice(-keep).join(sep)}` : shown;
}

function NetworkPage() {
  const t = useT();
  return (
    <>
      <Group title={t("settings.group.requests")}>
        <DefaultTimeoutRow />
        <MaxResponseBodyRow />
      </Group>
      <Group title={t("settings.group.proxy")}>
        <ProxySection />
      </Group>
      <Group title={t("settings.group.tls")}>
        <VerifyTlsRow />
        <CaBundleRows />
        <ClientCertRows shorten={shortenPath} />
      </Group>
    </>
  );
}

/** "2 certificates, the first expires 2027-03-01" for a bundle's row. */
export function describeCaBundle(bundle: CABundleDTO): string {
  const certs = bundle.certificates ?? [];
  if (certs.length === 0) return "";
  const count = translate("settings.ca.count", { count: certs.length });
  const soonest = certs.map((c) => c.notAfter).sort()[0] ?? "";
  const expired = certs.filter((c) => c.expired).length;
  if (expired === certs.length) return translate("settings.ca.expired", { count });
  if (!soonest) return count;
  const date = soonest.slice(0, 10);
  return certs.length === 1 ? translate("settings.ca.expires", { count, date }) : translate("settings.ca.firstExpires", { count, date });
}

/**
 * Custom CA certificates (NET-04, network.caBundles): PEM files every TLS
 * connection trusts on top of the system roots (HTTP, GraphQL, WebSocket,
 * gRPC). "Add file…" shows the native file dialog and Go checks the file
 * before saving it (not PEM, no certificate: an error here, nothing
 * saved). Each bundle is listed with its certificates and warnings (an
 * expired CA), or with the error that stops it from being used (a moved
 * file). Hand edits of the settings file show up as they arrive.
 */
function CaBundleRows() {
  const t = useT();
  const [bundles, setBundles] = useState<CABundleDTO[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const errorId = useId();

  useEffect(() => {
    let live = true;
    const refresh = () => {
      listCABundles().then(
        (next) => {
          if (live) setBundles(next);
        },
        () => {
          // No Go backend (the browser, tests without it): nothing to list.
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

  const act = (action: () => Promise<CABundleDTO[]>) => {
    if (busy) return;
    setBusy(true);
    setError(null);
    action()
      .then(setBundles, (err: unknown) => {
        if (!isCancelled(err)) setError(formatError(err));
      })
      .finally(() => setBusy(false));
  };

  return (
    <>
      <Row
        label={t("settings.ca.label")}
        setting="network.caBundles"
        description={t("settings.ca.description")}
        error={error}
        errorId={errorId}
      >
        {bundles.length === 0 && <span className={styles.unit}>{t("settings.none")}</span>}
        <button
          type="button"
          className={styles.smallButton}
          disabled={busy}
          aria-describedby={error ? errorId : undefined}
          onClick={() => act(addCABundle)}
        >
          {t("settings.ca.addFile")}
        </button>
      </Row>
      {bundles.map((b) => {
        const name = folderName(b.path);
        const summary = describeCaBundle(b);
        return (
          <Row
            key={b.path}
            label={name}
            setting="network.caBundles"
            description={
              <span className={styles.bundleDetails}>
                <span className={styles.path} title={b.path}>
                  {shortenPath(b.path)}
                </span>
                {summary && <span>{summary}</span>}
                {(b.warnings ?? []).map((w) => (
                  <span key={w.code + w.message} className={styles.rowWarning} role="note" title={w.hint || undefined}>
                    <IconWarning size={13} />
                    {w.message}
                  </span>
                ))}
              </span>
            }
            error={b.error ? formatError(b.error) : null}
          >
            <button
              type="button"
              className={styles.smallButton}
              disabled={busy}
              aria-label={t("settings.ca.removeLabel", { name })}
              title={t("settings.ca.removeTitle")}
              onClick={() => act(() => removeCABundle(b.path))}
            >
              {t("settings.remove")}
            </button>
          </Row>
        );
      })}
    </>
  );
}

/**
 * The global TLS verification switch (network.verifyTls, HTTP-23): on by
 * default. Off applies to every HTTP and GraphQL request without its own
 * setting and is marked there; the row itself warns while it is off.
 */
function VerifyTlsRow() {
  const t = useT();
  const verifyTls = useSettings((s) => s.verifyTls);
  const apply = useApplySetting();
  const [error, setError] = useState<string | null>(null);
  const errorId = useId();

  const set = (value: boolean) => {
    if (value === verifyTls) return;
    void apply({ verifyTls: value }).then((err) => setError(err ? err.message : null));
  };

  const required = useSettingsPolicy().requireTlsVerification;
  const description = required ? (
    t("settings.verifyTls.required")
  ) : verifyTls ? (
    t("settings.verifyTls.on")
  ) : (
    <span className={styles.rowWarning} role="note">
      <IconWarning size={13} />
      {t("settings.verifyTls.off")}
    </span>
  );

  return (
    <Row label={t("settings.verifyTls.label")} setting="network.verifyTls" description={description} error={error} errorId={errorId}>
      <div
        className={styles.segmented}
        role="group"
        aria-label={t("settings.verifyTls.group")}
        aria-describedby={error ? errorId : undefined}
      >
        <button type="button" className={styles.segment} aria-pressed={verifyTls} onClick={() => set(true)}>
          {t("settings.verifyTls.verify")}
        </button>
        <button type="button" className={styles.segment} aria-pressed={!verifyTls} onClick={() => set(false)}>
          {t("settings.verifyTls.dontVerify")}
        </button>
      </div>
    </Row>
  );
}

/**
 * Seconds in the field, milliseconds in the file (network.defaultTimeoutMs).
 * A valid value is saved FIELD_SAVE_DELAY_MS after the last keystroke, and
 * at once on Enter or when the field loses focus; an invalid one shows its
 * error and is not saved. While the field is not being edited it follows
 * the store, so a hand edit of the file shows up.
 */
function DefaultTimeoutRow() {
  const t = useT();
  const stored = useSettings((s) => s.defaultTimeoutMs);
  const apply = useApplySetting();
  const [draft, setDraft] = useState(() => formatTimeoutSeconds(stored));
  const [error, setError] = useState<string | null>(null);
  const editing = useRef(false);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const inputId = useId();
  const errorId = useId();

  useEffect(() => {
    if (editing.current) return;
    setDraft(formatTimeoutSeconds(stored));
    setError(null);
  }, [stored]);

  const clearTimer = () => {
    if (timer.current !== null) clearTimeout(timer.current);
    timer.current = null;
  };
  useEffect(() => clearTimer, []);

  const commit = (text: string) => {
    clearTimer();
    const parsed = parseTimeoutSeconds(text);
    if (!parsed.ok) {
      setError(parsed.error);
      return;
    }
    setError(null);
    void apply({ defaultTimeoutMs: parsed.ms }).then((err) => {
      if (err) setError(err.message);
    });
  };

  const onChange = (text: string) => {
    setDraft(text);
    clearTimer();
    const parsed = parseTimeoutSeconds(text);
    if (!parsed.ok) {
      setError(parsed.error);
      return;
    }
    setError(null);
    timer.current = setTimeout(() => commit(text), FIELD_SAVE_DELAY_MS);
  };

  return (
    <Row
      label={t("settings.timeout.label")}
      setting="network.defaultTimeoutMs"
      controlId={inputId}
      description={t("settings.timeout.description")}
      error={error}
      errorId={errorId}
    >
      <input
        id={inputId}
        className={styles.input}
        data-width="timeout"
        inputMode="decimal"
        placeholder={t("settings.timeout.placeholder")}
        spellCheck={false}
        autoComplete="off"
        value={draft}
        aria-invalid={error ? true : undefined}
        aria-describedby={error ? errorId : undefined}
        onFocus={() => {
          editing.current = true;
        }}
        onBlur={(e) => {
          editing.current = false;
          commit(e.currentTarget.value);
        }}
        onKeyDown={(e) => {
          if (e.key === "Enter") commit(e.currentTarget.value);
        }}
        onChange={(e) => onChange(e.currentTarget.value)}
      />
      <span className={styles.unit}>{t("settings.timeout.unit")}</span>
    </Row>
  );
}

/**
 * network.maxResponseBodyMb (HTTP-37): the largest response body kept, in
 * MiB. Saved like the default timeout: FIELD_SAVE_DELAY_MS after the last
 * keystroke, at once on Enter or blur; follows the store while not being
 * edited.
 */
function MaxResponseBodyRow() {
  const t = useT();
  const stored = useSettings((s) => s.maxResponseBodyMb);
  const apply = useApplySetting();
  const [draft, setDraft] = useState(() => String(stored));
  const [error, setError] = useState<string | null>(null);
  const editing = useRef(false);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const inputId = useId();
  const errorId = useId();

  useEffect(() => {
    if (editing.current) return;
    setDraft(String(stored));
    setError(null);
  }, [stored]);

  const clearTimer = () => {
    if (timer.current !== null) clearTimeout(timer.current);
    timer.current = null;
  };
  useEffect(() => clearTimer, []);

  const commit = (text: string) => {
    clearTimer();
    const parsed = parseMaxResponseBody(text);
    if (!parsed.ok) {
      setError(parsed.error);
      return;
    }
    setError(null);
    void apply({ maxResponseBodyMb: parsed.mb }).then((err) => {
      if (err) setError(err.message);
    });
  };

  const onChange = (text: string) => {
    setDraft(text);
    clearTimer();
    const parsed = parseMaxResponseBody(text);
    if (!parsed.ok) {
      setError(parsed.error);
      return;
    }
    setError(null);
    timer.current = setTimeout(() => commit(text), FIELD_SAVE_DELAY_MS);
  };

  return (
    <Row
      label={t("settings.bodyLimit.label")}
      setting="network.maxResponseBodyMb"
      controlId={inputId}
      description={t("settings.bodyLimit.description")}
      error={error}
      errorId={errorId}
    >
      <input
        id={inputId}
        className={styles.input}
        data-width="timeout"
        inputMode="numeric"
        spellCheck={false}
        autoComplete="off"
        value={draft}
        aria-invalid={error ? true : undefined}
        aria-describedby={error ? errorId : undefined}
        onFocus={() => {
          editing.current = true;
        }}
        onBlur={(e) => {
          editing.current = false;
          commit(e.currentTarget.value);
        }}
        onKeyDown={(e) => {
          if (e.key === "Enter") commit(e.currentTarget.value);
        }}
        onChange={(e) => onChange(e.currentTarget.value)}
      />
      <span className={styles.unit}>{t("settings.bodyLimit.unit")}</span>
    </Row>
  );
}

const PRIVACY_FACTS: readonly MessageKey[] = [
  "settings.privacy.fact.telemetry",
  "settings.privacy.fact.updates",
  "settings.privacy.fact.account",
  "settings.privacy.fact.network",
];

/**
 * The encryption of the stored secrets (SEC-03), the directories requests
 * may read files from (PRV-10), clearing the local data (PRV-13), then the
 * privacy guarantees (PRV-01,
 * PRV-02, PRV-05), shown as facts: there is nothing to switch.
 */
function PrivacyPage() {
  const t = useT();
  return (
    <>
      <Group title={t("settings.group.secrets")}>
        <SecretEncryptionRows />
      </Group>
      <Group title={t("settings.group.localFiles")}>
        <AllowedDirRows shorten={shortenPath} />
      </Group>
      <Group title={t("settings.group.localData")}>
        <LocalDataRows />
      </Group>
      <PrivacyFacts />
    </>
  );
}

function PrivacyFacts() {
  const t = useT();
  return (
    <div className={styles.facts}>
      <span className={styles.factsTitle}>{t("settings.privacy.factsTitle")}</span>
      <ul className={styles.factList}>
        {PRIVACY_FACTS.map((fact) => (
          <li key={fact} className={styles.fact}>
            <span className={styles.factIcon}>
              <IconCheck size={14} />
            </span>
            {t(fact)}
          </li>
        ))}
      </ul>
      <span className={styles.factsNote}>{t("settings.privacy.factsNote")}</span>
    </div>
  );
}

/**
 * Saves patches from one row and keeps the error of the last one: null
 * once a save succeeds, Go's message when it refuses the value.
 */
function useRowSave(): [error: string | null, save: (patch: Partial<SettingsState>) => void] {
  const apply = useApplySetting();
  const [error, setError] = useState<string | null>(null);
  const save = useCallback(
    (patch: Partial<SettingsState>) => {
      void apply(patch).then((err) => setError(err ? err.message : null));
    },
    [apply],
  );
  return [error, save];
}

const RESPONSE_POSITION_OPTIONS: readonly { value: ResponsePosition; labelKey: MessageKey }[] = [
  { value: "beside", labelKey: "settings.responsePosition.beside" },
  { value: "below", labelKey: "settings.responsePosition.below" },
];

/** ui.responsePosition (UI-02): the response pane right of the request or under it. */
function ResponsePositionRow() {
  const t = useT();
  const position = useSettings((s) => s.responsePosition);
  const [error, save] = useRowSave();
  const errorId = useId();

  return (
    <Row
      label={t("settings.responsePosition.label")}
      setting="ui.responsePosition"
      description={t("settings.responsePosition.description")}
      error={error}
      errorId={errorId}
    >
      <div
        className={styles.segmented}
        role="group"
        aria-label={t("settings.responsePosition.label")}
        aria-describedby={error ? errorId : undefined}
      >
        {RESPONSE_POSITION_OPTIONS.map(({ value, labelKey }) => (
          <button
            key={value}
            type="button"
            className={styles.segment}
            aria-pressed={position === value}
            onClick={() => value !== position && save({ responsePosition: value })}
          >
            {t(labelKey)}
          </button>
        ))}
      </div>
    </Row>
  );
}

// The Preview group's sample: nested, with a line long enough to wrap.
const PREVIEW_SAMPLE = `{
  "reason": "customer_request",
  "refund": true,
  "items": [{ "sku": "SM-5-BLK", "quantity": 1 }],
  "note": "A long line shows how word wrap treats text that does not fit the width of the editor."
}
`;

/**
 * The editor settings (UI-08), section "editor" of the file. They apply to
 * every code editor and response viewer as they change, and to the
 * preview below; the font also to the one-line fields such as the URL.
 */
function EditorPage() {
  const t = useT();
  const previewId = useId();
  return (
    <>
      <Group title={t("settings.group.text")}>
        <FontFamilyRow />
        <FontSizeRow />
        <TabSizeRow />
      </Group>
      <Group title={t("settings.group.behaviour")}>
        <ToggleRow
          setting="editorWordWrap"
          label={t("settings.wordWrap.label")}
          description={t("settings.wordWrap.description")}
        />
        <ToggleRow setting="editorLineNumbers" label={t("settings.lineNumbers.label")} />
        <ToggleRow
          setting="editorMatchBrackets"
          label={t("settings.matchBrackets.label")}
          description={t("settings.matchBrackets.description")}
        />
      </Group>
      <section aria-labelledby={previewId}>
        <h3 id={previewId} className={styles.groupTitle}>
          {t("settings.group.preview")}
        </h3>
        <div className={styles.preview}>
          <CodeEditor value={PREVIEW_SAMPLE} readOnly language="json" ariaLabel={t("settings.editor.previewLabel")} historyKey={null} />
        </div>
      </section>
    </>
  );
}

function FontFamilyRow() {
  const t = useT();
  const family = useSettings((s) => s.editorFontFamily);
  const [error, save] = useRowSave();
  const selectId = useId();
  const errorId = useId();

  return (
    <Row
      label={t("settings.font.label")}
      setting="editor.fontFamily"
      controlId={selectId}
      description={t("settings.font.description")}
      error={error}
      errorId={errorId}
    >
      <select
        id={selectId}
        className={styles.select}
        value={family}
        aria-describedby={error ? errorId : undefined}
        onChange={(e) => save({ editorFontFamily: e.currentTarget.value })}
      >
        {fontOptionsFor(family).map((o) => (
          <option key={o.value} value={o.value}>
            {o.label}
          </option>
        ))}
      </select>
    </Row>
  );
}

/**
 * editor.fontSize in px. Saved like the default timeout: FIELD_SAVE_DELAY_MS
 * after the last keystroke, at once on Enter or blur; follows the store
 * while not being edited.
 */
function FontSizeRow() {
  const t = useT();
  const stored = useSettings((s) => s.editorFontSize);
  const apply = useApplySetting();
  const [draft, setDraft] = useState(() => String(stored));
  const [error, setError] = useState<string | null>(null);
  const editing = useRef(false);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const inputId = useId();
  const errorId = useId();

  useEffect(() => {
    if (editing.current) return;
    setDraft(String(stored));
    setError(null);
  }, [stored]);

  const clearTimer = () => {
    if (timer.current !== null) clearTimeout(timer.current);
    timer.current = null;
  };
  useEffect(() => clearTimer, []);

  const commit = (text: string) => {
    clearTimer();
    const parsed = parseFontSize(text);
    if (!parsed.ok) {
      setError(parsed.error);
      return;
    }
    setError(null);
    void apply({ editorFontSize: parsed.px }).then((err) => {
      if (err) setError(err.message);
    });
  };

  const onChange = (text: string) => {
    setDraft(text);
    clearTimer();
    const parsed = parseFontSize(text);
    if (!parsed.ok) {
      setError(parsed.error);
      return;
    }
    setError(null);
    timer.current = setTimeout(() => commit(text), FIELD_SAVE_DELAY_MS);
  };

  return (
    <Row label={t("settings.fontSize.label")} setting="editor.fontSize" controlId={inputId} error={error} errorId={errorId}>
      <input
        id={inputId}
        className={styles.input}
        data-width="fontSize"
        inputMode="numeric"
        spellCheck={false}
        autoComplete="off"
        value={draft}
        aria-invalid={error ? true : undefined}
        aria-describedby={error ? errorId : undefined}
        onFocus={() => {
          editing.current = true;
        }}
        onBlur={(e) => {
          editing.current = false;
          commit(e.currentTarget.value);
        }}
        onKeyDown={(e) => {
          if (e.key === "Enter") commit(e.currentTarget.value);
        }}
        onChange={(e) => onChange(e.currentTarget.value)}
      />
      <span className={styles.unit}>{t("settings.fontSize.unit")}</span>
    </Row>
  );
}

function TabSizeRow() {
  const t = useT();
  const tabSize = useSettings((s) => s.editorTabSize);
  const [error, save] = useRowSave();
  const errorId = useId();

  return (
    <Row
      label={t("settings.tabSize.label")}
      setting="editor.tabSize"
      description={t("settings.tabSize.description")}
      error={error}
      errorId={errorId}
    >
      <div className={styles.segmented} role="group" aria-label={t("settings.tabSize.label")} aria-describedby={error ? errorId : undefined}>
        {tabSizeOptionsFor(tabSize).map((n) => (
          <button
            key={n}
            type="button"
            className={styles.segment}
            aria-pressed={tabSize === n}
            onClick={() => n !== tabSize && save({ editorTabSize: n })}
          >
            {n}
          </button>
        ))}
      </div>
    </Row>
  );
}

type EditorToggle = "editorWordWrap" | "editorLineNumbers" | "editorMatchBrackets";

function ToggleRow({ setting, label, description }: { setting: EditorToggle; label: string; description?: string }) {
  const on = useSettings((s) => s[setting]);
  const [error, save] = useRowSave();
  const inputId = useId();
  const errorId = useId();

  return (
    <Row
      label={label}
      setting={settingFileKey(setting)}
      controlId={inputId}
      description={description}
      error={error}
      errorId={errorId}
    >
      <input
        id={inputId}
        type="checkbox"
        className={styles.checkbox}
        checked={on}
        aria-describedby={error ? errorId : undefined}
        onChange={(e) => save({ [setting]: e.currentTarget.checked })}
      />
    </Row>
  );
}
