// @vitest-environment node
// Tests for check-dist.mjs: it must pass a clean build and fail as
// soon as a remote URL or a missing CSP <meta> shows up in dist/.

import { spawnSync } from "node:child_process";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { ALLOWED, checkDist, expectedPolicy, findRemoteUrls } from "./check-dist.mjs";

const here = dirname(fileURLToPath(import.meta.url));
const cspFile = join(here, "..", "csp.json");
const script = join(here, "check-dist.mjs");
const policy = expectedPolicy(cspFile);

describe("findRemoteUrls", () => {
  it.each([
    ["http", `fetch("http://example.com/x")`, "http://example.com/x"],
    ["https", "const u=`https://cdn.example.com/lib.js`;", "https://cdn.example.com/lib.js"],
    ["websocket", `new WebSocket('wss://example.com/s')`, "wss://example.com/s"],
    ["CSS import", `@import url(https://fonts.googleapis.com/css2?family=Inter);`, "https://fonts.googleapis.com/css2?family=Inter"],
    ["protocol-relative src", `<script src="//cdn.example.com/a.js"></script>`, "//cdn.example.com/a.js"],
    ["protocol-relative url()", `a{background:url(//img.example.com/b.png)}`, "//img.example.com/b.png"],
  ])("flags %s", (_, text, url) => {
    expect(findRemoteUrls(text).map((f) => f.url)).toEqual([url]);
  });

  it.each([
    ["allowlisted namespace", `createElementNS("http://www.w3.org/2000/svg","svg")`],
    ["relative asset", `<script type="module" src="/assets/index-abc.js"></script>`],
    ["line comment", `a=1;// see the docs\nb=2`],
    ["regular expression", `const re=/^\\/\\/[a-z]+/;`],
    ["data URL", `url('data:image/svg+xml,<svg xmlns="http://www.w3.org/2000/svg"/>')`],
  ])("ignores %s", (_, text) => {
    expect(findRemoteUrls(text)).toEqual([]);
  });

  it("matches the allowlist exactly, not by prefix", () => {
    expect(findRemoteUrls(`"http://www.w3.org/2000/svg/../evil.js"`)).toHaveLength(1);
  });

  it("reports line and column", () => {
    expect(findRemoteUrls(`one\ntwo "https://example.com"`)).toEqual([
      { url: "https://example.com", line: 2, column: 6 },
    ]);
  });

  it("gives every allowlist entry a reason", () => {
    for (const a of ALLOWED) expect(a.reason, a.url).not.toBe("");
  });
});

describe("checkDist", () => {
  let dist;

  beforeEach(() => {
    dist = mkdtempSync(join(tmpdir(), "check-dist-"));
    mkdirSync(join(dist, "assets"));
    writeFileSync(
      join(dist, "index.html"),
      `<!DOCTYPE html><html><head><meta http-equiv="Content-Security-Policy" content="${policy}"><meta charset="UTF-8" />` +
        `<script type="module" crossorigin src="/assets/index.js"></script></head><body></body></html>`,
    );
    writeFileSync(join(dist, "assets", "index.js"), `document.createElementNS("http://www.w3.org/2000/svg","svg");`);
    writeFileSync(join(dist, "assets", "font.woff2"), Buffer.from([0x77, 0x4f, 0x46, 0x32]));
  });

  afterEach(() => {
    rmSync(dist, { recursive: true, force: true });
  });

  const run = () => spawnSync(process.execPath, [script, dist], { encoding: "utf8" });

  it("passes a clean build", () => {
    expect(checkDist(dist, cspFile)).toEqual([]);
    const r = run();
    expect(r.status, r.stderr).toBe(0);
  });

  it("fails when a remote URL is added", () => {
    writeFileSync(join(dist, "assets", "style.css"), `@font-face{src:url(https://fonts.gstatic.com/x.woff2)}`);
    expect(checkDist(dist, cspFile)).toEqual([
      "assets/style.css:1:20: remote URL https://fonts.gstatic.com/x.woff2",
    ]);
    const r = run();
    expect(r.status).toBe(1);
    expect(r.stderr).toContain("https://fonts.gstatic.com/x.woff2");
  });

  it("fails when the CSP meta tag is missing", () => {
    writeFileSync(join(dist, "index.html"), `<!DOCTYPE html><html><head></head><body></body></html>`);
    expect(checkDist(dist, cspFile)).toEqual([
      "index.html: no Content-Security-Policy <meta> tag (vite.config.ts adds it)",
    ]);
    expect(run().status).toBe(1);
  });

  it("fails when the CSP meta tag differs from csp.json", () => {
    writeFileSync(
      join(dist, "index.html"),
      `<html><head><meta http-equiv="Content-Security-Policy" content="default-src *"></head></html>`,
    );
    expect(checkDist(dist, cspFile)[0]).toContain("but csp.json defines");
  });

  it("fails on an empty dist", () => {
    const empty = mkdtempSync(join(tmpdir(), "check-dist-empty-"));
    try {
      expect(checkDist(empty, cspFile)[0]).toContain("run npm run build first");
    } finally {
      rmSync(empty, { recursive: true, force: true });
    }
  });
});

describe("csp.json", () => {
  it("is strict", () => {
    expect(policy.startsWith("default-src 'self'; ")).toBe(true);
    expect(policy).not.toMatch(/unsafe-eval|https?:|\*/);
    expect(policy).toMatch(/script-src 'self'(;|$)/);
    expect(policy).toContain("object-src 'none'");
  });

  it("allows frames only from about: (the sandboxed srcdoc preview, HTTP-35)", () => {
    // Any navigation of the preview frame to the network, the app or a
    // blob:/data: document is refused: only about:srcdoc and about:blank.
    const frameSrc = policy.split("; ").filter((d) => d.startsWith("frame-src ") || d.startsWith("child-src "));
    expect(frameSrc).toEqual(["frame-src about:"]);
  });
});
