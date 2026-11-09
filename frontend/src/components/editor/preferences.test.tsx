import { afterEach, describe, expect, it, vi } from "vitest";
import { act, render } from "@testing-library/react";
import type { EditorView } from "@codemirror/view";
import { indentUnit } from "@codemirror/language";
import { CodeEditor } from "./CodeEditor";
import {
  DEFAULT_EDITOR_PREFERENCES,
  currentEditorPreferences,
  fontFamilyCss,
  lineHeightFor,
  resetEditorPreferencesForTests,
  setEditorPreferences,
  subscribeEditorPreferences,
  type EditorPreferences,
} from "./preferences";

afterEach(() => {
  resetEditorPreferencesForTests();
});

const rootVar = (name: string) => document.documentElement.style.getPropertyValue(name);
const prefs = (patch: Partial<EditorPreferences>): EditorPreferences => ({ ...DEFAULT_EDITOR_PREFERENCES, ...patch });

describe("editor preferences", () => {
  it("quotes a font name and falls back to the bundled stack; generic families stay bare", () => {
    expect(fontFamilyCss("Fira Code")).toBe('"Fira Code", var(--font-mono)');
    expect(fontFamilyCss("ui-monospace")).toBe("ui-monospace, var(--font-mono)");
    expect(fontFamilyCss('Bad"Name\\')).toBe('"BadName", var(--font-mono)');
  });

  it("scales the line height with the font size", () => {
    expect(lineHeightFor(12)).toBe(22);
    expect(lineHeightFor(16)).toBe(29);
    expect(lineHeightFor(8)).toBe(15);
  });

  it("sets the font variables on <html>, and removes them for the defaults", () => {
    setEditorPreferences(prefs({ editorFontFamily: "Menlo", editorFontSize: 15 }));
    expect(rootVar("--font-editor")).toBe('"Menlo", var(--font-mono)');
    expect(rootVar("--fs-editor")).toBe("15px");
    expect(rootVar("--lh-editor")).toBe("28px");

    setEditorPreferences(DEFAULT_EDITOR_PREFERENCES);
    expect(rootVar("--font-editor")).toBe("");
    expect(rootVar("--fs-editor")).toBe("");
    expect(rootVar("--lh-editor")).toBe("");
  });

  it("notifies listeners only when a value changes", () => {
    const listener = vi.fn();
    const stop = subscribeEditorPreferences(listener);
    const before = currentEditorPreferences();
    setEditorPreferences({ ...DEFAULT_EDITOR_PREFERENCES });
    expect(listener).not.toHaveBeenCalled();
    expect(currentEditorPreferences()).toBe(before);

    setEditorPreferences(prefs({ editorTabSize: 4 }));
    expect(listener).toHaveBeenCalledTimes(1);
    expect(currentEditorPreferences().editorTabSize).toBe(4);
    stop();
    setEditorPreferences(prefs({ editorTabSize: 8 }));
    expect(listener).toHaveBeenCalledTimes(1);
  });
});

describe("CodeEditor follows the editor preferences (UI-08)", () => {
  function mount(singleLine = false) {
    let view: EditorView | null = null;
    render(
      <CodeEditor
        value={'{\n  "a": [1, 2]\n}'}
        language="json"
        ariaLabel="Body"
        singleLine={singleLine}
        onReady={(v) => {
          view = v;
        }}
      />,
    );
    return () => view!;
  }

  it("uses the settings in effect when it mounts", () => {
    setEditorPreferences(prefs({ editorTabSize: 4, editorWordWrap: true, editorLineNumbers: false }));
    const view = mount();
    expect(view().state.tabSize).toBe(4);
    expect(view().state.facet(indentUnit)).toBe("    ");
    expect(view().contentDOM.classList.contains("cm-lineWrapping")).toBe(true);
    expect(view().dom.querySelector(".cm-lineNumbers")).toBeNull();
  });

  it("applies changes live, without a new view", () => {
    const view = mount();
    const first = view();
    expect(first.state.tabSize).toBe(2);
    expect(first.dom.querySelector(".cm-lineNumbers")).not.toBeNull();
    expect(first.contentDOM.classList.contains("cm-lineWrapping")).toBe(false);

    act(() => setEditorPreferences(prefs({ editorTabSize: 8, editorWordWrap: true, editorLineNumbers: false })));
    expect(view()).toBe(first);
    expect(first.state.tabSize).toBe(8);
    expect(first.state.facet(indentUnit)).toBe("        ");
    expect(first.contentDOM.classList.contains("cm-lineWrapping")).toBe(true);
    expect(first.dom.querySelector(".cm-lineNumbers")).toBeNull();

    act(() => setEditorPreferences(DEFAULT_EDITOR_PREFERENCES));
    expect(first.state.tabSize).toBe(2);
    expect(first.dom.querySelector(".cm-lineNumbers")).not.toBeNull();
  });

  it("turns bracket matching on and off", () => {
    const view = mount();
    const matched = () => {
      act(() => view().dispatch({ selection: { anchor: 10 } })); // just after "["
      return view().dom.querySelector(".cm-matchingBracket") !== null;
    };
    act(() => view().focus());
    expect(matched()).toBe(true);
    act(() => setEditorPreferences(prefs({ editorMatchBrackets: false })));
    expect(matched()).toBe(false);
  });

  it("gives one-line fields none of the code-editor settings", () => {
    setEditorPreferences(prefs({ editorWordWrap: true, editorLineNumbers: true }));
    const view = mount(true);
    expect(view().contentDOM.classList.contains("cm-lineWrapping")).toBe(false);
    expect(view().dom.querySelector(".cm-lineNumbers")).toBeNull();
  });
});
