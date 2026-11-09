// An error marker for the code editor (HTTP-12): a wavy underline on
// the character at one position, a tinted line, and the message on hover.
// @codemirror/lint is not a dependency, and one marker is all the JSON
// tools need.
//
// The marker lives in a StateField. Set it with setErrorMarker (null clears
// it); edits move it with the text, so it stays on the same character until
// the next check replaces it. Add errorMarkerExtension to a view once (see
// installErrorMarker).

import { StateEffect, StateField, type EditorState, type Extension, type Range } from "@codemirror/state";
import { Decoration, EditorView, hoverTooltip, type DecorationSet } from "@codemirror/view";

export interface ErrorMarker {
  /** Position of the offending character, in UTF-16 code units (a CodeMirror position). */
  readonly offset: number;
  readonly message: string;
}

/** Sets (or, with null, clears) the error marker. */
export const setErrorMarker = StateEffect.define<ErrorMarker | null>({
  map: (value, mapping) => (value === null ? null : { ...value, offset: mapping.mapPos(value.offset) }),
});

interface MarkerState {
  readonly marker: ErrorMarker | null;
  readonly decorations: DecorationSet;
}

/**
 * The range to underline for an error at `offset`: the character there, or
 * the last character when the text ends too early. A line break is not
 * visible, so an error on one marks the character before it.
 */
export function markerRange(doc: EditorState["doc"], offset: number): { from: number; to: number } | null {
  if (doc.length === 0) return null;
  let from = Math.max(0, Math.min(offset, doc.length - 1));
  if (from > 0 && doc.sliceString(from, from + 1) === "\n") from -= 1;
  // Keep a surrogate pair together.
  const code = doc.sliceString(from, from + 1).charCodeAt(0);
  const to = code >= 0xd800 && code <= 0xdbff && from + 2 <= doc.length ? from + 2 : from + 1;
  return { from, to };
}

const errorMark = Decoration.mark({ class: "cm-errorMarker" });
const errorLine = Decoration.line({ class: "cm-errorMarkerLine" });

function decorate(state: EditorState, marker: ErrorMarker | null): DecorationSet {
  if (marker === null) return Decoration.none;
  const range = markerRange(state.doc, marker.offset);
  if (range === null) return Decoration.none;
  const line = state.doc.lineAt(range.from);
  const ranges: Range<Decoration>[] = [
    errorLine.range(line.from),
    errorMark.range(range.from, range.to),
  ];
  return Decoration.set(ranges, true);
}

const markerField = StateField.define<MarkerState>({
  create: () => ({ marker: null, decorations: Decoration.none }),
  update(value, tr) {
    let { marker } = value;
    let changed = false;
    if (tr.docChanged && marker !== null) {
      marker = { ...marker, offset: tr.changes.mapPos(marker.offset) };
      changed = true;
    }
    for (const effect of tr.effects) {
      if (effect.is(setErrorMarker)) {
        marker = effect.value;
        changed = true;
      }
    }
    if (!changed) return value;
    return { marker, decorations: decorate(tr.state, marker) };
  },
  provide: (field) => EditorView.decorations.from(field, (value) => value.decorations),
});

/** The marker currently shown, with its position moved through edits since it was set. */
export function currentErrorMarker(state: EditorState): ErrorMarker | null {
  return state.field(markerField, false)?.marker ?? null;
}

const markerTooltip = hoverTooltip((view, pos) => {
  const marker = currentErrorMarker(view.state);
  if (marker === null) return null;
  const range = markerRange(view.state.doc, marker.offset);
  if (range === null || pos < range.from || pos > range.to) return null;
  return {
    pos: range.from,
    end: range.to,
    above: true,
    create() {
      const dom = document.createElement("div");
      dom.className = "cm-errorMarkerTooltip";
      dom.textContent = marker.message;
      return { dom };
    },
  };
});

const markerTheme = EditorView.theme({
  ".cm-errorMarker": {
    textDecoration: "underline wavy var(--tk-err)",
    textDecorationSkipInk: "none",
    textUnderlineOffset: "3px",
    backgroundColor: "color-mix(in srgb, var(--tk-err) 22%, transparent)",
    borderRadius: "var(--radius-xs)",
  },
  ".cm-errorMarkerLine": {
    backgroundColor: "color-mix(in srgb, var(--tk-err) 8%, transparent)",
  },
  ".cm-errorMarkerTooltip": {
    padding: "var(--space-2) var(--space-4)",
    color: "var(--text)",
    fontFamily: "var(--font-sans)",
    fontSize: "var(--fs-md)",
  },
});

/** The marker field, its hover tooltip and styles. */
export const errorMarkerExtension: Extension = [markerField, markerTooltip, markerTheme];

/** Adds the marker to a live view (e.g. from CodeEditor's onReady), once. */
export function installErrorMarker(view: EditorView): void {
  if (view.state.field(markerField, false) !== undefined) return;
  view.dispatch({ effects: StateEffect.appendConfig.of(errorMarkerExtension) });
}
