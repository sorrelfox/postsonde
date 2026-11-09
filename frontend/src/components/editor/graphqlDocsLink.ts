// Jump to the docs from the GraphQL query editor (GQL-15): Ctrl-click
// (Cmd-click on macOS) on a name asks Go what it refers to
// (GraphQLService.DocsTarget) and opens it in the docs explorer. While the
// modifier is held, the name under the pointer is underlined like a link.
// A Mod-click elsewhere (on blanks or punctuation) keeps CodeMirror's own
// meaning, adding a cursor. See docs/graphql.md.
//
// The handler lives in a StateField (setDocsLinkHandler), like the
// completion schema key: a query editor's state outlives the component
// that installs it, so the click must reach the component of now.

import { Prec, StateEffect, StateField, type EditorState, type Extension } from "@codemirror/state";
import { Decoration, EditorView, ViewPlugin, type DecorationSet, type ViewUpdate } from "@codemirror/view";
import { IS_MAC } from "../../lib/shortcuts";

/** Called with the document offset of a Mod-click on a name. */
export type DocsLinkHandler = (offset: number) => void;

/** Sets (or clears, with null) what a Mod-click on a name does. */
export const setDocsLinkHandler = StateEffect.define<DocsLinkHandler | null>();

const handlerField = StateField.define<DocsLinkHandler | null>({
  create: () => null,
  update(value, tr) {
    for (const effect of tr.effects) if (effect.is(setDocsLinkHandler)) value = effect.value;
    return value;
  },
});

/** Whether a mouse event carries the link modifier alone: ⌘ on macOS, Ctrl elsewhere. */
export function isDocsLinkModifier(e: Pick<MouseEvent, "metaKey" | "ctrlKey" | "altKey" | "shiftKey">, mac: boolean = IS_MAC): boolean {
  if (e.altKey || e.shiftKey) return false;
  return mac ? e.metaKey && !e.ctrlKey : e.ctrlKey && !e.metaKey;
}

const NAME_CHAR = /[_A-Za-z0-9]/;

/** The GraphQL name around `pos` (touching it on either side), or null. */
export function nameAt(state: EditorState, pos: number): { from: number; to: number } | null {
  const line = state.doc.lineAt(pos);
  const text = line.text;
  let start = pos - line.from;
  let end = start;
  while (start > 0 && NAME_CHAR.test(text[start - 1] ?? "")) start--;
  while (end < text.length && NAME_CHAR.test(text[end] ?? "")) end++;
  if (start === end || /[0-9]/.test(text[start] ?? "")) return null;
  return { from: line.from + start, to: line.from + end };
}

const linkMark = Decoration.mark({ class: "cm-gqlDocsLink" });

/** Underlines the name under the pointer while the modifier is held. */
const hoverLink = ViewPlugin.fromClass(
  class {
    decorations: DecorationSet = Decoration.none;
    current: { from: number; to: number } | null = null;
    constructor(readonly view: EditorView) {}
    update(update: ViewUpdate) {
      if (update.docChanged && this.current !== null) {
        this.current = null;
        this.decorations = Decoration.none;
      }
    }
    show(range: { from: number; to: number } | null) {
      const cur = this.current;
      if (range === cur || (range !== null && cur !== null && range.from === cur.from && range.to === cur.to)) return;
      this.current = range;
      this.decorations = range === null ? Decoration.none : Decoration.set([linkMark.range(range.from, range.to)]);
      // Redraw with the new decorations (an empty transaction).
      this.view.dispatch({});
    }
  },
  {
    decorations: (p) => p.decorations,
    eventHandlers: {
      mousemove(e, view) {
        const range =
          isDocsLinkModifier(e) && view.state.field(handlerField, false) ? nameUnder(view, e) : null;
        this.show(range);
      },
      mouseleave() {
        this.show(null);
      },
      keyup() {
        this.show(null);
      },
    },
  },
);

function nameUnder(view: EditorView, e: MouseEvent): { from: number; to: number } | null {
  const pos = view.posAtCoords({ x: e.clientX, y: e.clientY }, false);
  return pos === null ? null : nameAt(view.state, pos);
}

const linkTheme = EditorView.baseTheme({
  ".cm-gqlDocsLink": { textDecoration: "underline", cursor: "pointer", color: "var(--accent)" },
});

/** The click handling, underline and handler field. */
export function graphqlDocsLink(): Extension {
  return [
    handlerField,
    hoverLink,
    linkTheme,
    Prec.highest(
      EditorView.domEventHandlers({
        mousedown(e, view) {
          if (e.button !== 0 || !isDocsLinkModifier(e)) return false;
          const handler = view.state.field(handlerField, false);
          if (!handler) return false;
          const pos = view.posAtCoords({ x: e.clientX, y: e.clientY }, false);
          if (pos === null || nameAt(view.state, pos) === null) return false;
          e.preventDefault();
          handler(pos);
          return true;
        },
      }),
    ),
  ];
}

/** Adds the docs link to a live view (e.g. from CodeEditor's onReady), once. */
export function installGraphqlDocsLink(view: EditorView): void {
  if (view.state.field(handlerField, false) !== undefined) return;
  view.dispatch({ effects: StateEffect.appendConfig.of(graphqlDocsLink()) });
}
