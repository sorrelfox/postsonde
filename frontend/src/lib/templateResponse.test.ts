import { describe, expect, it } from "vitest";
import fixture from "../../../internal/core/template/testdata/responses.json?raw";
import { templateCompletionsAt } from "./templateCompletion";
import {
  lookupResponse,
  parseResponseRef,
  responseCompletions,
  responseValueLines,
  sendText,
  type ReferencedRequest,
  type ResponseValueDTO,
} from "./templateResponse";
import {
  describeReferences,
  isResolved,
  lookupVariable,
  toVariableSet,
  unresolvedReason,
  type TemplateVariableSet,
  type VariableDTO,
} from "./templateVariables";

interface ResponseCase {
  name: string;
  response: boolean;
  valid?: boolean;
  requestId?: string;
  source?: string;
  expr?: string;
  send?: string;
  maxAgeMs?: number;
  secret?: boolean;
}

const requests = new Map<string, ReferencedRequest>([
  ["req_login", { id: "req_login", name: "Log in", detail: "POST auth/login.json" }],
  ["req_me", { id: "req_me", name: "Me", detail: "GET me.json" }],
]);

const dynamic = (name: string): VariableDTO => ({ name, value: `${name} description`, secret: false, resolved: true, scope: "Dynamic", source: "dynamic" });

const set: TemplateVariableSet = {
  ...toVariableSet({ variables: [dynamic("$response"), dynamic("$uuid")], environment: "" }),
  requestId: "req_me",
  requests,
};

describe("response references (shared with Go: responses.json)", () => {
  const cases = (JSON.parse(fixture) as { cases: ResponseCase[] }).cases;
  for (const c of cases) {
    it(`reads ${JSON.stringify(c.name)} as Go does`, () => {
      const got = parseResponseRef(c.name);
      if (!c.response) {
        expect(got).toBeNull();
        return;
      }
      if (c.valid !== true) {
        expect(got !== null && "error" in got).toBe(true);
        return;
      }
      expect(got).toEqual({
        requestId: c.requestId,
        source: c.source,
        expr: c.expr ?? "",
        send: c.send,
        maxAgeMs: c.maxAgeMs ?? 0,
        secret: c.secret ?? false,
      });
    });
  }
});

describe("lookupResponse", () => {
  it("names the request instead of its ID", () => {
    const got = lookupResponse("$response req_login jsonpath $.access_token", requests);
    expect(got?.reason).toBeUndefined();
    expect(got?.request?.name).toBe("Log in");
    expect(got?.description).toBe("JSONPath $.access_token of the last response of “Log in”");
  });

  it("says when the request is not in the project, or the reference is not valid", () => {
    expect(lookupResponse("$response req_gone status", requests)?.reason).toBe(
      "No HTTP or GraphQL request of this project has the ID req_gone. Pick the request again.",
    );
    expect(lookupResponse("$response req_login nope", requests)?.reason).toBe(
      '"nope" is not a source: use jsonpath, header, status, body, cookie, xpath or regex.',
    );
    expect(lookupResponse("$response req_login jsonpath", requests)?.reason).toBe("The jsonpath source needs an expression after it.");
    expect(lookupResponse("$uuid", requests)).toBeUndefined();
    // Without the project's requests, the request is not checked.
    expect(lookupResponse("$response req_gone status", undefined)?.reason).toBeUndefined();
  });
});

describe("response references in the variable set", () => {
  it("resolves with the request's name as the scope", () => {
    const v = lookupVariable(set, "$response req_login header X-Token send=10m");
    expect(v).toMatchObject({ resolved: true, scope: "Response: Log in", source: "response", value: "header X-Token of the last response of “Log in”" });
    expect(isResolved(set, "$response req_gone status")).toBe(false);
    expect(unresolvedReason(set, "$response req_gone status")).toContain("req_gone");
    expect(describeReferences("Bearer {{$response req_login jsonpath $.token}}", set)).toBe(
      "{{$response req_login jsonpath $.token}} = JSONPath $.token of the last response of “Log in” (Response: Log in)",
    );
  });

  it("is the input of a pipe", () => {
    expect(lookupVariable(set, "$response req_login body | base64")).toMatchObject({ resolved: true, scope: "Response: Log in" });
  });

  it("leaves $response to a scope that defines it", () => {
    const defined: TemplateVariableSet = {
      ...toVariableSet({
        variables: [{ name: "$response", value: "x", secret: false, resolved: true, scope: "Global", source: "global" }],
        environment: "",
      }),
      requests,
    };
    expect(lookupVariable(defined, "$response req_login status")?.source).not.toBe("response");
  });
});

describe("completion of a response reference", () => {
  it("picks the request by its name and inserts its ID", () => {
    const got = templateCompletionsAt("Bearer {{$response ", set);
    expect(got?.from).toBe(19);
    expect(got?.items.map((i) => [i.label, i.apply])).toEqual([
      ["Log in", "req_login "],
      ["Me", "req_me "],
    ]);
    expect(got?.items[0]).toMatchObject({ kind: "response", detail: "POST auth/login.json", info: "req_login" });
    // While typing the name.
    expect(templateCompletionsAt("{{$response Lo", set)?.from).toBe(12);
  });

  it("offers the sources, then send= and secret after the expression", () => {
    expect(templateCompletionsAt("{{$response req_login ", set)?.items.map((i) => i.label)).toEqual([
      "jsonpath",
      "header",
      "status",
      "body",
      "cookie",
      "xpath",
      "regex",
    ]);
    expect(templateCompletionsAt("{{$response req_login jsonpath ", set)).toBeNull();
    expect(templateCompletionsAt("{{$response req_login jsonpath $.t ", set)?.items.map((i) => i.label)).toEqual([
      "send=missing",
      "send=10m",
      "send=always",
      "secret",
    ]);
    expect(templateCompletionsAt("{{$response req_login status send=always ", set)?.items.map((i) => i.label)).toEqual(["secret"]);
    expect(responseCompletions(" req_login nope ", "", requests)).toBeNull();
    expect(responseCompletions(" ", "", undefined)).toBeNull();
  });
});

describe("responseValueLines", () => {
  const dto = (over: Partial<ResponseValueDTO>): ResponseValueDTO => ({
    name: "$response req_login status",
    requestId: "req_login",
    requestName: "Log in",
    resolved: true,
    value: "200",
    secret: false,
    receivedAt: "2026-10-01T12:00:00Z",
    status: 200,
    fromHistory: false,
    send: "never",
    maxAgeMs: 0,
    sendFirst: false,
    problem: "",
    code: "",
    ...over,
  });
  const now = Date.parse("2026-10-01T12:03:00Z");

  it("says where the value comes from and when the request is sent first", () => {
    expect(responseValueLines(dto({}), now)).toEqual([
      "Last response of “Log in”: status 200, 3 min ago",
      "Never sent for this reference: the stored response is read.",
    ]);
    expect(responseValueLines(dto({ fromHistory: true, send: "stale", maxAgeMs: 600_000, sendFirst: true }), now)).toEqual([
      "Last response of “Log in” in the history: status 200, 3 min ago",
      "“Log in” is sent first at the next send.",
    ]);
    expect(responseValueLines(dto({ receivedAt: "", resolved: false, send: "missing", sendFirst: true }), now)).toEqual([
      "“Log in” is sent first at the next send.",
    ]);
    expect(sendText({ send: "stale", maxAgeMs: 600_000 })).toBe("Sent first when it has no response, or it is older than 10 min.");
  });
});
