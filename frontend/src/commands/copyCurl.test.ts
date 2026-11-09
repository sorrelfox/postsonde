import { beforeEach, describe, expect, it, vi } from "vitest";

const http = vi.hoisted(() => ({
  CurlCommand: vi.fn<(input: { requestId: string; includeSecrets: boolean }) => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../test/fakeWails")).fakeRuntime);
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/http", () => ({
  HTTPService: http,
}));

import type { TreeNode } from "../stores";
import { createStores } from "../stores";
import { fakeRuntime, resetFakeWails, settle } from "../test/fakeWails";
import { canCopyAsCurl, copyAsCurl } from "./copyCurl";
import { toaster } from "./toasts";

const COMMAND = "curl https://api.test/me \\\n  -H 'Authorization: Bearer ••••••'";

beforeEach(() => {
  resetFakeWails();
  toaster.clear();
  http.CurlCommand.mockReset();
  fakeRuntime.Clipboard.SetText.mockReset();
  fakeRuntime.Clipboard.SetText.mockResolvedValue(undefined);
});

describe("copyAsCurl", () => {
  it("copies the masked command and offers to copy it with secrets", async () => {
    const stores = createStores();
    http.CurlCommand.mockResolvedValueOnce({ command: COMMAND, masked: true, unresolved: [] });
    await copyAsCurl(stores, "req_1");
    expect(http.CurlCommand).toHaveBeenCalledWith({ requestId: "req_1", includeSecrets: false });
    expect(fakeRuntime.Clipboard.SetText).toHaveBeenCalledWith(COMMAND);
    const [toast] = toaster.toasts();
    expect(toast?.message).toBe("Copied as cURL, with secrets masked.");
    expect(toast?.action?.label).toBe("Copy with secrets");

    http.CurlCommand.mockResolvedValueOnce({ command: "curl https://api.test/me -H 'Authorization: Bearer t'", masked: false, unresolved: [] });
    toaster.runAction(toast!.id);
    await settle();
    expect(http.CurlCommand).toHaveBeenLastCalledWith({ requestId: "req_1", includeSecrets: true });
    expect(fakeRuntime.Clipboard.SetText).toHaveBeenLastCalledWith("curl https://api.test/me -H 'Authorization: Bearer t'");
    const all = toaster.toasts();
    const last = all[all.length - 1];
    expect(last?.message).toBe("Copied as cURL.");
    expect(last?.action).toBeUndefined();
  });

  it("names undefined variables", async () => {
    http.CurlCommand.mockResolvedValueOnce({ command: "curl x", masked: false, unresolved: ["host", "token"] });
    await copyAsCurl(createStores(), "req_1");
    expect(toaster.toasts()[0]?.message).toBe("Copied as cURL. {{host}}, {{token}} are not defined and left as written.");
  });

  it("falls back to the browser clipboard", async () => {
    const writeText = vi.fn(() => Promise.resolve());
    vi.stubGlobal("navigator", { clipboard: { writeText } });
    fakeRuntime.Clipboard.SetText.mockRejectedValueOnce(new Error("no runtime"));
    http.CurlCommand.mockResolvedValueOnce({ command: "curl x", masked: false, unresolved: [] });
    await copyAsCurl(createStores(), "req_1");
    expect(writeText).toHaveBeenCalledWith("curl x");
    vi.unstubAllGlobals();
  });

  it("says why it could not copy", async () => {
    const refused = Object.assign(new Error("refused"), {
      cause: { kind: "invalid_argument", code: "request.url_missing", message: "The request has no URL.", hint: "", details: {}, causes: [] },
    });
    http.CurlCommand.mockRejectedValueOnce(refused);
    await copyAsCurl(createStores(), "req_1");
    expect(fakeRuntime.Clipboard.SetText).not.toHaveBeenCalled();
    expect(toaster.toasts()[0]?.message).toBe("Could not copy as cURL: The request has no URL.");
  });
});

describe("canCopyAsCurl", () => {
  const request = (fileKind: string): TreeNode => ({ id: "r", parentId: null, name: "r", path: "r.json", fileKind, readOnly: "", kind: "request", method: "GET" });
  it("takes HTTP, GraphQL and SSE requests", () => {
    expect(canCopyAsCurl(request("http"))).toBe(true);
    expect(canCopyAsCurl(request("graphql"))).toBe(true);
    expect(canCopyAsCurl(request("sse"))).toBe(true);
    expect(canCopyAsCurl(request("websocket"))).toBe(false);
    expect(canCopyAsCurl(request("grpc"))).toBe(false);
    expect(canCopyAsCurl({ id: "f", parentId: null, name: "f", path: "f", fileKind: "", readOnly: "", kind: "folder", childIds: [] })).toBe(false);
    expect(canCopyAsCurl(undefined)).toBe(false);
  });
});
