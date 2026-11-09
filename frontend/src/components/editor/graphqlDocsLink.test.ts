import { EditorState } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { afterEach, describe, expect, it, vi } from "vitest";
import { IS_MAC } from "../../lib/shortcuts";
import { installGraphqlDocsLink, isDocsLinkModifier, nameAt, setDocsLinkHandler } from "./graphqlDocsLink";

const mods = { metaKey: false, ctrlKey: false, altKey: false, shiftKey: false };

describe("isDocsLinkModifier", () => {
  it("is ⌘ alone on macOS and Ctrl alone elsewhere", () => {
    expect(isDocsLinkModifier({ ...mods, metaKey: true }, true)).toBe(true);
    expect(isDocsLinkModifier({ ...mods, ctrlKey: true }, true)).toBe(false);
    expect(isDocsLinkModifier({ ...mods, ctrlKey: true }, false)).toBe(true);
    expect(isDocsLinkModifier({ ...mods, metaKey: true }, false)).toBe(false);
    expect(isDocsLinkModifier({ ...mods, ctrlKey: true, shiftKey: true }, false)).toBe(false);
    expect(isDocsLinkModifier({ ...mods, metaKey: true, altKey: true }, true)).toBe(false);
    expect(isDocsLinkModifier(mods, true)).toBe(false);
  });
});

describe("nameAt", () => {
  const state = EditorState.create({ doc: "{ user(id: 12) {\n  __typename }\n}" });

  it("finds the name around or touching a position", () => {
    expect(nameAt(state, 3)).toEqual({ from: 2, to: 6 });
    expect(nameAt(state, 2)).toEqual({ from: 2, to: 6 });
    expect(nameAt(state, 6)).toEqual({ from: 2, to: 6 });
    const line2 = state.doc.line(2).from;
    expect(nameAt(state, line2 + 4)).toEqual({ from: line2 + 2, to: line2 + 12 });
  });

  it("finds nothing on blanks, punctuation or numbers", () => {
    expect(nameAt(state, 1)).toBeNull();
    expect(nameAt(state, 12)).toBeNull(); // in "12"
    expect(nameAt(state, state.doc.length)).toBeNull();
  });
});

describe("the Ctrl/Cmd-click", () => {
  let view: EditorView | null = null;
  afterEach(() => {
    view?.destroy();
    view = null;
  });

  const click = (v: EditorView, over: Partial<MouseEventInit> = {}) => {
    const event = new MouseEvent("mousedown", { button: 0, bubbles: true, cancelable: true, [IS_MAC ? "metaKey" : "ctrlKey"]: true, ...over });
    v.contentDOM.dispatchEvent(event);
    return event;
  };

  it("calls the handler with the position of a name, and leaves other clicks to CodeMirror", () => {
    view = new EditorView({ doc: "{ user { id } }", parent: document.body });
    installGraphqlDocsLink(view);
    installGraphqlDocsLink(view); // once
    const pos = vi.spyOn(view, "posAtCoords").mockReturnValue(3);
    const handler = vi.fn();
    // No handler yet: CodeMirror's click (a cursor at the position).
    click(view);
    expect(handler).not.toHaveBeenCalled();

    view.dispatch({ effects: setDocsLinkHandler.of(handler) });
    expect(click(view).defaultPrevented).toBe(true);
    expect(handler).toHaveBeenCalledWith(3);

    // Without the modifier, or on a blank: CodeMirror's own click.
    click(view, { metaKey: false, ctrlKey: false });
    pos.mockReturnValue(1);
    click(view);
    expect(handler).toHaveBeenCalledTimes(1);

    view.dispatch({ effects: setDocsLinkHandler.of(null) });
    pos.mockReturnValue(3);
    click(view);
    expect(handler).toHaveBeenCalledTimes(1);
  });
});
