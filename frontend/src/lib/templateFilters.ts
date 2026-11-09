// Filter pipes (DYN-04, docs/templates.md): `{{ token | base64 }}`,
// `{{ "user:pass" | base64 }}`, `{{ body | hmacSha256 key base64 }}`. The
// part before the first `|` is the input (a variable name or a quoted
// text), each part after one a filter and its arguments. This reads a pipe
// exactly as Go's template package does (filter.go), so the editor can mark
// it and complete filter names; Go computes the value when the request is
// sent. The shared cases in internal/core/template/testdata/pipes.json and
// the table in filters.json.golden keep the two in step.
//
// Error messages are Go's wording in English (pipes.json checks them) and
// come from the catalogue (lib.templateFilters.error.*, UI-10).

import { t, type MessageKey } from "../i18n";

/** A filter: `keys` leading key arguments, then at most `maxArgs` others. */
export interface TemplateFilter {
  readonly name: string;
  readonly keys: number;
  readonly maxArgs: number;
  readonly description: string;
}

/**
 * Every filter, sorted by name (Go's FilterNames). `description` is Go's
 * English (filters.json.golden); the UI shows filterDescription().
 */
// i18n-ignore-start: Go's table, compared with filters.json.golden
export const TEMPLATE_FILTERS: readonly TemplateFilter[] = [
  { name: "base64", keys: 0, maxArgs: 0, description: "Base64 (standard alphabet, padded)" },
  { name: "base64Decode", keys: 0, maxArgs: 0, description: "Decodes Base64, standard or URL alphabet, padded or not" },
  { name: "base64url", keys: 0, maxArgs: 0, description: "Base64 with the URL alphabet, unpadded (as in JWTs)" },
  { name: "hex", keys: 0, maxArgs: 0, description: "Lowercase hex of the bytes" },
  { name: "hexDecode", keys: 0, maxArgs: 0, description: "Decodes hex digits to bytes" },
  { name: "hmacMd5", keys: 1, maxArgs: 1, description: "HMAC-MD5 with a key, hex (or base64, base64url)" },
  { name: "hmacSha1", keys: 1, maxArgs: 1, description: "HMAC-SHA1 with a key, hex (or base64, base64url)" },
  { name: "hmacSha256", keys: 1, maxArgs: 1, description: "HMAC-SHA256 with a key, hex (or base64, base64url)" },
  { name: "hmacSha512", keys: 1, maxArgs: 1, description: "HMAC-SHA512 with a key, hex (or base64, base64url)" },
  { name: "md5", keys: 0, maxArgs: 1, description: "MD5 digest, hex (or base64, base64url)" },
  { name: "sha1", keys: 0, maxArgs: 1, description: "SHA-1 digest, hex (or base64, base64url)" },
  { name: "sha256", keys: 0, maxArgs: 1, description: "SHA-256 digest, hex (or base64, base64url)" },
  { name: "sha512", keys: 0, maxArgs: 1, description: "SHA-512 digest, hex (or base64, base64url)" },
  { name: "urlDecode", keys: 0, maxArgs: 0, description: "Decodes %XX sequences (a + stays a +)" },
  { name: "urlEncode", keys: 0, maxArgs: 0, description: "Percent-encodes everything but letters, digits and - . _ ~" },
];
// i18n-ignore-end

/** The message of each filter's description (lib.templateFilters.*, the same English as Go's). */
const DESCRIPTIONS: Readonly<Record<string, MessageKey>> = {
  base64: "lib.templateFilters.base64",
  base64Decode: "lib.templateFilters.base64Decode",
  base64url: "lib.templateFilters.base64url",
  hex: "lib.templateFilters.hex",
  hexDecode: "lib.templateFilters.hexDecode",
  hmacMd5: "lib.templateFilters.hmacMd5",
  hmacSha1: "lib.templateFilters.hmacSha1",
  hmacSha256: "lib.templateFilters.hmacSha256",
  hmacSha512: "lib.templateFilters.hmacSha512",
  md5: "lib.templateFilters.md5",
  sha1: "lib.templateFilters.sha1",
  sha256: "lib.templateFilters.sha256",
  sha512: "lib.templateFilters.sha512",
  urlDecode: "lib.templateFilters.urlDecode",
  urlEncode: "lib.templateFilters.urlEncode",
};

/** What the filter does, in the language in use; Go's English for a filter without a message. */
export function filterDescription(filter: TemplateFilter): string {
  const key = DESCRIPTIONS[filter.name];
  return key === undefined ? filter.description : t(key);
}

const byName = new Map(TEMPLATE_FILTERS.map((f) => [f.name, f]));

/** The filter called `name`, if any. */
export function templateFilter(name: string): TemplateFilter | undefined {
  return byName.get(name);
}

/** An input or argument: a bare word, or a text in double quotes (without them). */
export interface PipeToken {
  readonly text: string;
  readonly quoted: boolean;
}

export interface PipeStep {
  readonly name: string;
  /** The key arguments (HMAC): a variable name, or a quoted text. */
  readonly keys: readonly PipeToken[];
  readonly args: readonly string[];
}

export interface Pipe {
  readonly input: PipeToken;
  readonly steps: readonly PipeStep[];
}

const SPACES = " \t\r\n";

function trimSpaces(s: string): string {
  let a = 0;
  let b = s.length;
  while (a < b && SPACES.includes(s[a])) a++;
  while (b > a && SPACES.includes(s[b - 1])) b--;
  return s.slice(a, b);
}

/**
 * The stages of `name` split on each `|` outside double quotes, trimmed;
 * null when there is no such `|` or a quote is not closed (not a pipe).
 */
export function splitPipe(name: string): string[] | null {
  const stages: string[] = [];
  let inQuote = false;
  let start = 0;
  for (let i = 0; i < name.length; i++) {
    if (name[i] === '"') inQuote = !inQuote;
    else if (name[i] === "|" && !inQuote) {
      stages.push(trimSpaces(name.slice(start, i)));
      start = i + 1;
    }
  }
  if (inQuote || stages.length === 0) return null;
  stages.push(trimSpaces(name.slice(start)));
  return stages;
}

/** Splits on whitespace; a quoted token may hold spaces. Null for an open quote or text right after one. */
export function splitTokens(s: string): PipeToken[] | null {
  const out: PipeToken[] = [];
  let i = 0;
  for (;;) {
    while (i < s.length && SPACES.includes(s[i])) i++;
    if (i === s.length) return out;
    if (s[i] === '"') {
      const end = s.indexOf('"', i + 1);
      if (end < 0) return null;
      out.push({ text: s.slice(i + 1, end), quoted: true });
      i = end + 1;
      if (i < s.length && !SPACES.includes(s[i])) return null;
      continue;
    }
    let j = i;
    while (j < s.length && !SPACES.includes(s[j])) j++;
    out.push({ text: s.slice(i, j), quoted: false });
    i = j;
  }
}

/** Go's %q for the messages (the same for the ASCII names the syntax uses). */
const q = (s: string) => JSON.stringify(s);

const ENCODINGS = ["hex", "base64", "base64url"];

/** Checks the stages of a pipe as Go does: the pipe, or why it is not valid (Go's wording). */
export function parsePipe(stages: readonly string[]): Pipe | { error: string } {
  const head = stages[0];
  let input: PipeToken;
  if (head === "") return { error: t("lib.templateFilters.error.noInput") };
  if (head.startsWith('"')) {
    const toks = splitTokens(head);
    if (toks === null || toks.length !== 1) {
      return { error: t("lib.templateFilters.error.badInput") };
    }
    input = toks[0];
  } else {
    input = { text: head, quoted: false };
  }
  const steps: PipeStep[] = [];
  for (const stage of stages.slice(1)) {
    const toks = splitTokens(stage);
    if (toks === null) {
      return { error: t("lib.templateFilters.error.badQuote", { filter: q(stage) }) };
    }
    if (toks.length === 0) return { error: t("lib.templateFilters.error.noName") };
    const name = toks[0].text;
    const spec = byName.get(name);
    if (toks[0].quoted || spec === undefined) return { error: t("lib.templateFilters.error.unknown", { name: q(name) }) };
    const rest = toks.slice(1);
    if (rest.length < spec.keys) return { error: t("lib.templateFilters.error.needsKey", { name }) };
    const args = rest.slice(spec.keys).map((token) => token.text);
    if (args.length > spec.maxArgs) {
      if (spec.maxArgs === 0) return { error: t("lib.templateFilters.error.noArgs", { name }) };
      if (spec.keys > 0) return { error: t("lib.templateFilters.error.keyAndEncoding", { name }) };
      return { error: t("lib.templateFilters.error.encodingOnly", { name }) };
    }
    if (spec.maxArgs > 0 && args.length > 0 && !ENCODINGS.includes(args[0])) {
      return { error: t("lib.templateFilters.error.badEncoding", { name, encoding: q(args[0]) }) };
    }
    steps.push({ name, keys: rest.slice(0, spec.keys), args });
  }
  return { input, steps };
}
