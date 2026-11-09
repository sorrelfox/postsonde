import { describe, expect, it } from "vitest";
import { mockTemplateSet, patternParams, type MockTemplateNameDTO } from "./mockTemplate";
import { templateCompletionsAt } from "./templateCompletion";
import { isResolved, lookupVariable } from "./templateVariables";

// As Go's MockTemplateNames lists them (a few).
const NAMES: MockTemplateNameDTO[] = [
  {
    name: "request.method",
    description: "The request method",
    source: "request",
  },
  {
    name: "request.body",
    description: "The request body as text",
    source: "request",
  },
  {
    name: "request.body.",
    description: "A field of the request body",
    source: "request",
  },
  {
    name: "request.params.",
    description: "A path parameter",
    source: "request",
  },
  {
    name: "request.query.",
    description: "A query parameter",
    source: "request",
  },
  {
    name: "request.headers.",
    description: "A request header",
    source: "request",
  },
  { name: "$uuid", description: "A random UUID", source: "dynamic" },
  { name: "$randomInt", description: "A random integer", source: "dynamic" },
];

describe("patternParams", () => {
  it("finds every kind of parameter once", () => {
    expect(patternParams("/users/:id/files/{name}/{n:[0-9]+}/*rest")).toEqual(["id", "name", "n", "rest"]);
    expect(patternParams("/a/*/b/**/:id/:id")).toEqual(["id"]);
    expect(patternParams("/v1/items:batchGet")).toEqual([]);
  });
});

describe("mockTemplateSet", () => {
  const set = mockTemplateSet(NAMES, ["id"]);

  it("resolves what Go resolves", () => {
    for (const name of [
      "request.method",
      "request.body",
      "request.body.user.name",
      "request.params.id",
      "request.params.other",
      "request.query.page",
      "request.headers.X-Trace",
      "$uuid",
      "$randomInt 1 6",
      "request.query.q | base64",
    ]) {
      expect(isResolved(set, name), name).toBe(true);
    }
    for (const name of ["request.nope", "request.query.", "request.body.a..b", "baseUrl", "$nope"]) {
      expect(isResolved(set, name), name).toBe(false);
    }
    expect(lookupVariable(set, "request.query.page")).toMatchObject({
      scope: "Request",
      value: "A query parameter",
    });
  });

  it("completes the request's names, the route's parameters and the dynamic values", () => {
    const labels = templateCompletionsAt("{{req", set)?.items.map((i) => i.label) ?? [];
    expect(labels).toEqual(
      expect.arrayContaining(["request.method", "request.params.id", "request.query.", "$uuid", "$randomInt"]),
    );
    expect(labels.indexOf("request.method")).toBeLessThan(labels.indexOf("$uuid"));
  });
});
