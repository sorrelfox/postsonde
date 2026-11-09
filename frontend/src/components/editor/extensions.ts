// The editor's extension set, composed explicitly. No `basicSetup`:
// everything that ships is listed here.
//
// Keyboard and focus:
// - Tab indents (indentWithTab) in editable editors, so Tab alone does not
//   leave the editor. To move focus out, press Escape and then Tab or
//   Shift-Tab within two seconds (CodeMirror's tab-focus mode). Ctrl-M
//   (Shift-Alt-M on macOS) turns tab-focus mode on or off for good.
// - Read-only views don't bind Tab, so Tab moves focus as usual.
// - Mod-F opens find/replace (Escape closes it), Mod-D selects the next
//   occurrence, Mod-click (Cmd-click on macOS, Ctrl-click elsewhere) adds a
//   cursor, Alt-drag makes a rectangular selection, Ctrl-Shift-[ / ]
//   (Cmd-Alt-[ / ] on macOS) fold and unfold.

import { Compartment, EditorState, type Extension, type Transaction } from "@codemirror/state";
import {
  EditorView,
  crosshairCursor,
  drawSelection,
  highlightActiveLine,
  highlightActiveLineGutter,
  keymap,
  rectangularSelection,
} from "@codemirror/view";
import {
  codeFolding,
  foldGutter,
  foldKeymap,
  indentOnInput,
  syntaxHighlighting,
} from "@codemirror/language";
import { defaultKeymap, history, indentWithTab } from "@codemirror/commands";
import { search, searchKeymap } from "@codemirror/search";
import { closeBrackets, closeBracketsKeymap } from "@codemirror/autocomplete";
import { editorHighlightStyle, editorTheme } from "./theme";
import { languageExtension, type EditorLanguage } from "./languages";
import { historyKeys } from "./historyKeys";
import { singleLineEditableExtensions, singleLineExtensions } from "./singleLine";
import { templateVariables } from "./templateVariables";
import { currentTheme, type Theme } from "../../styles/theme";
import { currentEditorPreferences, preferencesExtension, type EditorPreferences } from "./preferences";

/**
 * Shared by editable and read-only editors: display, selection, search,
 * folding. Line numbers, bracket matching, word wrap and the tab size come
 * from the editor settings (preferencesExtension, UI-08).
 */
export const baseExtensions: Extension = [
  foldGutter({ openText: "▾", closedText: "▸" }),
  codeFolding({ placeholderText: "…" }),
  drawSelection(),
  EditorState.allowMultipleSelections.of(true),
  rectangularSelection(),
  crosshairCursor(),
  search({ top: true }),
  syntaxHighlighting(editorHighlightStyle),
  editorTheme,
  keymap.of([...searchKeymap, ...foldKeymap, ...defaultKeymap]),
];

/** Editing: undo history, auto-closed brackets, auto-indent, active line, Tab indents. */
export const editableExtensions: Extension = [
  history(),
  closeBrackets(),
  indentOnInput(),
  highlightActiveLine(),
  highlightActiveLineGutter(),
  // Mode extensions come before baseExtensions, so these bindings win over
  // defaultKeymap (closeBrackets' Backspace must run before plain delete).
  keymap.of([...closeBracketsKeymap, ...historyKeys, indentWithTab]),
];

/** User edits (typing, paste, drop, delete, undo…), as opposed to a parent replacing the text. */
function isUserEdit(tr: Transaction): boolean {
  return ["input", "delete", "move", "undo", "redo"].some((e) => tr.isUserEvent(e));
}

/**
 * Response views: no editing, but selectable, searchable and foldable, and
 * focusable with the keyboard so Mod-F and copy work. No active-line highlight.
 */
export const readOnlyExtensions: Extension = [
  EditorState.readOnly.of(true),
  EditorView.editable.of(false),
  EditorView.contentAttributes.of({ tabindex: "0" }),
  // Belt and braces: commands already check readOnly; this also drops any
  // user edit dispatched some other way. Text set by the parent still applies.
  EditorState.changeFilter.of((tr) => !isUserEdit(tr)),
];

export function modeExtension(readOnly: boolean, singleLine = false): Extension {
  if (readOnly) return readOnlyExtensions;
  return singleLine ? singleLineEditableExtensions : editableExtensions;
}

export function labelExtension(ariaLabel: string): Extension {
  return EditorView.contentAttributes.of({ "aria-label": ariaLabel });
}

const darkAppearance = EditorView.darkTheme.of(true);
const lightAppearance = EditorView.darkTheme.of(false);

/**
 * Tells CodeMirror whether the app theme is dark (UI-04). The colours come
 * from the tokens either way (theme.ts); this switches which of CodeMirror's
 * "&light" / "&dark" base styles apply and what extensions read from
 * EditorView.darkTheme. The same value for the same theme, so a
 * compartment can compare it.
 */
export function appearanceExtension(theme: Theme): Extension {
  return theme === "dark" ? darkAppearance : lightAppearance;
}

/** The parts of the configuration that can change while the editor is open. One set per editor. */
export interface EditorCompartments {
  readonly mode: Compartment;
  readonly language: Compartment;
  readonly label: Compartment;
  /** Light or dark, following the app theme (appearanceExtension). */
  readonly appearance: Compartment;
  /** The editor settings (preferencesFor), following the settings file. */
  readonly preferences: Compartment;
}

export function createCompartments(): EditorCompartments {
  return {
    mode: new Compartment(),
    language: new Compartment(),
    label: new Compartment(),
    appearance: new Compartment(),
    preferences: new Compartment(),
  };
}

const noPreferences: Extension = [];

/**
 * What an editor takes from the editor settings: everything for a code
 * editor, nothing for a one-line field (its font follows the CSS
 * variables). The same value for the same settings, so a compartment can
 * compare it.
 */
export function preferencesFor(prefs: EditorPreferences, singleLine: boolean): Extension {
  return singleLine ? noPreferences : preferencesExtension(prefs);
}

export interface EditorOptions {
  readonly readOnly: boolean;
  readonly language: EditorLanguage;
  readonly ariaLabel: string;
  /** A one-line field (singleLine.ts) instead of a code editor. Fixed for the editor's life. */
  readonly singleLine?: boolean;
  /** Shown while a single-line field is empty. */
  readonly placeholder?: string;
  /**
   * `{{variable}}` chips (templateVariables.ts) in a code editor, for the
   * text fields of a request. Single-line fields always have them. Fixed
   * for the editor's life.
   */
  readonly templates?: boolean;
}

/** The full extension list for a new editor state. `extra` comes last (e.g. the change listener). */
export function buildExtensions(
  compartments: EditorCompartments,
  options: EditorOptions,
  extra: Extension = [],
): Extension {
  return [
    compartments.mode.of(modeExtension(options.readOnly, options.singleLine)),
    compartments.language.of(languageExtension(options.language)),
    compartments.label.of(labelExtension(options.ariaLabel)),
    compartments.appearance.of(appearanceExtension(currentTheme())),
    // Before baseExtensions: the line numbers go left of the fold gutter.
    compartments.preferences.of(preferencesFor(currentEditorPreferences(), options.singleLine ?? false)),
    options.singleLine ? singleLineExtensions(options.placeholder ?? "") : baseExtensions,
    !options.singleLine && options.templates ? templateVariables : [],
    extra,
  ];
}
