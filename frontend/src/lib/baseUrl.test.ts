import { describe, expect, it } from "vitest";
import { isRelativeUrl, joinBaseUrl, relativeUrl } from "./baseUrl";
import { SECRET_MASK, toVariableSet, type VariableDTO } from "./templateVariables";

const variable = (name: string, value: string, over: Partial<VariableDTO> = {}): VariableDTO => ({
  name,
  value,
  secret: false,
  resolved: true,
  scope: "Environment: Staging",
  source: "environment",
  ...over,
});

const set = (...vars: VariableDTO[]) => toVariableSet({ variables: vars, environment: "Staging" });

describe("relative URLs (COL-08)", () => {
  it("tells relative URLs from the others as Go does", () => {
    for (const url of ["/users", "  /users?q=1", "/", "/{{id}}"]) expect(isRelativeUrl(url), url).toBe(true);
    for (const url of ["https://h/x", "{{baseUrl}}/users", "//cdn/x", "users", ""]) expect(isRelativeUrl(url), url).toBe(false);
  });

  it("joins with one slash and keeps the base's path", () => {
    expect(joinBaseUrl("https://h/api/", "/v1")).toBe("https://h/api/v1");
    expect(joinBaseUrl("https://h", "/v1")).toBe("https://h/v1");
  });

  it("says where a relative URL goes", () => {
    expect(relativeUrl("https://h/x", set())).toBeNull();
    expect(relativeUrl(" /users/{{id}} ", set(variable("baseUrl", "https://stg.test/")))).toEqual({
      base: variable("baseUrl", "https://stg.test/"),
      url: "https://stg.test/users/{{id}}",
    });
    // A secret base is masked.
    const secret = variable("baseUrl", "", { secret: true });
    expect(relativeUrl("/users", set(secret))).toEqual({ base: secret, url: `${SECRET_MASK}/users` });
    // No base, or one that does not resolve.
    expect(relativeUrl("/users", set())).toEqual({ base: undefined, url: "" });
    const broken = variable("baseUrl", "{{host}}", { resolved: false });
    expect(relativeUrl("/users", set(broken))).toEqual({ base: broken, url: "" });
  });
});
