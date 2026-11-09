// A GraphQL response body read for the GraphQL response view (GQL-05): its
// `data`, its `errors` (message, path, locations, extensions) and whether
// it has `extensions`, and a pretty-printer that can leave `extensions` out
// and says on which lines `data`, `errors` and each error are.
//
// The body is parsed by a small JSON reader that keeps every string and
// number as the text received, so printing it again changes nothing but
// the whitespace: big integers keep their digits, escapes stay as sent and
// members keep their order (JSON.parse would lose all three). The printer
// indents like Go's jsonfmt.Beautify with two spaces, so with nothing
// hidden the text matches the Pretty view.

import type { ResponseDTO } from "./http";

/** A JSON value as received: literals (strings, numbers, true, false, null) keep their text. */
export type JsonNode =
  | { readonly t: "obj"; readonly members: readonly JsonMember[] }
  | { readonly t: "arr"; readonly items: readonly JsonNode[] }
  | { readonly t: "lit"; readonly raw: string };

export interface JsonMember {
  /** The key, decoded. */
  readonly key: string;
  /** The key as received, quotes included. */
  readonly raw: string;
  readonly value: JsonNode;
}

/** A location in the query, as the server sent it: 1-based line and column. */
export interface QueryLocation {
  readonly line: number;
  readonly column: number;
}

export interface GraphqlError {
  /** The error's `message`, or "" when it has none. */
  readonly message: string;
  /** The error's `path` (field names and list indexes), or null. */
  readonly path: readonly (string | number)[] | null;
  /** The error's `locations` that are valid positions. */
  readonly locations: readonly QueryLocation[];
  /** A one-line summary of the error's `extensions` (its `code`, or its keys), or "". */
  readonly extensions: string;
}

export interface GraphqlResponse {
  readonly root: JsonNode & { t: "obj" };
  /** Whether the response has a `data` member (it may be null). */
  readonly hasData: boolean;
  /** Whether `data` is present and not null. */
  readonly dataPresent: boolean;
  readonly errors: readonly GraphqlError[];
  /** Whether the response or one of its errors has `extensions`. */
  readonly hasExtensions: boolean;
}

/** A region of the printed text: 1-based first and last lines. */
export interface LineRegion {
  readonly from: number;
  readonly to: number;
}

export interface PrintedGraphql {
  readonly text: string;
  /** The lines of the top-level `data` member, or null. */
  readonly data: LineRegion | null;
  /** The lines of the top-level `errors` member, or null. */
  readonly errors: LineRegion | null;
  /** The first line of each error, in order. */
  readonly errorLines: readonly number[];
}

/** Deeper nesting is not read (the Pretty view still shows it). */
const MAX_DEPTH = 1000;

class JsonReader {
  private pos = 0;
  constructor(private readonly src: string) {}

  read(): JsonNode | null {
    const node = this.value(0);
    if (node === null) return null;
    this.ws();
    return this.pos === this.src.length ? node : null;
  }

  private ws(): void {
    while (this.pos < this.src.length) {
      const c = this.src.charCodeAt(this.pos);
      if (c !== 0x20 && c !== 0x09 && c !== 0x0a && c !== 0x0d) break;
      this.pos++;
    }
  }

  private value(depth: number): JsonNode | null {
    if (depth > MAX_DEPTH) return null;
    this.ws();
    const c = this.src[this.pos];
    if (c === "{") return this.object(depth);
    if (c === "[") return this.array(depth);
    if (c === '"') {
      const raw = this.string();
      return raw === null ? null : { t: "lit", raw };
    }
    const m = /^(?:-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?|true|false|null)/.exec(this.src.slice(this.pos, this.pos + 400));
    if (m === null) return null;
    this.pos += m[0].length;
    return { t: "lit", raw: m[0] };
  }

  private string(): string | null {
    const start = this.pos;
    this.pos++;
    while (this.pos < this.src.length) {
      const c = this.src.charCodeAt(this.pos);
      if (c === 0x22) {
        this.pos++;
        return this.src.slice(start, this.pos);
      }
      if (c === 0x5c) this.pos += 2;
      else if (c < 0x20) return null;
      else this.pos++;
    }
    return null;
  }

  private object(depth: number): JsonNode | null {
    this.pos++;
    const members: JsonMember[] = [];
    this.ws();
    if (this.src[this.pos] === "}") {
      this.pos++;
      return { t: "obj", members };
    }
    for (;;) {
      this.ws();
      if (this.src[this.pos] !== '"') return null;
      const raw = this.string();
      if (raw === null) return null;
      this.ws();
      if (this.src[this.pos] !== ":") return null;
      this.pos++;
      const value = this.value(depth + 1);
      if (value === null) return null;
      members.push({ key: decode(raw), raw, value });
      this.ws();
      const c = this.src[this.pos++];
      if (c === "}") return { t: "obj", members };
      if (c !== ",") return null;
    }
  }

  private array(depth: number): JsonNode | null {
    this.pos++;
    const items: JsonNode[] = [];
    this.ws();
    if (this.src[this.pos] === "]") {
      this.pos++;
      return { t: "arr", items };
    }
    for (;;) {
      const value = this.value(depth + 1);
      if (value === null) return null;
      items.push(value);
      this.ws();
      const c = this.src[this.pos++];
      if (c === "]") return { t: "arr", items };
      if (c !== ",") return null;
    }
  }
}

function decode(raw: string): string {
  try {
    return JSON.parse(raw) as string;
  } catch {
    return raw;
  }
}

/** Parses JSON text, keeping literals as received; null when it is not valid JSON. */
export function readJson(text: string): JsonNode | null {
  return new JsonReader(text).read();
}

function member(node: JsonNode, key: string): JsonNode | undefined {
  if (node.t !== "obj") return undefined;
  return node.members.find((m) => m.key === key)?.value;
}

function literal(node: JsonNode | undefined): unknown {
  if (node?.t !== "lit") return undefined;
  try {
    return JSON.parse(node.raw) as unknown;
  } catch {
    return undefined;
  }
}

function readError(node: JsonNode): GraphqlError {
  const message = literal(member(node, "message"));
  let path: (string | number)[] | null = null;
  const pathNode = member(node, "path");
  if (pathNode?.t === "arr") {
    path = pathNode.items.map((item) => {
      const v = literal(item);
      return typeof v === "string" || typeof v === "number" ? v : item.t === "lit" ? item.raw : "?";
    });
  }
  const locations: QueryLocation[] = [];
  const locNode = member(node, "locations");
  if (locNode?.t === "arr") {
    for (const loc of locNode.items) {
      const line = literal(member(loc, "line"));
      const column = literal(member(loc, "column"));
      if (typeof line === "number" && typeof column === "number" && Number.isInteger(line) && Number.isInteger(column) && line >= 1 && column >= 1) {
        locations.push({ line, column });
      }
    }
  }
  return { message: typeof message === "string" ? message : "", path, locations, extensions: summarizeExtensions(member(node, "extensions")) };
}

function summarizeExtensions(node: JsonNode | undefined): string {
  if (node?.t !== "obj" || node.members.length === 0) return "";
  const code = literal(member(node, "code"));
  if (typeof code === "string" && code !== "") return code;
  return node.members.map((m) => m.key).join(", ");
}

/** Whether Go's view of the body lets it be read whole as JSON. */
function readable(response: ResponseDTO): boolean {
  return (
    response.body !== "" &&
    response.bodyLanguage === "json" &&
    !response.bodyHex &&
    !response.bodyBinary &&
    !response.bodyClipped &&
    !response.bodyTruncated &&
    response.prettyState !== "too_large" &&
    response.prettyState !== "incomplete" &&
    response.prettyState !== "invalid"
  );
}

/**
 * The GraphQL response in a body: a JSON object with a `data` member or an
 * `errors` list. Null for anything else (another JSON value, a body too
 * large to pretty-print, cut short or not JSON), which the Pretty view shows.
 */
export function readGraphqlResponse(response: ResponseDTO): GraphqlResponse | null {
  if (!readable(response)) return null;
  return readGraphqlPayload(response.body);
}

/**
 * A GraphQL result in JSON text: an object with a `data` member or an
 * `errors` list, such as the payload of a subscription's `next` message
 * (GQL-34). Null for anything else.
 */
export function readGraphqlPayload(text: string): GraphqlResponse | null {
  const root = readJson(text);
  if (root?.t !== "obj") return null;
  const data = member(root, "data");
  const errorsNode = member(root, "errors");
  if (data === undefined && errorsNode?.t !== "arr") return null;
  const errors = errorsNode?.t === "arr" ? errorsNode.items.filter((e) => e.t === "obj").map(readError) : [];
  const hasExtensions =
    member(root, "extensions") !== undefined ||
    (errorsNode?.t === "arr" && errorsNode.items.some((e) => member(e, "extensions") !== undefined));
  return {
    root,
    hasData: data !== undefined,
    dataPresent: data !== undefined && !(data.t === "lit" && data.raw === "null"),
    errors,
    hasExtensions,
  };
}

/**
 * Pretty-prints the response with two-space indentation. With
 * `hideExtensions`, the top-level `extensions` and each error's
 * `extensions` are left out; `extensions` inside `data` is the server's
 * data and stays.
 */
export function printGraphqlResponse(response: GraphqlResponse, hideExtensions: boolean): PrintedGraphql {
  const out: string[] = [];
  let line = 1;
  let data: LineRegion | null = null;
  let errors: LineRegion | null = null;
  const errorLines: number[] = [];

  const write = (s: string) => out.push(s);
  const newline = (indent: number) => {
    out.push("\n", "  ".repeat(indent));
    line++;
  };

  const print = (node: JsonNode, indent: number, context: "root" | "errors" | "error" | "other") => {
    if (node.t === "lit") {
      write(node.raw);
      return;
    }
    if (node.t === "arr") {
      if (node.items.length === 0) {
        write("[]");
        return;
      }
      write("[");
      node.items.forEach((item, i) => {
        if (i > 0) write(",");
        newline(indent + 1);
        if (context === "errors") errorLines.push(line);
        print(item, indent + 1, context === "errors" ? "error" : "other");
      });
      newline(indent);
      write("]");
      return;
    }
    const members =
      hideExtensions && (context === "root" || context === "error") ? node.members.filter((m) => m.key !== "extensions") : node.members;
    if (members.length === 0) {
      write("{}");
      return;
    }
    write("{");
    members.forEach((m, i) => {
      if (i > 0) write(",");
      newline(indent + 1);
      const start = line;
      write(`${m.raw}: `);
      const top = context === "root" && (m.key === "data" || m.key === "errors");
      print(m.value, indent + 1, context === "root" && m.key === "errors" ? "errors" : "other");
      if (top && m.key === "data") data = { from: start, to: line };
      if (top && m.key === "errors") errors = { from: start, to: line };
    });
    newline(indent);
    write("}");
  };

  print(response.root, 0, "root");
  return { text: out.join(""), data, errors, errorLines };
}

/**
 * The CodeMirror position of a GraphQL location in a document: `line` is
 * 1-based, `column` 1-based and counted in code points (GraphQL source
 * characters), so a character outside the BMP counts once. Past the end of
 * a line is its end; past the last line is the end of the document.
 */
export function queryLocationOffset(doc: { readonly lines: number; line(n: number): { from: number; text: string } }, loc: QueryLocation): number {
  const lineNo = Math.min(Math.max(1, loc.line), doc.lines);
  const line = doc.line(lineNo);
  if (loc.line > doc.lines) return line.from + line.text.length;
  let offset = 0;
  let points = 1;
  while (points < loc.column && offset < line.text.length) {
    const code = line.text.charCodeAt(offset);
    offset += code >= 0xd800 && code <= 0xdbff && offset + 1 < line.text.length ? 2 : 1;
    points++;
  }
  return line.from + offset;
}

/** A path as `user.friends[0].name`. */
export function formatErrorPath(path: readonly (string | number)[]): string {
  let out = "";
  for (const part of path) {
    if (typeof part === "number") out += `[${part}]`;
    else out += out === "" ? part : `.${part}`;
  }
  return out;
}
