// The JSON value at a position of a JSON text, with its JSONPath (TEST-04):
// what a quick assertion from the response body checks. A small scanner
// of its own, since the editor's text is the pretty-printed body and
// JSON.parse keeps no positions. Paths are RFC 9535 normalized-style
// queries the Go evaluator takes (docs/jsonpath.md): $.name for names
// that are identifiers, $['other name'] otherwise, [n] for indexes.

export type JsonKind = "string" | "number" | "boolean" | "null" | "object" | "array";

export interface JsonValueAt {
  /** The value's JSONPath: "$.items[0].id". */
  readonly path: string;
  readonly kind: JsonKind;
  /** The value's JSON text as written in the document. */
  readonly text: string;
  /** Where the value is in the document: [from, to). */
  readonly from: number;
  readonly to: number;
}

const IDENTIFIER = /^[A-Za-z_][A-Za-z0-9_]*$/;

/** A path segment for an object member name. */
export function nameSegment(name: string): string {
  if (IDENTIFIER.test(name)) return `.${name}`;
  return `['${name.replace(/\\/g, "\\\\").replace(/'/g, "\\'")}']`;
}

class Scanner {
  pos = 0;
  constructor(readonly s: string) {}

  ws(): void {
    while (this.pos < this.s.length && " \t\r\n".includes(this.s.charAt(this.pos))) this.pos++;
  }

  fail(): never {
    throw new SyntaxError(`invalid JSON at ${this.pos}`);
  }
}

interface Found {
  best: JsonValueAt | null;
}

/**
 * The innermost value whose text contains `offset` (a member's name counts
 * as its value), or null when the text is not JSON or the offset is
 * outside every value.
 */
export function jsonValueAt(doc: string, offset: number): JsonValueAt | null {
  const sc = new Scanner(doc);
  const found: Found = { best: null };
  try {
    sc.ws();
    value(sc, "$", offset, found);
    sc.ws();
    if (sc.pos !== doc.length) return null;
  } catch {
    return null;
  }
  return found.best;
}

function record(found: Found, path: string, kind: JsonKind, doc: string, from: number, to: number, offset: number, keyFrom = from): void {
  const inside = (offset >= from && offset < to) || (offset >= keyFrom && offset < from);
  if (!inside) return;
  const best = found.best;
  // The innermost value wins: a value is recorded after the values inside
  // it, so a later, larger one does not replace it.
  if (best === null || (from >= best.from && to <= best.to)) {
    found.best = { path, kind, text: doc.slice(from, to), from, to };
  }
}

function value(sc: Scanner, path: string, offset: number, found: Found, keyFrom?: number): void {
  const s = sc.s;
  const from = sc.pos;
  const c = s.charAt(sc.pos);
  let kind: JsonKind;
  if (c === "{") {
    kind = "object";
    sc.pos++;
    sc.ws();
    if (s.charAt(sc.pos) === "}") sc.pos++;
    else {
      for (;;) {
        sc.ws();
        const nameFrom = sc.pos;
        const name = str(sc);
        sc.ws();
        if (s.charAt(sc.pos) !== ":") sc.fail();
        sc.pos++;
        sc.ws();
        value(sc, path + nameSegment(name), offset, found, nameFrom);
        sc.ws();
        const next = s.charAt(sc.pos++);
        if (next === "}") break;
        if (next !== ",") sc.fail();
      }
    }
  } else if (c === "[") {
    kind = "array";
    sc.pos++;
    sc.ws();
    if (s.charAt(sc.pos) === "]") sc.pos++;
    else {
      for (let i = 0; ; i++) {
        sc.ws();
        value(sc, `${path}[${i}]`, offset, found);
        sc.ws();
        const next = s.charAt(sc.pos++);
        if (next === "]") break;
        if (next !== ",") sc.fail();
      }
    }
  } else if (c === '"') {
    kind = "string";
    str(sc);
  } else if (s.startsWith("true", sc.pos) || s.startsWith("false", sc.pos)) {
    kind = "boolean";
    sc.pos += s.startsWith("true", sc.pos) ? 4 : 5;
  } else if (s.startsWith("null", sc.pos)) {
    kind = "null";
    sc.pos += 4;
  } else {
    const m = /^-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?/.exec(s.slice(sc.pos, sc.pos + 400));
    if (m === null) sc.fail();
    kind = "number";
    sc.pos += m[0].length;
  }
  record(found, path, kind, s, from, sc.pos, offset, keyFrom ?? from);
}

/** Reads a JSON string and returns its value. */
function str(sc: Scanner): string {
  const s = sc.s;
  if (s.charAt(sc.pos) !== '"') sc.fail();
  const start = sc.pos;
  sc.pos++;
  while (sc.pos < s.length) {
    const c = s.charAt(sc.pos);
    if (c === "\\") sc.pos += 2;
    else if (c === '"') {
      sc.pos++;
      return JSON.parse(s.slice(start, sc.pos)) as string;
    } else sc.pos++;
  }
  return sc.fail();
}

/**
 * The expected value of an "equals" assertion on the value: a string as it
 * is (the evaluator compares strings exactly), a number, boolean or null as
 * written, an object or array as compact JSON.
 */
export function expectedOf(v: JsonValueAt): string {
  switch (v.kind) {
    case "string":
      return JSON.parse(v.text) as string;
    case "object":
    case "array":
      return JSON.stringify(JSON.parse(v.text));
    default:
      return v.text;
  }
}
