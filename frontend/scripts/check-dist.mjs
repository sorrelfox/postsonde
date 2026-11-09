#!/usr/bin/env node
// check-dist.mjs: verifies the built frontend loads nothing from the network
// (PRV-03, docs/privacy.md).
//
//   node scripts/check-dist.mjs [dist]      (npm run check:dist)
//
// Fails (exit 1) when
//   1. any text asset under dist/ (HTML, CSS, JS, SVG, JSON, ...) contains a
//      remote URL: http://, https://, ws://, wss:// or ftp://, or a
//      protocol-relative //host.tld right after a quote, backtick or url(;
//      unless that exact URL is in ALLOWED below;
//   2. dist/index.html doesn't carry the Content-Security-Policy <meta> built
//      from csp.json.
//
// Plain Node, no dependencies: it runs in CI right after `npm run build`
// (scripts/check-frontend.sh, scripts/build-frontend.sh).
// A text scan, not a parser: it catches URLs written out in the bundle, not
// ones assembled at run time ("ht" + "tp://"). The CSP is the runtime guard
// for those.

import { readdirSync, readFileSync } from "node:fs";
import { extname, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";

/**
 * URLs that may appear in the bundle, matched exactly. Each one is a string
 * constant that is never fetched. Keep this list short; every entry needs a
 * reason. A new remote URL in the bundle is a bug to fix, not to allowlist,
 * unless it is provably inert like these.
 */
export const ALLOWED = [
  // XML namespace URIs: identifiers compared as strings by react-dom and
  // CodeMirror (createElementNS, xmlns attributes), never requested.
  { url: "http://www.w3.org/2000/svg", reason: "SVG namespace URI" },
  { url: "http://www.w3.org/1999/xhtml", reason: "XHTML namespace URI" },
  { url: "http://www.w3.org/1999/xlink", reason: "XLink namespace URI" },
  { url: "http://www.w3.org/1998/Math/MathML", reason: "MathML namespace URI" },
  { url: "http://www.w3.org/XML/1998/namespace", reason: "XML namespace URI" },
  // react-dom's production build puts this link into minified error
  // messages; it is only text in a thrown Error.
  {
    url: "https://reactjs.org/docs/error-decoder.html?invariant=",
    reason: "react-dom error message text",
  },
  // @wailsio/runtime prints this in a console.warn when the UI is opened in
  // a plain browser instead of the app.
  {
    url: "https://v3.wails.io/learn/build/#using-a-browser-for-development",
    reason: "@wailsio/runtime console message text",
  },
];

/** Extensions scanned as text. Fonts and images are binary and skipped. */
const TEXT_EXTENSIONS = new Set([
  ".html",
  ".htm",
  ".js",
  ".mjs",
  ".cjs",
  ".css",
  ".svg",
  ".json",
  ".webmanifest",
  ".xml",
  ".txt",
  ".map",
]);

// Absolute URLs with a network scheme. The URL ends at whitespace, a quote,
// a backtick, parentheses, angle brackets or a backslash.
const ABSOLUTE_URL = /\b(?:https?|wss?|ftp):\/\/[^\s"'`()<>\\]+/gi;
// Protocol-relative URLs (//cdn.example.com/x.js) where they would be used
// as a URL: right after a quote, backtick or url(. Requires a dotted host so
// comment markers and regular expressions don't match.
const PROTOCOL_RELATIVE_URL =
  /(?<=["'`]|url\(\s*)\/\/[a-z0-9-]+(?:\.[a-z0-9-]+)+(?::\d+)?(?:[/?#][^\s"'`()<>\\]*)?/gi;

/** Returns every remote URL in text with its 1-based line and column. */
export function findRemoteUrls(text, allowed = ALLOWED) {
  const allow = new Set(allowed.map((a) => a.url));
  const found = [];
  for (const re of [ABSOLUTE_URL, PROTOCOL_RELATIVE_URL]) {
    for (const m of text.matchAll(re)) {
      if (allow.has(m[0])) continue;
      const before = text.slice(0, m.index);
      const line = before.split("\n").length;
      const column = m.index - before.lastIndexOf("\n");
      found.push({ url: m[0], line, column });
    }
  }
  return found.sort((a, b) => a.line - b.line || a.column - b.column);
}

function* walk(dir) {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) yield* walk(path);
    else if (entry.isFile()) yield path;
  }
}

/** The CSP string csp.json defines, directives joined with "; ". */
export function expectedPolicy(cspFile) {
  const { directives } = JSON.parse(readFileSync(cspFile, "utf8"));
  if (!Array.isArray(directives) || directives.length === 0) {
    throw new Error(`${cspFile}: "directives" must be a non-empty array`);
  }
  return directives.join("; ");
}

/**
 * Checks a built dist directory. Returns a list of problems (strings); empty
 * means the build passes.
 */
export function checkDist(distDir, cspFile) {
  const problems = [];
  let scanned = 0;
  for (const file of walk(distDir)) {
    if (!TEXT_EXTENSIONS.has(extname(file).toLowerCase())) continue;
    scanned++;
    const text = readFileSync(file, "utf8");
    for (const { url, line, column } of findRemoteUrls(text)) {
      problems.push(`${relative(distDir, file)}:${line}:${column}: remote URL ${url}`);
    }
  }
  if (scanned === 0) {
    problems.push(`${distDir}: no HTML/CSS/JS files; run npm run build first`);
    return problems;
  }

  const policy = expectedPolicy(cspFile);
  let index = "";
  try {
    index = readFileSync(join(distDir, "index.html"), "utf8");
  } catch {
    problems.push("index.html: missing");
    return problems;
  }
  const meta = [...index.matchAll(/<meta\b[^>]*>/gi)].find((m) =>
    /http-equiv\s*=\s*["']?Content-Security-Policy/i.test(m[0]),
  );
  const content = meta?.[0]
    .match(/content\s*=\s*"([^"]*)"/i)?.[1]
    .replace(/&#39;|&apos;/g, "'")
    .replace(/&amp;/g, "&");
  if (content === undefined) {
    problems.push("index.html: no Content-Security-Policy <meta> tag (vite.config.ts adds it)");
  } else if (content !== policy) {
    problems.push(
      `index.html: Content-Security-Policy <meta> is\n    ${content}\n  but csp.json defines\n    ${policy}`,
    );
  }
  return problems;
}

function main() {
  const root = resolve(fileURLToPath(import.meta.url), "../..");
  const distDir = resolve(process.argv[2] ?? join(root, "dist"));
  const problems = checkDist(distDir, join(root, "csp.json"));
  if (problems.length > 0) {
    console.error(`check-dist: ${distDir} fails the no-remote-resources check (docs/privacy.md):`);
    for (const p of problems) console.error(`  ${p}`);
    console.error(
      "Bundle the resource instead. Only a provably inert string (never fetched) may be added to ALLOWED in scripts/check-dist.mjs.",
    );
    process.exit(1);
  }
  console.log(`check-dist: ok (${relative(process.cwd(), distDir) || "."}: no remote URLs, CSP meta present)`);
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main();
}
