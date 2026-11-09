import { describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { EditorView } from "@codemirror/view";
import { language as languageFacet } from "@codemirror/language";
import { undoDepth } from "@codemirror/commands";
import { CodeEditor, type CodeEditorProps } from "./CodeEditor";
import { applyTheme } from "../../styles/theme";

const JSON_DOC = '{\n  "name": "Studio Monitor 5",\n  "stock": 12\n}';

function setup(initial: Partial<CodeEditorProps> = {}) {
  const views: EditorView[] = [];
  const onReady = vi.fn((v: EditorView) => void views.push(v));
  const onChange = vi.fn();
  let props: CodeEditorProps = {
    value: JSON_DOC,
    language: "json",
    ariaLabel: "Body",
    onChange,
    onReady,
    ...initial,
  };
  const utils = render(<CodeEditor {...props} />);
  return {
    ...utils,
    onChange,
    onReady,
    view: () => views[views.length - 1],
    update(next: Partial<CodeEditorProps>) {
      props = { ...props, ...next };
      utils.rerender(<CodeEditor {...props} />);
    },
  };
}

const append = (view: EditorView, text: string, userEvent?: string) =>
  act(() => {
    view.dispatch({ changes: { from: view.state.doc.length, insert: text }, userEvent });
  });

describe("CodeEditor", () => {
  it("mounts and shows the initial JSON, highlighted as JSON", () => {
    const { view } = setup();
    const box = screen.getByRole("textbox", { name: "Body" });
    expect(box.textContent).toContain('"name": "Studio Monitor 5"');
    expect(view().state.doc.toString()).toBe(JSON_DOC);
    expect(view().state.facet(languageFacet)?.name).toBe("json");
  });

  it("calls onChange with the full text after a change", () => {
    const { view, onChange } = setup();
    append(view(), "\n", "input.type");
    expect(onChange).toHaveBeenCalledTimes(1);
    expect(onChange).toHaveBeenCalledWith(`${JSON_DOC}\n`);
  });

  it("applies Enter as an edit when editable", () => {
    const { view } = setup();
    act(() => view().dispatch({ selection: { anchor: view().state.doc.length } }));
    fireEvent.keyDown(view().contentDOM, { key: "Enter", keyCode: 13 });
    expect(view().state.doc.toString()).not.toBe(JSON_DOC);
  });

  it("indents on Tab, but lets Tab leave the editor right after Escape", () => {
    const { view } = setup();
    const v = view();
    const tab = () => fireEvent.keyDown(v.contentDOM, { key: "Tab", keyCode: 9 });

    // fireEvent returns false when the handler called preventDefault.
    expect(tab()).toBe(false);
    const indented = v.state.doc.toString();
    expect(indented).not.toBe(JSON_DOC);

    fireEvent.keyDown(v.contentDOM, { key: "Escape", keyCode: 27 });
    expect(tab()).toBe(true); // not handled: the browser moves focus
    expect(v.state.doc.toString()).toBe(indented);
  });

  it("blocks edits when readOnly", () => {
    const { view, onChange } = setup({ readOnly: true });
    const v = view();
    expect(v.state.readOnly).toBe(true);
    expect(v.contentDOM.getAttribute("contenteditable")).toBe("false");
    expect(v.contentDOM.getAttribute("aria-readonly")).toBe("true");
    // Still reachable with the keyboard (Mod-F, copy).
    expect(v.contentDOM.getAttribute("tabindex")).toBe("0");

    append(v, "typed", "input.type");
    append(v, "pasted", "input.paste");
    act(() => v.dispatch({ selection: { anchor: v.state.doc.length } }));
    fireEvent.keyDown(v.contentDOM, { key: "Enter", keyCode: 13 });

    expect(v.state.doc.toString()).toBe(JSON_DOC);
    expect(onChange).not.toHaveBeenCalled();
  });

  it("takes a new value from the parent without calling onChange", () => {
    const { view, onChange, update } = setup({ readOnly: true });
    update({ value: '{ "ok": true }' });
    expect(view().state.doc.toString()).toBe('{ "ok": true }');
    expect(onChange).not.toHaveBeenCalled();
  });

  it("ignores a value that matches its own text", () => {
    const { view, update } = setup();
    append(view(), " ", "input.type");
    const state = view().state;
    update({ value: `${JSON_DOC} ` });
    expect(view().state).toBe(state);
  });

  it("loads a fresh document with empty history when docKey changes", () => {
    const { view, update, onReady } = setup({ docKey: "a" });
    append(view(), " ", "input.type");
    expect(undoDepth(view().state)).toBe(1);
    update({ docKey: "b", value: "[1, 2]" });
    expect(view().state.doc.toString()).toBe("[1, 2]");
    expect(undoDepth(view().state)).toBe(0);
    expect(onReady).toHaveBeenCalledTimes(1);
  });

  it("switches readOnly, language and label without re-creating the view", () => {
    const { view, update, onReady } = setup();
    const v = view();
    const dom = v.dom;

    update({ readOnly: true, language: "text", ariaLabel: "Response" });
    expect(v.state.readOnly).toBe(true);
    expect(v.state.facet(languageFacet)).toBeNull();
    expect(screen.getByRole("textbox", { name: "Response" })).toBe(v.contentDOM);

    update({ readOnly: false, language: "json" });
    expect(v.state.readOnly).toBe(false);
    expect(v.contentDOM.getAttribute("contenteditable")).toBe("true");
    expect(v.state.facet(languageFacet)?.name).toBe("json");

    expect(onReady).toHaveBeenCalledTimes(1);
    expect(v.dom).toBe(dom);
    expect(dom.isConnected).toBe(true);
    expect(v.state.doc.toString()).toBe(JSON_DOC);
  });

  it("replaces only the part of the text that differs, so the selection elsewhere stays", () => {
    const { view, update } = setup({ value: "abc-XYZ-def" });
    act(() => view().dispatch({ selection: { anchor: 1, head: 2 } }));
    update({ value: "abc-12345-def" });
    expect(view().state.doc.toString()).toBe("abc-12345-def");
    expect(view().state.selection.main).toMatchObject({ anchor: 1, head: 2 });
    act(() => view().dispatch({ selection: { anchor: 11 } }));
    update({ value: "abc-1-def" });
    expect(view().state.selection.main.head).toBe(7);
  });

  it("is a one-line field with singleLine: no gutters, no line breaks, Tab left alone", () => {
    const { view, onChange, container } = setup({ value: "", singleLine: true, placeholder: "Enter a URL", language: "text" });
    const v = view();
    expect(container.querySelector(".cm-gutters")).toBeNull();
    expect(container.querySelector(".cm-placeholder")?.textContent).toBe("Enter a URL");
    // Not an editor scope, so ⌘/ and ⌘Enter stay global shortcuts.
    expect(v.dom.parentElement?.hasAttribute("data-shortcut-scope")).toBe(false);

    fireEvent.keyDown(v.contentDOM, { key: "Enter", keyCode: 13 });
    append(v, "a\nb\r\nc", "input.type");
    expect(v.state.doc.toString()).toBe("abc");
    expect(v.state.selection.main.head).toBe(3);
    expect(onChange).toHaveBeenLastCalledWith("abc");

    const tab = new KeyboardEvent("keydown", { key: "Tab", keyCode: 9, bubbles: true, cancelable: true });
    v.contentDOM.dispatchEvent(tab);
    expect(tab.defaultPrevented).toBe(false);
  });

  it("destroys the view on unmount", () => {
    const destroy = vi.spyOn(EditorView.prototype, "destroy");
    const { view, unmount, container } = setup();
    const dom = view().dom;
    unmount();
    expect(destroy).toHaveBeenCalledTimes(1);
    expect(dom.isConnected).toBe(false);
    expect(container.querySelector(".cm-editor")).toBeNull();
    destroy.mockRestore();
  });

  it("follows the app theme's light/dark mode without re-creating the view", () => {
    applyTheme("light");
    const { view, onReady, update } = setup({ docKey: "a" });
    expect(view().state.facet(EditorView.darkTheme)).toBe(false);

    act(() => void applyTheme("dark"));
    expect(view().state.facet(EditorView.darkTheme)).toBe(true);
    expect(onReady).toHaveBeenCalledTimes(1);

    // A new document keeps the current mode.
    update({ docKey: "b", value: "[]" });
    expect(view().state.facet(EditorView.darkTheme)).toBe(true);

    act(() => void applyTheme("light"));
    expect(view().state.facet(EditorView.darkTheme)).toBe(false);
  });
});
