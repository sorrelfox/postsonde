// Underlines a range of a single-line field with the problem's message on
// hover: the route path's diagnostic (MOCK-01), whose position Go gives as
// a UTF-8 byte range. The JSON errorMarker marks one character; a path
// problem ("{id" without its brace) is a span.

import { StateEffect, StateField, type Extension } from "@codemirror/state";
import { Decoration, EditorView, hoverTooltip, type DecorationSet } from "@codemirror/view";

export interface RangeMarker {
  /** UTF-16 positions in the document. */
  readonly from: number;
  readonly to: number;
  readonly message: string;
}

/** Sets (or, with null, clears) the marked range. */
export const setRangeMarker = StateEffect.define<RangeMarker | null>();

const mark = Decoration.mark({ class: "cm-rangeMarker" });

interface MarkerState {
  readonly marker: RangeMarker | null;
  readonly decorations: DecorationSet;
}

/** The range clamped to a document of `length`, at least one character when possible. */
export function clampRange(marker: RangeMarker, length: number): { from: number; to: number } | null {
  if (length === 0) return null;
  const from = Math.max(0, Math.min(marker.from, length - 1));
  const to = Math.max(from + 1, Math.min(marker.to, length));
  return { from, to };
}

const field = StateField.define<MarkerState>({
  create: () => ({ marker: null, decorations: Decoration.none }),
  update(value, tr) {
    let marker = value.marker;
    let changed = false;
    if (tr.docChanged && marker !== null) {
      marker = { ...marker, from: tr.changes.mapPos(marker.from), to: tr.changes.mapPos(marker.to, 1) };
      changed = true;
    }
    for (const e of tr.effects) {
      if (e.is(setRangeMarker)) {
        marker = e.value;
        changed = true;
      }
    }
    if (!changed) return value;
    const range = marker === null ? null : clampRange(marker, tr.state.doc.length);
    return {
      marker,
      decorations: range === null ? Decoration.none : Decoration.set([mark.range(range.from, range.to)]),
    };
  },
  provide: (f) => EditorView.decorations.from(f, (v) => v.decorations),
});

const tooltip = hoverTooltip((view, pos) => {
  const marker = view.state.field(field, false)?.marker ?? null;
  if (marker === null) return null;
  const range = clampRange(marker, view.state.doc.length);
  if (range === null || pos < range.from || pos > range.to) return null;
  return {
    pos: range.from,
    end: range.to,
    above: true,
    create() {
      const dom = document.createElement("div");
      dom.className = "cm-rangeMarkerTooltip";
      dom.textContent = marker.message;
      return { dom };
    },
  };
});

const theme = EditorView.theme({
  ".cm-rangeMarker": {
    textDecoration: "underline wavy var(--tk-err)",
    textDecorationSkipInk: "none",
    textUnderlineOffset: "3px",
    backgroundColor: "color-mix(in srgb, var(--tk-err) 18%, transparent)",
    borderRadius: "var(--radius-xs)",
  },
  ".cm-rangeMarkerTooltip": {
    padding: "var(--space-2) var(--space-4)",
    color: "var(--text)",
    fontFamily: "var(--font-sans)",
    fontSize: "var(--fs-md)",
  },
});

const rangeMarkerExtension: Extension = [field, tooltip, theme];

/** Adds the range marker to a live view (from CodeEditor's onReady), once. */
export function installRangeMarker(view: EditorView): void {
  if (view.state.field(field, false) !== undefined) return;
  view.dispatch({ effects: StateEffect.appendConfig.of(rangeMarkerExtension) });
}

/** The UTF-16 index of UTF-8 byte offset `byte` in `text` (the end when past it). */
export function byteToIndex(text: string, byte: number): number {
  let bytes = 0;
  let i = 0;
  while (i < text.length && bytes < byte) {
    const code = text.codePointAt(i) ?? 0;
    bytes += code < 0x80 ? 1 : code < 0x800 ? 2 : code < 0x10000 ? 3 : 4;
    i += code >= 0x10000 ? 2 : 1;
  }
  return i;
}
