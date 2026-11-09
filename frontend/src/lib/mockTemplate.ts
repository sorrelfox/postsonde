// The names a templated mock response can use (MOCK-06, Go's mockhttp
// RequestNames and the dynamic values, ServersService.MockTemplateNames),
// as a TemplateVariableSet: the body editor draws {{references}} as chips
// with it, marks the ones Go would not resolve, and completes them after
// `{{`. The request's values have no value yet, so hover shows what each
// one is. See docs/mock-routes.md (Templates).
import {
  ServersService,
  type MockTemplateNameDTO,
} from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/servers";
import type { VariableDTO } from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/http";
import { t } from "../i18n";
import type { TemplateVariableSet } from "./templateVariables";

export type { MockTemplateNameDTO };


let cached: Promise<readonly MockTemplateNameDTO[]> | null = null;

/** The names from Go, loaded once; rejects when the service is not bound. */
export function loadMockTemplateNames(): Promise<readonly MockTemplateNameDTO[]> {
  if (cached === null) {
    cached = Promise.resolve()
      .then(() => ServersService.MockTemplateNames())
      .then(
        (names) => names ?? [],
        (err: unknown) => {
          cached = null; // try again next time
          throw err;
        },
      );
  }
  return cached;
}

/** The path parameter names of a mock route pattern: `:id`, `{id}`, `{id:[0-9]+}`, `*rest`. */
export function patternParams(pattern: string): string[] {
  const out: string[] = [];
  for (const seg of pattern.split("/")) {
    const m = /^(?::|\*)([A-Za-z_]\w*)$/.exec(seg) ?? /^\{([A-Za-z_]\w*)(?::.*)?\}$/.exec(seg);
    if (m !== null && !out.includes(m[1])) out.push(m[1]);
  }
  return out;
}

function requestVariable(name: string, description: string, resolved = true): VariableDTO {
  return {
    name,
    value: description,
    secret: false,
    resolved,
    // The scope shown for the request's values.
    scope: t("lib.mockTemplate.requestScope"),
    source: "request",
  };
}

/**
 * A map whose lookups also answer the names under a prefix
 * (`request.query.` + anything), which Go resolves for any parameter,
 * header or JSON path, "" when the request has none. The prefixes
 * themselves are listed, for completion, but do not resolve.
 */
class MockNames extends Map<string, VariableDTO> {
  private readonly prefixes: readonly VariableDTO[];

  constructor(entries: readonly (readonly [string, VariableDTO])[], prefixes: readonly VariableDTO[]) {
    super(entries);
    this.prefixes = prefixes;
  }

  override get(name: string): VariableDTO | undefined {
    const own = super.get(name);
    if (own !== undefined) return own;
    if (/[\s|]/.test(name)) return undefined;
    for (const p of this.prefixes) {
      if (!name.startsWith(p.name) || name.length === p.name.length) continue;
      const rest = name.slice(p.name.length);
      if (p.name === "request.body." && rest.split(".").includes("")) return undefined;
      return requestVariable(name, p.value);
    }
    return undefined;
  }
}

/** The set a templated response's editors use; `params` are the route's path parameters. */
export function mockTemplateSet(names: readonly MockTemplateNameDTO[], params: readonly string[]): TemplateVariableSet {
  const entries: [string, VariableDTO][] = [];
  const prefixes: VariableDTO[] = [];
  for (const n of names) {
    if (n.source === "dynamic") {
      entries.push([
        n.name,
        {
          name: n.name,
          value: n.description,
          secret: false,
          resolved: true,
          scope: "Dynamic", // i18n-ignore: Go's scope name of dynamic values (HTTPService.Variables)
          source: "dynamic",
        },
      ]);
    } else if (n.name.endsWith(".")) {
      const v = requestVariable(n.name, n.description, false);
      prefixes.push(v);
      entries.push([n.name, v]);
    } else {
      entries.push([n.name, requestVariable(n.name, n.description)]);
    }
  }
  for (const p of params) {
    const name = `request.params.${p}`;
    entries.push([name, requestVariable(name, t("lib.mockTemplate.pathParam", { name: p }))]);
  }
  return { byName: new MockNames(entries, prefixes), environment: "" };
}
