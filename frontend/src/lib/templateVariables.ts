// The variables a request's {{references}} resolve to (ENV-11), as Go
// resolves them at send time: global < active environment < collection
// (the top-level folder) < folders (ENV-06, HTTPService.Variables,
// docs/templates.md). The editors mark each
// reference resolved or unresolved with this set and show its value and
// scope on hover.
import {
  HTTPService,
  type VariableDTO,
  type VariablesDTO,
} from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/http";
import { t } from "../i18n";
import type { CancellablePromise } from "./calls";
import { scanTemplate, type TemplateSpan } from "./templateSyntax";
import { parsePipe, splitPipe, type PipeToken } from "./templateFilters";
import { lookupResponse, responseScope, type ReferencedRequest } from "./templateResponse";

export type { VariableDTO, VariablesDTO };

/** The resolved variables of one request. */
export interface TemplateVariableSet {
  readonly byName: ReadonlyMap<string, VariableDTO>;
  /** The active environment's name, "" for none. */
  readonly environment: string;
  /** The request the set is for, which the hover of a {{$response}} reference asks Go about. */
  readonly requestId?: string;
  /**
   * The project's HTTP and GraphQL requests by ID, which {{$response}}
   * references read (DYN-06): their names for the hover and the picker.
   * Undefined when not known: a reference's request is then not checked.
   */
  readonly requests?: ReadonlyMap<string, ReferencedRequest>;
}

/** Go's scope name of dynamic values and pipes (HTTPService.Variables reports the others). */
const DYNAMIC_SCOPE = "Dynamic"; // i18n-ignore: Go's scope name, as the other scopes come from Go

/** A sentence from Go's lower-case wording: "{{a}} has no field …" → "{{a}} has no field ….". */
function sentence(reason: string): string {
  return t("lib.templateVariables.sentence", { reason: `${reason.charAt(0).toLocaleUpperCase()}${reason.slice(1)}` });
}

/** What a secret value is shown as. */
export const SECRET_MASK = "••••••";

/** The variables request `id` can use, resolved by Go. */
export function loadRequestVariables(id: string): CancellablePromise<VariablesDTO> {
  return HTTPService.Variables(id);
}

export function toVariableSet(dto: VariablesDTO): TemplateVariableSet {
  return {
    byName: new Map((dto.variables ?? []).map((v) => [v.name, v])),
    environment: dto.environment,
  };
}

/**
 * The variable `{{name}}` refers to. A dynamic value with arguments
 * (`{{$randomInt 1 6}}`) is listed under its first word only, with source
 * "dynamic"; Go checks the arguments when the request is sent. A dotted
 * name no scope defines (`{{auth.token}}`, ENV-13) is read from its base
 * variable's JSON value, see lookupPath. A filter pipe
 * (`{{token | base64}}`, DYN-04) is resolved from its input, see
 * lookupPipe.
 */
export function lookupVariable(set: TemplateVariableSet, name: string): VariableDTO | undefined {
  const v = set.byName.get(name);
  if (v !== undefined) return v;
  const pipe = lookupPipe(set, name);
  if (pipe !== undefined) return pipe.variable;
  const response = responseVariable(set, name);
  if (response !== undefined) return response;
  if (name.startsWith("$")) {
    const head = set.byName.get(name.split(/\s/, 1)[0]);
    if (head?.source === "dynamic") return head;
  }
  return lookupPath(set, name)?.variable;
}

/**
 * A filter pipe (`{{ body | hmacSha256 key }}`, docs/templates.md) as the
 * editor can tell without computing it: resolved when its input and keys
 * resolve, secret when any of them is, with a line saying what Go computes
 * at send time as its value. Undefined when the name is not a pipe; a pipe
 * with an unknown filter or bad arguments is unresolved with Go's reason.
 * A name that reads as an invalid pipe but starts with `$` may be a
 * dynamic value with a `|` in its arguments (`{{$datetime YYYY|MM}}`), so
 * it is left to the dynamic lookup, as Go tries the dynamic value first.
 */
export function lookupPipe(set: TemplateVariableSet, name: string): PathLookup | undefined {
  const stages = splitPipe(name);
  if (stages === null) return undefined;
  const pipe = parsePipe(stages);
  const unresolved = (reason: string): PathLookup => ({
    variable: { name, value: "", secret: false, resolved: false, scope: DYNAMIC_SCOPE, source: "dynamic" },
    reason,
  });
  if ("error" in pipe) {
    if (name.startsWith("$")) return undefined;
    return unresolved(sentence(pipe.error));
  }
  const operands: PipeToken[] = [pipe.input, ...pipe.steps.flatMap((s) => s.keys)];
  let secret = false;
  let scope = DYNAMIC_SCOPE;
  let source = "dynamic";
  for (const [i, op] of operands.entries()) {
    if (op.quoted) continue;
    const v = lookupVariable(set, op.text);
    if (v === undefined || !v.resolved) {
      return unresolved(
        v === undefined
          ? t("lib.templateVariables.notDefined", { ref: `{{${op.text}}}` })
          : t("lib.templateVariables.valueUndefined", { ref: `{{${op.text}}}` }),
      );
    }
    secret ||= v.secret;
    if (i === 0) {
      scope = v.scope;
      source = v.source;
    }
  }
  const input = pipe.input.quoted ? JSON.stringify(pipe.input.text) : `{{${pipe.input.text}}}`;
  const value = t("lib.templateVariables.pipeValue", { filters: pipe.steps.map((s) => s.name).join(" | "), input });
  return { variable: { name, value, secret, resolved: true, scope, source } };
}

/**
 * A response reference (`{{$response req_… jsonpath $.token}}`, DYN-06) as
 * the editor can tell without Go: resolved when it is valid and names a
 * request of the project, with the request's name in its scope and a line
 * saying what it reads as its value (Go reads the value at send time; the
 * hover asks Go for the stored one). Undefined when the name is not one.
 */
export function responseVariable(set: TemplateVariableSet, name: string): VariableDTO | undefined {
  if (set.byName.get(name.split(/\s/, 1)[0])?.source !== "dynamic") return undefined; // a scope defines $response
  const found = lookupResponse(name, set.requests);
  if (found === undefined) return undefined;
  return {
    name,
    value: found.description,
    secret: false,
    resolved: found.reason === undefined,
    scope: responseScope(found.request?.name ?? ""),
    source: "response",
  };
}

/** A dotted reference read from its base variable's JSON value. */
export interface PathLookup {
  /** The value found, under the dotted name, with the base's scope and secrecy. */
  readonly variable: VariableDTO;
  /** Why it does not resolve, when it does not. */
  readonly reason?: string;
}

/**
 * Resolves a dotted name no scope defines, as Go's template.Select does
 * (docs/templates.md, dot notation): the longest defined prefix is the
 * base, and the rest of the name is a path of object keys and array
 * indexes into its JSON value. A string is its text, anything else compact
 * JSON. A secret base's value is not known here, so the reference counts as
 * resolved and Go checks the path when the request is sent. Undefined when
 * the name has no dot, has whitespace or an empty part, or no prefix is
 * defined.
 */
export function lookupPath(set: TemplateVariableSet, name: string): PathLookup | undefined {
  if (!name.includes(".") || /\s/.test(name)) return undefined;
  const parts = name.split(".");
  if (parts.includes("")) return undefined;
  for (let n = parts.length - 1; n >= 1; n--) {
    const baseName = parts.slice(0, n).join(".");
    const base = set.byName.get(baseName);
    if (base === undefined || base.source === "dynamic") continue;
    if (!base.resolved) {
      return {
        variable: { ...base, name, resolved: false },
        reason: t("lib.templateVariables.valueUndefined", { ref: `{{${baseName}}}` }),
      };
    }
    if (base.secret) return { variable: { ...base, name, value: "" } };
    const picked = selectPath(baseName, base.value, parts.slice(n));
    if (typeof picked === "string") return { variable: { ...base, name, value: picked } };
    return { variable: { ...base, name, value: "", resolved: false }, reason: picked.error };
  }
  return undefined;
}

const INDEX = /^(0|[1-9][0-9]*)$/;

/** Walks `path` through the JSON `value` of `{{base}}`: the text found, or why not (Go's wording). */
export function selectPath(base: string, value: string, path: readonly string[]): string | { error: string } {
  const fail = (msg: string) => ({ error: sentence(msg) });
  const ref = (name: string) => `{{${name}}}`;
  let cur: unknown;
  try {
    cur = JSON.parse(value);
  } catch {
    return fail(t("lib.templateVariables.path.notJson", { ref: ref(base) }));
  }
  let at = base;
  for (const key of path) {
    if (Array.isArray(cur)) {
      if (!INDEX.test(key)) return fail(t("lib.templateVariables.path.notIndex", { ref: ref(at), key: JSON.stringify(key) }));
      const i = Number(key);
      if (i >= cur.length) return fail(t("lib.templateVariables.path.noElement", { ref: ref(at), index: key }));
      cur = cur[i];
    } else if (typeof cur === "object" && cur !== null) {
      if (!Object.prototype.hasOwnProperty.call(cur, key)) {
        return fail(t("lib.templateVariables.path.noField", { ref: ref(at), key: JSON.stringify(key) }));
      }
      cur = (cur as Record<string, unknown>)[key];
    } else {
      return fail(t("lib.templateVariables.path.notContainer", { ref: ref(at) }));
    }
    at += `.${key}`;
  }
  return typeof cur === "string" ? cur : JSON.stringify(cur);
}

/** Whether `{{name}}` resolves: defined (or dynamic), and its value has no undefined names of its own. */
export function isResolved(set: TemplateVariableSet, name: string): boolean {
  return lookupVariable(set, name)?.resolved === true;
}

/** A variable's value as the UI may show it: masked when secret. */
export function shownValue(v: VariableDTO): string {
  return v.secret ? SECRET_MASK : v.value;
}

/**
 * Where a variable's value comes from, for its hover: the scope ("Global",
 * "Environment: dev"), and whether it is this computer's local value
 * (SEC-02): "Environment: dev · local value".
 */
export function scopeText(v: VariableDTO): string {
  return v.local === true ? t("lib.templateVariables.localScope", { scope: v.scope }) : v.scope;
}

/**
 * The scopes of lower precedence a variable's value wins over (ENV-06),
 * as a line for its hover: "Overrides Environment: dev, Global". "" when
 * only one scope defines it.
 */
export function overridesText(v: VariableDTO): string {
  const scopes = v.overrides ?? [];
  return scopes.length === 0 ? "" : t("lib.templateVariables.overrides", { scopes: scopes.join(", ") });
}

/** Why `{{name}}` does not resolve, in a sentence. */
export function unresolvedReason(set: TemplateVariableSet, name: string): string {
  if (name === "") return t("lib.templateVariables.noName");
  if (!set.byName.has(name)) {
    const reason =
      lookupPipe(set, name)?.reason ??
      (responseVariable(set, name) !== undefined ? lookupResponse(name, set.requests)?.reason : undefined) ??
      lookupPath(set, name)?.reason;
    if (reason !== undefined) return reason;
  }
  if (lookupVariable(set, name) !== undefined) return t("lib.templateVariables.itsValueUndefined");
  return set.environment === ""
    ? t("lib.templateVariables.undefinedNoEnvironment")
    : t("lib.templateVariables.undefinedIn", { environment: set.environment });
}

/** One line per reference in `text`: "{{name}} = value (scope)" or why it does not resolve. For titles. */
export function describeReferences(text: string, set: TemplateVariableSet | null): string {
  if (set === null || !text.includes("{{")) return "";
  const seen = new Set<string>();
  const lines: string[] = [];
  for (const span of scanTemplate(text)) {
    if (span.kind !== "var" || seen.has(span.name)) continue;
    seen.add(span.name);
    const v = lookupVariable(set, span.name);
    if (v !== undefined && v.resolved) {
      const overrides = v.overrides ?? [];
      const scope =
        overrides.length === 0
          ? scopeText(v)
          : t("lib.templateVariables.scopeOverrides", { scope: scopeText(v), scopes: overrides.join(", ") });
      lines.push(t("lib.templateVariables.reference", { ref: `{{${span.name}}}`, value: shownValue(v), scope }));
    } else {
      lines.push(t("lib.templateVariables.unresolved", { ref: `{{${span.name}}}`, reason: unresolvedReason(set, span.name) }));
    }
  }
  return lines.join("\n");
}

/** The names in `text` that do not resolve with `set`, in order, each once. */
export function unresolvedNames(text: string, set: TemplateVariableSet): string[] {
  const out: string[] = [];
  for (const span of scanTemplate(text)) {
    if (span.kind === "var" && !isResolved(set, span.name) && !out.includes(span.name)) out.push(span.name);
  }
  return out;
}

/** A piece of a text split for display: plain text or a reference. */
export type TemplatePiece =
  | { readonly kind: "text"; readonly text: string }
  | { readonly kind: "var"; readonly text: string; readonly span: TemplateSpan };

/** Splits `text` into plain text and `{{references}}`, for drawing chips outside CodeMirror. */
export function splitTemplate(text: string): TemplatePiece[] {
  const out: TemplatePiece[] = [];
  let at = 0;
  for (const span of scanTemplate(text)) {
    if (span.kind !== "var") continue;
    if (span.start > at) out.push({ kind: "text", text: text.slice(at, span.start) });
    out.push({ kind: "var", text: text.slice(span.start, span.end), span });
    at = span.end;
  }
  if (at < text.length) out.push({ kind: "text", text: text.slice(at) });
  return out;
}
