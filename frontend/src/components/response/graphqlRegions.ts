// Line decorations for the GraphQL response view (GQL-05): every line of the
// top-level `data` member gets a green bar at the left edge, every line of
// `errors` a red bar and a faint red tint, so both stand out in the pretty
// JSON. Set the regions with setGraphqlRegions after the text changes.

import { StateEffect, StateField, type EditorState, type Extension, type Range } from "@codemirror/state";
import { Decoration, EditorView, type DecorationSet } from "@codemirror/view";
import type { LineRegion } from "../../lib/graphqlResponse";

export interface GraphqlRegions {
  readonly data: LineRegion | null;
  readonly errors: LineRegion | null;
}

export const setGraphqlRegions = StateEffect.define<GraphqlRegions>();

const dataLine = Decoration.line({ class: "cm-gqlData", attributes: { "data-gql": "data" } });
const errorsLine = Decoration.line({ class: "cm-gqlErrors", attributes: { "data-gql": "errors" } });

function decorate(state: EditorState, regions: GraphqlRegions): DecorationSet {
  const ranges: Range<Decoration>[] = [];
  const add = (region: LineRegion | null, deco: Decoration) => {
    if (region === null) return;
    const last = Math.min(region.to, state.doc.lines);
    for (let n = Math.max(1, region.from); n <= last; n++) ranges.push(deco.range(state.doc.line(n).from));
  };
  add(regions.data, dataLine);
  add(regions.errors, errorsLine);
  return Decoration.set(ranges, true);
}

const regionsField = StateField.define<DecorationSet>({
  create: () => Decoration.none,
  update(decorations, tr) {
    let next = decorations.map(tr.changes);
    for (const effect of tr.effects) {
      if (effect.is(setGraphqlRegions)) next = decorate(tr.state, effect.value);
    }
    return next;
  },
  provide: (field) => EditorView.decorations.from(field),
});

const regionsTheme = EditorView.theme({
  ".cm-gqlData": {
    boxShadow: "inset 2px 0 0 var(--ok)",
  },
  ".cm-gqlErrors": {
    boxShadow: "inset 2px 0 0 var(--bad)",
    backgroundColor: "color-mix(in srgb, var(--bad) 6%, transparent)",
  },
});

export const graphqlRegionsExtension: Extension = [regionsField, regionsTheme];

/** Adds the regions to a live view (from CodeEditor's onReady), once. */
export function installGraphqlRegions(view: EditorView): void {
  if (view.state.field(regionsField, false) !== undefined) return;
  view.dispatch({ effects: StateEffect.appendConfig.of(graphqlRegionsExtension) });
}
