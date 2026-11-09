// Response references (DYN-06, docs/templates.md#response-references):
// `{{$response REQUEST SOURCE [EXPR] [send=WHEN] [secret]}}` is a value
// from another request's last response, such as a login's token. This
// reads a reference as Go's template.ParseResponse does, so the editor can
// mark it, show the request by its name instead of its ID and complete the
// request (a picker that inserts the ID) and the source. Go reads the
// value: the hover asks it (HTTPService.ResponseValue), and the send reads
// it, sending the request first when the reference asks for it.

import {
  HTTPService,
  type ResponseValueDTO,
} from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/http";
import { t, type MessageKey } from "../i18n";
import type { CancellablePromise } from "./calls";
import { formatTimeoutMs } from "./http";
import { splitTokens } from "./templateFilters";

export type { ResponseValueDTO };

/** The dynamic value name of a response reference (Go's template.ResponseName). */
export const RESPONSE_NAME = "$response"; // i18n-ignore: the syntax

/** Go's Ref.Scope of a value read from a response, before the request's name. */
const RESPONSE_SCOPE = "Response"; // i18n-ignore: Go's scope name, as the other scopes come from Go

/** A request a reference can read: an HTTP or GraphQL request of the project. */
export interface ReferencedRequest {
  readonly id: string;
  readonly name: string;
  /** "GET /users", or the path: a few words next to the name in the picker. */
  readonly detail: string;
}

export type ResponseSource = "jsonpath" | "header" | "status" | "body" | "cookie" | "xpath" | "regex";

/** The sources in the order completion offers them (Go's ResponseSources). */
// i18n-ignore-start: the syntax's words
export const RESPONSE_SOURCES: readonly ResponseSource[] = ["jsonpath", "header", "status", "body", "cookie", "xpath", "regex"];
// i18n-ignore-end

const SOURCE_DESCRIPTIONS: Readonly<Record<ResponseSource, MessageKey>> = {
  jsonpath: "lib.templateResponse.source.jsonpath",
  header: "lib.templateResponse.source.header",
  status: "lib.templateResponse.source.status",
  body: "lib.templateResponse.source.body",
  cookie: "lib.templateResponse.source.cookie",
  xpath: "lib.templateResponse.source.xpath",
  regex: "lib.templateResponse.source.regex",
};

/** What a source reads, for completion. */
export function sourceDescription(source: ResponseSource): string {
  return t(SOURCE_DESCRIPTIONS[source]);
}

/** Whether a source needs an expression after it (status and body do not). */
export function takesExpr(source: ResponseSource): boolean {
  return source !== "status" && source !== "body";
}

export type SendWhen = "never" | "missing" | "stale" | "always";

/** A parsed `{{$response}}` reference. */
export interface ResponseRef {
  readonly requestId: string;
  readonly source: ResponseSource;
  readonly expr: string;
  readonly send: SendWhen;
  /** The age above which the response is stale, for "stale". */
  readonly maxAgeMs: number;
  readonly secret: boolean;
}

const SPACES = " \t\r\n";
const MAX_AGE_MS = 365 * 24 * 3600 * 1000;
const UNIT_MS: Readonly<Record<string, number>> = { s: 1000, m: 60_000, h: 3_600_000, d: 86_400_000 };

/** Reads `send=`'s age as Go does: terms of up to 6 digits and s, m, h or d. */
function parseAge(v: string): number | null {
  if (v === "") return null;
  let total = 0;
  const term = /^([0-9]{1,6})([smhd])/;
  while (v !== "") {
    const m = term.exec(v);
    if (m === null) return null;
    total += Number(m[1]) * UNIT_MS[m[2]];
    v = v.slice(m[0].length);
  }
  return total;
}

/**
 * Reads `name` (as written between the braces) as Go's ParseResponse does:
 * null when it is not a response reference at all, `{ error }` when its
 * arguments are not valid.
 */
export function parseResponseRef(name: string): ResponseRef | { error: string } | null {
  if (!name.startsWith(RESPONSE_NAME)) return null;
  const rest = name.slice(RESPONSE_NAME.length);
  if (rest !== "" && !SPACES.includes(rest[0])) return null;
  const invalid = { error: t("lib.templateResponse.invalid") };
  const toks = splitTokens(name);
  if (toks === null) return invalid;
  let args = toks.slice(1);
  if (args.some((a) => !a.quoted && a.text.includes("|")) || args.length < 2) return invalid;
  const requestId = args[0].text;
  if (requestId.trim() === "") return invalid;
  const source = args[1].text as ResponseSource;
  if (args[1].quoted || !RESPONSE_SOURCES.includes(source)) {
    return { error: t("lib.templateResponse.unknownSource", { source: JSON.stringify(args[1].text) }) };
  }
  args = args.slice(2);
  let expr = "";
  if (takesExpr(source)) {
    if (args.length === 0 || args[0].text.trim() === "") return { error: t("lib.templateResponse.needsExpr", { source }) };
    expr = args[0].text;
    args = args.slice(1);
  }
  let send: SendWhen = "never";
  let maxAgeMs = 0;
  let secret = false;
  let seenSend = false;
  for (const a of args) {
    if (!a.quoted && a.text === "secret" && !secret) {
      secret = true;
    } else if (!a.quoted && a.text.startsWith("send=") && !seenSend) {
      seenSend = true;
      const v = a.text.slice("send=".length);
      if (v === "never" || v === "missing" || v === "always") {
        send = v;
      } else {
        const age = parseAge(v);
        if (age === null || age <= 0 || age > MAX_AGE_MS) return { error: t("lib.templateResponse.badSend", { value: v }) };
        send = "stale";
        maxAgeMs = age;
      }
    } else {
      return invalid;
    }
  }
  return { requestId, source, expr, send, maxAgeMs, secret };
}

/** "JSONPath $.token", "header X-Token", "status code", "body": what a reference reads. */
export function sourceLabel(ref: ResponseRef): string {
  switch (ref.source) {
    case "jsonpath":
      return t("lib.templateResponse.label.jsonpath", { expr: ref.expr });
    case "xpath":
      return t("lib.templateResponse.label.xpath", { expr: ref.expr });
    case "regex":
      return t("lib.templateResponse.label.regex", { expr: ref.expr });
    case "header":
      return t("lib.templateResponse.label.header", { expr: ref.expr });
    case "cookie":
      return t("lib.templateResponse.label.cookie", { expr: ref.expr });
    case "status":
      return t("lib.templateResponse.label.status");
    case "body":
      return t("lib.templateResponse.label.body");
  }
}

/** When the request is sent first, in a sentence. */
export function sendText(ref: Pick<ResponseRef, "send" | "maxAgeMs">): string {
  switch (ref.send) {
    case "missing":
      return t("lib.templateResponse.send.missing");
    case "stale":
      return t("lib.templateResponse.send.stale", { age: formatTimeoutMs(ref.maxAgeMs) });
    case "always":
      return t("lib.templateResponse.send.always");
    case "never":
      return t("lib.templateResponse.send.never");
  }
}

/** Go's scope of a value read from request `name`'s response: "Response: Log in". */
export function responseScope(name: string): string {
  return name === "" ? RESPONSE_SCOPE : `${RESPONSE_SCOPE}: ${name}`;
}

/** What the editor can tell of a reference without Go: valid, its request known, and a line describing it. */
export interface ResponseLookup {
  readonly ref: ResponseRef | null;
  /** The referenced request, when the project has it. */
  readonly request?: ReferencedRequest;
  /** Why it is not valid (a red chip); undefined when it is. */
  readonly reason?: string;
  /** "JSONPath $.token of the last response of “Log in”". */
  readonly description: string;
}

/**
 * Reads `name` as a response reference against the project's requests
 * (`requests`, undefined when not known: the request is then not
 * checked). Undefined when `name` is not one.
 */
export function lookupResponse(name: string, requests: ReadonlyMap<string, ReferencedRequest> | undefined): ResponseLookup | undefined {
  const parsed = parseResponseRef(name);
  if (parsed === null) return undefined;
  if ("error" in parsed) return { ref: null, reason: parsed.error, description: "" };
  const request = requests?.get(parsed.requestId);
  if (requests !== undefined && request === undefined) {
    return { ref: parsed, reason: t("lib.templateResponse.noRequest", { id: parsed.requestId }), description: "" };
  }
  const description = t("lib.templateResponse.description", {
    what: sourceLabel(parsed),
    request: request?.name ?? parsed.requestId,
  });
  return { ref: parsed, request, description };
}

/** What a stored response reference reads now, from Go (never sends). */
export function loadResponseValue(requestId: string, name: string): CancellablePromise<ResponseValueDTO> {
  return HTTPService.ResponseValue(requestId, name);
}

/**
 * The hover's lines below the value for Go's answer: where the value comes
 * from ("Last response of “Log in”, status 200, 3 min ago"), and whether the
 * next send sends the request first. `now` is the current time in ms.
 */
export function responseValueLines(dto: ResponseValueDTO, now: number): string[] {
  const lines: string[] = [];
  const name = dto.requestName === "" ? dto.requestId : dto.requestName;
  if (dto.receivedAt !== "") {
    const ago = Math.max(0, Math.round((now - Date.parse(dto.receivedAt)) / 1000) * 1000);
    lines.push(
      t(dto.fromHistory ? "lib.templateResponse.fromHistory" : "lib.templateResponse.from", {
        request: name,
        status: String(dto.status),
        ago: formatTimeoutMs(ago),
      }),
    );
  }
  if (dto.sendFirst) lines.push(t("lib.templateResponse.sendFirst", { request: name }));
  else lines.push(sendText({ send: dto.send as SendWhen, maxAgeMs: dto.maxAgeMs }));
  return lines;
}

/** A completion inside the arguments of `{{$response …`. */
export interface ResponseCompletion {
  readonly label: string;
  /** The text inserted, when it is not the label (the request's ID). */
  readonly apply?: string;
  readonly detail: string;
  readonly info: string;
}

/** The send= and secret options offered after the expression. */
// i18n-ignore-start: the syntax's words
const OPTIONS = ["send=missing", "send=10m", "send=always", "secret"];
// i18n-ignore-end

const OPTION_INFO: Readonly<Record<string, MessageKey>> = {
  "send=missing": "lib.templateResponse.send.missing",
  "send=10m": "lib.templateResponse.option.age",
  "send=always": "lib.templateResponse.send.always",
  secret: "lib.templateResponse.option.secret",
};

/**
 * The completions while typing the arguments of a response reference:
 * `args` is the text after `$response` up to the cursor (starting with
 * whitespace), `word` the word being typed at its end. The first argument
 * offers the project's requests by name and inserts the ID (the picker),
 * the second the sources, the ones after the expression send= and secret.
 * Null when there is nothing to offer.
 */
export function responseCompletions(
  args: string,
  word: string,
  requests: ReadonlyMap<string, ReferencedRequest> | undefined,
): ResponseCompletion[] | null {
  const done = splitTokens(args.slice(0, args.length - word.length));
  if (done === null) return null;
  const index = done.length;
  if (index === 0) {
    if (requests === undefined || requests.size === 0) return null;
    return [...requests.values()]
      .sort((a, b) => a.name.localeCompare(b.name))
      .map((r) => ({ label: r.name, apply: `${r.id} `, detail: r.detail, info: r.id }));
  }
  if (index === 1) {
    return RESPONSE_SOURCES.map((s) => ({ label: s, apply: `${s} `, detail: t("lib.templateResponse.sourceDetail"), info: sourceDescription(s) }));
  }
  const source = done[1].text as ResponseSource;
  if (!RESPONSE_SOURCES.includes(source)) return null;
  const first = takesExpr(source) ? 3 : 2;
  if (index < first) return null;
  return OPTIONS.filter((o) => !done.slice(first).some((d) => (o === "secret" ? d.text === o : d.text.startsWith("send=")))).map(
    (o) => ({ label: o, detail: t("lib.templateResponse.optionDetail"), info: t(OPTION_INFO[o]) }),
  );
}
