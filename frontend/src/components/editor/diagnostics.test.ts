import { EditorState } from "@codemirror/state";
import { describe, expect, it } from "vitest";
import { currentDiagnostics, diagnosticRange, diagnosticsExtension, setDiagnostics } from "./diagnostics";

describe("diagnostics", () => {
  const state = (doc: string) => EditorState.create({ doc, extensions: diagnosticsExtension });

  it("underlines a visible range, also for empty ones", () => {
    const doc = state("{ a(\n").doc;
    expect(diagnosticRange(doc, { from: 2, to: 3 })).toEqual({ from: 2, to: 3 });
    // The end of input: the character before it.
    expect(diagnosticRange(doc, { from: 5, to: 5 })).toEqual({ from: 3, to: 4 });
    // An empty range on a line break: the character before it.
    expect(diagnosticRange(doc, { from: 4, to: 4 })).toEqual({ from: 3, to: 4 });
    expect(diagnosticRange(state("").doc, { from: 0, to: 0 })).toBeNull();
  });

  it("holds several diagnostics, moves them with edits and clears them", () => {
    let s = state("{ logn nope }");
    s = s.update({
      effects: setDiagnostics.of([
        { from: 2, to: 6, severity: "error", message: "unknown logn" },
        { from: 7, to: 11, severity: "warning", message: "unknown nope" },
      ]),
    }).state;
    expect(currentDiagnostics(s)).toHaveLength(2);
    s = s.update({ changes: { from: 0, insert: "query " } }).state;
    expect(currentDiagnostics(s).map((d) => [d.from, d.to])).toEqual([
      [8, 12],
      [13, 17],
    ]);
    s = s.update({ effects: setDiagnostics.of([]) }).state;
    expect(currentDiagnostics(s)).toEqual([]);
  });
});
