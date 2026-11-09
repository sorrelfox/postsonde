import { describe, expect, it } from "vitest";
import type { KeyValueDTO } from "./collection";
import { dayBound, groupByDay, restorePatch, statusTone, type RestoreDTO } from "./history";
import type { HistorySummaryDTO } from "./responseHistory";

const MASK = "••••••";

const kv = (name: string, value: string, enabled = true): KeyValueDTO => ({ name, value, description: "", enabled, extra: "" });

function restored(over: Partial<RestoreDTO> = {}): RestoreDTO {
  return {
    id: "h_1",
    requestId: "req_1",
    requestName: "Create user",
    kind: "http",
    templated: true,
    method: "POST",
    url: "{{base}}/users?team={{team}}",
    params: [kv("team", "{{team}}")],
    pathParams: [],
    headers: [kv("Authorization", "Bearer "), kv("X-Trace", "{{trace}}")],
    body: { mode: "raw", type: "json", text: '{"name":"{{name}}"}', fields: [], parts: [], path: "", contentType: "", extra: "" },
    query: "",
    variables: "",
    operationName: "",
    hidden: [{ section: "headers", name: "Authorization", reason: "masked" }],
    ...over,
  };
}

function summary(over: Partial<HistorySummaryDTO> = {}): HistorySummaryDTO {
  return {
    id: "h_1",
    time: "2026-09-23T10:15:02Z",
    kind: "http",
    requestId: "req_1",
    requestName: "Get user",
    environment: "",
    method: "GET",
    url: "https://api.test/u",
    outcome: "ok",
    statusCode: 200,
    status: "200 OK",
    errorCode: "",
    error: "",
    durationMs: 12,
    requestSize: 0,
    responseSize: 2,
    masked: false,
    ...over,
  };
}

describe("restorePatch (NAV-03)", () => {
  it("restores into the request, keeping its own value where the history hid a secret", () => {
    const current = { headers: [kv("authorization", "Bearer real-token"), kv("X-Old", "1")], url: "old", body: { mode: "none" } };
    const patch = restorePatch(restored(), current);
    expect(patch).toMatchObject({
      method: "POST",
      url: "{{base}}/users?team={{team}}",
      params: [kv("team", "{{team}}")],
      headers: [kv("Authorization", "Bearer real-token"), kv("X-Trace", "{{trace}}")],
      body: { mode: "raw", text: '{"name":"{{name}}"}' },
    });
    expect(JSON.stringify(patch)).not.toContain(MASK);
  });

  it("keeps the request's URL, body and texts when the history hid them", () => {
    const current = { url: "{{base}}/users?key={{key}}", params: [kv("key", "{{key}}")], body: { mode: "raw", text: "mine" }, headers: [] };
    const r = restored({
      url: "{{base}}/users?key=",
      params: [kv("key", "")],
      body: { mode: "raw", type: "json", text: '{"pw":""}', fields: [], parts: [], path: "", contentType: "", extra: "" },
      hidden: [
        { section: "url", name: "", reason: "masked" },
        { section: "params", name: "key", reason: "masked" },
        { section: "body", name: "", reason: "clipped" },
      ],
    });
    const patch = restorePatch(r, current);
    expect("url" in patch).toBe(false);
    expect("params" in patch).toBe(false);
    expect("body" in patch).toBe(false);
  });

  it("gives a scratch request what is left, never a mask", () => {
    const patch = restorePatch(restored(), null);
    expect(patch.headers).toEqual([kv("Authorization", "Bearer "), kv("X-Trace", "{{trace}}")]);
    expect(patch.url).toBe("{{base}}/users?team={{team}}");
    expect(patch.pathParams).toEqual([]);
  });

  it("makes the query rows of an entry kept only as sent from its URL", () => {
    const patch = restorePatch(restored({ templated: false, url: "https://api.test/users?team=a&page=2", params: [], hidden: [] }), null);
    expect((patch.params as KeyValueDTO[]).map((p) => [p.name, p.value, p.enabled])).toEqual([
      ["team", "a", true],
      ["page", "2", true],
    ]);
    expect("pathParams" in patch).toBe(false);
  });

  it("restores a GraphQL request's query, variables and operation", () => {
    const r = restored({ kind: "graphql", method: "", url: "{{gql}}", query: "{ me }", variables: '{"a":1}', operationName: "Me", hidden: [{ section: "variables", name: "", reason: "masked" }] });
    expect(restorePatch(r, { variables: "mine", headers: [] })).toEqual({
      method: "",
      url: "{{gql}}",
      headers: r.headers,
      query: "{ me }",
      operationName: "Me",
    });
    expect(restorePatch(r, null)).toMatchObject({ variables: '{"a":1}' });
  });
});

describe("history helpers", () => {
  it("turns a date field into RFC 3339 bounds of the local day", () => {
    const start = new Date(dayBound("2026-09-23"));
    expect([start.getFullYear(), start.getMonth(), start.getDate(), start.getHours()]).toEqual([2026, 8, 23, 0]);
    const end = new Date(dayBound("2026-09-23", true));
    expect([end.getDate(), end.getHours()]).toEqual([24, 0]);
    expect(dayBound("")).toBe("");
    expect(dayBound("nope")).toBe("");
  });

  it("groups entries by day: today, yesterday, earlier", () => {
    const now = new Date(2026, 8, 23, 15, 0);
    const at = (d: number, h: number) => new Date(2026, 8, d, h, 0).toISOString();
    const groups = groupByDay(
      [summary({ id: "a", time: at(23, 9) }), summary({ id: "b", time: at(23, 1) }), summary({ id: "c", time: at(22, 20) }), summary({ id: "d", time: at(19, 8) })],
      now,
    );
    expect(groups.map((g) => [g.day, g.entries.map((e) => e.id)])).toEqual([
      ["today", ["a", "b"]],
      ["yesterday", ["c"]],
      ["earlier", ["d"]],
    ]);
  });

  it("colours a status by class", () => {
    expect(statusTone(summary())).toBe("ok");
    expect(statusTone(summary({ statusCode: 404 }))).toBe("warn");
    expect(statusTone(summary({ statusCode: 503 }))).toBe("bad");
    expect(statusTone(summary({ outcome: "error", statusCode: 0 }))).toBe("bad");
    expect(statusTone(summary({ outcome: "cancelled", statusCode: 0 }))).toBe("muted");
  });
});
