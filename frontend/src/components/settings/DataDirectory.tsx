import { useCallback, useEffect, useId, useState, type ReactNode } from "react";
import { restartToApply } from "../../commands/dataDir";
import {
  cancelDataDirChange,
  deletePreviousDataDir,
  forgetPreviousDataDir,
  getDataDirInfo,
  pickDataDir,
  revealDataDir,
  scheduleDataDir,
  type DataDirAction,
  type DataDirCandidate,
  type DataDirInfo,
} from "../../lib/dataDir";
import { toAppError, type AppError } from "../../lib/errors";
import { useStores } from "../../stores";
import { Dialog } from "../common/Dialog";
import { IconTrash, IconWarning } from "../common/icons";
import styles from "./DataDirectory.module.css";
import { t, type MessageKey } from "../../i18n";
import { Trans, useT } from "../../i18n/react";
import screen from "./SettingsScreen.module.css";

/**
 * The data directory (SET-03) and portable mode (APP-04) on the settings
 * screen and the welcome screen. A change is scheduled with Go and carried
 * out at the next start: a move copies the data, checks every file and
 * switches, keeping the old folder until the user deletes it here.
 */

/** Loads the data directory state; `null` until it arrives or when Go cannot say. */
export function useDataDirInfo(): [DataDirInfo | null, (info: DataDirInfo) => void] {
  const [info, setInfo] = useState<DataDirInfo | null>(null);
  useEffect(() => {
    let live = true;
    getDataDirInfo().then(
      (i) => live && setInfo(i),
      () => undefined,
    );
    return () => {
      live = false;
    };
  }, []);
  return [info, setInfo];
}

const ACTION_TEXT: Record<DataDirAction, { labelKey: MessageKey; helpKey: MessageKey }> = {
  move: { labelKey: "settings.dataDir.action.move", helpKey: "settings.dataDir.action.move.help" },
  fresh: { labelKey: "settings.dataDir.action.fresh", helpKey: "settings.dataDir.action.fresh.help" },
  use: { labelKey: "settings.dataDir.action.use", helpKey: "settings.dataDir.action.use.help" },
};

/** The actions that fit the chosen folder, the first being the default. */
export function actionsFor(c: DataDirCandidate): DataDirAction[] {
  if (c.hasData) return ["use"];
  return c.canMove ? ["move", "fresh"] : ["fresh"];
}

interface ChangeFlow {
  /** Shows the folder dialog, then the choice of action. */
  start: () => void;
  busy: boolean;
  error: AppError | null;
  /** The dialogs of the flow; render them once. */
  dialogs: ReactNode;
}

/** Choose… → folder dialog → what to do with the data → restart prompt. */
export function useDataDirChange(onInfo: (info: DataDirInfo) => void): ChangeFlow {
  const t = useT();
  const stores = useStores();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<AppError | null>(null);
  const [candidate, setCandidate] = useState<DataDirCandidate | null>(null);
  const [action, setAction] = useState<DataDirAction>("move");
  const [restartFor, setRestartFor] = useState<DataDirInfo | null>(null);
  const radioName = useId();
  const helpId = useId();

  const start = useCallback(() => {
    setError(null);
    setBusy(true);
    pickDataDir().then(
      (c) => {
        setBusy(false);
        if (c.path === "") return;
        setAction(actionsFor(c)[0]!);
        setCandidate(c);
      },
      (err: unknown) => {
        setBusy(false);
        setError(toAppError(err));
      },
    );
  }, []);

  const schedule = () => {
    if (candidate === null) return;
    setBusy(true);
    scheduleDataDir(candidate.path, action).then(
      (info) => {
        setBusy(false);
        setCandidate(null);
        onInfo(info);
        if (info.canRestart) setRestartFor(info);
      },
      (err: unknown) => {
        setBusy(false);
        setCandidate(null);
        setError(toAppError(err));
      },
    );
  };

  const restart = () => {
    setBusy(true);
    restartToApply(stores).catch((err: unknown) => {
      setBusy(false);
      setRestartFor(null);
      setError(toAppError(err));
    });
  };

  const dialogs = (
    <>
      <Dialog
        open={candidate !== null}
        title={t("settings.dataDir.changeTitle")}
        onCancel={() => setCandidate(null)}
        onConfirm={schedule}
        confirmLabel={t("settings.dataDir.changeConfirm")}
        busy={busy}
        width={480}
        describedBy={helpId}
      >
        {candidate !== null && (
          <>
            <p className={styles.dialogPath} title={candidate.path}>
              {candidate.path}
            </p>
            <div className={styles.choices} role="radiogroup" aria-label={t("settings.dataDir.choices")}>
              {actionsFor(candidate).map((a) => (
                <label key={a} className={styles.choice}>
                  <input
                    type="radio"
                    name={radioName}
                    value={a}
                    checked={a === action}
                    disabled={busy}
                    onChange={() => setAction(a)}
                  />
                  <span className={styles.choiceText}>
                    <span className={styles.choiceLabel}>{t(ACTION_TEXT[a].labelKey)}</span>
                    <span className={styles.choiceHelp}>{t(ACTION_TEXT[a].helpKey)}</span>
                  </span>
                </label>
              ))}
            </div>
            <p id={helpId} className={styles.note}>
              {t("settings.dataDir.takesEffect")}
            </p>
          </>
        )}
      </Dialog>
      <Dialog
        open={restartFor !== null}
        title={t("settings.dataDir.restartTitle")}
        onCancel={() => setRestartFor(null)}
        onConfirm={restart}
        confirmLabel={t("settings.dataDir.restartNow")}
        cancelLabel={t("settings.dataDir.later")}
        busy={busy}
      >
        <p className={styles.note}>{t("settings.dataDir.restartBody")}</p>
      </Dialog>
    </>
  );

  return { start, busy, error, dialogs };
}

function describePending(info: DataDirInfo): string {
  const p = info.pending!;
  switch (p.action) {
    case "move":
      return t("settings.dataDir.pending.move");
    case "fresh":
      return t("settings.dataDir.pending.fresh");
    default:
      return t("settings.dataDir.pending.use");
  }
}

function describeMode(mode: string): string {
  switch (mode) {
    case "portable":
      return t("settings.dataDir.mode.portable");
    case "env":
      return t("settings.dataDir.mode.env");
    default:
      return t("settings.dataDir.mode.default");
  }
}

function Problems({ problems }: { problems: readonly AppError[] }) {
  if (problems.length === 0) return null;
  return (
    <ul className={styles.problems} role="status">
      {problems.map((p, i) => (
        <li key={i} className={styles.problem}>
          <IconWarning size={13} />
          <span>
            {p.message}
            {p.hint && <span className={styles.hint}> {p.hint}</span>}
          </span>
        </li>
      ))}
    </ul>
  );
}

function ErrorLine({ error }: { error: AppError | null }) {
  if (error === null) return null;
  return (
    <span className={screen.rowError} role="alert">
      <IconWarning size={13} />
      {error.message}
      {error.hint ? ` ${error.hint}` : ""}
    </span>
  );
}

export interface DataDirectoryRowsProps {
  /** Shown while the state loads, or when Go cannot say (SettingsService.Location's dataDir). */
  fallbackPath: string;
  /** Formats a path for the row (SettingsScreen's shortenPath). */
  shorten: (path: string) => string;
}

/** The data directory rows of Settings, General (design: SettingsGeneral, Data). */
export function DataDirectoryRows({ fallbackPath, shorten }: DataDirectoryRowsProps) {
  const t = useT();
  const [info, setInfo] = useDataDirInfo();
  const change = useDataDirChange(setInfo);
  const stores = useStores();
  const [error, setError] = useState<AppError | null>(null);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [busy, setBusy] = useState(false);

  const act = (run: () => Promise<DataDirInfo | void>) => {
    setError(null);
    setBusy(true);
    run().then(
      (next) => {
        setBusy(false);
        if (next) setInfo(next);
      },
      (err: unknown) => {
        setBusy(false);
        setError(toAppError(err));
      },
    );
  };

  const path = info ? info.dataDir || info.wanted : fallbackPath;
  const problems = (info?.problems ?? []).map((p) => toAppError(p));
  const blocked = info?.changeBlocked ? toAppError(info.changeBlocked) : null;
  const anyError = change.error ?? error;

  return (
    <>
      <div className={screen.row} data-invalid={anyError || problems.length > 0 ? "true" : undefined}>
        <div className={screen.rowText}>
          <span className={screen.rowLabel}>{t("settings.dataDir.label")}</span>
          <span className={screen.rowDescription}>{describeMode(info?.mode ?? "default")}</span>
          {blocked && <span className={screen.rowDescription}>{blocked.hint}</span>}
          <Problems problems={problems} />
          <ErrorLine error={anyError} />
        </div>
        <div className={screen.rowControl}>
          <span className={screen.path} title={path || undefined}>
            {path ? shorten(path) : t("settings.dataDir.unknown")}
          </span>
          {info?.dataDir && (
            <button type="button" className={screen.smallButton} onClick={() => act(() => revealDataDir("data"))} disabled={busy}>
              {t("settings.show")}
            </button>
          )}
          <button
            type="button"
            className={screen.smallButton}
            onClick={change.start}
            disabled={info === null || blocked !== null || change.busy}
            title={blocked ? `${blocked.message} ${blocked.hint}` : undefined}
          >
            {t("settings.dataDir.change")}
          </button>
        </div>
      </div>

      {info?.pending && (
        <div className={screen.row}>
          <div className={screen.rowText}>
            <span className={screen.rowLabel}>{t("settings.dataDir.afterRestart")}</span>
            <span className={screen.rowDescription}>{describePending(info)}</span>
            <span className={screen.path} title={info.pending.dataDir}>
              {info.pending.dataDir}
            </span>
          </div>
          <div className={screen.rowControl}>
            <button type="button" className={screen.smallButton} onClick={() => act(cancelDataDirChange)} disabled={busy}>
              {t("settings.dataDir.cancelChange")}
            </button>
            {info.canRestart && (
              <button
                type="button"
                className={screen.smallButton}
                onClick={() => act(() => restartToApply(stores))}
                disabled={busy}
              >
                {t("settings.dataDir.restartNow")}
              </button>
            )}
          </div>
        </div>
      )}

      {info?.previous && (
        <div className={screen.row}>
          <div className={screen.rowText}>
            <span className={screen.rowLabel}>{t("settings.dataDir.old")}</span>
            <span className={screen.rowDescription}>
              {info.moved ? t("settings.dataDir.oldMoved") : ""}
              {t("settings.dataDir.oldCopy")}
            </span>
            <span className={screen.path} title={info.previous.dataDir}>
              {info.previous.dataDir}
            </span>
          </div>
          <div className={screen.rowControl}>
            <button type="button" className={screen.smallButton} onClick={() => act(() => revealDataDir("previous"))} disabled={busy}>
              {t("settings.show")}
            </button>
            <button type="button" className={screen.smallButton} onClick={() => act(forgetPreviousDataDir)} disabled={busy}>
              {t("settings.dataDir.keep")}
            </button>
            <button type="button" className={screen.smallButton} onClick={() => setConfirmDelete(true)} disabled={busy}>
              {t("settings.dataDir.delete")}
            </button>
          </div>
        </div>
      )}

      {change.dialogs}
      <Dialog
        open={confirmDelete}
        title={t("settings.dataDir.deleteTitle")}
        variant="danger"
        confirmLabel={t("settings.dataDir.deleteConfirm")}
        confirmIcon={<IconTrash size={13} />}
        onCancel={() => setConfirmDelete(false)}
        onConfirm={() => {
          setConfirmDelete(false);
          act(deletePreviousDataDir);
        }}
      >
        <p className={styles.note}>
          <Trans
            k="settings.dataDir.deleteBody"
            values={{ folder: <span className={styles.inlinePath}>{info?.previous?.dataDir}</span> }}
          />
        </p>
      </Dialog>
    </>
  );
}

/**
 * On the welcome screen: why there is no usable data directory (a portable
 * folder that cannot be written, a chosen folder on a disk that is not
 * connected, a failed move), with a way out. Nothing when all is well.
 */
export function DataDirNotice() {
  const t = useT();
  const [info, setInfo] = useDataDirInfo();
  const change = useDataDirChange(setInfo);
  const stores = useStores();
  const [error, setError] = useState<AppError | null>(null);
  const problems = (info?.problems ?? []).map((p) => toAppError(p));
  if (info === null || (problems.length === 0 && !info.pending)) return null;
  const anyError = change.error ?? error;

  return (
    <section className={styles.notice} aria-label={t("settings.dataDir.label")}>
      <Problems problems={problems} />
      {info.pending && (
        <p className={styles.note}>
          {describePending(info)} <span className={styles.inlinePath}>{info.pending.dataDir}</span>
        </p>
      )}
      <div className={styles.noticeButtons}>
        {info.changeBlocked === null && (
          <button type="button" className={screen.smallButton} onClick={change.start} disabled={change.busy}>
            {t("settings.dataDir.chooseAnother")}
          </button>
        )}
        {info.pending && info.canRestart && (
          <button
            type="button"
            className={screen.smallButton}
            onClick={() => restartToApply(stores).catch((err: unknown) => setError(toAppError(err)))}
          >
            {t("settings.dataDir.restartNow")}
          </button>
        )}
      </div>
      <ErrorLine error={anyError} />
      {change.dialogs}
    </section>
  );
}
