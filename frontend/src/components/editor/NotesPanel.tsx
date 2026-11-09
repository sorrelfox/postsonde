import { useCallback, useState } from "react";
import { openExternalLink } from "../../commands/links";
import { useT } from "../../i18n/react";
import { Markdown } from "../common/Markdown";
import { CodeEditor } from "./CodeEditor";
import styles from "./NotesPanel.module.css";

export type NotesMode = "edit" | "preview";

export interface NotesPanelProps {
  /** The Markdown source: the request's or folder's `description`. */
  value: string;
  /** What the notes belong to, for the texts. */
  subject: "request" | "folder";
  readOnly: boolean;
  /** Called with the full text after each edit; the caller auto-saves it. */
  onChange: (text: string) => void;
}

/**
 * The Notes sub-tab of the request editors and the folder view (COL-07):
 * the item's `description` as Markdown, with an Edit/Preview toggle. Edit
 * is a CodeMirror editor with Markdown highlighting; every change goes to
 * `onChange`, which auto-saves it like any other edit (and records it for
 * undo). Preview renders it with the in-house renderer (common/Markdown):
 * links open in the system browser after a confirmation, images are never
 * loaded. Opens in Preview when there is a note, in Edit when there is none.
 */
export function NotesPanel({ value, subject, readOnly, onChange }: NotesPanelProps) {
  const t = useT();
  const [mode, setMode] = useState<NotesMode>(() => (value.trim() === "" && !readOnly ? "edit" : "preview"));
  const openLink = useCallback((url: string) => void openExternalLink(url), []);

  return (
    <div className={styles.notes} data-notes-mode={mode}>
      <div className={styles.toolbar}>
        <div className={styles.segmented} role="group" aria-label={t("editor.notes.view")}>
          {(["edit", "preview"] as const).map((m) => (
            <button
              key={m}
              type="button"
              className={styles.segment}
              aria-pressed={mode === m}
              onClick={() => setMode(m)}
            >
              {m === "edit" ? t("editor.notes.edit") : t("editor.notes.preview")}
            </button>
          ))}
        </div>
        <p className={styles.hint}>{subject === "folder" ? t("editor.notes.hint.folder") : t("editor.notes.hint.request")}</p>
      </div>
      {mode === "edit" ? (
        <div className={styles.editorFrame}>
          <CodeEditor
            value={value}
            language="markdown"
            ariaLabel={t("editor.notes.label")}
            readOnly={readOnly}
            onChange={readOnly ? undefined : onChange}
          />
        </div>
      ) : value.trim() === "" ? (
        <p className={styles.empty}>
          {t("editor.notes.empty")}{" "}
          {!readOnly && (
            <button type="button" className={styles.linkButton} onClick={() => setMode("edit")}>
              {t("editor.notes.write")}
            </button>
          )}
        </p>
      ) : (
        <div className={styles.preview}>
          <Markdown source={value} onOpenLink={openLink} />
        </div>
      )}
    </div>
  );
}
