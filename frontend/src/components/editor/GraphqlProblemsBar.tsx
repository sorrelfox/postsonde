import { useEffect } from "react";
import type { EditorView } from "@codemirror/view";
import type { GraphqlDiagnostics } from "../../commands/useGraphqlDiagnostics";
import { useT } from "../../i18n/react";
import type { DiagnosticDTO } from "../../lib/graphqlSchema";
import { IconCheck, IconWarning } from "../common/icons";
import { setDiagnostics, type EditorDiagnostic } from "./diagnostics";
import styles from "./GraphqlEditor.module.css";

// The query editor's live diagnostics (GQL-12): Go's syntax and schema
// problems as wavy underlines in the editor (diagnostics.ts), and the
// problems bar under it, as in the mockup: "Document is valid against the
// schema." or the list of problems, each jumping to its position.

/** The editor form of Go's diagnostics. */
export function toEditorDiagnostics(list: readonly DiagnosticDTO[]): EditorDiagnostic[] {
  return list.map((d) => ({
    from: d.offset,
    to: d.endOffset,
    severity: d.severity === "warning" ? "warning" : "error",
    message: d.message,
  }));
}

/**
 * Shows a check's diagnostics in `view` when the check is for the editor's
 * current text; until then the last ones stay, moved with the edits.
 */
export function useDiagnosticMarks(view: EditorView | null, check: GraphqlDiagnostics): void {
  useEffect(() => {
    if (view === null || check.text === null || check.text !== view.state.doc.toString()) return;
    view.dispatch({ effects: setDiagnostics.of(toEditorDiagnostics(check.diagnostics)) });
  }, [view, check]);
}

export interface GraphqlProblemsBarProps {
  check: GraphqlDiagnostics;
  /** Puts the cursor on a problem. */
  onJump: (offset: number) => void;
}

export function GraphqlProblemsBar({ check, onJump }: GraphqlProblemsBarProps) {
  const t = useT();
  if (check.text === null || check.text.trim() === "") return null;
  if (check.count === 0) {
    return (
      <p className={styles.problemsBar} data-state="valid" role="status">
        <IconCheck size={14} />
        {check.schema ? t("editorProtocols.problems.valid") : t("editorProtocols.problems.noSyntaxErrors")}
      </p>
    );
  }
  const title = t("editorProtocols.problems.count", { count: check.count });
  return (
    <div className={styles.problemsBar} data-state="problems" role="status">
      <span className={styles.problemsCount}>
        <IconWarning size={14} />
        {title}
      </span>
      <ul className={styles.problemsList} aria-label={t("editorProtocols.problems.label")}>
        {check.diagnostics.map((d, i) => (
          <li key={`${i}-${d.offset}`} className={styles.problemRow} data-severity={d.severity}>
            <button type="button" className={styles.position} onClick={() => onJump(d.offset)}>
              {t("editorProtocols.problems.position", { line: d.line, column: d.column })}
            </button>
            <span className={styles.problemMessage} title={d.message}>
              {d.message}
            </span>
          </li>
        ))}
      </ul>
    </div>
  );
}
