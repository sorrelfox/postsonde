import { describe, expect, it } from "vitest";
import {
  describeReferences,
  isResolved,
  lookupPath,
  lookupVariable,
  overridesText,
  scopeText,
  shownValue,
  splitTemplate,
  toVariableSet,
  unresolvedNames,
  unresolvedReason,
  type VariableDTO,
} from "./templateVariables";

const variable = (name: string, over: Partial<VariableDTO> = {}): VariableDTO => ({
  name,
  value: `${name}-value`,
  secret: false,
  resolved: true,
  scope: "Global",
  source: "global",
  ...over,
});

const set = toVariableSet({
  variables: [variable("host"), variable("key", { secret: true, value: "" }), variable("bad", { resolved: false })],
  environment: "",
});

describe("templateVariables", () => {
  it("tells resolved names from unresolved ones", () => {
    expect(isResolved(set, "host")).toBe(true);
    expect(isResolved(set, "key")).toBe(true);
    expect(isResolved(set, "bad")).toBe(false);
    expect(isResolved(set, "nope")).toBe(false);
    expect(unresolvedNames("{{nope}}/{{host}}/{{bad}}/{{nope}}/\\{{esc}}", set)).toEqual(["nope", "bad"]);
    expect(unresolvedReason(set, "nope")).toBe("Not defined. No environment is active.");
    expect(unresolvedReason(toVariableSet({ variables: [], environment: "dev" }), "nope")).toBe(
      "Not defined in dev or any other scope.",
    );
  });

  it("resolves dynamic values, also with arguments", () => {
    const dyn = toVariableSet({
      variables: [
        variable("$uuid", { scope: "Dynamic", source: "dynamic", value: "A random UUID" }),
        variable("$randomInt", { scope: "Dynamic", source: "dynamic", value: "A random integer" }),
        variable("$timestamp", { scope: "Folder", source: "folder", value: "fixed" }),
      ],
      environment: "",
    });
    expect(isResolved(dyn, "$uuid")).toBe(true);
    expect(isResolved(dyn, "$randomInt 1 6")).toBe(true);
    expect(isResolved(dyn, "$randomInt\t1 6")).toBe(true);
    // A scope that defines $timestamp hides the dynamic entry, so the
    // editor cannot tell that Go would still generate "$timestamp +1d".
    expect(isResolved(dyn, "$timestamp +1d")).toBe(false);
    expect(isResolved(dyn, "$nope 1")).toBe(false);
    expect(lookupVariable(dyn, "$randomInt 1 6")?.name).toBe("$randomInt");
    expect(unresolvedNames("{{$uuid}}{{$randomInt 1 6}}{{$nope}}", dyn)).toEqual(["$nope"]);
    expect(describeReferences("{{$randomInt 1 6}}", dyn)).toBe("{{$randomInt 1 6}} = A random integer (Dynamic)");
  });

  it("resolves filter pipes from their input and keys (DYN-04)", () => {
    const vars = toVariableSet({
      variables: [
        variable("body"),
        variable("key", { secret: true, value: "" }),
        variable("bad", { resolved: false }),
        variable("auth", { value: '{"token":"t"}' }),
        variable("$uuid", { scope: "Dynamic", source: "dynamic", value: "A random UUID" }),
        variable("$datetime", { scope: "Dynamic", source: "dynamic", value: "UTC time" }),
      ],
      environment: "dev",
    });
    expect(lookupVariable(vars, "body | sha256 base64")).toEqual({
      name: "body | sha256 base64",
      value: "sha256 of {{body}}, computed when sent",
      secret: false,
      resolved: true,
      scope: "Global",
      source: "global",
    });
    // A hash of a secret, or an HMAC with a secret key, is secret.
    expect(lookupVariable(vars, "key | base64 | md5")?.secret).toBe(true);
    expect(lookupVariable(vars, "body | hmacSha256 key")?.secret).toBe(true);
    expect(lookupVariable(vars, 'body | hmacSha256 "k"')?.secret).toBe(false);
    expect(lookupVariable(vars, '"user:pass" | base64')).toMatchObject({
      resolved: true,
      scope: "Dynamic",
      value: 'base64 of "user:pass", computed when sent',
    });
    expect(isResolved(vars, "auth.token | urlEncode")).toBe(true);
    expect(isResolved(vars, "$uuid | md5")).toBe(true);
    // A dynamic value with a | in its arguments is left to Go.
    expect(isResolved(vars, "$datetime YYYY|MM")).toBe(true);

    expect(isResolved(vars, "nope | base64")).toBe(false);
    expect(unresolvedReason(vars, "nope | base64")).toBe("{{nope}} is not defined.");
    expect(unresolvedReason(vars, "body | hmacSha256 nokey")).toBe("{{nokey}} is not defined.");
    expect(unresolvedReason(vars, "bad | hex")).toBe("The value of {{bad}} uses a variable that is not defined.");
    expect(unresolvedReason(vars, "body | b64")).toBe('"b64" is not a filter.');
    expect(unresolvedReason(vars, "body | hmacSha256")).toBe(
      "HmacSha256 needs a key: a variable name or a quoted text.",
    );
    expect(unresolvedNames("{{body | base64}}{{body | nope}}", vars)).toEqual(["body | nope"]);
    expect(describeReferences("{{key | sha1}}", vars)).toBe("{{key | sha1}} = •••••• (Global)");
  });

  it("reads dotted names from a base variable's JSON value (ENV-13)", () => {
    const json = toVariableSet({
      variables: [
        variable("auth", { value: '{"token":"t0k","user":{"id":7,"tags":["a","b"]},"on":true,"none":null}' }),
        variable("auth.token", { value: "flat" }),
        variable("api.v2", { value: '{"url":"https://v2"}' }),
        variable("api", { value: '{"url":"https://v1"}' }),
        variable("text", { value: "not json" }),
        variable("creds", { value: "", secret: true }),
        variable("broken", { value: "{{nope}}", resolved: false }),
        variable("$uuid", { scope: "Dynamic", source: "dynamic", value: "A random UUID" }),
      ],
      environment: "dev",
    });
    expect(lookupVariable(json, "auth.token")?.value).toBe("flat");
    expect(lookupVariable(json, "auth.user.id")?.value).toBe("7");
    expect(lookupVariable(json, "auth.user.tags.1")?.value).toBe("b");
    expect(lookupVariable(json, "auth.user.tags")?.value).toBe('["a","b"]');
    expect(lookupVariable(json, "auth.on")?.value).toBe("true");
    expect(lookupVariable(json, "auth.none")?.value).toBe("null");
    expect(lookupVariable(json, "api.v2.url")?.value).toBe("https://v2");
    expect(lookupVariable(json, "api.url")?.value).toBe("https://v1");
    expect(lookupVariable(json, "auth.user.id")?.name).toBe("auth.user.id");
    // A secret base is masked and checked by Go at send time.
    expect(isResolved(json, "creds.password")).toBe(true);
    expect(describeReferences("{{creds.password}}", json)).toBe("{{creds.password}} = •••••• (Global)");

    expect(unresolvedNames(
      "{{auth.user.email}}{{auth.user.tags.2}}{{auth.user.tags.x}}{{text.a}}{{auth.on.x}}{{broken.a}}{{nope.a}}{{auth..token}}\\{{auth.nope}}",
      json,
    )).toEqual([
      "auth.user.email",
      "auth.user.tags.2",
      "auth.user.tags.x",
      "text.a",
      "auth.on.x",
      "broken.a",
      "nope.a",
      "auth..token",
    ]);
    expect(unresolvedReason(json, "auth.user.email")).toBe('{{auth.user}} has no field "email".');
    expect(unresolvedReason(json, "auth.user.tags.2")).toBe("{{auth.user.tags}} has no element 2.");
    expect(unresolvedReason(json, "auth.user.tags.x")).toBe('{{auth.user.tags}} is a list: "x" is not an index.');
    expect(unresolvedReason(json, "text.a")).toBe("The value of {{text}} is not JSON.");
    expect(unresolvedReason(json, "auth.on.x")).toBe("{{auth.on}} is not an object or a list.");
    expect(unresolvedReason(json, "broken.a")).toBe("The value of {{broken}} uses a variable that is not defined.");
    expect(unresolvedReason(json, "nope.a")).toBe("Not defined in dev or any other scope.");
    // Dynamic values keep their arguments, dots included.
    expect(lookupVariable(json, "$uuid")?.source).toBe("dynamic");
    expect(lookupPath(json, "$datetime YYYY.MM")).toBeUndefined();
  });

  it("describes the references of a text for a title, secrets masked", () => {
    expect(describeReferences("Bearer {{key}} {{host}} {{key}} {{x}}", set)).toBe(
      "{{key}} = •••••• (Global)\n{{host}} = host-value (Global)\n{{x}}: Not defined. No environment is active.",
    );
    expect(describeReferences("plain", set)).toBe("");
    expect(describeReferences("{{host}}", null)).toBe("");
  });

  it("names the scopes a value wins over (ENV-06)", () => {
    const shadowing = variable("base", {
      value: "https://admins.test",
      scope: "Folder: Admins",
      source: "folder",
      overrides: ["Collection: API", "Global"],
    });
    expect(overridesText(shadowing)).toBe("Overrides Collection: API, Global");
    expect(overridesText(variable("host"))).toBe("");
    const withBase = toVariableSet({ variables: [shadowing], environment: "" });
    expect(describeReferences("{{base}}", withBase)).toBe(
      "{{base}} = https://admins.test (Folder: Admins; overrides Collection: API, Global)",
    );
  });

  it("says when a value is this computer's local value (SEC-02)", () => {
    const local = variable("base", { value: "http://localhost", scope: "Environment: dev", source: "environment", local: true });
    expect(scopeText(local)).toBe("Environment: dev · local value");
    expect(scopeText(variable("host"))).toBe("Global");
    const withLocal = toVariableSet({ variables: [local, { ...local, name: "over", overrides: ["Global"] }], environment: "dev" });
    expect(describeReferences("{{base}} {{over}}", withLocal)).toBe(
      "{{base}} = http://localhost (Environment: dev · local value)\n" +
        "{{over}} = http://localhost (Environment: dev · local value; overrides Global)",
    );
  });

  it("never shows a secret's value, even one that reached the UI (SEC-01)", () => {
    const leaky = toVariableSet({ variables: [variable("key", { secret: true, value: "should-not-show" })], environment: "" });
    expect(shownValue(leaky.byName.get("key") as VariableDTO)).toBe("••••••");
    expect(describeReferences("{{key}} {{key.field}} {{key | base64}}", leaky)).not.toContain("should-not-show");
  });

  it("splits a text into text and references", () => {
    expect(splitTemplate("a{{b}}c\\{{d}}{{ e }}").map((p) => `${p.kind}:${p.text}`)).toEqual([
      "text:a",
      "var:{{b}}",
      "text:c\\{{d}}",
      "var:{{ e }}",
    ]);
    expect(splitTemplate("")).toEqual([]);
  });
});
