import { EditorState } from "@codemirror/state";
import { highlightTree, tagHighlighter, tags as t } from "@lezer/highlight";
import { describe, expect, it } from "vitest";
import { foldByIndent, htmlLanguage, xmlLanguage } from "./markup";
import type { StreamLanguage } from "@codemirror/language";

/** Class names for the tags the tokenizer emits (sub-tags first, so they win). */
const highlighter = tagHighlighter([
  { tag: t.tagName, class: "tag" },
  { tag: t.attributeName, class: "attr" },
  { tag: t.character, class: "entity" },
  { tag: t.string, class: "string" },
  { tag: t.comment, class: "comment" },
  { tag: t.meta, class: "meta" },
  { tag: t.angleBracket, class: "bracket" },
]);

/** The highlighted tokens of `text`: [text, class] pairs. */
function tokens(language: StreamLanguage<unknown>, text: string): [string, string][] {
  const tree = language.parser.parse(text);
  const out: [string, string][] = [];
  highlightTree(tree, highlighter, (from, to, classes) => out.push([text.slice(from, to), classes]));
  return out;
}

const cls = (t: [string, string][], text: string) => t.find(([s]) => s === text)?.[1];

describe("markup highlighting", () => {
  it("colours XML tags, attributes, values, comments, declarations and entities", () => {
    const toks = tokens(xmlLanguage as StreamLanguage<unknown>, '<?xml version="1.0"?>\n<!-- note -->\n<a:b x="1" y=\'2\'>t &amp; u<![CDATA[<raw>]]></a:b>');
    expect(cls(toks, '<?xml version="1.0"?>')).toBe("meta");
    expect(cls(toks, "<!-- note -->")).toBe("comment");
    expect(cls(toks, "a:b")).toBe("tag");
    expect(cls(toks, "x")).toBe("attr");
    expect(cls(toks, '"1"')).toBe("string");
    expect(cls(toks, "'2'")).toBe("string");
    expect(cls(toks, "&amp;")).toBe("entity");
    expect(cls(toks, "<![CDATA[<raw>]]>")).toBe("meta");
    expect(cls(toks, "<")).toBe("bracket");
    expect(toks.some(([s]) => s === "raw")).toBe(false);
  });

  it("carries comments and attribute values across lines", () => {
    const toks = tokens(xmlLanguage as StreamLanguage<unknown>, '<!-- one\ntwo -->\n<a title="x\ny">');
    expect(cls(toks, "<!-- one")).toBe("comment");
    expect(cls(toks, "two -->")).toBe("comment");
    expect(cls(toks, '"x')).toBe("string");
    expect(cls(toks, 'y"')).toBe("string");
  });

  it("reads HTML leniently: unquoted values and script text", () => {
    const toks = tokens(htmlLanguage as StreamLanguage<unknown>, '<!DOCTYPE html><p class=big>x</p><script>if (a<b) x = "<i>";</script><br/>');
    expect(cls(toks, "<!DOCTYPE html>")).toBe("meta");
    expect(cls(toks, "big")).toBe("string");
    expect(cls(toks, "script")).toBe("tag");
    // The script's text is not markup.
    expect(toks.some(([s]) => s === "i")).toBe(false);
    expect(cls(toks, "br")).toBe("tag");
  });
});

describe("foldByIndent", () => {
  it("folds a line with the deeper lines after it", () => {
    const state = EditorState.create({ doc: "<a>\n  <b>\n    <c/>\n  </b>\n</a>" });
    expect(foldByIndent(state, 0)).toEqual({ from: 3, to: state.doc.line(4).to });
    expect(foldByIndent(state, state.doc.line(2).from)).toEqual({ from: state.doc.line(2).to, to: state.doc.line(3).to });
    expect(foldByIndent(state, state.doc.line(3).from)).toBeNull();
  });
});
