import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const http = vi.hoisted(() => ({
  PreviewBody: vi.fn<(input: { bodyId: string }) => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/http", () => ({
  HTTPService: http,
}));

import { httpResponse } from "../../test/httpResponse";
import { PREVIEW_CSP, previewSrcdoc } from "../../lib/preview";
import { ResponseView } from "./ResponseView";

const PNG = "\x89PNG\r\n\x1a\n";

const image = (over: Parameters<typeof httpResponse>[0] = {}) =>
  httpResponse({
    bodyId: "body_img",
    contentType: "image/png",
    previewType: "image/png",
    bodyLanguage: "text",
    bodyHex: true,
    body: "00000000  89 50 4e 47 0d 0a 1a 0a",
    bodyPretty: "",
    prettyState: "",
    charset: "",
    bodyShown: 8,
    size: 8,
    bodySize: 8,
    ...over,
  });

const html = (body: string, over: Parameters<typeof httpResponse>[0] = {}) =>
  httpResponse({
    contentType: "text/html; charset=utf-8",
    previewType: "text/html",
    bodyLanguage: "html",
    body,
    bodyPretty: "",
    prettyState: "",
    bodyShown: body.length,
    size: body.length,
    bodySize: body.length,
    ...over,
  });

const flush = () => act(async () => await Promise.resolve());

/** A page that tries every way to reach the network or run a script. */
const HOSTILE = [
  "<!doctype html><html><head>",
  '<link rel="stylesheet" href="https://evil.example/x.css">',
  '<meta http-equiv="refresh" content="0;url=https://evil.example/">',
  '<script src="https://evil.example/x.js"></script><script>parent.postMessage("ran", "*")</script>',
  "</head><body>",
  '<img src="https://evil.example/pixel.png"><iframe src="https://evil.example/"></iframe>',
  '<a href="https://evil.example/">link</a><form action="https://evil.example/"><button>Go</button></form>',
  "</body></html>",
].join("");

describe("ResponsePreview (HTTP-35, PRV-04)", () => {
  let created: Blob[];
  beforeEach(() => {
    http.PreviewBody.mockReset();
    created = [];
    URL.createObjectURL = vi.fn((b: Blob) => {
      created.push(b);
      return `blob:postsonde/${created.length}`;
    });
    URL.revokeObjectURL = vi.fn();
  });
  afterEach(() => vi.restoreAllMocks());

  it("opens an image in Preview, as an <img> of a blob: URL of the body's bytes", async () => {
    http.PreviewBody.mockResolvedValue({ data: btoa(PNG), size: 8 });
    const { container, unmount } = render(<ResponseView response={image()} />);
    await flush();
    expect(http.PreviewBody).toHaveBeenCalledWith({ bodyId: "body_img" });
    const img = screen.getByRole("img", { name: "Preview of the response image" }) as HTMLImageElement;
    expect(img.getAttribute("src")).toBe("blob:postsonde/1");
    expect(created[0]?.type).toBe("image/png");
    expect(new Uint8Array(await created[0]!.arrayBuffer())).toEqual(Uint8Array.from(PNG, (c) => c.charCodeAt(0)));
    expect(screen.getByRole("button", { name: "Preview" }).getAttribute("aria-pressed")).toBe("true");
    expect(container.querySelector("iframe")).toBeNull();
    unmount();
    expect(URL.revokeObjectURL).toHaveBeenCalledWith("blob:postsonde/1");
  });

  it("never puts an SVG into the page: it is an <img> source", async () => {
    const svg = '<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script><circle r="4"/></svg>';
    const response = image({
      bodyId: "",
      contentType: "image/svg+xml",
      previewType: "image/svg+xml",
      bodyHex: false,
      bodyLanguage: "xml",
      body: svg,
      charset: "UTF-8",
    });
    const { container } = render(<ResponseView response={response} />);
    await flush();
    expect(http.PreviewBody).not.toHaveBeenCalled();
    expect(container.innerHTML).not.toContain("<script");
    expect(container.innerHTML).not.toContain('r="4"');
    const img = screen.getByRole("img", { name: "Preview of the response image" });
    expect(img.getAttribute("src")).toBe("blob:postsonde/1");
    expect(created[0]?.type).toBe("image/svg+xml");
    expect(await created[0]!.text()).toBe(svg);
  });

  it("says why when the bytes are not kept or too large", async () => {
    const { unmount } = render(<ResponseView response={image({ bodyId: "" })} />);
    await flush();
    expect(screen.getByText(/this response's bytes are not kept/)).toBeTruthy();
    expect(screen.queryByRole("img")).toBeNull();
    unmount();

    http.PreviewBody.mockRejectedValue({
      kind: "invalid_argument",
      code: "http.preview_too_large",
      message: "The body is 20 MiB, too large to preview (at most 16 MiB).",
      hint: "",
      details: {},
      causes: [],
    });
    render(<ResponseView response={image()} />);
    await flush();
    expect(screen.getByText(/too large to preview/)).toBeTruthy();
    expect(screen.queryByRole("img")).toBeNull();
  });

  it("shows HTML in a frame sandboxed with nothing allowed, its CSP first", async () => {
    render(<ResponseView response={html(HOSTILE)} />);
    // HTML opens as code; Preview is offered.
    expect(document.querySelector("iframe")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Preview" }));
    await flush();
    const frame = screen.getByTitle("Preview of the response HTML") as HTMLIFrameElement;
    expect(frame.tagName).toBe("IFRAME");
    expect(frame.getAttribute("sandbox")).toBe("");
    expect(frame.hasAttribute("src")).toBe(false);
    expect(frame.getAttribute("referrerpolicy")).toBe("no-referrer");
    const srcdoc = frame.getAttribute("srcdoc") ?? "";
    expect(srcdoc).toBe(previewSrcdoc(HOSTILE));
    expect(srcdoc.startsWith(`<meta http-equiv="Content-Security-Policy" content="${PREVIEW_CSP}">`)).toBe(true);
    // Nothing of the body's markup ends up in the app's own document.
    expect(document.querySelector('img[src^="https:"], script[src^="https:"], link[href^="https:"]')).toBeNull();
  });

  it("offers no preview of HTML it does not have whole", async () => {
    render(<ResponseView response={html("<p>start", { bodyClipped: true, bodyShown: 1 << 20, size: 5 << 20, bodySize: 5 << 20 })} />);
    fireEvent.click(screen.getByRole("button", { name: "Preview" }));
    await flush();
    expect(document.querySelector("iframe")).toBeNull();
    expect(screen.getByText(/the HTML is larger than/)).toBeTruthy();
  });

  it("has no Preview for other bodies", () => {
    render(<ResponseView response={httpResponse()} />);
    expect(screen.queryByRole("button", { name: "Preview" })).toBeNull();
  });
});

describe("the preview document's policy", () => {
  it("blocks every load but inline images and styles, and every script", () => {
    const directives = PREVIEW_CSP.split("; ");
    expect(directives[0]).toBe("default-src 'none'");
    expect(directives).toContain("img-src data: blob:");
    expect(directives).toContain("style-src 'unsafe-inline'");
    expect(directives).toContain("form-action 'none'");
    expect(directives).toContain("base-uri 'none'");
    // No source that reaches a host or the app, no script directive to widen default-src.
    expect(PREVIEW_CSP).not.toMatch(/https?:|wss?:|\*|'self'|script-src|connect-src|frame-src|unsafe-eval/);
  });

  it("escapes the policy into its <meta> and keeps the body after it", () => {
    const doc = previewSrcdoc("<p>hi</p>");
    expect(doc.indexOf("Content-Security-Policy")).toBeLessThan(doc.indexOf("<p>hi</p>"));
    expect(doc.endsWith("<p>hi</p>")).toBe(true);
    expect(doc).toContain('<meta http-equiv="x-dns-prefetch-control" content="off">');
  });
});
