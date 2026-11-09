import { useCallback, useEffect, useRef, useState } from "react";
import { allowDirectory, checkFileAccess } from "../../lib/settings";
import { formatError } from "../../lib/errors";
import { folderName } from "../../lib/workspace";
import { useT } from "../../i18n/react";
import { IconClose, IconInfo, IconWarning } from "./icons";
import styles from "./FileAccessNotice.module.css";

/** A folder a chosen file would need to be allowed (PRV-10). */
interface Offer {
  /** The folder to allow: where the file really is, links followed. */
  folder: string;
  /** The chosen paths in it, as stored. */
  paths: string[];
  /** An administrator manages the allowed directories (SET-02): no offer. */
  locked: boolean;
  /** "idle", "busy", "done" (allowed now), or an error to show. */
  state: "idle" | "busy" | "done" | { error: string };
}

/** What a file picker holds for the notice; see useFileAccessCheck. */
export interface FileAccessCheck {
  offers: readonly Offer[];
  /** Checks the chosen paths (as stored), replacing what was shown. */
  check: (paths: string | readonly string[]) => void;
  /** Hides the notice. */
  clear: () => void;
  /** Allows the folder of an offer. */
  allow: (folder: string) => void;
}

/**
 * The file-access check of a file picker (PRV-10). Features that read a
 * file named in a project (a JWT key, a schema, .proto files, a server
 * certificate) read it only from the project's folder or the allowed
 * directories of the settings (privacy.allowedDirs). After the user
 * picks a file, `check(path)` asks Go whether it may be read; one that may
 * not is listed by FileAccessNotice with an offer to allow its folder, or,
 * when an administrator locks the list, why that cannot be done here.
 * Nothing is shown for allowed files, or without a Go backend.
 */
export function useFileAccessCheck(): FileAccessCheck {
  const [offers, setOffers] = useState<Offer[]>([]);
  const seq = useRef(0);
  const live = useRef(true);
  useEffect(() => {
    live.current = true;
    return () => {
      live.current = false;
    };
  }, []);

  const check = useCallback((input: string | readonly string[]) => {
    const paths = (typeof input === "string" ? [input] : [...input]).filter((p) => p.trim() !== "");
    const n = ++seq.current;
    setOffers([]);
    if (paths.length === 0) return;
    Promise.all(
      paths.map((path) =>
        checkFileAccess(path).then(
          (res) => ({ path, res }),
          // No Go backend, or a path Go cannot place: nothing to offer.
          () => null,
        ),
      ),
    ).then((results) => {
      if (!live.current || n !== seq.current) return;
      const next: Offer[] = [];
      for (const r of results) {
        if (r === null || r.res.allowed || r.res.folder === "") continue;
        const same = next.find((o) => o.folder === r.res.folder);
        if (same) same.paths.push(r.path);
        else next.push({ folder: r.res.folder, paths: [r.path], locked: r.res.locked, state: "idle" });
      }
      setOffers(next);
    });
  }, []);

  const clear = useCallback(() => {
    seq.current++;
    setOffers([]);
  }, []);

  const allow = useCallback((folder: string) => {
    const set = (state: Offer["state"]) => setOffers((cur) => cur.map((o) => (o.folder === folder ? { ...o, state } : o)));
    set("busy");
    allowDirectory(folder).then(
      () => {
        if (live.current) set("done");
      },
      (err: unknown) => {
        if (live.current) set({ error: formatError(err) });
      },
    );
  }, []);

  return { offers, check, clear, allow };
}

/**
 * The notice of useFileAccessCheck, shown under a file picker: for each
 * folder with chosen files outside the allowed directories, what will
 * happen and a button that allows the folder (Settings, Privacy, Local
 * files), or that an administrator manages the list. Nothing when every
 * chosen file may be read. A status message, so screen readers read it
 * out after the dialog closes.
 */
export function FileAccessNotice({ access, className }: { access: FileAccessCheck; className?: string }) {
  const t = useT();
  const { offers, clear, allow } = access;
  if (offers.length === 0) return null;
  return (
    <div role="status" className={className ? `${styles.notice} ${className}` : styles.notice}>
      <ul className={styles.list}>
        {offers.map((o) => {
          const folder = folderName(o.folder);
          if (o.state === "done") {
            return (
              <li key={o.folder} className={styles.item} data-state="done">
                <IconInfo size={12} />
                <span>{t("common.fileAccess.allowed", { folder })}</span>
              </li>
            );
          }
          return (
            <li key={o.folder} className={styles.item}>
              <IconWarning size={12} />
              <span className={styles.text}>
                {o.paths.length === 1
                  ? t("common.fileAccess.outside", { name: folderName(o.paths[0]) })
                  : t("common.fileAccess.outsideMany", { count: o.paths.length })}{" "}
                {o.locked ? t("common.fileAccess.locked") : null}
                {typeof o.state === "object" ? <span className={styles.error}>{o.state.error}</span> : null}
              </span>
              {!o.locked && (
                <button
                  type="button"
                  className={styles.button}
                  disabled={o.state === "busy"}
                  title={t("common.fileAccess.allowTitle", { path: o.folder })}
                  onClick={() => allow(o.folder)}
                >
                  {t("common.fileAccess.allow", { folder })}
                </button>
              )}
            </li>
          );
        })}
      </ul>
      <button type="button" className={styles.dismiss} aria-label={t("common.fileAccess.dismiss")} title={t("common.fileAccess.dismiss")} onClick={clear}>
        <IconClose size={11} />
      </button>
    </div>
  );
}
