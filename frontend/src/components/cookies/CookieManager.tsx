import { useCallback, useEffect, useId, useMemo, useRef, useState } from "react";
import { confirm } from "../../commands/dialogs";
import {
  clearCookies,
  COOKIE_CODES,
  deleteCookie,
  deleteCookieDomain,
  draftFromCookie,
  emptyDraft,
  filterCookies,
  groupCookies,
  jarLabel,
  listCookies,
  setCookie,
  setCookiesPerEnvironment,
  type CookieDraft,
  type DraftResult,
  type CookieDTO,
  type CookiesDTO,
  type JarDTO,
  type ProblemDTO,
} from "../../lib/cookies";
import { formatDate } from "../../i18n";
import { useT, type Translate } from "../../i18n/react";
import { onEnvironmentsChanged } from "../../lib/environments";
import { toAppError } from "../../lib/errors";
import { onWorkspaceChanged } from "../../lib/workspace";
import { Dialog } from "../common/Dialog";
import { EmptyState } from "../common/EmptyState";
import { IconCheck, IconCookie, IconPlus, IconTrash, IconWarning } from "../common/icons";
import { CookieForm } from "./CookieForm";
import styles from "./CookieManager.module.css";

export interface CookieManagerProps {
  open: boolean;
  onClose: () => void;
}

/**
 * The cookie manager (CK-02): the cookie jar requests send from now,
 * grouped by domain, with add, edit, delete, clear domain and clear all.
 * That jar is the open project's or, with "Separate cookies per
 * environment" on (CK-03), the active environment's; the header names it.
 * It reads and writes the jar through CookiesService, so an edit applies
 * to the next request. The list is loaded when the dialog opens, after
 * each change and when the active environment or the project changes;
 * Refresh picks up cookies that responses stored meanwhile.
 */
export function CookieManager({ open, onClose }: CookieManagerProps) {
  if (!open) return null;
  return <Manager onClose={onClose} />;
}

type Load =
  | { status: "loading" }
  | { status: "ready"; cookies: CookieDTO[]; problem: ProblemDTO | null; jar: JarDTO | null }
  | { status: "error"; message: string };

/** The edit form: `key` is the cookie replaced, "" to add one. */
interface Editing {
  key: string;
  draft: CookieDraft;
}

function describe(err: unknown): string {
  const e = toAppError(err);
  return e.hint === "" ? e.message : `${e.message} ${e.hint}`;
}

/** When a cookie expires, in the language in use: "Session" for a session cookie, the raw text if it does not parse. */
function expiresText(t: Translate, expires: string): string {
  if (expires === "") return t("cookies.row.session");
  const date = new Date(expires);
  return Number.isNaN(date.getTime()) ? expires : formatDate(date, { dateStyle: "medium", timeStyle: "short" });
}

function ready(dto: CookiesDTO): Load {
  return { status: "ready", cookies: dto.cookies ?? [], problem: dto.problem, jar: dto.jar ?? null };
}

function Manager({ onClose }: { onClose: () => void }) {
  const t = useT();
  const [load, setLoad] = useState<Load>({ status: "loading" });
  const [query, setQuery] = useState("");
  const [editing, setEditing] = useState<Editing | null>(null);
  const [busy, setBusy] = useState(false);
  const [formError, setFormError] = useState<{ message: string; field: string } | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const alive = useRef(true);
  const filterId = useId();

  const reload = useCallback(async () => {
    try {
      const dto = await listCookies();
      if (alive.current) setLoad(ready(dto));
    } catch (err: unknown) {
      if (alive.current) setLoad({ status: "error", message: describe(err) });
    }
  }, []);

  useEffect(() => {
    alive.current = true;
    void reload();
    // Another environment or project means another jar.
    const offEnvironments = onEnvironmentsChanged(() => void reload());
    const offWorkspace = onWorkspaceChanged(() => void reload());
    return () => {
      alive.current = false;
      offEnvironments();
      offWorkspace();
    };
  }, [reload]);

  const cookies = load.status === "ready" ? load.cookies : [];
  const jar = load.status === "ready" ? load.jar : null;
  const label = jar === null ? null : jarLabel(jar);
  const groups = useMemo(() => groupCookies(filterCookies(cookies, query)), [cookies, query]);

  /** Runs a change, then reloads the list; a failure is shown above the list. */
  const change = async (action: () => Promise<unknown>) => {
    setActionError(null);
    try {
      await action();
    } catch (err: unknown) {
      if (alive.current) setActionError(describe(err));
    }
    await reload();
  };

  const startEdit = (next: Editing) => {
    setFormError(null);
    setEditing(next);
  };

  const onDelete = async (c: CookieDTO) => {
    const ok = await confirm({
      title: t("cookies.delete.title"),
      message: t("cookies.delete.message", { name: c.name, domain: c.domain }),
      confirmLabel: t("cookies.delete.confirm"),
      variant: "danger",
      confirmIcon: <IconTrash size={14} />,
    });
    if (ok) await change(() => deleteCookie(c.key));
  };

  const onClearDomain = async (domain: string, count: number) => {
    const ok = await confirm({
      title: t("cookies.clearDomain.title", { domain }),
      message: t("cookies.clearDomain.message", { count }),
      confirmLabel: t("cookies.clearDomain.confirm"),
      variant: "danger",
      confirmIcon: <IconTrash size={14} />,
    });
    if (ok) await change(() => deleteCookieDomain(domain));
  };

  const onPerEnvironment = async (on: boolean) => {
    if (busy) return;
    setBusy(true);
    setActionError(null);
    try {
      const dto = await setCookiesPerEnvironment(on);
      if (alive.current) setLoad(ready(dto));
    } catch (err: unknown) {
      if (alive.current) setActionError(describe(err));
    } finally {
      if (alive.current) setBusy(false);
    }
  };

  const onClearAll = async () => {
    const ok = await confirm({
      title: t("cookies.clearAll.title"),
      message: label === null ? t("cookies.clearAll.message") : t("cookies.clearAll.messageNamed", { jar: label.name }),
      confirmLabel: t("cookies.clearAll.confirm"),
      variant: "danger",
      confirmIcon: <IconTrash size={14} />,
    });
    if (ok) await change(() => clearCookies());
  };

  const save = async (result: DraftResult) => {
    if (editing === null || busy) return;
    if ("error" in result) {
      setFormError({ message: result.error, field: result.field });
      return;
    }
    setBusy(true);
    setFormError(null);
    try {
      await setCookie(editing.key, result.cookie);
      if (!alive.current) return;
      setEditing(null);
      await reload();
    } catch (err: unknown) {
      if (!alive.current) return;
      const e = toAppError(err);
      setFormError({ message: describe(err), field: e.details.field ?? "" });
      if (e.code === COOKIE_CODES.notFound) void reload();
    } finally {
      if (alive.current) setBusy(false);
    }
  };

  if (editing !== null) {
    return (
      <CookieForm
        adding={editing.key === ""}
        draft={editing.draft}
        onChange={(draft) => {
          setEditing({ ...editing, draft });
          setFormError(null);
        }}
        error={formError}
        busy={busy}
        onCancel={() => setEditing(null)}
        onSave={(result) => void save(result)}
      />
    );
  }

  const total = cookies.length;
  return (
    <Dialog open title={t("cookies.manager.title")} onCancel={onClose} width={920} bodyClassName={styles.body}>
      {jar !== null && label !== null && (
        <div className={styles.jarBar}>
          <p className={styles.jar} aria-label={t("cookies.manager.jar")}>
            <span className={styles.jarCaption}>{t("cookies.manager.jarCaption")}</span>
            <span className={styles.jarName} title={label.name}>
              {label.name}
            </span>
            <span className={styles.jarScope}>{label.scope}</span>
          </p>
          <label className={styles.jarSetting} title={t("cookies.manager.perEnvironment.title")}>
            <input
              type="checkbox"
              checked={jar.perEnvironment}
              disabled={busy}
              onChange={(e) => void onPerEnvironment(e.target.checked)}
            />
            {t("cookies.manager.perEnvironment")}
          </label>
        </div>
      )}
      <div className={styles.toolbar}>
        <label className={styles.filter} htmlFor={filterId}>
          <input
            id={filterId}
            className={styles.input}
            type="search"
            value={query}
            placeholder={t("cookies.manager.filter.placeholder")}
            aria-label={t("cookies.manager.filter.label")}
            spellCheck={false}
            autoComplete="off"
            onChange={(e) => setQuery(e.target.value)}
          />
        </label>
        <span className={styles.count} aria-live="polite">
          {load.status === "ready" ? t("cookies.manager.count", { count: total }) : ""}
        </span>
        <button type="button" className={styles.button} onClick={() => void change(async () => undefined)}>
          {t("cookies.manager.refresh")}
        </button>
        <button type="button" className={styles.button} disabled={total === 0} onClick={() => void onClearAll()}>
          {t("cookies.manager.clearAll")}
        </button>
        <button
          type="button"
          className={`${styles.button} ${styles.primary}`}
          disabled={load.status !== "ready"}
          onClick={() => startEdit({ key: "", draft: emptyDraft() })}
        >
          <IconPlus size={14} />
          {t("cookies.manager.add")}
        </button>
      </div>

      {load.status === "ready" && load.problem !== null && (
        <p className={styles.banner} role="status">
          <IconWarning size={14} />
          <span>
            {load.problem.message}
            {load.problem.hint !== "" && ` ${load.problem.hint}`}
          </span>
        </p>
      )}
      {actionError !== null && (
        <p className={`${styles.banner} ${styles.bannerBad}`} role="alert">
          <IconWarning size={14} />
          <span>{actionError}</span>
        </p>
      )}

      <div className={styles.list}>
        {load.status === "loading" && <p className={styles.muted}>{t("cookies.manager.loading")}</p>}
        {load.status === "error" && (
          <p className={`${styles.banner} ${styles.bannerBad}`} role="alert">
            <IconWarning size={14} />
            <span>{load.message}</span>
          </p>
        )}
        {load.status === "ready" && total === 0 && (
          <EmptyState icon={<IconCookie size={28} strokeWidth={1.5} />} title={t("cookies.manager.empty.title")}>
            {t("cookies.manager.empty.body")}
          </EmptyState>
        )}
        {load.status === "ready" && total > 0 && groups.length === 0 && <p className={styles.muted}>{t("cookies.manager.noMatch")}</p>}
        {groups.map((g) => (
          <section key={g.domain} className={styles.group} aria-label={g.domain}>
            <div className={styles.groupHeader}>
              <h3 className={styles.domain}>{g.domain}</h3>
              <span className={styles.groupCount}>{g.cookies.length}</span>
              <span className={styles.spacer} />
              <button
                type="button"
                className={styles.iconButton}
                aria-label={t("cookies.group.add", { domain: g.domain })}
                title={t("cookies.group.add.title")}
                onClick={() => startEdit({ key: "", draft: emptyDraft(g.domain) })}
              >
                <IconPlus size={14} />
              </button>
              <button
                type="button"
                className={styles.iconButton}
                aria-label={t("cookies.group.clear", { domain: g.domain })}
                title={t("cookies.group.clear.title")}
                onClick={() => void onClearDomain(g.domain, g.cookies.length)}
              >
                <IconTrash size={14} />
              </button>
            </div>
            <table className={styles.table}>
              <thead>
                <tr>
                  <th scope="col">{t("cookies.col.name")}</th>
                  <th scope="col">{t("cookies.col.value")}</th>
                  <th scope="col">{t("cookies.col.path")}</th>
                  <th scope="col">{t("cookies.col.expires")}</th>
                  {/* i18n-ignore-start: cookie attribute names (Secure, HttpOnly, SameSite) */}
                  <th scope="col" className={styles.flag}>
                    Secure
                  </th>
                  <th scope="col" className={styles.flag}>
                    HttpOnly
                  </th>
                  <th scope="col">SameSite</th>
                  {/* i18n-ignore-end */}
                  <th scope="col" className={styles.actionsCol}>
                    <span className={styles.visuallyHidden}>{t("cookies.col.actions")}</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {g.cookies.map((c) => (
                  <tr
                    key={c.key}
                    onDoubleClick={() => {
                      if (!c.binary) startEdit({ key: c.key, draft: draftFromCookie(c) });
                    }}
                  >
                    <td className={styles.mono}>
                      <span className={styles.name}>{c.name === "" ? <em className={styles.muted}>{t("cookies.row.noName")}</em> : c.name}</span>
                      {!c.hostOnly && (
                        <span className={styles.badge} title={t("cookies.row.subdomains.title", { domain: c.domain })}>
                          {t("cookies.row.subdomains")}
                        </span>
                      )}
                    </td>
                    <td className={`${styles.mono} ${styles.value}`} title={c.value}>
                      {c.value}
                    </td>
                    <td className={styles.mono}>{c.path}</td>
                    <td className={styles.expires} title={c.expires}>
                      {expiresText(t, c.expires)}
                    </td>
                    <td className={styles.flag}>{c.secure && <IconCheck size={13} aria-label={t("cookies.row.yes")} />}</td>
                    <td className={styles.flag}>{c.httpOnly && <IconCheck size={13} aria-label={t("cookies.row.yes")} />}</td>
                    <td>{c.sameSite === "" ? <span className={styles.muted}>—</span> : c.sameSite}</td>
                    <td className={styles.actions}>
                      <button
                        type="button"
                        className={styles.linkButton}
                        disabled={c.binary}
                        title={c.binary ? t("cookies.row.binary") : undefined}
                        aria-label={c.name === "" ? t("cookies.row.editUnnamed") : t("cookies.row.editNamed", { name: c.name })}
                        onClick={() => startEdit({ key: c.key, draft: draftFromCookie(c) })}
                      >
                        {t("cookies.row.edit")}
                      </button>
                      <button
                        type="button"
                        className={styles.iconButton}
                        aria-label={c.name === "" ? t("cookies.row.deleteUnnamed") : t("cookies.row.deleteNamed", { name: c.name })}
                        title={t("cookies.row.delete")}
                        onClick={() => void onDelete(c)}
                      >
                        <IconTrash size={13} />
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </section>
        ))}
      </div>
    </Dialog>
  );
}
