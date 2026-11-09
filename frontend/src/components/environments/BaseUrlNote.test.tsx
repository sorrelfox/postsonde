import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";
import type { EnvironmentVariable } from "../../stores";
import { BaseUrlNote } from "./BaseUrlNote";

const v = (name: string, value: string, over: Partial<EnvironmentVariable> = {}): EnvironmentVariable => ({
  name,
  value,
  description: "",
  secret: false,
  enabled: true,
  localValue: null,
  ...over,
});

const text = (variables: EnvironmentVariable[], base?: EnvironmentVariable[] | null) =>
  render(<BaseUrlNote variables={variables} base={base} />).container.textContent;

describe("BaseUrlNote (COL-08)", () => {
  it("shows the environment's own base URL, its local value, masked when secret", () => {
    expect(text([v("baseUrl", "https://stg.test")], [])).toMatch(/^Base URL: https:\/\/stg\.test\. Requests whose URL starts with \//);
    expect(text([v("baseUrl", "https://stg.test", { localValue: "http://localhost" })], [])).toContain("http://localhost");
    expect(text([v("baseUrl", "https://s3cret.test", { secret: true })], [])).toContain("••••••");
  });

  it("shows the one inherited from the global variables", () => {
    const got = text([v("baseUrl", "x", { enabled: false })], [v("baseUrl", "https://prod.test")]);
    expect(got).toMatch(/^Base URL: https:\/\/prod\.test, the baseUrl of the global variables/);
  });

  it("says how to set one", () => {
    expect(text([], [])).toMatch(/^No base URL. Add a variable baseUrl/);
    // A secret without a value on this computer does not count.
    expect(text([v("baseUrl", "", { secret: true })], null)).toMatch(/^No base URL/);
    expect(text([])).toMatch(/in every environment that does not set its own/);
  });
});
