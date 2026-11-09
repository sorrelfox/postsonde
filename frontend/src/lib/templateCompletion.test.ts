import { describe, expect, it } from "vitest";
import { templateCompletionsAt } from "./templateCompletion";
import { TEMPLATE_FILTERS } from "./templateFilters";
import { toVariableSet, type VariableDTO } from "./templateVariables";

const variable = (name: string, over: Partial<VariableDTO> = {}): VariableDTO => ({
  name,
  value: `${name}-value`,
  secret: false,
  resolved: true,
  scope: "Environment: dev",
  source: "environment",
  ...over,
});

const set = toVariableSet({
  variables: [
    variable("token", { secret: true, value: "" }),
    variable("host"),
    variable("$randomEmail", { scope: "Dynamic", source: "dynamic", value: "A random email address" }),
    variable("$uuid", { scope: "Dynamic", source: "dynamic", value: "A random UUID (v4)" }),
  ],
  environment: "dev",
});

describe("templateCompletionsAt", () => {
  it("offers the variables, then the dynamic values, right after {{", () => {
    const got = templateCompletionsAt("https://{{", set);
    expect(got?.from).toBe(10);
    expect(got?.items.map((i) => i.label)).toEqual(["host", "token", "$randomEmail", "$uuid"]);
    expect(got?.items[1]).toEqual({ label: "token", kind: "variable", detail: "Environment: dev", info: "••••••" });
    expect(got?.items[2]).toEqual({
      label: "$randomEmail",
      kind: "dynamic",
      detail: "dynamic",
      info: "A random email address",
    });
  });

  it("marks local values and never lists a secret's value (SEC-01, SEC-02)", () => {
    const vars = toVariableSet({
      variables: [variable("base", { local: true }), variable("key", { secret: true, value: "should-not-show" })],
      environment: "dev",
    });
    const got = templateCompletionsAt("{{", vars);
    expect(got?.items.find((i) => i.label === "base")?.detail).toBe("Environment: dev · local value");
    expect(got?.items.find((i) => i.label === "key")?.info).toBe("••••••");
    expect(JSON.stringify(got)).not.toContain("should-not-show");
  });

  it("completes the word being typed", () => {
    expect(templateCompletionsAt("a {{ $ran", set)?.from).toBe(5);
    expect(templateCompletionsAt("{{auth.to", set)?.from).toBe(2);
  });

  it("offers the filters after a |", () => {
    const got = templateCompletionsAt("{{ token | sha", set);
    expect(got?.from).toBe(11);
    expect(got?.items.map((i) => i.label)).toEqual(TEMPLATE_FILTERS.map((f) => f.name));
    expect(got?.items.find((i) => i.label === "hmacSha256")?.detail).toBe("filter, key");
    expect(templateCompletionsAt('{{"a|b" | ', null)?.from).toBe(10);
    expect(templateCompletionsAt("{{a | base64 | ", set)?.items[0].kind).toBe("filter");
  });

  it("offers nothing outside a reference or in arguments", () => {
    expect(templateCompletionsAt("plain", set)).toBeNull();
    expect(templateCompletionsAt("{{host}} x", set)).toBeNull();
    expect(templateCompletionsAt("\\{{", set)).toBeNull();
    expect(templateCompletionsAt("{{$randomInt 1 ", set)).toBeNull();
    expect(templateCompletionsAt("{{a | hmacSha256 ", set)).toBeNull();
    expect(templateCompletionsAt('{{"a|', set)).toBeNull();
    expect(templateCompletionsAt("{{", null)).toBeNull();
    expect(templateCompletionsAt("\\\\{{", set)?.from).toBe(4);
  });
});
