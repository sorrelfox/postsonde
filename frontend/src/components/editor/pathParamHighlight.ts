// Marks the path placeholders (`:id`, `{id}`, HTTP-05) in the URL field with
// `cm-pathParam`, found with lib/pathParams.ts, the same rule the Params
// tab's path table and Go use. Only the URL field gets it (not every
// single-line field), so it is added to the live view from CodeEditor's
// onReady with installPathParamHighlight.

import { StateEffect, type Extension } from "@codemirror/state";
import { Decoration, EditorView, ViewPlugin, type DecorationSet, type ViewUpdate } from "@codemirror/view";
import { findPathParams } from "../../lib/pathParams";

const pathParamMark = Decoration.mark({ class: "cm-pathParam" });

function decorations(view: EditorView): DecorationSet {
  const doc = view.state.doc.toString();
  return Decoration.set(findPathParams(doc).map((p) => pathParamMark.range(p.start, p.end)));
}

const plugin = ViewPlugin.fromClass(
  class {
    decorations: DecorationSet;
    constructor(view: EditorView) {
      this.decorations = decorations(view);
    }
    update(update: ViewUpdate) {
      if (update.docChanged) this.decorations = decorations(update.view);
    }
  },
  { decorations: (v) => v.decorations },
);

const theme = EditorView.theme({
  ".cm-pathParam": { color: "var(--tk-arg)" },
});

/** The highlight of path placeholders. */
export const pathParamHighlight: Extension = [plugin, theme];

/** Adds the highlight to a live view (e.g. from CodeEditor's onReady), once. */
export function installPathParamHighlight(view: EditorView): void {
  if (view.plugin(plugin) !== null) return;
  view.dispatch({ effects: StateEffect.appendConfig.of(pathParamHighlight) });
}
