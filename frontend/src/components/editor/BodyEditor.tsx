import { useCallback, useEffect, useId, useRef, useState, type ChangeEvent } from "react";
import type { EditorView } from "@codemirror/view";
import {
  bodyMode,
  EDITABLE_BODY_MODES,
  formatRequestBody,
  isJsonBody,
  RAW_BODY_TYPES,
  setBodyMode,
  setBodyText,
  setFormFields,
  setRawBodyType,
  type JsonFormat,
} from "../../commands/body";
import { useJsonCheck } from "../../commands/useJsonCheck";
import type { BodyDTO, BodyExampleDTO, KeyValueDTO } from "../../lib/collection";
import { ariaShortcut, shortcutLabel } from "../../lib/shortcuts";
import type { MessageKey } from "../../i18n";
import { useT } from "../../i18n/react";
import { useDrafts, useStores, type RequestId } from "../../stores";
import { IconWarning } from "../common/icons";
import { KeyValueTable } from "../common/KeyValueTable";
import { useShortcut } from "../shell/useShortcut";
import { BodyExamples } from "./BodyExamples";
import { FileBodyEditor, MultipartEditor } from "./BodyFiles";
import { CodeEditor, type CodeEditorHandle } from "./CodeEditor";
import { installErrorMarker, markerRange, setErrorMarker } from "./errorMarker";
import { useKeyValueRows } from "./keyValueRows";
import type { EditorLanguage } from "./languages";
import { SoapHelper } from "./SoapHelper";
import styles from "./BodyEditor.module.css";

/** The body modes, in the order of the mode picker. */
const MODES: readonly { mode: string; labelKey: MessageKey }[] = [
  { mode: "none", labelKey: "editor.body.mode.none" },
  { mode: "raw", labelKey: "editor.body.mode.raw" },
  { mode: "urlencoded", labelKey: "editor.body.mode.urlencoded" },
  { mode: "multipart", labelKey: "editor.body.mode.multipart" },
  { mode: "file", labelKey: "editor.body.mode.file" },
];

/** Names of the raw types (formats, not text); "text" is from the catalogue. */
const RAW_TYPE_LABELS: Readonly<Record<string, string>> = {
  json: "JSON",
  xml: "XML",
  html: "HTML",
  yaml: "YAML",
};

/**
 * The editor language of a raw type. Only JSON is highlighted: XML, HTML
 * and YAML need @codemirror/lang-* packages, which are not dependencies
 * yet, so they are plain text for now.
 */
function rawLanguage(type: string): EditorLanguage {
  return type === "json" ? "json" : "text";
}

export interface BodyEditorProps {
  id: RequestId;
  body: BodyDTO;
  /** The request's body examples (HTTP-17). */
  examples: readonly BodyExampleDTO[];
  readOnly: boolean;
}

/**
 * The Body section of the request editor (HTTP-10…15): the body
 * mode (none, raw, URL-encoded form, multipart form or binary file; a mode
 * of a newer version is shown and kept), the raw body's language and text,
 * for JSON the Go-backed validation marker, Beautify (⇧⌘F) and Minify, the
 * form's fields table, the multipart parts and the binary file. Above it
 * the body examples bar (HTTP-17, BodyExamples), and the SOAP helper
 * (HTTP-18, SoapHelper) opens from the toolbar. Every change is an
 * auto-saved edit (commands/body.ts, bodyExamples.ts, soap.ts).
 */
export function BodyEditor({ id, body, examples, readOnly }: BodyEditorProps) {
  const t = useT();
  const stores = useStores();
  const [soapOpen, setSoapOpen] = useState(false);
  const mode = bodyMode(body);
  const stash = useDrafts((s) => s.bodyStash[id]);
  const json = isJsonBody(body);
  const baseId = useId();

  const editorRef = useRef<CodeEditorHandle>(null);
  const [view, setView] = useState<EditorView | null>(null);
  const onReady = useCallback((v: EditorView) => {
    installErrorMarker(v);
    setView(v);
  }, []);

  // The marker follows Go's check of the current text.
  const check = useJsonCheck(body.text, json);
  useEffect(() => {
    if (view === null) return;
    const error = check.error !== null && check.text === view.state.doc.toString() ? check.error : null;
    view.dispatch({ effects: setErrorMarker.of(error === null ? null : { offset: error.offset, message: error.message }) });
  }, [view, check]);

  const [busy, setBusy] = useState<JsonFormat | null>(null);
  const [failure, setFailure] = useState<string | null>(null);
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  const goTo = useCallback((offset: number) => {
    const v = editorRef.current?.view;
    if (!v) return;
    const range = markerRange(v.state.doc, offset);
    v.dispatch({ selection: { anchor: range?.from ?? 0 }, scrollIntoView: true });
    v.focus();
  }, []);

  const runFormat = useCallback(
    async (format: JsonFormat) => {
      setBusy(format);
      setFailure(null);
      const outcome = await formatRequestBody(stores, id, format);
      if (!mounted.current) return;
      setBusy(null);
      if (outcome.status === "failed") setFailure(outcome.message);
      if (outcome.status === "invalid") {
        const v = editorRef.current?.view;
        v?.dispatch({ effects: setErrorMarker.of({ offset: outcome.error.offset, message: outcome.error.message }) });
        goTo(outcome.error.offset);
      }
    },
    [stores, id, goTo],
  );

  const canFormat = json && !readOnly && body.text.trim() !== "";
  useShortcut(
    "request.beautify",
    () => {
      void runFormat("beautify");
    },
    json && !readOnly,
  );

  const onMode = (e: ChangeEvent<HTMLInputElement>) => setBodyMode(stores, id, e.target.value);
  const onType = (e: ChangeEvent<HTMLSelectElement>) => setRawBodyType(stores, id, e.target.value);
  const onText = useCallback((text: string) => setBodyText(stores, id, text), [stores, id]);
  const onFields = useCallback((fields: KeyValueDTO[]) => setFormFields(stores, id, fields), [stores, id]);
  const [fieldRows, setFieldRows] = useKeyValueRows(body.fields, onFields);

  const modes = MODES.map((m) => ({ mode: m.mode, label: t(m.labelKey) }));
  if (!MODES.some((m) => m.mode === mode)) modes.push({ mode, label: t("editor.auth.quoted", { value: mode }) });
  const types = RAW_BODY_TYPES.includes(body.type) || mode !== "raw" ? RAW_BODY_TYPES : [...RAW_BODY_TYPES, body.type];
  const error = json ? check.error : null;

  return (
    <div className={styles.body} data-body-mode={mode}>
      <BodyExamples id={id} examples={examples} readOnly={readOnly} />

      <div className={styles.toolbar}>
        <div className={styles.modes} role="radiogroup" aria-label={t("editor.body.type")}>
          {modes.map((m) => {
            // A mode of a newer version can only be picked when the body is (or was) in it.
            const available = EDITABLE_BODY_MODES.includes(m.mode) || m.mode === mode || stash?.[m.mode] !== undefined;
            return (
              <label key={m.mode} className={styles.mode} data-disabled={readOnly || !available || undefined}>
                <input
                  type="radio"
                  name={`${baseId}-mode`}
                  value={m.mode}
                  checked={m.mode === mode}
                  disabled={readOnly || !available}
                  onChange={onMode}
                />
                {m.label}
              </label>
            );
          })}
        </div>

        {mode === "raw" && (
          <select
            className={styles.type}
            aria-label={t("editor.body.language")}
            value={body.type === "" ? "text" : body.type}
            disabled={readOnly}
            onChange={onType}
          >
            {types.map((type) => (
              <option key={type} value={type}>
                {type === "text" ? t("editor.body.language.text") : (RAW_TYPE_LABELS[type] ?? type)}
              </option>
            ))}
          </select>
        )}

        <div className={styles.tools}>
          {json && (
            <>
              <button
                type="button"
                className={styles.tool}
                disabled={!canFormat || busy !== null}
                title={t("editor.body.beautify.title", { shortcut: shortcutLabel("request.beautify") })}
                aria-keyshortcuts={ariaShortcut("request.beautify")}
                onClick={() => void runFormat("beautify")}
              >
                {t("editor.body.beautify")}
              </button>
              <button
                type="button"
                className={styles.tool}
                disabled={!canFormat || busy !== null}
                title={t("editor.body.minify.title")}
                onClick={() => void runFormat("minify")}
              >
                {t("editor.body.minify")}
              </button>
            </>
          )}
          {!readOnly && (
            <button
              type="button"
              className={styles.tool}
              aria-expanded={soapOpen}
              title={t("editor.body.soap.title")}
              onClick={() => setSoapOpen((open) => !open)}
            >
              {t("editor.body.soap")}
            </button>
          )}
        </div>
      </div>

      {soapOpen && !readOnly && <SoapHelper id={id} readOnly={readOnly} onClose={() => setSoapOpen(false)} />}

      {mode === "none" && <p className={styles.note}>{t("editor.body.none")}</p>}

      {mode === "raw" && (
        <>
          <div className={styles.editorFrame}>
            <CodeEditor
              ref={editorRef}
              value={body.text}
              language={rawLanguage(body.type)}
              ariaLabel={t("editor.body.label")}
              readOnly={readOnly}
              onChange={readOnly ? undefined : onText}
              onReady={onReady}
            />
          </div>
          {(error !== null || failure !== null) && (
            <p className={styles.status} role="status">
              <IconWarning size={12} />
              {error !== null && (
                <>
                  <button type="button" className={styles.position} onClick={() => goTo(error.offset)}>
                    {t("editor.body.position", { line: error.line, column: error.column })}
                  </button>
                  <span>{error.message}</span>
                </>
              )}
              {error === null && failure}
            </p>
          )}
        </>
      )}

      {mode === "urlencoded" && (
        <KeyValueTable
          label={t("editor.body.formFields")}
          bulkEdit
          rows={fieldRows}
          onChange={readOnly ? undefined : setFieldRows}
          readOnly={readOnly}
          placeholders={{ key: t("editor.body.placeholder.field") }}
        />
      )}

      {mode === "multipart" && <MultipartEditor id={id} body={body} readOnly={readOnly} />}

      {mode === "file" && <FileBodyEditor id={id} body={body} readOnly={readOnly} />}

      {!EDITABLE_BODY_MODES.includes(mode) && (
        <p className={styles.note}>{t("editor.body.unknownMode", { mode })}</p>
      )}
    </div>
  );
}
