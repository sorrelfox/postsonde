import { describe, expect, it } from "vitest";
import { base64UrlDecode, decodeJwt, formatClaimTime } from "./jwt";

// RFC 7515 appendix A.1.
const RFC_TOKEN =
  "eyJ0eXAiOiJKV1QiLA0KICJhbGciOiJIUzI1NiJ9" +
  ".eyJpc3MiOiJqb2UiLA0KICJleHAiOjEzMDA4MTkzODAsDQogImh0dHA6Ly9leGFtcGxlLmNvbS9pc19yb290Ijp0cnVlfQ" +
  ".dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk";

describe("decodeJwt", () => {
  it("decodes the RFC 7515 example", () => {
    const r = decodeJwt(RFC_TOKEN);
    expect(r.ok).toBe(true);
    if (!r.ok) return;
    expect(r.jwt.algorithm).toBe("HS256");
    expect(JSON.parse(r.jwt.header)).toEqual({ typ: "JWT", alg: "HS256" });
    expect(JSON.parse(r.jwt.payload)).toEqual({ iss: "joe", exp: 1300819380, "http://example.com/is_root": true });
    expect(r.jwt.expiresAt).toBe(1300819380);
    expect(r.jwt.issuedAt).toBeNull();
    expect(r.jwt.signatureBytes).toBe(32);
    expect(r.jwt.payload).toContain('\n  "iss": "joe"');
  });

  it("ignores a Bearer prefix and spaces", () => {
    expect(decodeJwt(`  Bearer ${RFC_TOKEN}\n`).ok).toBe(true);
  });

  it("decodes UTF-8 claims", () => {
    const enc = (o: unknown) =>
      btoa(String.fromCharCode(...new TextEncoder().encode(JSON.stringify(o))))
        .replace(/\+/g, "-")
        .replace(/\//g, "_")
        .replace(/=+$/, "");
    const r = decodeJwt(`${enc({ alg: "EdDSA" })}.${enc({ sub: "zoë", iat: 5, nbf: 6 })}.`);
    expect(r.ok && JSON.parse(r.jwt.payload).sub).toBe("zoë");
    expect(r.ok && [r.jwt.algorithm, r.jwt.issuedAt, r.jwt.notBefore, r.jwt.signatureBytes]).toEqual(["EdDSA", 5, 6, 0]);
  });

  it("explains what is wrong", () => {
    expect(decodeJwt("")).toEqual({ ok: false, error: "Paste a token to decode it." });
    expect(decodeJwt("a.b")).toMatchObject({ ok: false, error: expect.stringContaining("three parts") });
    expect(decodeJwt("a.b.c.d.e")).toMatchObject({ ok: false, error: expect.stringContaining("JWE") });
    expect(decodeJwt("a*.b.c")).toMatchObject({ ok: false, error: expect.stringContaining("base64url") });
    expect(decodeJwt("aGVsbG8.e30.")).toMatchObject({ ok: false, error: "The header is not JSON." });
    expect(decodeJwt("e30.WzFd.")).toMatchObject({ ok: false, error: "The payload is not a JSON object." });
  });
});

describe("base64UrlDecode", () => {
  it("takes padded and unpadded input", () => {
    expect(Array.from(base64UrlDecode("_-8") ?? [])).toEqual([255, 239]);
    expect(Array.from(base64UrlDecode("_-8=") ?? [])).toEqual([255, 239]);
    expect(base64UrlDecode("a")).toBeNull();
    expect(base64UrlDecode("a+b")).toBeNull();
  });
});

describe("formatClaimTime", () => {
  it("formats Unix seconds as UTC", () => {
    expect(formatClaimTime(1300819380)).toBe("2011-03-22T18:43:00Z");
  });
});
