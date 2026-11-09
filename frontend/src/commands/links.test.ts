import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@wailsio/runtime", async () => (await import("../test/fakeWails")).fakeRuntime);

import { openInBrowser } from "../lib/browser";
import { fakeRuntime, resetFakeWails, settle } from "../test/fakeWails";
import { confirmQueue } from "./dialogs";
import { openExternalLink } from "./links";
import { toaster } from "./toasts";

let detach: () => void = () => {};
beforeEach(() => {
  resetFakeWails();
  toaster.clear();
  detach = confirmQueue.attach();
});
afterEach(() => detach());

/** Answers the pending confirmation. */
async function answer(ok: boolean) {
  await settle();
  const asked = confirmQueue.current();
  expect(asked).not.toBeNull();
  confirmQueue.settle(asked?.id ?? 0, ok);
  return asked;
}

describe("openExternalLink", () => {
  it("asks with the full URL, then opens it in the system browser", async () => {
    const opened = openExternalLink("https://docs.example/a b");
    const asked = await answer(true);
    expect(asked).toMatchObject({ title: "Open this link in your browser?", message: "https://docs.example/a%20b", confirmLabel: "Open link" });
    expect(await opened).toBe(true);
    expect(fakeRuntime.Browser.OpenURL).toHaveBeenCalledWith("https://docs.example/a%20b");
  });

  it("asks differently for mailto links", async () => {
    const opened = openExternalLink("mailto:team@example.com");
    expect(await answer(true)).toMatchObject({ title: "Write an email?", confirmLabel: "Open mail app" });
    expect(await opened).toBe(true);
    expect(fakeRuntime.Browser.OpenURL).toHaveBeenCalledWith("mailto:team@example.com");
  });

  it("opens nothing when cancelled", async () => {
    const opened = openExternalLink("https://docs.example/");
    await answer(false);
    expect(await opened).toBe(false);
    expect(fakeRuntime.Browser.OpenURL).not.toHaveBeenCalled();
  });

  it("neither asks nor opens for other URLs", async () => {
    for (const url of ["javascript:alert(1)", "file:///etc/passwd", "../a.json", "data:text/html,x"]) {
      expect(await openExternalLink(url)).toBe(false);
    }
    expect(confirmQueue.current()).toBeNull();
    expect(fakeRuntime.Browser.OpenURL).not.toHaveBeenCalled();
  });

  it("shows a toast when the OS refuses", async () => {
    fakeRuntime.Browser.OpenURL.mockRejectedValueOnce(new Error("no browser"));
    const opened = openExternalLink("https://docs.example/");
    await answer(true);
    expect(await opened).toBe(false);
    expect(String(toaster.toasts()[0]?.message)).toContain("Could not open the link");
  });
});

describe("openInBrowser", () => {
  it("refuses anything but http(s) and mailto", async () => {
    await expect(openInBrowser("javascript:alert(1)")).rejects.toThrow("Only http, https and mailto");
    expect(fakeRuntime.Browser.OpenURL).not.toHaveBeenCalled();
  });
});
