import { describe, expect, it } from "vitest";
import { isSecretExtra, withSecretExtra } from "./secretExtra";

describe("secretExtra", () => {
  it("reads the secret flag", () => {
    expect(isSecretExtra("")).toBe(false);
    expect(isSecretExtra('{"secret":true}')).toBe(true);
    expect(isSecretExtra('{"secret":"true"}')).toBe(false);
    expect(isSecretExtra("[1]")).toBe(false);
    expect(isSecretExtra("not json")).toBe(false);
  });

  it("sets and clears it, keeping the other members", () => {
    expect(withSecretExtra("", true)).toBe('{"secret":true}');
    expect(withSecretExtra('{"secret":true}', false)).toBe("");
    expect(withSecretExtra('{"x-a":1}', true)).toBe('{"x-a":1,"secret":true}');
    expect(withSecretExtra('{"x-a":1,"secret":true}', false)).toBe('{"x-a":1}');
    expect(withSecretExtra("{}", true)).toBe('{"secret":true}');
  });

  it("leaves text that is not a JSON object alone", () => {
    expect(withSecretExtra("[1]", true)).toBe("[1]");
    expect(withSecretExtra("oops", true)).toBe("oops");
  });
});
