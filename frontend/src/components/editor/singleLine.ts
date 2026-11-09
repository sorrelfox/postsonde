// The single-line variant of the shared code editor: the URL field,
// and later other one-line template fields. No gutters, folding, search or
// Tab indent (Tab moves focus); no line breaks; `{{variables}}` drawn as
// chips (templateVariables.ts).
//
// - Enter and Shift-Enter do nothing. ⌘Enter / Ctrl+Enter is the global
//   Send shortcut: the root is not marked data-shortcut-scope, so the
//   registry's capture listener takes it before CodeMirror.
// - Line breaks in any user edit (typing, paste, drop) are removed; pasted
//   text is also trimmed, and its lines are trimmed and joined, so a URL
//   copied across lines comes back as one.

import { EditorState, Prec, Transaction, type Extension, type TransactionSpec } from "@codemirror/state";
import { EditorView, drawSelection, keymap, placeholder as placeholderExtension } from "@codemirror/view";
import { defaultKeymap, history } from "@codemirror/commands";
import { historyKeys } from "./historyKeys";
import { templateVariables } from "./templateVariables";

const LINE_BREAKS = /\r\n|\r|\n/;

/** Pasted text for a single-line field: each line trimmed, joined, trimmed. */
export function cleanPastedLine(text: string): string {
  if (!LINE_BREAKS.test(text)) return text.trim();
  return text
    .split(LINE_BREAKS)
    .map((line) => line.trim())
    .join("");
}

/** Removes line breaks from user edits (parent-set text is left as it is). */
const noLineBreaks = EditorState.transactionFilter.of((tr): Transaction | TransactionSpec => {
  if (!tr.docChanged || tr.newDoc.lines === 1) return tr;
  const userEvent = tr.annotation(Transaction.userEvent);
  if (userEvent === undefined) return tr;
  const changes: { from: number; to: number; insert: string }[] = [];
  tr.changes.iterChanges((from, to, _fromB, _toB, inserted) => {
    changes.push({ from, to, insert: inserted.toString().split(LINE_BREAKS).join("") });
  });
  const set = tr.startState.changes(changes);
  return {
    changes: set,
    selection: tr.startState.selection.map(set, 1),
    userEvent,
    scrollIntoView: tr.scrollIntoView,
  };
});

const swallowEnter = Prec.highest(
  keymap.of([
    { key: "Enter", run: () => true, shift: () => true }, // i18n-ignore: a key name
    { key: "Mod-Enter", run: () => true },
  ]),
);

/** Colours and metrics from the tokens; the field's box comes from the parent. */
export const singleLineTheme = EditorView.theme({
  "&": {
    height: "100%",
    color: "var(--text)",
    backgroundColor: "transparent",
    fontSize: "var(--fs-mono-lg)",
  },
  "&.cm-focused": { outline: "none" },
  ".cm-scroller": {
    // The base theme's flex-start is !important (it lines gutters up with
    // the text); a single line has no gutter, so centre it in the field.
    alignItems: "center !important",
    overflowX: "auto",
    overflowY: "hidden",
    scrollbarWidth: "none",
    fontFamily: "var(--font-editor)", // the editor font setting (UI-08)
    lineHeight: "var(--lh-code)",
  },
  ".cm-scroller::-webkit-scrollbar": { display: "none" },
  // The base theme's min-height: 100% would stretch the content to the
  // field and keep the line at its top.
  ".cm-content": {
    minHeight: "0",
    padding: "0",
    caretColor: "var(--accent)",
    userSelect: "text",
    WebkitUserSelect: "text",
  },
  ".cm-line": { padding: "0 var(--space-5)" },
  ".cm-cursor, .cm-dropCursor": { borderLeft: "2px solid var(--accent)" },
  ".cm-selectionBackground": { backgroundColor: "var(--sel)" },
  "&.cm-focused > .cm-scroller > .cm-selectionLayer .cm-selectionBackground": {
    backgroundColor: "var(--accent-dim)",
  },
  ".cm-placeholder": { color: "var(--text3)" },
});

/** Shared by editable and read-only single-line fields. */
export function singleLineExtensions(placeholderText: string): Extension {
  return [
    swallowEnter,
    noLineBreaks,
    EditorView.clipboardInputFilter.of(cleanPastedLine),
    drawSelection(),
    templateVariables,
    singleLineTheme,
    placeholderText === "" ? [] : placeholderExtension(placeholderText),
    keymap.of(defaultKeymap),
  ];
}

/** Editing a single-line field: undo history. */
export const singleLineEditableExtensions: Extension = [history(), keymap.of(historyKeys)];

