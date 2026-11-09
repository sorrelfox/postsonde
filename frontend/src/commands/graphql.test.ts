import { beforeEach, describe, expect, it, vi } from "vitest";

/** Go's answers, set per test. Calls return a promise with cancel(), like Wails. */
const format = vi.hoisted(() => ({
  PrettifyGraphQL: vi.fn<(text: string) => unknown>(),
  MinifyGraphQL: vi.fn<(text: string) => unknown>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../test/fakeWails")).fakeRuntime);
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/format", () => ({
  FormatService: format,
}));

import type { GraphQLErrorDTO } from "../lib/format";
import { FakeCancelError } from "../test/fakeWails";
import { formatGraphqlQuery, type GraphqlQueryText } from "./graphql";

function cancellable<T>(value: T) {
  return Object.assign(Promise.resolve(value), { cancel: vi.fn() });
}

/** A query held in a variable, like an editor's draft. */
function holder(initial: string | undefined): GraphqlQueryText & { text: string | undefined; set: ReturnType<typeof vi.fn> } {
  const h = {
    text: initial,
    get: () => h.text,
    set: vi.fn((text: string) => {
      h.text = text;
    }),
  };
  return h;
}

const syntaxError: GraphQLErrorDTO = {
  offset: 5,
  endOffset: 6,
  byteOffset: 5,
  line: 1,
  column: 6,
  message: 'expected value, found ")"',
};

describe("formatGraphqlQuery", () => {
  beforeEach(() => {
    format.PrettifyGraphQL.mockReset();
    format.MinifyGraphQL.mockReset();
  });

  it("prettifies the query in Go and saves the result", async () => {
    format.PrettifyGraphQL.mockImplementation(() => cancellable({ valid: true, text: "{\n  a # c\n}", error: null, errorCount: 0 }));
    const q = holder("{ a # c\n}");
    await expect(formatGraphqlQuery(q, "prettify")).resolves.toEqual({ status: "formatted", text: "{\n  a # c\n}" });
    expect(format.PrettifyGraphQL).toHaveBeenCalledWith("{ a # c\n}");
    expect(q.text).toBe("{\n  a # c\n}");
  });

  it("minifies the query", async () => {
    format.MinifyGraphQL.mockImplementation(() => cancellable({ valid: true, text: "{a}", error: null, errorCount: 0 }));
    const q = holder("{\n  a\n}");
    await expect(formatGraphqlQuery(q, "minify")).resolves.toEqual({ status: "formatted", text: "{a}" });
    expect(format.PrettifyGraphQL).not.toHaveBeenCalled();
    expect(q.text).toBe("{a}");
  });

  it("does not save a result that is already the text", async () => {
    format.MinifyGraphQL.mockImplementation(() => cancellable({ valid: true, text: "{a}", error: null, errorCount: 0 }));
    const q = holder("{a}");
    await expect(formatGraphqlQuery(q, "minify")).resolves.toEqual({ status: "formatted", text: "{a}" });
    expect(q.set).not.toHaveBeenCalled();
  });

  it("leaves a query with syntax errors alone and returns the first error", async () => {
    format.PrettifyGraphQL.mockImplementation(() =>
      cancellable({ valid: false, text: "{ a(x: ) }", error: syntaxError, errorCount: 2 }),
    );
    const q = holder("{ a(x: ) }");
    await expect(formatGraphqlQuery(q, "prettify")).resolves.toEqual({ status: "invalid", error: syntaxError, count: 2 });
    expect(q.set).not.toHaveBeenCalled();
  });

  it("skips a blank or missing query without calling Go", async () => {
    await expect(formatGraphqlQuery(holder("  \n"), "prettify")).resolves.toEqual({ status: "skipped" });
    await expect(formatGraphqlQuery(holder(undefined), "minify")).resolves.toEqual({ status: "skipped" });
    expect(format.PrettifyGraphQL).not.toHaveBeenCalled();
    expect(format.MinifyGraphQL).not.toHaveBeenCalled();
  });

  it("drops the result when the query changed while Go worked", async () => {
    let answer: (v: unknown) => void = () => {};
    format.PrettifyGraphQL.mockImplementation(() => Object.assign(new Promise((r) => (answer = r)), { cancel: vi.fn() }));
    const q = holder("{a}");
    const done = formatGraphqlQuery(q, "prettify");
    q.text = "{ab}";
    answer({ valid: true, text: "{\n  a\n}", error: null, errorCount: 0 });
    await expect(done).resolves.toEqual({ status: "skipped" });
    expect(q.set).not.toHaveBeenCalled();
    expect(q.text).toBe("{ab}");
  });

  it("reports a failed call, and skips a cancelled one", async () => {
    format.PrettifyGraphQL.mockImplementation(() => Promise.reject(new Error("boom")));
    const failed = await formatGraphqlQuery(holder("{a}"), "prettify");
    expect(failed.status).toBe("failed");

    format.MinifyGraphQL.mockImplementation(() => Promise.reject(new FakeCancelError()));
    await expect(formatGraphqlQuery(holder("{a}"), "minify")).resolves.toEqual({ status: "skipped" });
  });
});
