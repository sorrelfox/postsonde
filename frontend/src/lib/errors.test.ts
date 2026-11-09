import { afterEach, describe, expect, it, vi } from "vitest";
import { Call, CancelError } from "@wailsio/runtime";
// Response bodies of failed calls, written by the Go test that runs errors
// through Wails' binding code (internal/app/wails_errors_test.go).
import waitInvalidBody from "../../../internal/app/testdata/wails/wait_invalid_argument.golden?raw";
import unknownErrorBody from "../../../internal/app/testdata/wails/unknown_error.golden?raw";
import {
  ERROR_KINDS,
  errorTitle,
  formatError,
  isAppError,
  isCancelled,
  toAppError,
  type AppError,
} from "./errors";

// ErrorDTO JSON of representative Go errors (internal/app/apperr tests).
const dtoGoldens = import.meta.glob<string>("../../../internal/app/apperr/testdata/error/*.golden", {
  query: "?raw",
  import: "default",
  eager: true,
});

const WAIT_METHOD_ID = 711690568; // SystemService.Wait in the generated bindings
const CANCEL_CALL_OBJECT = 10; // objectNames.CancelCall in @wailsio/runtime

/** Answers runtime calls the way Wails' HTTP transport answers a failed call. */
function failCallsWith(body: string): void {
  vi.stubGlobal(
    "fetch",
    vi.fn(
      async () =>
        new Response(body, { status: 422, headers: { "Content-Type": "application/json" } }),
    ),
  );
}

/** An Error with `cause` set, as the Wails runtime builds its rejections. */
function withCause<E extends Error>(err: E, cause: unknown): E {
  return Object.assign(err, { cause });
}

async function rejectionOf(promise: Promise<unknown>): Promise<unknown> {
  return promise.then(
    () => {
      throw new Error("the call resolved; want a rejection");
    },
    (reason: unknown) => reason,
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("toAppError on rejections from the real Wails runtime", () => {
  it("reads the ErrorDTO from the RuntimeError's cause", async () => {
    failCallsWith(waitInvalidBody);
    const reason = await rejectionOf(Call.ByID(WAIT_METHOD_ID, -1));

    expect(reason).toBeInstanceOf(Call.RuntimeError);
    expect(toAppError(reason)).toEqual({
      kind: "invalid_argument",
      code: "system.wait_out_of_range",
      message: "The wait time must be between 0 and 60000 ms, not -1 ms.",
      hint: "Enter a value from 0 to 60000 ms.",
      details: { field: "ms", min: "0", max: "60000" },
      causes: [],
    });
    expect(isCancelled(reason)).toBe(false);
    expect(formatError(reason)).toBe(
      "The wait time must be between 0 and 60000 ms, not -1 ms. Enter a value from 0 to 60000 ms.",
    );
  });

  it("keeps the raw text of an unknown Go error out of the message", async () => {
    failCallsWith(unknownErrorBody);
    const reason = await rejectionOf(Call.ByID(WAIT_METHOD_ID, 0));

    // Wails puts err.Error() in the message; the UI must not show it as is.
    expect((reason as Error).message).toBe("render template: unexpected nil map");
    expect(toAppError(reason)).toEqual({
      kind: "internal",
      code: "",
      message: "Something went wrong inside PostSonde.",
      hint: "",
      details: {},
      causes: ["render template", "unexpected nil map"],
    });
  });

  it("maps a call cancelled with .cancel() to cancelled", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((_url: unknown, init?: RequestInit) => {
        const { object } = JSON.parse(String(init?.body)) as { object: number };
        if (object === CANCEL_CALL_OBJECT) {
          return Promise.resolve(
            new Response("{}", { status: 200, headers: { "Content-Type": "application/json" } }),
          );
        }
        return new Promise<Response>(() => {}); // the call itself never answers
      }),
    );
    const call = Call.ByID(WAIT_METHOD_ID, 60000);
    void call.cancel();
    const reason = await rejectionOf(call);

    expect(reason).toBeInstanceOf(CancelError);
    expect(isCancelled(reason)).toBe(true);
    expect(toAppError(reason)).toEqual({
      kind: "cancelled",
      code: "",
      message: "The operation was cancelled.",
      hint: "",
      details: {},
      causes: [],
    });
  });
});

describe("toAppError on Go ErrorDTO goldens", () => {
  const entries = Object.entries(dtoGoldens);

  it("finds the golden files", () => {
    expect(entries.length).toBeGreaterThanOrEqual(6);
  });

  it.each(entries)("%s has the AppError shape and round-trips", (_path, json) => {
    const dto: unknown = JSON.parse(json);
    expect(isAppError(dto)).toBe(true);
    const reason = withCause(new Call.RuntimeError("raw Go message"), dto);
    expect(toAppError(reason)).toEqual(dto);
  });
});

describe("toAppError on other reasons", () => {
  const dto: AppError = {
    kind: "not_found",
    code: "workspace.no_request",
    message: "The request does not exist.",
    hint: "",
    details: {},
    causes: [],
  };

  it("uses the first ErrorDTO when a method returned several errors", () => {
    const reason = withCause(new Error("a\nb"), [dto, { ...dto, kind: "io" }]);
    expect(toAppError(reason)).toEqual(dto);
  });

  it("fills in missing fields and treats unknown kinds as internal", () => {
    const reason = withCause(new Error("x"), { kind: "quota_exceeded", message: "Too many requests." });
    expect(toAppError(reason)).toEqual({
      kind: "internal",
      code: "",
      message: "Too many requests.",
      hint: "",
      details: {},
      causes: [],
    });
  });

  it("returns an AppError unchanged", () => {
    expect(toAppError(dto)).toBe(dto);
  });

  it("reads a bare ErrorDTO leniently, as from an operation's done event", () => {
    expect(toAppError({ kind: "network", message: "Refused.", code: "netx.connection_refused", causes: null })).toEqual({
      kind: "network",
      code: "netx.connection_refused",
      message: "Refused.",
      hint: "",
      details: {},
      causes: [],
    });
  });

  const generic = "Something went wrong inside PostSonde.";
  it.each([
    ["a plain Error", new Error("Failed to fetch"), "Failed to fetch"],
    ["a TypeError without a DTO", new TypeError("Wait expects 2 arguments, got 1"), "Wait expects 2 arguments, got 1"],
    ["an Error without a message", new Error(""), generic],
    ["a string", "boom", "boom"],
    ["an empty string", "", generic],
    ["undefined", undefined, generic],
    ["an unrelated object", { status: 500 }, generic],
  ])("maps %s to internal", (_name, reason, message) => {
    expect(toAppError(reason)).toEqual({
      kind: "internal",
      code: "",
      message,
      hint: "",
      details: {},
      causes: [],
    });
  });

  it("treats a cancelled ErrorDTO from Go as cancelled", () => {
    const reason = withCause(new Error("x"), { ...dto, kind: "cancelled", message: "Stopped." });
    expect(isCancelled(reason)).toBe(true);
  });
});

describe("isAppError", () => {
  it.each([
    ["null", null],
    ["missing causes", { kind: "io", code: "", message: "m", hint: "", details: {} }],
    ["unknown kind", { kind: "nope", code: "", message: "m", hint: "", details: {}, causes: [] }],
    ["non-string detail", { kind: "io", code: "", message: "m", hint: "", details: { n: 1 }, causes: [] }],
  ])("rejects %s", (_name, v) => {
    expect(isAppError(v)).toBe(false);
  });
});

describe("display helpers", () => {
  it("has a distinct title for every kind", () => {
    const titles = ERROR_KINDS.map(errorTitle);
    expect(titles.every((t) => t.length > 0)).toBe(true);
    expect(new Set(titles).size).toBe(ERROR_KINDS.length);
    expect(errorTitle("invalid_argument")).toBe("Invalid input");
  });

  it("formats the message alone when there is no hint", () => {
    expect(formatError(new Error("Failed to fetch"))).toBe("Failed to fetch");
    expect(formatError(undefined)).toBe("Something went wrong inside PostSonde.");
  });
});
