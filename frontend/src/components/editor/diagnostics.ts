// Diagnostics for the code editor (GQL-12): wavy underlines on the ranges
// Go reports, a tint on their lines, and the messages on hover. Like
// errorMarker.ts (one marker for the JSON tools) it is our own small
// extension, since @codemirror/lint is not a dependency; this one holds
// any number of ranges with a severity.
//
// The diagnostics live in a StateField. Set them with setDiagnostics (an
// empty list clears them); edits move them with the text until the next
// check replaces them. Add the extension to a view once (installDiagnostics).

import { StateEffect, StateField, type EditorState, type Extension, type Range } from "@codemirror/state";
import { Decoration, EditorView, hoverTooltip, type DecorationSet } from "@codemirror/view";

export type DiagnosticSeverity = "error" | "warning";

export interface EditorDiagnostic {
  /** The range in UTF-16 code units (CodeMirror positions); `to` may equal `from`. */
  readonly from: number;
  readonly to: number;
  readonly severity: DiagnosticSeverity;
  readonly message: string;
}

/** Replaces the diagnostics shown. */
export const setDiagnostics = StateEffect.define<readonly EditorDiagnostic[]>({
  map: (value, mapping) =>
    value.map((d) => ({ ...d, from: mapping.mapPos(d.from), to: mapping.mapPos(d.to, 1) })),
});

interface DiagnosticsState {
  readonly diagnostics: readonly EditorDiagnostic[];
  readonly decorations: DecorationSet;
}

/**
 * The range to underline for a diagnostic: its own range, or for an empty
 * one (a missing token at the end of input) the character before it, so
 * something is visible. A line break alone is not visible either.
 */
export function diagnosticRange(doc: EditorState["doc"], d: Pick<EditorDiagnostic, "from" | "to">): { from: number; to: number } | null {
  if (doc.length === 0) return null;
  let from = Math.max(0, Math.min(d.from, doc.length));
  let to = Math.max(from, Math.min(d.to, doc.length));
  if (to === from) {
    if (from === doc.length || doc.sliceString(from, from + 1) === "\n") from = Math.max(0, from - 1);
    to = from + 1;
    if (doc.sliceString(from, to) === "\n" && from > 0) {
      from -= 1;
      to -= 1;
    }
  }
  return { from, to };
}

// CSS class names, not text.
// i18n-ignore-start
const marks: Record<DiagnosticSeverity, Decoration> = {
  error: Decoration.mark({ class: "cm-diagnostic cm-diagnostic-error" }),
  warning: Decoration.mark({ class: "cm-diagnostic cm-diagnostic-warning" }),
};
// i18n-ignore-end
const lines: Record<DiagnosticSeverity, Decoration> = {
  error: Decoration.line({ class: "cm-diagnosticLine-error" }),
  warning: Decoration.line({ class: "cm-diagnosticLine-warning" }),
};

function decorate(state: EditorState, diagnostics: readonly EditorDiagnostic[]): DecorationSet {
  if (diagnostics.length === 0) return Decoration.none;
  const ranges: Range<Decoration>[] = [];
  const lineSeverity = new Map<number, DiagnosticSeverity>();
  for (const d of diagnostics) {
    const range = diagnosticRange(state.doc, d);
    if (range === null) continue;
    ranges.push(marks[d.severity].range(range.from, range.to));
    const line = state.doc.lineAt(range.from).from;
    if (lineSeverity.get(line) !== "error") lineSeverity.set(line, d.severity);
  }
  for (const [line, severity] of lineSeverity) ranges.push(lines[severity].range(line));
  return Decoration.set(ranges, true);
}

const diagnosticsField = StateField.define<DiagnosticsState>({
  create: () => ({ diagnostics: [], decorations: Decoration.none }),
  update(value, tr) {
    let { diagnostics } = value;
    let changed = false;
    if (tr.docChanged && diagnostics.length > 0) {
      diagnostics = diagnostics.map((d) => ({ ...d, from: tr.changes.mapPos(d.from), to: tr.changes.mapPos(d.to, 1) }));
      changed = true;
    }
    for (const effect of tr.effects) {
      if (effect.is(setDiagnostics)) {
        diagnostics = effect.value;
        changed = true;
      }
    }
    if (!changed) return value;
    return { diagnostics, decorations: decorate(tr.state, diagnostics) };
  },
  provide: (field) => EditorView.decorations.from(field, (value) => value.decorations),
});

/** The diagnostics shown now, with their positions moved through edits since they were set. */
export function currentDiagnostics(state: EditorState): readonly EditorDiagnostic[] {
  return state.field(diagnosticsField, false)?.diagnostics ?? [];
}

const diagnosticsTooltip = hoverTooltip((view, pos) => {
  const hits: { d: EditorDiagnostic; from: number; to: number }[] = [];
  for (const d of currentDiagnostics(view.state)) {
    const range = diagnosticRange(view.state.doc, d);
    if (range !== null && pos >= range.from && pos <= range.to) hits.push({ d, ...range });
  }
  if (hits.length === 0) return null;
  return {
    pos: Math.min(...hits.map((h) => h.from)),
    end: Math.max(...hits.map((h) => h.to)),
    above: true,
    create() {
      const dom = document.createElement("div");
      dom.className = "cm-diagnosticTooltip";
      for (const { d } of hits) {
        const row = document.createElement("div");
        row.className = `cm-diagnosticMessage cm-diagnosticMessage-${d.severity}`; // i18n-ignore: class names
        row.textContent = d.message;
        dom.appendChild(row);
      }
      return { dom };
    },
  };
});

const diagnosticsTheme = EditorView.theme({
  ".cm-diagnostic": {
    textDecorationLine: "underline",
    textDecorationStyle: "wavy",
    textDecorationSkipInk: "none",
    textUnderlineOffset: "3px",
  },
  ".cm-diagnostic-error": { textDecorationColor: "var(--tk-err)" },
  ".cm-diagnostic-warning": { textDecorationColor: "var(--warn)" },
  ".cm-diagnosticLine-error": { backgroundColor: "color-mix(in srgb, var(--tk-err) 8%, transparent)" },
  ".cm-diagnosticLine-warning": { backgroundColor: "color-mix(in srgb, var(--warn) 8%, transparent)" },
  ".cm-diagnosticTooltip": {
    maxWidth: "480px",
    padding: "var(--space-2) var(--space-4)",
    color: "var(--text)",
    fontFamily: "var(--font-sans)",
    fontSize: "var(--fs-md)",
  },
  ".cm-diagnosticMessage + .cm-diagnosticMessage": {
    marginTop: "var(--space-2)",
    paddingTop: "var(--space-2)",
    borderTop: "1px solid var(--line)",
  },
  ".cm-diagnosticMessage-error": { borderLeft: "2px solid var(--tk-err)", paddingLeft: "var(--space-3)" },
  ".cm-diagnosticMessage-warning": { borderLeft: "2px solid var(--warn)", paddingLeft: "var(--space-3)" },
});

/** The field, its hover tooltip and styles. */
export const diagnosticsExtension: Extension = [diagnosticsField, diagnosticsTooltip, diagnosticsTheme];

/** Adds the diagnostics to a live view (e.g. from CodeEditor's onReady), once. */
export function installDiagnostics(view: EditorView): void {
  if (view.state.field(diagnosticsField, false) !== undefined) return;
  view.dispatch({ effects: StateEffect.appendConfig.of(diagnosticsExtension) });
}
