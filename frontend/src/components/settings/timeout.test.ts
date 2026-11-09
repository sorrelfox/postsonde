import { describe, expect, it } from "vitest";
import { formatTimeoutSeconds, parseMaxResponseBody, parseTimeoutSeconds } from "./timeout";

describe("default timeout field", () => {
  it("shows seconds, and nothing for no timeout", () => {
    expect(formatTimeoutSeconds(0)).toBe("");
    expect(formatTimeoutSeconds(30000)).toBe("30");
    expect(formatTimeoutSeconds(2500)).toBe("2.5");
  });

  it("reads seconds into whole milliseconds", () => {
    expect(parseTimeoutSeconds("")).toEqual({ ok: true, ms: 0 });
    expect(parseTimeoutSeconds("  ")).toEqual({ ok: true, ms: 0 });
    expect(parseTimeoutSeconds("30")).toEqual({ ok: true, ms: 30000 });
    expect(parseTimeoutSeconds(" 1.5 ")).toEqual({ ok: true, ms: 1500 });
    expect(parseTimeoutSeconds("1.0004")).toEqual({ ok: true, ms: 1000 });
    expect(parseTimeoutSeconds("86400")).toEqual({ ok: true, ms: 86400000 });
  });

  it("rejects values the settings file would not take", () => {
    for (const text of ["-5", "0", "0.5"]) {
      expect(parseTimeoutSeconds(text)).toEqual({
        ok: false,
        error: "Enter at least 1 second, or leave it empty for no timeout.",
      });
    }
    expect(parseTimeoutSeconds("86401")).toMatchObject({ ok: false, error: "Enter at most 86400 seconds (24 hours)." });
    for (const text of ["abc", "1e3", "30s", "0x10", "1,5", "."]) {
      expect(parseTimeoutSeconds(text)).toMatchObject({ ok: false, error: expect.stringContaining("number of seconds") });
    }
  });
});

describe("response body limit field (HTTP-37)", () => {
  it("reads whole MiB, with or without the unit", () => {
    expect(parseMaxResponseBody("512")).toEqual({ ok: true, mb: 512 });
    expect(parseMaxResponseBody(" 1024 MiB ")).toEqual({ ok: true, mb: 1024 });
    expect(parseMaxResponseBody("2048mb")).toEqual({ ok: true, mb: 2048 });
  });

  it("rejects values the settings file would not take", () => {
    for (const text of ["", "0", "16385", "1.5", "-2", "big"]) {
      expect(parseMaxResponseBody(text)).toEqual({ ok: false, error: "Enter a whole number of MiB from 1 to 16384." });
    }
  });
});
