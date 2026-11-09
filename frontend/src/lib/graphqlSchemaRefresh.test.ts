import { describe, expect, it } from "vitest";
import { readGraphqlPayload } from "./graphqlResponse";
import { responseSignalsSchemaChange, schemaRefreshOf, schemaRefreshPatch } from "./graphqlSchemaRefresh";
import type { RequestSettingsDTO } from "./collection";

const settings = (over: Partial<RequestSettingsDTO> = {}): RequestSettingsDTO =>
  ({ timeoutMs: null, extra: "", ...over }) as RequestSettingsDTO;

describe("schemaRefreshOf", () => {
  it("is off without the member and reads both triggers", () => {
    expect(schemaRefreshOf(settings())).toEqual({ onSend: false, intervalMinutes: 0 });
    expect(schemaRefreshOf(settings({ graphqlSchemaRefresh: null }))).toEqual({ onSend: false, intervalMinutes: 0 });
    expect(schemaRefreshOf(settings({ graphqlSchemaRefresh: { onSend: true, intervalMinutes: 15, extra: "" } }))).toEqual({
      onSend: true,
      intervalMinutes: 15,
    });
  });

  it("reads a broken interval as off", () => {
    for (const intervalMinutes of [-5, 1.5, Number.NaN]) {
      expect(schemaRefreshOf(settings({ graphqlSchemaRefresh: { onSend: false, intervalMinutes, extra: "" } })).intervalMinutes).toBe(0);
    }
  });
});

describe("schemaRefreshPatch", () => {
  it("stores nothing when both are off, and keeps unknown members", () => {
    expect(schemaRefreshPatch(null, { onSend: false, intervalMinutes: 0 })).toBeNull();
    expect(schemaRefreshPatch(null, { onSend: true, intervalMinutes: 0 })).toEqual({ onSend: true, intervalMinutes: 0, extra: "" });
    const kept = { onSend: true, intervalMinutes: 5, extra: '{"x":1}' };
    expect(schemaRefreshPatch(kept, { onSend: false, intervalMinutes: 0 })).toEqual({ onSend: false, intervalMinutes: 0, extra: '{"x":1}' });
  });
});

describe("responseSignalsSchemaChange", () => {
  const signals = (body: string) => responseSignalsSchemaChange(readGraphqlPayload(body));

  it("takes validation errors without data as the signal", () => {
    expect(signals('{"errors":[{"message":"Cannot query field \\"email\\" on type \\"User\\".","locations":[{"line":3,"column":5}]}]}')).toBe(true);
    expect(signals('{"errors":[{"message":"Unknown argument \\"first\\" on field \\"Query.users\\"."}]}')).toBe(true);
    expect(signals('{"errors":[{"message":"Field \\"user\\" argument \\"id\\" of type \\"ID!\\" is required, but it was not provided."}]}')).toBe(true);
    expect(signals('{"errors":[{"message":"Value \\"OLD\\" does not exist in \\"Sort\\" enum."}]}')).toBe(true);
    expect(signals('{"errors":[{"message":"Validation error (FieldUndefined@[user/email]) : Field \'email\' in type \'User\' is undefined"}]}')).toBe(true);
    expect(signals('{"errors":[{"message":"anything","extensions":{"code":"GRAPHQL_VALIDATION_FAILED"}}]}')).toBe(true);
  });

  it("ignores execution errors, partial data and other responses", () => {
    expect(signals('{"data":null,"errors":[{"message":"Cannot query field \\"x\\" on type \\"Y\\"."}]}')).toBe(false);
    expect(signals('{"data":{"user":null},"errors":[{"message":"Not found","path":["user"]}]}')).toBe(false);
    expect(signals('{"errors":[{"message":"Unauthorized","extensions":{"code":"UNAUTHENTICATED"}}]}')).toBe(false);
    expect(signals('{"errors":[{"message":"Syntax Error: Expected Name, found <EOF>."}]}')).toBe(false);
    expect(signals('{"errors":[]}')).toBe(false);
    expect(signals('{"data":{"a":1}}')).toBe(false);
    expect(responseSignalsSchemaChange(null)).toBe(false);
  });
});
