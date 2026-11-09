import { describe, expect, it } from "vitest";
import { EditorState } from "@codemirror/state";
import {
  ensureSyntaxTree,
  foldable,
  getIndentation,
  matchBrackets,
  StringStream,
} from "@codemirror/language";
import { highlightTree } from "@lezer/highlight";
import { graphql, graphqlParser, type GraphQLState } from "./graphql";
import { editorHighlightStyle } from "./theme";

type Token = [text: string, style: string];

/** Runs the stream parser over `doc` line by line, dropping whitespace. */
function tokenize(doc: string): Token[][] {
  const state: GraphQLState = graphqlParser.startState!(2);
  return doc.split("\n").map((line) => {
    const out: Token[] = [];
    if (line === "") {
      graphqlParser.blankLine?.(state, 2);
      return out;
    }
    const stream = new StringStream(line, 2, 2);
    while (!stream.eol()) {
      stream.start = stream.pos;
      const style = graphqlParser.token(stream, state);
      if (stream.pos === stream.start) throw new Error(`no progress at "${line.slice(stream.pos)}"`);
      if (style) out.push([stream.current(), style]);
    }
    return out;
  });
}

/** The style of each named token, first occurrence, across the document. */
function styles(doc: string): Map<string, string> {
  const map = new Map<string, string>();
  for (const [text, style] of tokenize(doc).flat()) {
    if (!map.has(text)) map.set(text, style);
  }
  return map;
}

function expectStyles(doc: string, expected: Record<string, string>) {
  const got = styles(doc);
  for (const [text, style] of Object.entries(expected)) {
    expect([text, got.get(text)]).toEqual([text, style]);
  }
}

describe("GraphQL tokenizer", () => {
  it("highlights a query with variables, arguments, aliases and directives", () => {
    const doc = `# Fetch a product
query GetProduct($id: ID!, $first: Int = 10, $withTags: Boolean!) {
  product(id: $id) {
    name
    title: displayName
    price(currency: EUR, round: true, scale: 2.5)
    tags(first: $first) @include(if: $withTags)
  }
}`;
    expectStyles(doc, {
      "# Fetch a product": "lineComment",
      query: "keyword",
      GetProduct: "className",
      $id: "variableName",
      ID: "typeName",
      "!": "operator",
      Int: "typeName",
      "10": "number",
      Boolean: "typeName",
      product: "field",
      id: "attributeName",
      name: "field",
      title: "field",
      displayName: "field",
      price: "field",
      currency: "attributeName",
      EUR: "atom",
      round: "attributeName",
      true: "bool",
      "2.5": "number",
      tags: "field",
      "@include": "meta",
      if: "attributeName",
      $withTags: "variableName",
      "{": "bracket",
      "(": "bracket",
      ":": "punctuation",
    });
  });

  it("highlights mutations with input objects, lists and null", () => {
    const doc = `mutation {
  createOrder(input: { sku: "SM-5", qty: 2, notes: null, tags: [A, "b", $c] }) {
    id
  }
}`;
    expectStyles(doc, {
      mutation: "keyword",
      createOrder: "field",
      input: "attributeName",
      sku: "attributeName",
      '"SM-5"': "string",
      qty: "attributeName",
      notes: "attributeName",
      null: "null",
      tags: "attributeName",
      A: "atom",
      '"b"': "string",
      $c: "variableName",
      id: "field",
    });
  });

  it("highlights subscriptions, list types and nested list types", () => {
    expectStyles(
      `subscription OnEvent($ids: [ID!]!, $grid: [[Int]]) { event(ids: $ids) { kind } }`,
      {
        subscription: "keyword",
        OnEvent: "className",
        ID: "typeName",
        Int: "typeName",
        "[": "bracket",
        event: "field",
        ids: "attributeName",
        kind: "field",
      },
    );
  });

  it("highlights fragments, spreads and inline fragments", () => {
    const doc = `fragment ProductFields on Product {
  id
  ...PriceFields
  ... on DigitalProduct { url }
  ... @skip(if: true) { sku }
}`;
    const lines = tokenize(doc);
    expect(lines[0]).toEqual([
      ["fragment", "keyword"],
      ["ProductFields", "className"],
      ["on", "keyword"],
      ["Product", "typeName"],
      ["{", "bracket"],
    ]);
    expect(lines[2]).toEqual([
      ["...", "punctuation"],
      ["PriceFields", "className"],
    ]);
    expect(lines[3]).toEqual([
      ["...", "punctuation"],
      ["on", "keyword"],
      ["DigitalProduct", "typeName"],
      ["{", "bracket"],
      ["url", "field"],
      ["}", "bracket"],
    ]);
    expect(lines[4].slice(0, 5)).toEqual([
      ["...", "punctuation"],
      ["@skip", "meta"],
      ["(", "bracket"],
      ["if", "attributeName"],
      [":", "punctuation"],
    ]);
  });

  it("treats keywords inside selection sets as field names", () => {
    expectStyles(`{ query fragment on type }`, {
      query: "field",
      fragment: "field",
      on: "field",
      type: "field",
    });
  });

  it("returns to the top level after a definition", () => {
    const lines = tokenize(`query A { a }\nfragment B on C { b }`);
    expect(lines[1][0]).toEqual(["fragment", "keyword"]);
    expect(lines[1][1]).toEqual(["B", "className"]);
  });

  it("carries a block string across lines, with escaped quotes", () => {
    const doc = `query {
  search(text: """
first line
has \\""" inside
  end""", limit: 5) { id }
}`;
    const lines = tokenize(doc);
    expect(lines[1]).toEqual([
      ["search", "field"],
      ["(", "bracket"],
      ["text", "attributeName"],
      [":", "punctuation"],
      ['"""', "string"],
    ]);
    expect(lines[2]).toEqual([["first line", "string"]]);
    expect(lines[3]).toEqual([['has \\""" inside', "string"]]);
    expect(lines[4]).toEqual([
      ['  end"""', "string"],
      [",", "punctuation"],
      ["limit", "attributeName"],
      [":", "punctuation"],
      ["5", "number"],
      [")", "bracket"],
      ["{", "bracket"],
      ["id", "field"],
      ["}", "bracket"],
    ]);
  });

  it("keeps brackets and # inside strings as part of the string", () => {
    expect(tokenize(`{ a(q: "{ # not a comment \\" }") }`)[0]).toEqual([
      ["{", "bracket"],
      ["a", "field"],
      ["(", "bracket"],
      ["q", "attributeName"],
      [":", "punctuation"],
      ['"{ # not a comment \\" }"', "string"],
      [")", "bracket"],
      ["}", "bracket"],
    ]);
  });

  it("highlights SDL definitions", () => {
    const doc = `"""A product"""
type Product implements Node & Priced @key(fields: "id") {
  id: ID!
  price(currency: Currency = EUR): Float @deprecated(reason: "use cost")
}
enum Currency { EUR USD }
union Result = Product | Error
directive @auth(role: String) repeatable on FIELD_DEFINITION | OBJECT
schema { query: Query }`;
    const lines = tokenize(doc);
    expect(lines[0]).toEqual([['"""A product"""', "string"]]);
    expectStyles(doc, {
      type: "keyword",
      Product: "typeName",
      implements: "keyword",
      Node: "typeName",
      Priced: "typeName",
      "@key": "meta",
      fields: "attributeName",
      id: "field",
      ID: "typeName",
      price: "field",
      currency: "attributeName",
      Currency: "typeName",
      EUR: "atom",
      Float: "typeName",
      reason: "attributeName",
      enum: "keyword",
      USD: "atom",
      union: "keyword",
      Result: "typeName",
      Error: "typeName",
      directive: "keyword",
      "@auth": "meta",
      role: "attributeName",
      String: "typeName",
      repeatable: "keyword",
      FIELD_DEFINITION: "atom",
      OBJECT: "atom",
      schema: "keyword",
      query: "field",
      Query: "typeName",
    });
  });

  it("indents by bracket depth and dedents closing brackets", () => {
    const state = EditorState.create({
      doc: "query {\n  a(x: 1) {\n    b\n  }\n}",
      extensions: graphql(),
    });
    ensureSyntaxTree(state, state.doc.length, 5000);
    expect(getIndentation(state, state.doc.line(3).from)).toBe(4);
    expect(getIndentation(state, state.doc.line(4).from)).toBe(2);
    expect(getIndentation(state, state.doc.line(5).from)).toBe(0);
  });
});

describe("GraphQL editor support", () => {
  const doc = `query Q($id: ID) {
  product(id: $id, note: "{ not folded") {
    name
  }
}`;
  const state = EditorState.create({ doc, extensions: graphql() });
  const tree = ensureSyntaxTree(state, state.doc.length, 5000)!;

  it("matches brackets and skips brackets in strings", () => {
    const open = doc.indexOf("{");
    const match = matchBrackets(state, open, 1);
    expect(match?.matched).toBe(true);
    expect(match?.end?.from).toBe(doc.lastIndexOf("}"));

    const productOpen = doc.indexOf(`") {`) + 3;
    expect(matchBrackets(state, productOpen, 1)?.end?.from).toBe(doc.indexOf("}"));
  });

  it("folds selection sets, not single-line brackets or brackets in strings", () => {
    const l1 = state.doc.line(1);
    expect(foldable(state, l1.from, l1.to)).toEqual({
      from: doc.indexOf("{") + 1,
      to: doc.lastIndexOf("}"),
    });
    const l2 = state.doc.line(2);
    expect(foldable(state, l2.from, l2.to)).toEqual({
      from: doc.indexOf(`") {`) + 4,
      to: doc.indexOf("}"),
    });
    const l3 = state.doc.line(3);
    expect(foldable(state, l3.from, l3.to)).toBeNull();
  });

  it("maps GraphQL tokens to the --tk-* colour classes", () => {
    const classes = new Map<string, string>();
    highlightTree(tree, editorHighlightStyle, (from, to, cls) => {
      classes.set(doc.slice(from, to), cls);
    });
    const rules = editorHighlightStyle.module!.getRules();
    const colourOf = (text: string) => {
      const cls = classes.get(text);
      expect(cls, text).toBeTruthy();
      const rule = rules.split("\n").find((r) => r.startsWith(`.${cls} `) || r.startsWith(`.${cls}{`));
      return rule?.match(/color:\s*(var\(--tk-[a-z]+\))/)?.[1];
    };
    expect(colourOf("query")).toBe("var(--tk-kw)");
    expect(colourOf("product")).toBe("var(--tk-field)");
    expect(colourOf("id")).toBe("var(--tk-arg)");
    expect(colourOf("$id")).toBe("var(--tk-var)");
    expect(colourOf("ID")).toBe("var(--tk-type)");
    expect(colourOf('"{ not folded"')).toBe("var(--tk-str)");
  });
});
