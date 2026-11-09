import { useEffect, useId, useState, type ReactNode } from "react";
import { formatError, isCancelled } from "../../lib/errors";
import {
  addAllowedDir,
  listAllowedDirs,
  onSettingsChanged,
  removeAllowedDir,
  type AllowedDirDTO,
} from "../../lib/settings";
import { folderName } from "../../lib/workspace";
import { IconWarning } from "../common/icons";
import { LockBadge, RowControl, useSettingLocked } from "./Locked";
import { useT } from "../../i18n/react";
import screen from "./SettingsScreen.module.css";

/**
 * Allowed directories for file access (PRV-10, privacy.allowedDirs): the
 * folders whose files a request may insert with {{$file PATH}}, besides
 * the open project's folder, which is always allowed. "Add folder…" shows
 * the native folder dialog; Remove takes a folder out (nothing on disk is
 * touched). A folder that is gone is listed with its error. The list is
 * an app setting, never part of a project, so opening someone's project
 * cannot widen it. Hand edits of the settings file show up as they arrive.
 */
export function AllowedDirRows({ shorten }: { shorten: (path: string) => string }) {
  const t = useT();
  const locked = useSettingLocked("privacy.allowedDirs");
  const [dirs, setDirs] = useState<AllowedDirDTO[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const errorId = useId();

  useEffect(() => {
    let live = true;
    const refresh = () => {
      listAllowedDirs().then(
        (next) => {
          if (live) setDirs(next);
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

  const act = (action: () => Promise<AllowedDirDTO[]>) => {
    if (busy) return;
    setBusy(true);
    setError(null);
    action()
      .then(setDirs, (err: unknown) => {
        if (!isCancelled(err)) setError(formatError(err));
      })
      .finally(() => setBusy(false));
  };

  return (
    <>
      <Row
        label={t("settings.allowedDirs.label")}
        locked={locked}
        description={t("settings.allowedDirs.description")}
        error={error}
        errorId={errorId}
      >
        {dirs.length === 0 && <span className={screen.unit}>{t("settings.none")}</span>}
        <button
          type="button"
          className={screen.smallButton}
          disabled={busy}
          aria-describedby={error ? errorId : undefined}
          onClick={() => act(addAllowedDir)}
        >
          {t("settings.allowedDirs.addFolder")}
        </button>
      </Row>
      {dirs.map((d) => {
        const name = folderName(d.path);
        return (
          <Row
            key={d.path}
            locked={locked}
            label={name}
            description={
              <span className={screen.path} title={d.path}>
                {shorten(d.path)}
              </span>
            }
            error={d.error ? formatError(d.error) : null}
          >
            <button
              type="button"
              className={screen.smallButton}
              disabled={busy}
              aria-label={t("settings.allowedDirs.removeLabel", { name })}
              title={t("settings.allowedDirs.removeTitle")}
              onClick={() => act(() => removeAllowedDir(d.path))}
            >
              {t("settings.remove")}
            </button>
          </Row>
        );
      })}
    </>
  );
}

/** A settings row, as SettingsScreen's (kept local to avoid an import cycle). */
function Row({
  label,
  description,
  error,
  errorId,
  locked = false,
  children,
}: {
  label: string;
  description?: ReactNode;
  error?: string | null;
  errorId?: string;
  /** Locked by the admin policy (SET-02): a lock, and the controls disabled. */
  locked?: boolean;
  children: ReactNode;
}) {
  return (
    <div className={screen.row} data-invalid={error ? "true" : undefined} data-locked={locked ? "true" : undefined}>
      <div className={screen.rowText}>
        <span className={screen.rowLabel}>
          {label}
          {locked && <LockBadge />}
        </span>
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
