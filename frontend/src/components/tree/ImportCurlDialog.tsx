import { useEffect, useId, useMemo, useRef, useState } from "react";
import { createItem } from "../../commands/collection";
import { parseCurl, type CurlDiagnosticDTO, type CurlImportDTO } from "../../lib/collection";
import { useT, type Translate } from "../../i18n/react";
import { toAppError } from "../../lib/errors";
import { useCollection, useStores, useWorkspace, type CollectionState, type NodeId } from "../../stores";
import { Dialog } from "../common/Dialog";
import { IconInfo, IconWarning } from "../common/icons";
import { severityLabel } from "./badges";
import { FolderSelect } from "./FolderSelect";
import styles from "./ImportCurlDialog.module.css";
import { defaultTarget, folderOptions, previewPath, TOP_LEVEL, validateName } from "./newItem";
import fieldStyles from "./NewItemDialog.module.css";

/** The Import cURL dialog's request: the node it was asked for (its folder is the target) and a command to start with. */
export interface ImportCurlRequest {
  readonly from: NodeId | null;
  readonly text: string;
}

export interface ImportCurlDialogProps {
  /** null: closed. A new request object opens a fresh dialog. */
  request: ImportCurlRequest | null;
  onClose: () => void;
}

/** Wait this long after typing before reading the command again, in ms. */
export const PARSE_DELAY_MS = 150;

/**
 * The Import cURL dialog (IMP-01): a field to paste a curl command into,
 * the parser's diagnostics as they are found (errors keep the dialog from
 * importing; a click selects the text concerned), a one-line summary of
 * the request, its name (the suggested one until edited) and the folder it
 * goes into. On confirm Go creates the request (CollectionService
 * ImportCurl) and it opens in a tab.
 */
export function ImportCurlDialog({ request, onClose }: ImportCurlDialogProps) {
  if (request === null) return null;
  return <ImportCurlForm request={request} onClose={onClose} />;
}

/** `name`, or `name 2`, `name 3`, … when the folder already has an item called that. */
export function freeName(state: CollectionState, parentId: NodeId, name: string): string {
  if (name === "" || validateName(state, parentId, name) === null) return name;
  for (let n = 2; n < 1000; n++) {
    const next = `${name} ${n}`;
    if (validateName(state, parentId, next) === null) return next;
  }
  return name;
}

/** The one-line summary of the parsed request: "POST https://… · 1 header · JSON body · Basic auth". */
function summary(t: Translate, result: CurlImportDTO): string {
  const r = result.request;
  const parts = [`${r.method} ${r.url}`];
  const headers = (r.headers ?? []).length;
  if (headers > 0) parts.push(t("tree.curl.headers", { count: headers }));
  switch (r.body.mode) {
    case "raw":
      parts.push(t("tree.curl.rawBody", { type: r.body.type === "json" ? "JSON" : r.body.type.toUpperCase() })); // i18n-ignore: a media type name
      break;
    case "urlencoded":
      parts.push(t("tree.curl.formBody", { count: (r.body.fields ?? []).length }));
      break;
    case "multipart":
      parts.push(t("tree.curl.multipartBody", { count: (r.body.parts ?? []).length }));
      break;
    case "file":
      parts.push(t("tree.curl.fileBody"));
      break;
  }
  if (r.auth.type === "basic") parts.push(t("tree.curl.basicAuth"));
  if (r.auth.type === "bearer") parts.push(t("tree.curl.bearerToken"));
  return parts.join(" · ");
}

/** "1 error", "2 warnings", "1 error, 2 warnings", or "Notes" for info only. */
function countsLine(t: Translate, errors: number, warnings: number): string {
  const e = t("tree.curl.errors", { count: errors });
  const w = t("tree.curl.warnings", { count: warnings });
  if (errors > 0 && warnings > 0) return t("tree.curl.errorsAndWarnings", { errors: e, warnings: w });
  if (errors > 0) return e;
  if (warnings > 0) return w;
  return t("tree.curl.notes");
}

function ImportCurlForm({ request, onClose }: { request: ImportCurlRequest; onClose: () => void }) {
  const t = useT();
  const stores = useStores();
  const state = useCollection((s) => s);
  const projectName = useWorkspace((s) => s.current?.name) ?? t("tree.project");

  const [text, setText] = useState(request.text);
  // The answer for `parsed.text`; stale while a newer text is being read.
  const [parsed, setParsed] = useState<{ text: string; result: CurlImportDTO } | null>(null);
  const [name, setName] = useState("");
  const [nameEdited, setNameEdited] = useState(false);
  const [parentId, setParentId] = useState<NodeId>(() => defaultTarget(state, request.from));
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);

  const baseId = useId();
  const textRef = useRef<HTMLTextAreaElement>(null);
  const textId = `${baseId}-text`;
  const diagId = `${baseId}-diagnostics`;
  const nameErrorId = `${baseId}-name-error`;
  const pathId = `${baseId}-path`;

  const options = useMemo(() => folderOptions(state, projectName), [state, projectName]);
  const target = options.some((o) => o.id === parentId && !o.disabled) ? parentId : TOP_LEVEL;

  // Read the command again shortly after each change; only the latest answer counts.
  useEffect(() => {
    if (text.trim() === "") return;
    let current = true;
    const timer = setTimeout(() => {
      parseCurl(text).then(
        (r) => {
          if (current) setParsed({ text, result: r });
        },
        (err: unknown) => {
          if (current) setFailure(toAppError(err).message);
        },
      );
    }, PARSE_DELAY_MS);
    return () => {
      current = false;
      clearTimeout(timer);
    };
  }, [text]);

  const result = text.trim() === "" ? null : (parsed?.result ?? null);
  const fresh = parsed !== null && parsed.text === text;
  const suggested = result === null ? "" : freeName(state, target, result.name);
  const shownName = nameEdited ? name : suggested;
  const nameError = validateName(state, target, shownName);
  const path = previewPath(state, target, shownName.trim(), false);
  const diagnostics = result?.diagnostics ?? [];
  const errors = diagnostics.filter((d) => d.severity === "error").length;
  const warnings = diagnostics.filter((d) => d.severity === "warning").length;
  const canImport = fresh && result !== null && result.ok && nameError === null && !busy;

  const confirm = async () => {
    if (!canImport) return;
    setBusy(true);
    setFailure(null);
    try {
      await createItem(stores, target === TOP_LEVEL ? null : target, shownName.trim(), { type: "curl", text });
      onClose();
    } catch (err: unknown) {
      const appErr = toAppError(err);
      setFailure(appErr.hint === "" ? appErr.message : `${appErr.message} ${appErr.hint}`);
      setBusy(false);
    }
  };

  const select = (d: CurlDiagnosticDTO) => {
    const el = textRef.current;
    if (el === null) return;
    el.focus();
    el.setSelectionRange(d.from, Math.max(d.from, d.to));
  };

  const shownNameError = nameError !== null && nameError !== "" ? nameError : null;

  return (
    <Dialog
      open
      title={t("tree.curl.title")}
      confirmLabel={t("tree.curl.confirm")}
      onCancel={onClose}
      onConfirm={() => void confirm()}
      confirmDisabled={!canImport}
      busy={busy}
      width={600}
      initialFocus={textRef}
    >
      <div className={fieldStyles.field}>
        <label className={fieldStyles.label} htmlFor={textId}>
          {t("tree.curl.command")}
        </label>
        <textarea
          id={textId}
          ref={textRef}
          className={styles.command}
          value={text}
          rows={8}
          placeholder={t("tree.curl.placeholder")}
          spellCheck={false}
          autoComplete="off"
          autoCapitalize="off"
          readOnly={busy}
          aria-describedby={diagId}
          aria-invalid={errors > 0 || undefined}
          onChange={(e) => {
            setText(e.target.value);
            setFailure(null);
          }}
        />
        <span className={fieldStyles.help}>
          {t("tree.curl.help")}
        </span>
      </div>

      <div id={diagId} className={styles.result} aria-live="polite">
        {result !== null && result.ok && <p className={styles.summary}>{summary(t, result)}</p>}
        {diagnostics.length > 0 && (
          <>
            <p className={styles.counts}>{countsLine(t, errors, warnings)}</p>
            <ul className={styles.diagnostics} aria-label={t("tree.curl.problems")}>
              {diagnostics.map((d, i) => (
                <li key={i} className={styles.diagnostic} data-severity={d.severity}>
                  <button type="button" className={styles.diagnosticButton} onClick={() => select(d)} title={t("tree.curl.selectText")}>
                    {d.severity === "info" ? <IconInfo size={13} /> : <IconWarning size={13} />}
                    <span className={styles.severity}>{severityLabel(d.severity)}</span>
                    <span>{d.message}</span>
                  </button>
                </li>
              ))}
            </ul>
          </>
        )}
      </div>

      <div className={fieldStyles.field}>
        <label className={fieldStyles.label} htmlFor={`${baseId}-name`}>
          {t("tree.name")}
        </label>
        <input
          id={`${baseId}-name`}
          className={fieldStyles.input}
          value={shownName}
          placeholder={t("tree.requestName")}
          maxLength={1000}
          spellCheck={false}
          autoComplete="off"
          readOnly={busy}
          aria-invalid={shownNameError !== null || undefined}
          aria-describedby={shownNameError !== null ? nameErrorId : undefined}
          onChange={(e) => {
            setName(e.target.value);
            setNameEdited(true);
            setFailure(null);
          }}
        />
        {shownNameError !== null && (
          <span className={fieldStyles.error} id={nameErrorId} role="alert">
            {shownNameError}
          </span>
        )}
      </div>

      <div className={fieldStyles.field}>
        <label className={fieldStyles.label} htmlFor={`${baseId}-folder`}>
          {t("tree.folder")}
        </label>
        <FolderSelect
          id={`${baseId}-folder`}
          options={options}
          value={target}
          disabled={busy}
          describedBy={pathId}
          onChange={(id) => {
            setParentId(id);
            setFailure(null);
          }}
        />
        <span className={fieldStyles.path} id={pathId}>
          {path}
        </span>
      </div>

      {failure !== null && (
        <span className={fieldStyles.error} role="alert">
          {failure}
        </span>
      )}
    </Dialog>
  );
}
