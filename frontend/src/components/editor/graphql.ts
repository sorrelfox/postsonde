// GraphQL syntax highlighting for the shared editor (GQL-12).
//
// A CodeMirror StreamLanguage tokenizer, no graphql-js. Parsing and schema
// validation belong to the Go engine; this mode only colours the text and
// gives bracket matching and folding. It reads executable documents
// (queries, mutations, subscriptions, fragments) and, more loosely, SDL.
//
// The tokenizer keeps a small state machine:
// - `stack`: the open brackets and what they mean (selection set, arguments,
//   variable definitions, input object or list value, list type, SDL body);
// - `expect`: what the next name is, from the tokens just before it
//   (a type after `on` or after `:` in variable definitions, a value after
//   `:` in arguments, an operation or fragment name after its keyword…);
// - `definition`: the kind of the current top-level definition;
// - `blockString`: inside a `"""` string that spans lines.
//
// Token → tag → colour (theme.ts):
//   keyword       query, fragment, on, type…   --tk-kw
//   field         fields and aliases           --tk-field
//   attributeName arguments, input fields      --tk-arg
//   variableName  $variables                   --tk-var
//   typeName      types                        --tk-type
//   className     operation and fragment names --tk-type
//   meta          @directives                  --tk-dir
//   string        strings and block strings    --tk-str
//   number, bool, null, atom (enum values)      --tk-num / --tk-bool
//   lineComment   # comments                   --tk-cm

import {
  LanguageSupport,
  StreamLanguage,
  foldService,
  matchBrackets,
  syntaxTree,
  type StreamParser,
  type StringStream,
} from "@codemirror/language";
import type { EditorState } from "@codemirror/state";
import { Tag, tags as t } from "@lezer/highlight";

/** GraphQL-specific highlight tags. Each falls back to its parent tag in other highlight styles. */
export const graphqlTags = {
  /** A field in a selection set or SDL type body, or an alias. */
  field: Tag.define(t.propertyName),
};

/** What an open bracket means. */
export type GraphQLContext =
  | "selection" // { … } of an operation, fragment or field
  | "args" // ( … ) after a field or directive
  | "varDefs" // ( … ) after an operation name
  | "object" // { … } input object value
  | "list" // [ … ] list value
  | "listType" // [ … ] list type
  | "sdlBody" // { … } of a type, interface, input, enum or schema definition
  | "sdlArgs"; // ( … ) argument definitions in SDL

/** What the next name means. */
export type GraphQLExpect =
  | "none"
  | "key" // argument or input field name
  | "value"
  | "type"
  | "opName"
  | "fragName"
  | "fragOn" // `on` after a fragment name
  | "spread" // after `...`: `on` or a fragment name
  | "directive" // after `@name`: `(` opens its arguments
  | "locations"; // directive locations after `directive @x on`

type Definition = "none" | "operation" | "fragment" | "sdl" | "enum" | "schema" | "directive";

export interface GraphQLState {
  stack: GraphQLContext[];
  expect: GraphQLExpect;
  definition: Definition;
  blockString: boolean;
}

const NAME = /^[_A-Za-z][_0-9A-Za-z]*/;
const NUMBER = /^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?/;
const VARIABLE = /^\$[_A-Za-z][_0-9A-Za-z]*/;
const DIRECTIVE = /^@[_A-Za-z][_0-9A-Za-z]*/;

const top = (state: GraphQLState): GraphQLContext | undefined =>
  state.stack[state.stack.length - 1];

/** Where a value inside `ctx` leaves the tokenizer. */
function afterValueIn(ctx: GraphQLContext | undefined): GraphQLExpect {
  if (ctx === "args" || ctx === "object" || ctx === "sdlArgs") return "key";
  if (ctx === "list") return "value";
  return "none";
}

function valueDone(state: GraphQLState): void {
  if (state.expect === "value") state.expect = afterValueIn(top(state));
}

function readBlockString(stream: StringStream, state: GraphQLState): string {
  while (!stream.eol()) {
    if (stream.match('\\"""')) continue;
    if (stream.match('"""')) {
      state.blockString = false;
      valueDone(state);
      return "string";
    }
    stream.next();
  }
  return "string";
}

function readString(stream: StringStream, state: GraphQLState): string {
  // An unclosed string stays a string: marking syntax errors is the engine's job.
  while (!stream.eol()) {
    const ch = stream.next();
    if (ch === "\\") stream.next();
    else if (ch === '"') break;
  }
  valueDone(state);
  return "string";
}

function openBracket(ch: string, state: GraphQLState): void {
  const ctx = top(state);
  let kind: GraphQLContext;
  let expect: GraphQLExpect = "none";
  if (ch === "{") {
    if (state.expect === "value") {
      kind = "object";
      expect = "key";
    } else if (ctx === undefined && ["sdl", "enum", "schema"].includes(state.definition)) {
      kind = "sdlBody";
    } else {
      kind = "selection";
    }
  } else if (ch === "(") {
    if (ctx === undefined && state.definition === "directive") {
      kind = "sdlArgs";
    } else if (state.expect === "directive") {
      kind = "args";
    } else if (ctx === undefined) {
      kind = "varDefs";
    } else {
      kind = ctx === "sdlBody" ? "sdlArgs" : "args";
    }
    if (kind !== "varDefs") expect = "key";
  } else if (state.expect === "type") {
    kind = "listType";
    expect = "type";
  } else {
    kind = "list";
    expect = "value";
  }
  state.stack.push(kind);
  state.expect = expect;
}

function closeBracket(state: GraphQLState): void {
  const closed = state.stack.pop();
  const parent = top(state);
  state.expect = closed === "object" || closed === "list" ? afterValueIn(parent) : "none";
  if (parent === undefined && (closed === "selection" || closed === "sdlBody")) {
    state.definition = "none";
  }
}

function punctuation(ch: string, state: GraphQLState): string {
  const ctx = top(state);
  switch (ch) {
    case ":":
      if (ctx === "args" || ctx === "object") state.expect = "value";
      else if (ctx === "varDefs" || ctx === "sdlBody" || ctx === "sdlArgs") state.expect = "type";
      else state.expect = "none";
      return "punctuation";
    case "=":
      // `union U = A | B` at the top level; a default value elsewhere.
      state.expect = ctx === undefined ? "type" : "value";
      return "operator";
    case "|":
      state.expect = state.definition === "directive" ? "locations" : "type";
      return "operator";
    case "&":
      state.expect = "type";
      return "operator";
    case "!":
      return "operator";
    case ",":
      if (ctx !== undefined && ctx !== "listType") state.expect = afterValueIn(ctx);
      return "punctuation";
    default:
      return "invalid";
  }
}

function topLevelName(name: string, state: GraphQLState): string | null {
  switch (name) {
    case "query":
    case "mutation":
    case "subscription":
      state.definition = "operation";
      state.expect = "opName";
      return "keyword";
    case "fragment":
      state.definition = "fragment";
      state.expect = "fragName";
      return "keyword";
    case "schema":
      state.definition = "schema";
      return "keyword";
    case "type":
    case "interface":
    case "input":
    case "scalar":
    case "union":
      state.definition = "sdl";
      state.expect = "type";
      return "keyword";
    case "enum":
      state.definition = "enum";
      state.expect = "type";
      return "keyword";
    case "directive":
      state.definition = "directive";
      return "keyword";
    case "implements":
      state.expect = "type";
      return "keyword";
    case "on":
      state.expect = state.definition === "directive" ? "locations" : "type";
      return "keyword";
    case "extend":
    case "repeatable":
      return "keyword";
    default:
      return null;
  }
}

function readName(stream: StringStream, state: GraphQLState): string | null {
  const name = stream.current();
  switch (state.expect) {
    case "value":
      valueDone(state);
      if (name === "true" || name === "false") return "bool";
      if (name === "null") return "null";
      return "atom";
    case "type":
      state.expect = "none";
      return "typeName";
    case "opName":
      state.expect = "none";
      return "className";
    case "fragName":
      state.expect = "fragOn";
      return "className";
    case "fragOn":
    case "spread":
      if (name === "on") {
        state.expect = "type";
        return "keyword";
      }
      if (state.expect === "spread") {
        state.expect = "none";
        return "className";
      }
      state.expect = "none";
      break;
    case "directive":
      state.expect = "none";
      break;
    case "locations":
      if (/^[A-Z_]+$/.test(name)) return "atom";
      state.expect = "none";
      break;
  }

  switch (top(state)) {
    case undefined:
      return topLevelName(name, state);
    case "selection":
      return "field";
    case "sdlBody":
      return state.definition === "enum" ? "atom" : "field";
    case "args":
    case "object":
    case "sdlArgs":
      return "attributeName";
    default:
      return null;
  }
}

export const graphqlParser: StreamParser<GraphQLState> = {
  name: "graphql",

  startState: () => ({ stack: [], expect: "none", definition: "none", blockString: false }),

  copyState: (s) => ({ ...s, stack: s.stack.slice() }),

  token(stream, state) {
    if (state.blockString) return readBlockString(stream, state);
    if (stream.eatSpace()) return null;

    if (stream.peek() === "#") {
      stream.skipToEnd();
      return "lineComment";
    }
    if (stream.match('"""')) {
      state.blockString = true;
      return readBlockString(stream, state);
    }
    if (stream.eat('"')) return readString(stream, state);
    if (stream.match("...")) {
      state.expect = "spread";
      return "punctuation";
    }
    if (stream.match(VARIABLE)) {
      if (top(state) === "varDefs" && state.expect !== "value") state.expect = "none";
      else valueDone(state);
      return "variableName";
    }
    if (stream.match(DIRECTIVE)) {
      state.expect = "directive";
      return "meta";
    }
    if (stream.match(NUMBER)) {
      valueDone(state);
      return "number";
    }
    if (stream.match(NAME)) return readName(stream, state);

    const ch = stream.next()!;
    if (ch === "{" || ch === "(" || ch === "[") {
      openBracket(ch, state);
      return "bracket";
    }
    if (ch === "}" || ch === ")" || ch === "]") {
      closeBracket(state);
      return "bracket";
    }
    return punctuation(ch, state);
  },

  indent(state, textAfter, cx) {
    if (state.blockString) return null;
    const closing = /^\s*[}\])]/.test(textAfter) ? 1 : 0;
    return Math.max(0, state.stack.length - closing) * cx.unit;
  },

  languageData: {
    commentTokens: { line: "#" },
    closeBrackets: { brackets: ["(", "[", "{", '"'] },
    indentOnInput: /^\s*[}\])]$/,
  },

  tokenTable: { field: graphqlTags.field },
};

export const graphqlLanguage = StreamLanguage.define(graphqlParser);

/**
 * Folds a multi-line `{ … }`, `( … )` or `[ … ]` that opens on the line:
 * the first bracket on the line whose match is on a later line. Brackets in
 * strings and comments are not bracket tokens and are skipped.
 */
export function foldGraphQL(state: EditorState, lineStart: number, lineEnd: number) {
  const tree = syntaxTree(state);
  const text = state.sliceDoc(lineStart, lineEnd);
  for (let i = 0; i < text.length; i++) {
    const ch = text[i];
    if (ch !== "{" && ch !== "(" && ch !== "[") continue;
    const pos = lineStart + i;
    if (tree.resolveInner(pos, 1).name !== "bracket") continue;
    const match = matchBrackets(state, pos, 1);
    if (!match?.matched || !match.end) continue;
    if (match.end.from > lineEnd) return { from: pos + 1, to: match.end.from };
  }
  return null;
}

/** GraphQL language support for the editor: highlighting, indentation, bracket data and folding. */
export function graphql(): LanguageSupport {
  return new LanguageSupport(graphqlLanguage, foldService.of(foldGraphQL));
}
