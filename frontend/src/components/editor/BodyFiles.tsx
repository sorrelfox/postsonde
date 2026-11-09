import { useCallback, useEffect, useRef, useState, type ChangeEvent } from "react";
import {
  chooseBodyFile,
  choosePartFile,
  setBodyFileContentType,
  setMultipartParts,
  setPartContentType,
  setPartText,
  type PickFileOutcome,
} from "../../commands/body";
import { useBodyFile } from "../../commands/useBodyFile";
import type { BodyDTO, PartDTO } from "../../lib/collection";
import { baseName, fileMediaType } from "../../lib/contentType";
import { useT } from "../../i18n/react";
import { useStores, type RequestId } from "../../stores";
import { IconFile, IconWarning } from "../common/icons";
import { KeyValueTable, type KeyValueCellProps } from "../common/KeyValueTable";
import { formatSize } from "../messagelog/format";
import { usePartRows } from "./partRows";
import styles from "./BodyFiles.module.css";

export interface FileChipProps {
  /** The path as stored ("" for no file). */
  path: string;
  /** Opens the file dialog. */
  onChoose: () => void;
  readOnly: boolean;
  /** The dialog is open. */
  busy?: boolean;
  /** Accessible name of the Choose button. */
  chooseLabel: string;
  /** Keyboard-navigation attributes for the Choose button (inside a table). */
  cell?: KeyValueCellProps;
  compact?: boolean;
}

/**
 * A chosen file (HTTP-14, HTTP-15): icon, name and size, or "missing" when
 * there is no file at the path, with the full path in the tooltip, and a
 * Choose… button that opens the native file dialog.
 */
export function FileChip({ path, onChoose, readOnly, busy = false, chooseLabel, cell, compact = false }: FileChipProps) {
  const t = useT();
  const file = useBodyFile(path);
  const missing = path !== "" && file !== null && !file.exists;
  const size = file === null ? "" : missing ? t("editor.files.missing") : formatSize(file.size);
  const title = path === "" ? undefined : [file?.absolutePath || path, size].filter((s) => s !== "").join(" · ");
  return (
    <span className={compact ? `${styles.chip} ${styles.compact}` : styles.chip} data-missing={missing || undefined}>
      {path === "" ? (
        <span className={styles.empty}>{t("editor.files.none")}</span>
      ) : (
        <span className={styles.file} title={title}>
          {missing ? <IconWarning size={12} className={styles.warn} /> : <IconFile size={12} />}
          <span className={styles.name}>{baseName(path)}</span>
          <span className={styles.size}>{size}</span>
        </span>
      )}
      <button
        {...cell}
        type="button"
        className={styles.choose}
        tabIndex={cell ? -1 : undefined}
        disabled={readOnly || busy}
        aria-label={chooseLabel}
        onClick={onChoose}
      >
        {t("editor.files.choose")}
      </button>
    </span>
  );
}

/** Runs a pick, reports a failure, and ignores results after unmount (also the GraphQL Files tab). */
export function usePick(): [string | null, boolean, (run: () => Promise<PickFileOutcome>) => void] {
  const [failure, setFailure] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);
  const start = useCallback((run: () => Promise<PickFileOutcome>) => {
    setBusy(true);
    setFailure(null);
    void run().then((outcome) => {
      if (!mounted.current) return;
      setBusy(false);
      setFailure(outcome.status === "failed" ? outcome.message : null);
    });
  }, []);
  return [failure, busy, start];
}

export function Failure({ message }: { message: string | null }) {
  if (message === null) return null;
  return (
    <p className={styles.failure} role="alert">
      <IconWarning size={12} />
      <span>{message}</span>
    </p>
  );
}

export interface BodyPartsProps {
  id: RequestId;
  body: BodyDTO;
  readOnly: boolean;
}

/**
 * The multipart form editor (HTTP-14): a key/value table of parts. A part
 * is Text (the value cell) or File (a file chip with Choose…, picked with
 * the native dialog); switching to File opens the dialog and a cancelled
 * dialog keeps the part as text. Each part has an optional Content-Type:
 * a file part's placeholder shows the type guessed from its name.
 */
export function MultipartEditor({ id, body, readOnly }: BodyPartsProps) {
  const t = useT();
  const stores = useStores();
  const parts = body.parts ?? [];
  const onParts = useCallback((next: PartDTO[]) => setMultipartParts(stores, id, next), [stores, id]);
  const [rows, setRows] = usePartRows(body.parts, onParts);
  const [failure, busy, start] = usePick();
  const choose = (index: number) => start(() => choosePartFile(stores, id, index));

  const valueCell = (_row: unknown, index: number, cell: KeyValueCellProps) => {
    const part = parts[index];
    if (part === undefined || part.file === "") return null;
    return (
      <FileChip
        compact
        path={part.file}
        readOnly={readOnly}
        busy={busy}
        cell={cell}
        chooseLabel={t("editor.files.chooseRow", { row: index + 1 })}
        onChoose={() => choose(index)}
      />
    );
  };

  const valueActions = (_row: unknown, index: number) => {
    const part = parts[index];
    if (part === undefined) return null;
    const isFile = part.file !== "";
    const onKind = (e: ChangeEvent<HTMLSelectElement>) => {
      if (e.target.value === "file") choose(index);
      else setPartText(stores, id, index);
    };
    return (
      <>
        <input
          type="text"
          className={styles.partType}
          value={part.contentType}
          placeholder={isFile ? fileMediaType(part.file, "") : "Content-Type"}
          aria-label={t("editor.files.contentTypeRow", { row: index + 1 })}
          title={t("editor.files.contentType.title")}
          readOnly={readOnly}
          spellCheck={false}
          autoComplete="off"
          onChange={(e) => setPartContentType(stores, id, index, e.target.value)}
        />
        <select
          className={styles.kind}
          value={isFile ? "file" : "text"}
          aria-label={t("editor.files.kindRow", { row: index + 1 })}
          disabled={readOnly || busy}
          onChange={onKind}
        >
          <option value="text">{t("editor.files.kind.text")}</option>
          <option value="file">{t("editor.files.kind.file")}</option>
        </select>
      </>
    );
  };

  return (
    <div className={styles.parts}>
      <KeyValueTable
        label={t("editor.files.parts")}
        rows={rows}
        onChange={readOnly ? undefined : setRows}
        readOnly={readOnly}
        placeholders={{ key: t("editor.body.placeholder.field") }}
        valueCell={valueCell}
        valueActions={valueActions}
      />
      <Failure message={failure} />
    </div>
  );
}

/**
 * The binary file body editor (HTTP-15): the chosen file with Choose…, and
 * an optional Content-Type whose placeholder is the type guessed from the
 * file name.
 */
export function FileBodyEditor({ id, body, readOnly }: BodyPartsProps) {
  const t = useT();
  const stores = useStores();
  const [failure, busy, start] = usePick();
  return (
    <div className={styles.fileBody}>
      <FileChip
        path={body.path}
        readOnly={readOnly}
        busy={busy}
        chooseLabel={t("editor.files.chooseBody")}
        onChoose={() => start(() => chooseBodyFile(stores, id))}
      />
      <label className={styles.field}>
        <span>Content-Type</span>
        <input
          type="text"
          className={styles.input}
          value={body.contentType}
          placeholder={fileMediaType(body.path, "")}
          readOnly={readOnly}
          spellCheck={false}
          autoComplete="off"
          onChange={(e) => setBodyFileContentType(stores, id, e.target.value)}
        />
      </label>
      <Failure message={failure} />
      <p className={styles.hint}>{t("editor.files.hint")}</p>
    </div>
  );
}
