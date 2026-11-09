import { useEffect, useId, useRef, useState, type FormEvent } from "react";
import {
  chooseAndOpenWorkspace,
  chooseProjectFolder,
  createWorkspaceAt,
  forgetRecentWorkspace,
  openWorkspaceAt,
} from "../../commands/workspace";
import { openInNewWindow } from "../../commands/windows";
import { errorTitle, isCancelled, toAppError, type AppError } from "../../lib/errors";
import { folderName, WORKSPACE_CODES } from "../../lib/workspace";
import { shallowEqual, useStores, useWorkspace } from "../../stores";
import { useT } from "../../i18n/react";
import { IconClose, IconFolder, IconFolderPlus, IconNewWindow, IconSignal } from "../common/icons";
import { DataDirNotice } from "../settings/DataDirectory";
import styles from "./WelcomeScreen.module.css";

/** The inline "create project" form: the chosen folder and the name being typed. */
interface CreateForm {
  path: string;
  name: string;
}

export interface WelcomeScreenProps {
  /** A project this window was asked to open that did not open (APP-06), shown as an error. */
  notice?: AppError | null;
}

/**
 * Shown when no project is open (STO-01): open a folder, create a
 * project in a folder, or reopen a recent one, here or in a new window
 * (APP-06; every window then shows it). Folders are chosen with the
 * native dialog; the create form is inline until dialogs exist.
 */
export function WelcomeScreen({ notice = null }: WelcomeScreenProps) {
  const t = useT();
  const stores = useStores();
  const recent = useWorkspace((s) => s.recent, shallowEqual);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<AppError | null>(notice);
  useEffect(() => {
    if (notice !== null) setError(notice);
  }, [notice]);
  const [form, setForm] = useState<CreateForm | null>(null);
  const nameId = useId();
  const nameInput = useRef<HTMLInputElement>(null);

  // Runs one action at a time and shows its error. The component unmounts
  // when a project opens, so state is only set on failure or cancel.
  const act = async (action: () => Promise<unknown>) => {
    if (busy) return;
    setBusy(true);
    setError(null);
    try {
      await action();
    } catch (err: unknown) {
      if (!isCancelled(err)) setError(toAppError(err));
    }
    setBusy(false);
  };

  const startCreate = (path: string) => {
    setError(null);
    setForm({ path, name: folderName(path) });
    requestAnimationFrame(() => nameInput.current?.select());
  };

  const onOpenFolder = () => void act(() => chooseAndOpenWorkspace(stores));

  const onCreateProject = () =>
    void act(async () => {
      const path = await chooseProjectFolder();
      if (path !== "") startCreate(path);
    });

  const onSubmitCreate = (e: FormEvent) => {
    e.preventDefault();
    if (form === null) return;
    void act(() => createWorkspaceAt(stores, form.path, form.name));
  };

  const errorPath = error?.details.path ?? "";

  return (
    <main className={styles.welcome}>
      <div className={styles.column}>
        <header className={styles.header}>
          <IconSignal size={36} className={styles.logo} />
          <h1 className={styles.title}>PostSonde</h1>
          <p className={styles.tagline}>{t("workspace.welcome.tagline")}</p>
        </header>

        <div className={styles.actions}>
          <button type="button" className={styles.action} onClick={onOpenFolder} disabled={busy}>
            <IconFolder size={20} className={styles.actionIcon} />
            <span className={styles.actionText}>
              <span className={styles.actionLabel}>{t("workspace.welcome.open")}</span>
              <span className={styles.actionHint}>{t("workspace.welcome.openHint")}</span>
            </span>
          </button>
          <button type="button" className={styles.action} onClick={onCreateProject} disabled={busy}>
            <IconFolderPlus size={20} className={styles.actionIcon} />
            <span className={styles.actionText}>
              <span className={styles.actionLabel}>{t("workspace.welcome.create")}</span>
              <span className={styles.actionHint}>{t("workspace.welcome.createHint")}</span>
            </span>
          </button>
        </div>

        {form !== null && (
          <form className={styles.panel} onSubmit={onSubmitCreate} aria-label={t("workspace.welcome.form.label")}>
            <label className={styles.fieldLabel} htmlFor={nameId}>
              {t("workspace.form.projectName")}
            </label>
            <input
              id={nameId}
              ref={nameInput}
              className={styles.input}
              value={form.name}
              onChange={(e) => setForm({ ...form, name: e.target.value })}
              placeholder={folderName(form.path)}
              autoFocus
              disabled={busy}
            />
            <p className={styles.path} title={form.path}>
              {form.path}
            </p>
            <div className={styles.formButtons}>
              <button type="button" className={styles.secondary} onClick={() => setForm(null)} disabled={busy}>
                {t("common.cancel")}
              </button>
              <button type="submit" className={styles.primary} disabled={busy}>
                {t("workspace.welcome.form.submit")}
              </button>
            </div>
          </form>
        )}

        {error !== null && (
          <div className={styles.error} role="alert">
            <p className={styles.errorTitle}>{errorTitle(error.kind)}</p>
            <p>{error.message}</p>
            {error.hint !== "" && <p className={styles.errorHint}>{error.hint}</p>}
            {error.code === WORKSPACE_CODES.notAProject && errorPath !== "" && (
              <button type="button" className={styles.link} onClick={() => startCreate(errorPath)} disabled={busy}>
                {t("workspace.welcome.createHere")}
              </button>
            )}
            {error.code === WORKSPACE_CODES.alreadyAProject && errorPath !== "" && (
              <button
                type="button"
                className={styles.link}
                onClick={() => void act(() => openWorkspaceAt(stores, errorPath))}
                disabled={busy}
              >
                {t("workspace.welcome.openInstead")}
              </button>
            )}
          </div>
        )}

        <DataDirNotice />

        {recent.length > 0 && (
          <section className={styles.recent} aria-label={t("workspace.welcome.recent")}>
            <h2 className={styles.caption}>{t("workspace.welcome.recent")}</h2>
            <ul className={styles.recentList}>
              {recent.map((r) => (
                <li key={r.path} className={styles.recentItem}>
                  <button
                    type="button"
                    className={styles.recentOpen}
                    onClick={() => void act(() => openWorkspaceAt(stores, r.path))}
                    disabled={busy}
                    title={r.path}
                  >
                    <span className={r.available ? styles.recentName : `${styles.recentName} ${styles.missing}`}>
                      {r.name || folderName(r.path)}
                    </span>
                    <span className={styles.recentPath}>{r.path}</span>
                    {!r.available && <span className={styles.badge}>{t("workspace.welcome.recent.notFound")}</span>}
                  </button>
                  {r.available && (
                    <button
                      type="button"
                      className={styles.remove}
                      aria-label={t("workspace.welcome.recent.openInNewWindowNamed", { name: r.name || folderName(r.path) })}
                      title={t("workspace.welcome.recent.openInNewWindow")}
                      onClick={() => void act(() => openInNewWindow(r.path))}
                      disabled={busy}
                    >
                      <IconNewWindow size={13} />
                    </button>
                  )}
                  <button
                    type="button"
                    className={styles.remove}
                    aria-label={t("workspace.welcome.recent.removeNamed", { name: r.name || folderName(r.path) })}
                    title={t("workspace.welcome.recent.remove")}
                    onClick={() => void act(() => forgetRecentWorkspace(stores, r.path))}
                    disabled={busy}
                  >
                    <IconClose size={12} />
                  </button>
                </li>
              ))}
            </ul>
          </section>
        )}

        <p className={styles.note}>{t("workspace.welcome.note")}</p>
      </div>
    </main>
  );
}
