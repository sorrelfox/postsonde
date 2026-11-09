import { useCallback, useEffect, useId, useMemo, useRef, useState } from "react";
import { importFile } from "../../commands/collection";
import {
  IMPORT_CODES,
  importFormats,
  pickImportFile,
  previewImport,
  END,
  type ImportDiagnosticDTO,
  type ImportFormatDTO,
  type ImportItemDTO,
  type ImportReportDTO,
} from "../../lib/collection";
import { t as translate } from "../../i18n";
import { Trans, useT } from "../../i18n/react";
import { toAppError } from "../../lib/errors";
import {
  importProblems,
  importRows,
  locationLabel,
  previewSummary,
  reportSummary,
  worstSeverity,
  type ImportRow,
} from "../../lib/importReport";
import { useCollection, useStores, useWorkspace, type NodeId } from "../../stores";
import { Dialog } from "../common/Dialog";
import { IconEnvironments, IconFile, IconFolder, IconInfo, IconWarning } from "../common/icons";
import { methodBadge, severityLabel, type Badge } from "./badges";
import { FolderSelect } from "./FolderSelect";
import { freeName } from "./ImportCurlDialog";
import styles from "./ImportDialog.module.css";
import { defaultTarget, folderOptions, TOP_LEVEL, validateName } from "./newItem";
import fieldStyles from "./NewItemDialog.module.css";

/** The Import dialog's request: the node it was asked for (its folder is the target) and, optionally, a file to preview at once. */
export interface ImportRequest {
  readonly from: NodeId | null;
  readonly path?: string;
  /** The format Go detected for `path` (a dropped file, IMP-11): the Format select starts on it. */
  readonly detectedFormat?: string;
  /** Where this file is in a drop of several, shown in the title: "Import (2 of 3)". */
  readonly position?: { readonly index: number; readonly total: number };
}

/** The dialog title, with the file's place in a drop of several. */
export function importTitle(base: string, request: Pick<ImportRequest, "position">): string {
  const p = request.position;
  return p === undefined || p.total < 2 ? base : translate("tree.import.titlePosition", { title: base, index: p.index, total: p.total });
}

export interface ImportDialogProps {
  /** null: closed. A new request object opens a fresh dialog. */
  request: ImportRequest | null;
  onClose: () => void;
}

/** The preview lists at most this many rows; the rest is counted. */
export const MAX_PREVIEW_ROWS = 500;

/**
 * The Import dialog (IMP-10, docs/frontend.md "Import"): choose a file
 * (native dialog), see what it would create (a tree of the folders,
 * requests and environments, what is skipped and every diagnostic with
 * its place in the file), choose where it goes, import, and read the
 * report. Go reads, detects and writes (CollectionService PreviewImport
 * and Import); nothing is written before Import.
 */
export function ImportDialog({ request, onClose }: ImportDialogProps) {
  if (request === null) return null;
  return <ImportFlow request={request} onClose={onClose} />;
}

type Step =
  | { readonly kind: "choose" }
  | { readonly kind: "preview"; readonly preview: ImportReportDTO }
  | { readonly kind: "report"; readonly report: ImportReportDTO };

function ImportFlow({ request, onClose }: { request: ImportRequest; onClose: () => void }) {
  const t = useT();
  const stores = useStores();
  const state = useCollection((s) => s);
  const projectName = useWorkspace((s) => s.current?.name) ?? t("tree.project");

  const [step, setStep] = useState<Step>({ kind: "choose" });
  const [formats, setFormats] = useState<ImportFormatDTO[]>([]);
  const [format, setFormat] = useState(request.detectedFormat ?? "");
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);
  const [parentId, setParentId] = useState<NodeId>(() => defaultTarget(state, request.from));
  const [name, setName] = useState("");
  const [nameEdited, setNameEdited] = useState(false);
  const [newFolder, setNewFolder] = useState(true);
  const [withEnvironments, setWithEnvironments] = useState(true);

  const baseId = useId();
  const nameRef = useRef<HTMLInputElement>(null);
  // Only the latest preview request counts.
  const previewSeq = useRef(0);

  useEffect(() => {
    let current = true;
    importFormats().then(
      (list) => {
        if (current) setFormats(list);
      },
      () => undefined,
    );
    return () => {
      current = false;
    };
  }, []);

  const options = useMemo(() => folderOptions(state, projectName), [state, projectName]);
  const target = options.some((o) => o.id === parentId && !o.disabled) ? parentId : TOP_LEVEL;

  const preview = useCallback(async (path: string, formatId: string) => {
    const seq = ++previewSeq.current;
    setBusy(true);
    setFailure(null);
    try {
      const result = await previewImport(path, formatId);
      if (seq !== previewSeq.current) return;
      setStep({ kind: "preview", preview: result });
    } catch (err: unknown) {
      if (seq !== previewSeq.current) return;
      const e = toAppError(err);
      setFailure(e.hint === "" ? e.message : `${e.message} ${e.hint}`);
    } finally {
      if (seq === previewSeq.current) setBusy(false);
    }
  }, []);

  // A file handed over (dropped on the window, IMP-11) is previewed at once, in the detected format.
  useEffect(() => {
    if (request.path !== undefined && request.path !== "") void preview(request.path, request.detectedFormat ?? "");
  }, [request.path, request.detectedFormat, preview]);

  const choose = async () => {
    setFailure(null);
    let path: string;
    try {
      path = await pickImportFile();
    } catch (err: unknown) {
      setFailure(toAppError(err).message);
      return;
    }
    if (path !== "") await preview(path, format);
  };

  const formatField = (path: string | null) => (
    <div className={fieldStyles.field}>
      <label className={fieldStyles.label} htmlFor={`${baseId}-format`}>
        {t("tree.import.format")}
      </label>
      <select
        id={`${baseId}-format`}
        className={`${fieldStyles.input} ${styles.formatSelect}`}
        value={format}
        disabled={busy}
        onChange={(e) => {
          const next = e.target.value;
          setFormat(next);
          if (path !== null) void preview(path, next);
        }}
      >
        <option value="">{t("tree.import.detect")}</option>
        {formats.map((f) => (
          <option key={f.id} value={f.id}>
            {f.name}
          </option>
        ))}
      </select>
    </div>
  );

  const failureLine = failure !== null && (
    <span className={fieldStyles.error} role="alert">
      {failure}
    </span>
  );

  if (step.kind === "choose") {
    return (
      <Dialog
        open
        title={importTitle(t("tree.import.title"), request)}
        confirmLabel={t("tree.import.chooseFile")}
        onCancel={onClose}
        onConfirm={() => void choose()}
        busy={busy}
        width={560}
      >
        <p className={styles.intro}>{t("tree.import.intro")}</p>
        {formats.length > 0 && (
          <p className={styles.formats}>
            <Trans
              k="tree.import.formats"
              values={{
                formats: formats.map((f, i) => (
                  <span key={f.id}>
                    {i > 0 && (i === formats.length - 1 ? t("tree.import.formatsLastSeparator") : t("tree.import.formatsSeparator"))}
                    {f.name}
                    {(f.extensions ?? []).length > 0 && <span className={styles.ext}> ({(f.extensions ?? []).join(" ")})</span>}
                  </span>
                )),
              }}
            />
          </p>
        )}
        {formatField(null)}
        {failureLine}
      </Dialog>
    );
  }

  if (step.kind === "report") {
    return <ReportStep report={step.report} title={importTitle(t("tree.import.reportTitle"), request)} onClose={onClose} />;
  }

  const p = step.preview;
  const items = p.items ?? [];
  const root = items[0];
  const suggested = freeName(state, target, root?.name ?? "");
  const shownName = nameEdited ? name : suggested;
  const environments = items.filter((i) => i.kind === "environment").length;
  // No folder or request (only environments or skipped items): Go makes no folder, so there is nothing to place.
  const hasItems = items.some((i, index) => index > 0 && (i.kind === "folder" || i.kind === "request"));
  const makesFolder = hasItems && newFolder;
  const nameError = makesFolder ? validateName(state, target, shownName) : null;
  const shownNameError = nameError !== null && nameError !== "" ? nameError : null;
  const counts = p.counts;
  const creates = counts.requests > 0 || counts.folders > 1 || (environments > 0 && withEnvironments);
  const canImport = !busy && nameError === null && creates;
  const rows = importRows(items, makesFolder).filter((r) => withEnvironments || r.item.kind !== "environment");

  const confirm = async () => {
    if (!canImport) return;
    setBusy(true);
    setFailure(null);
    try {
      const result = await importFile(stores, {
        path: p.path,
        format: p.format.id,
        digest: p.digest,
        parentId: target,
        folderName: makesFolder ? shownName.trim() : "",
        intoParent: !newFolder,
        skipEnvironments: !withEnvironments,
        index: END,
      });
      if (result === null) {
        onClose();
        return;
      }
      setBusy(false);
      setStep({ kind: "report", report: result.report });
    } catch (err: unknown) {
      const e = toAppError(err);
      setBusy(false);
      if (e.code === IMPORT_CODES.sourceChanged) {
        await preview(p.path, format);
        setFailure(t("tree.import.sourceChanged"));
        return;
      }
      setFailure(e.hint === "" ? e.message : `${e.message} ${e.hint}`);
    }
  };

  return (
    <Dialog
      open
      title={importTitle(t("tree.import.title"), request)}
      confirmLabel={t("tree.import.confirm")}
      onCancel={onClose}
      onConfirm={() => void confirm()}
      confirmDisabled={!canImport}
      busy={busy}
      width={680}
      initialFocus={makesFolder ? nameRef : undefined}
    >
      <div className={styles.fileLine}>
        <IconFile size={14} />
        <span className={styles.fileName} title={p.path}>
          {p.fileName}
        </span>
        <span className={styles.formatName}>
          {format === "" || (format === request.detectedFormat && p.path === request.path)
            ? t("tree.import.detected", { format: p.format.name })
            : p.format.name}
        </span>
        <button type="button" className={styles.linkButton} disabled={busy} onClick={() => void choose()}>
          {t("tree.import.chooseAnother")}
        </button>
      </div>

      {formatField(p.path)}

      <p className={styles.summary} aria-live="polite">
        {previewSummary(counts)}
      </p>

      <FileDiagnostics diagnostics={p.diagnostics ?? []} />

      <PreviewTree rows={rows} rootName={makesFolder ? shownName.trim() : ""} />

      {hasItems && (
        <div className={fieldStyles.field}>
        <label className={fieldStyles.label} htmlFor={`${baseId}-folder`}>
          {t("tree.import.into")}
        </label>
        <FolderSelect
          id={`${baseId}-folder`}
          options={options}
          value={target}
          disabled={busy}
          onChange={(id) => {
            setParentId(id);
            setFailure(null);
          }}
        />
        </div>
      )}

      <div className={styles.checks}>
        {hasItems && (
          <label className={styles.check}>
            <input type="checkbox" checked={newFolder} disabled={busy} onChange={(e) => setNewFolder(e.target.checked)} />
            {t("tree.import.newFolder")}
          </label>
        )}
        {environments > 0 && (
          <label className={styles.check}>
            <input
              type="checkbox"
              checked={withEnvironments}
              disabled={busy}
              onChange={(e) => setWithEnvironments(e.target.checked)}
            />
            {t("tree.import.environments", { count: environments })}
          </label>
        )}
      </div>

      {makesFolder && (
        <div className={fieldStyles.field}>
          <label className={fieldStyles.label} htmlFor={`${baseId}-name`}>
            {t("tree.folderName")}
          </label>
          <input
            id={`${baseId}-name`}
            ref={nameRef}
            className={fieldStyles.input}
            value={shownName}
            placeholder={t("tree.folderName")}
            maxLength={1000}
            spellCheck={false}
            autoComplete="off"
            readOnly={busy}
            aria-invalid={shownNameError !== null || undefined}
            aria-describedby={shownNameError !== null ? `${baseId}-name-error` : undefined}
            onChange={(e) => {
              setName(e.target.value);
              setNameEdited(true);
              setFailure(null);
            }}
          />
          {shownNameError !== null && (
            <span className={fieldStyles.error} id={`${baseId}-name-error`} role="alert">
              {shownNameError}
            </span>
          )}
        </div>
      )}
      {hasItems && !newFolder && (
        <p className={styles.note}>{t("tree.import.intoParentNote")}</p>
      )}
      {failureLine}
    </Dialog>
  );
}

function FileDiagnostics({ diagnostics }: { diagnostics: readonly ImportDiagnosticDTO[] }) {
  const t = useT();
  if (diagnostics.length === 0) return null;
  return (
    <ul className={styles.fileDiagnostics} aria-label={t("tree.import.aboutFile")}>
      {diagnostics.map((d, i) => (
        <DiagnosticLine key={i} d={d} />
      ))}
    </ul>
  );
}

function DiagnosticLine({ d }: { d: ImportDiagnosticDTO }) {
  useT(); // Re-renders in a new language: severityLabel reads the catalogue.
  const where = locationLabel(d.source);
  return (
    <li className={styles.diagnostic} data-severity={d.severity}>
      {d.severity === "info" ? <IconInfo size={13} /> : <IconWarning size={13} />}
      <span className={styles.severity}>{severityLabel(d.severity)}</span>
      <span className={styles.message}>
        {d.message}
        {where !== "" && <span className={styles.where}> {where}</span>}
      </span>
    </li>
  );
}

function rowBadge(item: ImportItemDTO): Badge | null {
  if (item.kind !== "request") return null;
  if (item.fileKind === "http" || item.fileKind === "") return methodBadge(item.method);
  const kinds: Record<string, Badge> = {
    graphql: { label: "GQL", tone: "gql" },
    websocket: { label: "WS", tone: "ws" },
    socketio: { label: "SIO", tone: "ws" },
    grpc: { label: "GRPC", tone: "dir" },
    tcp: { label: "TCP", tone: "dir" },
  };
  return kinds[item.fileKind] ?? { label: item.fileKind.toUpperCase().slice(0, 5), tone: "dir" };
}

/** The tree of what the import creates, each row with its diagnostics behind a toggle. */
function PreviewTree({ rows, rootName }: { rows: readonly ImportRow[]; rootName: string }) {
  const t = useT();
  const [open, setOpen] = useState<ReadonlySet<number>>(new Set());
  const shown = rows.slice(0, MAX_PREVIEW_ROWS);
  const toggle = (index: number) =>
    setOpen((prev) => {
      const next = new Set(prev);
      if (next.has(index)) next.delete(index);
      else next.add(index);
      return next;
    });
  return (
    <div className={styles.tree}>
      <ul className={styles.rows} aria-label={t("tree.import.creates")}>
        {shown.map(({ index, item, depth }) => {
          const diagnostics = item.diagnostics ?? [];
          const worst = worstSeverity(diagnostics);
          const badge = rowBadge(item);
          const name = index === 0 && item.kind === "folder" ? rootName || item.name : item.name;
          const expanded = open.has(index);
          return (
            <li key={index} className={styles.row} data-status={item.status} data-kind={item.kind}>
              <div className={styles.rowLine} style={{ paddingLeft: `calc(${depth} * var(--space-8) + var(--space-3))` }}>
                <span className={styles.icon}>
                  {item.kind === "folder" && <IconFolder size={14} />}
                  {item.kind === "environment" && <IconEnvironments size={14} />}
                  {badge !== null && (
                    <span className={styles.badge} data-tone={badge.tone}>
                      {badge.label}
                    </span>
                  )}
                  {item.kind === "skipped" && <span className={styles.skippedBadge}>{t("tree.import.skipBadge")}</span>}
                </span>
                <span className={styles.name}>{name || t("tree.import.untitled")}</span>
                {item.kind === "skipped" && item.skippedKind !== "" && <span className={styles.detail}>{item.skippedKind}</span>}
                {item.detail !== "" && <span className={styles.detail}>{item.detail}</span>}
                {item.status === "failed" && <span className={styles.failed}>{t("tree.import.failed")}</span>}
                {worst !== null && (
                  <button
                    type="button"
                    className={styles.diagToggle}
                    data-severity={worst}
                    aria-expanded={expanded}
                    title={expanded ? t("tree.import.hideNotes") : t("tree.import.showNotes")}
                    onClick={() => toggle(index)}
                  >
                    {worst === "info" ? <IconInfo size={12} /> : <IconWarning size={12} />}
                    {diagnostics.length}
                  </button>
                )}
              </div>
              {expanded && (
                <ul className={styles.rowDiagnostics} style={{ paddingLeft: `calc(${depth} * var(--space-8) + 44px)` }}>
                  {diagnostics.map((d, i) => (
                    <DiagnosticLine key={i} d={d} />
                  ))}
                </ul>
              )}
            </li>
          );
        })}
      </ul>
      {rows.length > shown.length && <p className={styles.more}>{t("tree.import.more", { count: rows.length - shown.length })}</p>}
    </div>
  );
}

/** The report after an import: the summary and every item that was skipped, failed or converted with a loss. */
function ReportStep({ report, title, onClose }: { report: ImportReportDTO; title: string; onClose: () => void }) {
  const t = useT();
  const [withInfo, setWithInfo] = useState(false);
  const problems = importProblems(report, withInfo);
  const hasInfo = importProblems(report, true).length > importProblems(report, false).length;
  const c = report.counts;
  return (
    <Dialog open title={title} confirmLabel={t("tree.import.done")} onCancel={onClose} onConfirm={onClose} hideCancel width={680}>
      <p className={styles.reportSummary} role="status">
        {reportSummary(c)}
      </p>
      <dl className={styles.counts}>
        <div>
          <dt>{t("tree.import.count.folders")}</dt>
          <dd>{c.folders}</dd>
        </div>
        <div>
          <dt>{t("tree.import.count.requests")}</dt>
          <dd>{c.requests}</dd>
        </div>
        <div>
          <dt>{t("tree.import.count.environments")}</dt>
          <dd>{c.environments}</dd>
        </div>
        <div data-tone={c.skipped > 0 ? "warn" : undefined}>
          <dt>{t("tree.import.count.skipped")}</dt>
          <dd>{c.skipped}</dd>
        </div>
        <div data-tone={c.lossy > 0 ? "warn" : undefined}>
          <dt>{t("tree.import.count.lossy")}</dt>
          <dd>{c.lossy}</dd>
        </div>
        <div data-tone={c.failed > 0 ? "bad" : undefined}>
          <dt>{t("tree.import.count.failed")}</dt>
          <dd>{c.failed}</dd>
        </div>
      </dl>
      {problems.length === 0 ? (
        <p className={styles.note}>{t("tree.import.nothingChanged")}</p>
      ) : (
        <ul className={styles.problems} aria-label={t("tree.import.problems")}>
          {problems.map((pr) => (
            <li key={pr.index} className={styles.problem}>
              <div className={styles.problemHead}>
                <span className={styles.problemName}>{pr.label}</span>
                {pr.status === "skipped" && <span className={styles.skippedBadge}>{t("tree.import.skipped")}</span>}
                {pr.status === "failed" && <span className={styles.failed}>{t("tree.import.failed")}</span>}
              </div>
              <ul className={styles.problemDiagnostics}>
                {pr.diagnostics.map((d, i) => (
                  <DiagnosticLine key={i} d={d} />
                ))}
              </ul>
            </li>
          ))}
        </ul>
      )}
      {hasInfo && (
        <label className={styles.check}>
          <input type="checkbox" checked={withInfo} onChange={(e) => setWithInfo(e.target.checked)} />
          {t("tree.import.showInfo")}
        </label>
      )}
    </Dialog>
  );
}
