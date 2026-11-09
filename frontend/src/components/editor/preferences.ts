// The editor settings (UI-08) as every CodeMirror editor applies them: font
// family and size, tab size, word wrap, line numbers and bracket matching.
//
// The settings store owns the values (settings file section "editor");
// useEditorPreferencesSync (App) hands them to setEditorPreferences, and
// this module applies them in two ways, both live:
// - The font goes into CSS variables on <html> (--font-editor,
//   --fs-editor, --lh-editor; defaults in styles/tokens.css), which the
//   editor themes use. Code editors, response viewers and the one-line
//   fields (URL) take the family; the size is for code editors and viewers.
// - The rest is an extension (preferencesExtension) in each editor's
//   `preferences` compartment, reconfigured by CodeEditor whenever the
//   preferences change, so open editors follow without being re-created.

import { EditorState, type Extension } from "@codemirror/state";
import { EditorView, lineNumbers } from "@codemirror/view";
import { bracketMatching, indentUnit } from "@codemirror/language";
import { initialSettingsState, editorSettingsOf, type EditorSettings } from "../../stores/settings";

export type EditorPreferences = EditorSettings;

export const DEFAULT_EDITOR_PREFERENCES: EditorPreferences = editorSettingsOf(initialSettingsState);

/** Font names that are CSS generic families, written without quotes. */
const GENERIC_FAMILIES = new Set(["monospace", "ui-monospace"]);

/**
 * The CSS font-family value for a font name: the name, quoted unless it is
 * a generic family, then the bundled stack (--font-mono) for a font that is
 * not installed. The name is validated by the settings file, so it cannot
 * contain quotes or backslashes; they are dropped here anyway.
 */
export function fontFamilyCss(name: string): string {
  if (GENERIC_FAMILIES.has(name)) return `${name}, var(--font-mono)`;
  return `"${name.replace(/["\\]/g, "")}", var(--font-mono)`;
}

/** Line height for a font size: the design's 22px at 12px, in proportion, whole pixels. */
export function lineHeightFor(fontSize: number): number {
  return Math.round((fontSize * 22) / 12);
}

let current: EditorPreferences = DEFAULT_EDITOR_PREFERENCES;
const listeners = new Set<() => void>();

/** The preferences in effect. The same object until they change. */
export function currentEditorPreferences(): EditorPreferences {
  return current;
}

/** Calls `listener` after the preferences change. Returns the unsubscribe function. */
export function subscribeEditorPreferences(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

function samePreferences(a: EditorPreferences, b: EditorPreferences): boolean {
  return (Object.keys(a) as (keyof EditorPreferences)[]).every((k) => a[k] === b[k]);
}

/** Writes the font variables on <html>; the defaults are removed so tokens.css applies. */
function applyFont(prefs: EditorPreferences): void {
  const style = document.documentElement.style;
  const set = (name: string, value: string | null) => {
    if (value === null) style.removeProperty(name);
    else style.setProperty(name, value);
  };
  const d = DEFAULT_EDITOR_PREFERENCES;
  set("--font-editor", prefs.editorFontFamily === d.editorFontFamily ? null : fontFamilyCss(prefs.editorFontFamily));
  const defaultSize = prefs.editorFontSize === d.editorFontSize;
  set("--fs-editor", defaultSize ? null : `${prefs.editorFontSize}px`);
  set("--lh-editor", defaultSize ? null : `${lineHeightFor(prefs.editorFontSize)}px`);
}

/**
 * Applies new preferences: the font variables at once, then every open
 * editor through its listener. Equal values change nothing.
 */
export function setEditorPreferences(next: EditorPreferences): void {
  if (samePreferences(next, current)) return;
  current = { ...next };
  applyFont(current);
  for (const listener of [...listeners]) listener();
}

/** Test helper: back to the defaults, with no listeners notified. */
export function resetEditorPreferencesForTests(): void {
  current = DEFAULT_EDITOR_PREFERENCES;
  applyFont(current);
}

// One extension per preferences object, so a compartment can compare it
// with what it holds and every editor shares the same instances.
const extensions = new WeakMap<EditorPreferences, Extension>();

/**
 * The code-editor part of the preferences: tab size and indent unit, word
 * wrap, the line-number gutter and bracket matching. One-line fields take
 * none of it (only the font, through the CSS variables).
 */
export function preferencesExtension(prefs: EditorPreferences): Extension {
  let ext = extensions.get(prefs);
  if (ext === undefined) {
    ext = [
      EditorState.tabSize.of(prefs.editorTabSize),
      indentUnit.of(" ".repeat(prefs.editorTabSize)),
      prefs.editorWordWrap ? EditorView.lineWrapping : [],
      prefs.editorLineNumbers ? lineNumbers() : [],
      prefs.editorMatchBrackets ? bracketMatching() : [],
    ];
    extensions.set(prefs, ext);
  }
  return ext;
}
