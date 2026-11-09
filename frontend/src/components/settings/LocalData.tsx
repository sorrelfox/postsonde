import { useEffect, useId, useState, type ReactNode } from "react";
import { formatError, isCancelled, toAppError } from "../../lib/errors";
import {
  clearLocalData,
  LOCAL_DATA_CATEGORIES,
  localDataUsage,
  type ClearResultDTO,
  type LocalDataCategory,
  type LocalDataScope,
  type UsageDTO,
} from "../../lib/localData";
import type { MessageKey } from "../../i18n";
import { useT } from "../../i18n/react";
import { Dialog } from "../common/Dialog";
import { IconTrash, IconWarning } from "../common/icons";
import { formatSize } from "../messagelog/format";
import styles from "./LocalData.module.css";
import screen from "./SettingsScreen.module.css";

/** The label and description of each kind of local data. */
const TEXT: Readonly<Record<LocalDataCategory, { label: MessageKey; description: MessageKey }>> = {
  history: { label: "settings.localData.history", description: "settings.localData.historyDescription" },
  runs: { label: "settings.localData.runs", description: "settings.localData.runsDescription" },
  cookies: { label: "settings.localData.cookies", description: "settings.localData.cookiesDescription" },
  tokens: { label: "settings.localData.tokens", description: "settings.localData.tokensDescription" },
  schemas: { label: "settings.localData.schemas", description: "settings.localData.schemasDescription" },
  runtimeVariables: {
    label: "settings.localData.runtimeVariables",
    description: "settings.localData.runtimeVariablesDescription",
  },
};

/** What Go measured: every project's, and the chosen scope's. */
interface Loaded {
  all: UsageDTO;
  shown: UsageDTO;
}

/**
 * Settings, Privacy, Local data (PRV-13): what PostSonde keeps about the
 * projects on this computer (the request and run histories, cookies,
 * OAuth 2.0 tokens, cached GraphQL and gRPC schemas, runtime variables),
 * how much of each, for the open project or for all of them, and one action that
 * clears the kinds ticked, after a confirmation the user types. Go tells
 * the services that hold them in memory, then deletes the files, so it
 * works while the secrets are locked. Shows nothing until Go has answered
 * (no backend in some tests).
 */
export function LocalDataRows() {
  const t = useT();
  const scopeId = useId();
  const [scope, setScope] = useState<LocalDataScope>("project");
  const [loaded, setLoaded] = useState<Loaded | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [attempt, setAttempt] = useState(0);
  const [picked, setPicked] = useState<ReadonlySet<LocalDataCategory>>(() => new Set(LOCAL_DATA_CATEGORIES));
  const [confirming, setConfirming] = useState(false);
  const [result, setResult] = useState<ClearResultDTO | null>(null);

  useEffect(() => {
    let live = true;
    const load = async (): Promise<Loaded> => {
      const all = await localDataUsage("all");
      // Without an open project there is only "all".
      const shown = scope === "project" && all.project !== "" ? await localDataUsage("project") : all;
      return { all, shown };
    };
    // A promise even when the call throws at once (no backend).
    Promise.resolve()
      .then(load)
      .then(
        (next) => {
          if (!live) return;
          setLoaded(next);
          setLoadError(null);
        },
        (err: unknown) => {
          if (!live || isCancelled(err)) return;
          const appErr = toAppError(err);
          // An error of Go's is shown; no backend at all shows nothing.
          setLoadError(appErr.code !== "" ? formatError(err) : null);
        },
      );
    return () => {
      live = false;
    };
  }, [scope, attempt]);

  if (loaded === null) {
    return loadError === null ? null : (
      <Row label={t("settings.group.localData")} error={loadError}>
        {null}
      </Row>
    );
  }
  const { all, shown } = loaded;
  const effectiveScope: LocalDataScope = all.project === "" ? "all" : scope;
  const usageOf = (c: LocalDataCategory) => (shown.categories ?? []).find((u) => u.category === c) ?? { category: c, files: 0, bytes: 0 };
  const sizeText = (c: LocalDataCategory) => {
    const u = usageOf(c);
    return t("settings.localData.size", { count: u.files, size: formatSize(u.bytes) });
  };
  const chosen = LOCAL_DATA_CATEGORIES.filter((c) => picked.has(c));
  const toggle = (c: LocalDataCategory, on: boolean) => {
    setPicked((prev) => {
      const next = new Set(prev);
      if (on) next.add(c);
      else next.delete(c);
      return next;
    });
  };

  return (
    <>
      <Row
        label={t("settings.localData.scope.label")}
        controlId={scopeId}
        description={t("settings.localData.scope.description")}
      >
        <select
          id={scopeId}
          className={screen.select}
          value={effectiveScope}
          onChange={(e) => {
            setResult(null);
            setScope(e.currentTarget.value === "all" ? "all" : "project");
          }}
        >
          {all.project !== "" && <option value="project">{t("settings.localData.scope.project", { name: all.project })}</option>}
          <option value="all">{t("settings.localData.scope.all", { count: all.projects })}</option>
        </select>
      </Row>
      {LOCAL_DATA_CATEGORIES.map((c) => (
        <CategoryRow
          key={c}
          label={t(TEXT[c].label)}
          description={t(TEXT[c].description)}
          size={sizeText(c)}
          checked={picked.has(c)}
          onChange={(on) => toggle(c, on)}
        />
      ))}
      <Row label={t("settings.localData.clearRow")} description={t("settings.localData.clearDescription")}>
        <button
          type="button"
          className={screen.smallButton}
          disabled={chosen.length === 0}
          onClick={() => {
            setResult(null);
            setConfirming(true);
          }}
        >
          {t("settings.localData.clear")}
        </button>
      </Row>
      {result !== null && <ClearResult result={result} />}
      {confirming && (
        <ClearDialog
          scope={effectiveScope}
          usage={shown}
          categories={chosen}
          sizeText={sizeText}
          onClose={() => setConfirming(false)}
          onDone={(res) => {
            setConfirming(false);
            setResult(res);
            setAttempt((n) => n + 1);
          }}
        />
      )}
    </>
  );
}

/** A kind of local data: its checkbox, what it covers and its size. */
function CategoryRow({
  label,
  description,
  size,
  checked,
  onChange,
}: {
  label: string;
  description: string;
  size: string;
  checked: boolean;
  onChange: (on: boolean) => void;
}) {
  const id = useId();
  return (
    <Row
      label={label}
      controlId={id}
      description={
        <>
          {description} <span className={styles.size}>{size}</span>
        </>
      }
    >
      <input id={id} type="checkbox" className={screen.checkbox} checked={checked} onChange={(e) => onChange(e.currentTarget.checked)} />
    </Row>
  );
}

/** What the last clear removed, and what it could not. */
function ClearResult({ result }: { result: ClearResultDTO }) {
  const t = useT();
  const removed = result.removed ?? [];
  const files = removed.reduce((n, r) => n + r.files, 0);
  const bytes = removed.reduce((n, r) => n + r.bytes, 0);
  return (
    <div className={styles.result}>
      <p className={styles.done} role="status">
        {t("settings.localData.done", { count: files, size: formatSize(bytes) })}
      </p>
      {(result.failed ?? []).map((f) => (
        <p key={f.category} className={styles.failed} role="alert">
          <IconWarning size={13} />
          {t("settings.localData.failed", {
            label: t(TEXT[f.category as LocalDataCategory]?.label ?? "settings.group.localData"),
            message: f.hint !== "" ? `${f.message} ${f.hint}` : f.message,
          })}
        </p>
      ))}
    </div>
  );
}

/**
 * The confirmation: what will be removed and for which projects, and the
 * confirmation word to type, since nothing brings the data back.
 */
function ClearDialog({
  scope,
  usage,
  categories,
  sizeText,
  onClose,
  onDone,
}: {
  scope: LocalDataScope;
  usage: UsageDTO;
  categories: LocalDataCategory[];
  sizeText: (c: LocalDataCategory) => string;
  onClose: () => void;
  onDone: (result: ClearResultDTO) => void;
}) {
  const t = useT();
  const ids = { body: useId(), word: useId() };
  const word = t("settings.localData.confirm.word");
  const [typed, setTyped] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const ready = typed.trim() === word;

  const confirm = () => {
    if (!ready || busy) return;
    setBusy(true);
    setError(null);
    Promise.resolve()
      .then(() => clearLocalData(scope, categories))
      .then(
        (res) => {
          setBusy(false);
          onDone(res);
        },
        (err: unknown) => {
          setBusy(false);
          if (!isCancelled(err)) setError(formatError(err));
        },
      );
  };

  return (
    <Dialog
      open
      title={t("settings.localData.confirm.title")}
      onCancel={onClose}
      onConfirm={confirm}
      confirmLabel={t("settings.localData.confirm.button")}
      confirmIcon={<IconTrash size={14} />}
      confirmDisabled={!ready}
      busy={busy}
      variant="danger"
      describedBy={ids.body}
    >
      <div className={styles.form}>
        <div id={ids.body} className={styles.help}>
          <p>
            {scope === "project"
              ? t("settings.localData.confirm.bodyProject", { name: usage.project })
              : t("settings.localData.confirm.bodyAll", { count: usage.projects })}
          </p>
          <ul className={styles.list}>
            {categories.map((c) => (
              <li key={c}>{t("settings.localData.confirm.item", { label: t(TEXT[c].label), size: sizeText(c) })}</li>
            ))}
          </ul>
          <p>{t("settings.localData.clearDescription")}</p>
        </div>
        <div className={styles.field}>
          <label className={styles.label} htmlFor={ids.word}>
            {t("settings.localData.confirm.typeLabel", { word })}
          </label>
          <input
            id={ids.word}
            className={styles.input}
            value={typed}
            autoComplete="off"
            spellCheck={false}
            onChange={(e) => setTyped(e.target.value)}
            data-autofocus
          />
        </div>
        {error && (
          <p className={styles.failed} role="alert">
            <IconWarning size={13} />
            {error}
          </p>
        )}
      </div>
    </Dialog>
  );
}

/** A settings row, as SettingsScreen's (kept local to avoid an import cycle). */
function Row({
  label,
  controlId,
  description,
  error,
  children,
}: {
  label: string;
  controlId?: string;
  description?: ReactNode;
  error?: string | null;
  children: ReactNode;
}) {
  return (
    <div className={screen.row} data-invalid={error ? "true" : undefined}>
      <div className={screen.rowText}>
        {controlId ? (
          <label className={screen.rowLabel} htmlFor={controlId}>
            {label}
          </label>
        ) : (
          <span className={screen.rowLabel}>{label}</span>
        )}
        {description && <span className={screen.rowDescription}>{description}</span>}
        {error && (
          <span className={screen.rowError} role="alert">
            <IconWarning size={13} />
            {error}
          </span>
        )}
      </div>
      <div className={screen.rowControl}>{children}</div>
    </div>
  );
}
