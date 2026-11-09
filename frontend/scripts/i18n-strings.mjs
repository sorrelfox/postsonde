#!/usr/bin/env node
// i18n-strings.mjs: finds UI text written into the frontend source instead
// of going through the message catalogue's t() (UI-10;
// docs/frontend.md#localization).
//
//   node scripts/i18n-strings.mjs            report per area, check the allowlist
//   node scripts/i18n-strings.mjs --files    the same, with every file
//   node scripts/i18n-strings.mjs --list     every hard-coded string found
//   node scripts/i18n-strings.mjs --update   write the current counts to the allowlist
//
// scripts/i18n-strings.test.mjs runs the check with `npm test`, so
// scripts/check-frontend.sh (and CI) fail when it fails.
//
// It parses every .ts/.tsx file under src/ (not tests, src/test/ or
// src/i18n/) with the TypeScript compiler API and reports:
//
//   jsx-text   text between JSX tags: <span>Ready</span>
//   jsx-attr   a string in a UI attribute: aria-label="Close", title, placeholder, alt,
//              label, and any …Label, …Title, …Text, …Message, …Hint, …Placeholder prop
//   jsx-expr   a string as a JSX child: {busy ? "Saving…" : "Save"}
//   ui-prop    a string in an object property with a UI name: showToast({ message: "…" }),
//              confirm({ title: "…" }), { label: "…" }
//   ui-call    a string passed to setError("…"), setMessage("…"), …Status, …Label, …Text
//   literal    any other sentence-like string ("Hide sidebar", "Ready") where a value
//              goes: a variable, a return, a property, an array, an argument
//
// Strings without letters, single code-like tokens (settings.json,
// Content-Type, PostSonde, GET, message keys such as "shell.ready") and
// what is inside <pre> and <code> are not text. A line with an
// `i18n-ignore` comment, or the line after one, is skipped; so are the
// lines between `i18n-ignore-start` and `i18n-ignore-end` comments, and a
// file with `i18n-ignore-file` in a comment. Say why in the comment.
//
// It also fails, allowlist or not, on a t() call at module level: the
// message would be fixed in the language at import time.
//
// The allowlist (src/i18n/allowlist/<area>.json, one file per area so that
// areas can be converted in parallel) holds the files not converted yet and
// how many strings each has. The check fails when a file has more strings
// than its entry allows, or has any without one; fewer is fine (it prints a
// note: run --update to lower the counts).

import { existsSync, mkdirSync, readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join, relative, sep } from "node:path";
import { fileURLToPath } from "node:url";
import ts from "typescript";

const here = dirname(fileURLToPath(import.meta.url));
export const FRONTEND = join(here, "..");
export const SRC = join(FRONTEND, "src");
export const ALLOWLIST_DIR = join(SRC, "i18n", "allowlist");

/** JSX attributes whose string value is UI text. */
const UI_ATTR =
  /^(aria-(label|description|roledescription|valuetext|placeholder)|title|placeholder|alt|label|message|description|hint|tooltip|heading|caption|note|detail|summary|[a-z][A-Za-z0-9]*(Label|Title|Text|Message|Description|Hint|Tooltip|Placeholder|Body|Heading|Caption|Note|Summary))$/;

/** Object properties whose string value is UI text (the JSX names, and quoted "aria-label"). */
const UI_PROP = UI_ATTR;

/** Setters whose string argument is UI text: setError("…"), setStatusText("…"). */
const UI_CALL = /^set([A-Z][A-Za-z0-9]*)?(Error|Message|Status|Label|Title|Text|Hint|Note|Notice|Warning|Description)$/;

/** Calls whose string arguments are never UI text. */
const NON_UI_CALLEE = new Set([
  "Error", "TypeError", "RangeError", "SyntaxError", "RegExp", "Symbol", "BigInt",
  "require", "import", "describe", "it", "test", "expect",
  "querySelector", "querySelectorAll", "getElementById", "closest", "matches", "matchMedia",
  "addEventListener", "removeEventListener", "dispatchEvent", "createElement", "createElementNS",
  "getAttribute", "setAttribute", "removeAttribute", "hasAttribute", "toggleAttribute",
  "setProperty", "getPropertyValue", "removeProperty",
  "getItem", "setItem", "removeItem",
  "startsWith", "endsWith", "includes", "indexOf", "lastIndexOf", "split", "replace", "replaceAll",
  "match", "matchAll", "search", "localeCompare", "padStart", "padEnd", "join", "concat", "trim", "at",
  "has", "get", "delete", "set", "add",
  "On", "Off", "Emit", "Once", "OnMultiple", "Call", "ByName",
  "fetch", "postMessage", "encode", "decode", "parse", "stringify",
  "debug", "info", "log", "warn", "error", "trace", "assert", "group", "groupCollapsed", "time", "timeEnd",
  "t", "tParts",
]);

/** Receivers whose methods never take UI text. */
const NON_UI_RECEIVER = new Set(["console", "JSON", "Object", "Reflect", "localStorage", "sessionStorage", "Events", "document", "window", "Math", "Number", "Intl"]);

const LETTER = /\p{L}/u;
/** A single token that is code, not words: a file, a key, a header, an identifier, a constant. */
function isCodeLike(text) {
  if (/\s/.test(text)) return false;
  const word = text.replace(/[.…!?:,]+$/u, ""); // "Done." and "Loading..." are words
  return (
    /[./_:@#$%\\[\]()=<>{}+*&|^~0-9-]/.test(word) || // settings.json, Content-Type, shell.ready, v1
    /\p{Ll}\p{Lu}/u.test(word) || // camelCase, PostSonde
    (/^\p{Lu}{2,}$/u.test(word) && word !== "OK") // GET, JSON, TLS
  );
}

/** Whether a string written in the source is UI text (in a place where text is expected). */
export function isText(text) {
  const trimmed = text.trim();
  return trimmed !== "" && LETTER.test(trimmed) && !isCodeLike(trimmed);
}

/**
 * Whether a string in a general value position looks like UI text: words
 * with spaces ("Hide sidebar"), a capitalised word ("Ready"), or text
 * ending in an ellipsis or a sentence mark. Code-ish strings (with quotes,
 * braces, `=`, `;`, `var(--`, line breaks) are not.
 */
export function isSentenceLike(text) {
  const s = text.trim();
  if (!isText(s)) return false;
  if (/[{}<>=;"`\n\\]|var\(--|\b\d+(px|em|rem|ms|s|%)\b|^[a-z-]+\([^)]*\)$/.test(s)) return false;
  if (/\p{L}[^\S\n]+[\p{L}(“‘'"]/u.test(s)) return true; // two words
  if (/^\p{Lu}\p{Ll}+[.…!?:]?$/u.test(s)) return true; // Ready, Light
  if (/\p{L}[…!?]$/u.test(s)) return true; // Saving…
  return false;
}

function lineOf(sf, pos) {
  return sf.getLineAndCharacterOfPosition(pos);
}

function calleeName(expr) {
  if (ts.isIdentifier(expr)) return { name: expr.text, receiver: null };
  if (ts.isPropertyAccessExpression(expr)) {
    const receiver = ts.isIdentifier(expr.expression) ? expr.expression.text : null;
    return { name: expr.name.text, receiver };
  }
  return { name: null, receiver: null };
}

function propName(name) {
  if (ts.isIdentifier(name) || ts.isStringLiteral(name) || ts.isNoSubstitutionTemplateLiteral(name)) return name.text;
  return null;
}

/** The literal text pieces of an expression that becomes a string: literals, template parts, both branches of ?:, ??, ||, +. */
function textPieces(node) {
  if (ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node)) return [{ node, text: node.text }];
  if (ts.isTemplateExpression(node)) {
    const text = [node.head.text, ...node.templateSpans.map((s) => s.literal.text)].join(" ");
    return [{ node, text }];
  }
  if (ts.isParenthesizedExpression(node) || ts.isAsExpression(node) || ts.isSatisfiesExpression?.(node)) {
    return textPieces(node.expression);
  }
  if (ts.isConditionalExpression(node)) return [...textPieces(node.whenTrue), ...textPieces(node.whenFalse)];
  if (ts.isBinaryExpression(node)) {
    const op = node.operatorToken.kind;
    if (op === ts.SyntaxKind.QuestionQuestionToken || op === ts.SyntaxKind.BarBarToken || op === ts.SyntaxKind.PlusToken) {
      return [...textPieces(node.left), ...textPieces(node.right)];
    }
    if (op === ts.SyntaxKind.AmpersandAmpersandToken) return textPieces(node.right);
  }
  return [];
}

function isFunctionLike(node) {
  return (
    ts.isFunctionDeclaration(node) ||
    ts.isFunctionExpression(node) ||
    ts.isArrowFunction(node) ||
    ts.isMethodDeclaration(node) ||
    ts.isGetAccessorDeclaration(node) ||
    ts.isSetAccessorDeclaration(node) ||
    ts.isConstructorDeclaration(node) ||
    ts.isClassStaticBlockDeclaration(node)
  );
}

/**
 * Scans one source file. Returns { findings, errors }: findings are
 * hard-coded strings ({ line, column, kind, text }), errors are rule
 * breaks the allowlist cannot excuse (a module-level t() call).
 */
export function scanSource(fileName, source) {
  const kind = fileName.endsWith(".tsx") ? ts.ScriptKind.TSX : ts.ScriptKind.TS;
  const sf = ts.createSourceFile(fileName, source, ts.ScriptTarget.Latest, true, kind);
  const findings = [];
  const errors = [];
  if (/\/[/*].*i18n-ignore-file/.test(source)) return { findings, errors };

  const lines = source.split("\n");
  // Lines between `i18n-ignore-start` and `i18n-ignore-end` comments (a table of names, say).
  const blocked = new Set();
  let inBlock = false;
  lines.forEach((text, l) => {
    if (/(\/\/|\/\*).*i18n-ignore-start/.test(text)) inBlock = true;
    if (inBlock) blocked.add(l);
    if (/(\/\/|\/\*).*i18n-ignore-end/.test(text)) inBlock = false;
  });
  const ignored = (pos) => {
    const { line } = lineOf(sf, pos);
    const has = (l) => l >= 0 && /(\/\/|\/\*).*i18n-ignore(?![-\w])/.test(lines[l] ?? "");
    return blocked.has(line) || has(line) || has(line - 1);
  };
  const seen = new Set();
  const report = (node, kindName, text) => {
    const pos = node.getStart(sf);
    if (seen.has(pos) || ignored(pos)) return;
    seen.add(pos);
    const { line, character } = lineOf(sf, pos);
    findings.push({ line: line + 1, column: character + 1, kind: kindName, text: text.trim().replace(/\s+/g, " ") });
  };
  const reportPieces = (expr, kindName, test = isText) => {
    for (const piece of textPieces(expr)) {
      if (test(piece.text)) report(piece.node, kindName, piece.text);
    }
  };

  const visit = (node, depth) => {
    // Code samples are not translated: skip what is inside <pre> and <code>.
    if (ts.isJsxElement(node)) {
      const tag = node.openingElement.tagName.getText(sf);
      if (tag === "pre" || tag === "code") return;
    }

    // Module-level t(): the text would be fixed at import time.
    if (depth === 0 && ts.isCallExpression(node)) {
      const { name, receiver } = calleeName(node.expression);
      if ((name === "t" && receiver === null) || (name === "t" && receiver === "i18n")) {
        const { line, character } = lineOf(sf, node.getStart(sf));
        errors.push({
          line: line + 1,
          column: character + 1,
          message: "t() called at module level: call it where the text is shown (in render, or in the function that shows it)",
        });
      }
    }

    if (ts.isJsxText(node)) {
      if (isText(node.text)) report(node, "jsx-text", node.text);
    } else if (ts.isJsxAttribute(node)) {
      const name = ts.isIdentifier(node.name) ? node.name.text : node.name.getText(sf);
      if (UI_ATTR.test(name) && node.initializer) {
        if (ts.isStringLiteral(node.initializer)) {
          if (isText(node.initializer.text)) report(node.initializer, "jsx-attr", node.initializer.text);
        } else if (ts.isJsxExpression(node.initializer) && node.initializer.expression) {
          reportPieces(node.initializer.expression, "jsx-attr");
        }
      }
    } else if (ts.isJsxExpression(node) && node.expression && (ts.isJsxElement(node.parent) || ts.isJsxFragment(node.parent))) {
      reportPieces(node.expression, "jsx-expr");
    } else if (ts.isPropertyAssignment(node)) {
      const name = propName(node.name);
      if (name !== null && UI_PROP.test(name)) reportPieces(node.initializer, "ui-prop");
      else reportPieces(node.initializer, "literal", isSentenceLike);
    } else if (ts.isCallExpression(node) || ts.isNewExpression(node)) {
      const { name, receiver } = calleeName(node.expression);
      const args = node.arguments ?? [];
      if (name !== null && UI_CALL.test(name)) {
        for (const arg of args) reportPieces(arg, "ui-call");
      } else if (!(name !== null && NON_UI_CALLEE.has(name)) && !(receiver !== null && NON_UI_RECEIVER.has(receiver))) {
        for (const arg of args) reportPieces(arg, "literal", isSentenceLike);
      }
    } else if (ts.isVariableDeclaration(node) && node.initializer) {
      reportPieces(node.initializer, "literal", isSentenceLike);
    } else if (ts.isReturnStatement(node) && node.expression) {
      reportPieces(node.expression, "literal", isSentenceLike);
    } else if (ts.isArrowFunction(node) && !ts.isBlock(node.body)) {
      reportPieces(node.body, "literal", isSentenceLike);
    } else if (ts.isArrayLiteralExpression(node)) {
      for (const el of node.elements) reportPieces(el, "literal", isSentenceLike);
    } else if (ts.isParameter(node) && node.initializer) {
      reportPieces(node.initializer, "literal", isSentenceLike);
    } else if (ts.isBindingElement(node) && node.initializer) {
      reportPieces(node.initializer, "literal", isSentenceLike);
    } else if (
      ts.isBinaryExpression(node) &&
      (node.operatorToken.kind === ts.SyntaxKind.EqualsToken || node.operatorToken.kind === ts.SyntaxKind.PlusEqualsToken)
    ) {
      reportPieces(node.right, "literal", isSentenceLike);
    }

    const nextDepth = isFunctionLike(node) ? depth + 1 : depth;
    ts.forEachChild(node, (child) => visit(child, nextDepth));
  };
  ts.forEachChild(sf, (child) => visit(child, 0));
  findings.sort((a, b) => a.line - b.line || a.column - b.column);
  return { findings, errors };
}

/** Whether a file under src/ is scanned: sources, not tests, test helpers or the catalogue itself. */
export function isScanned(rel) {
  const path = rel.split(sep).join("/");
  return (
    /\.(ts|tsx)$/.test(path) &&
    !/\.test\.(ts|tsx)$/.test(path) &&
    !path.endsWith(".d.ts") &&
    !path.startsWith("test/") &&
    !path.startsWith("i18n/")
  );
}

/**
 * The area a file belongs to, for the report and the allowlist:
 * components/<folder>, else the top folder under src/; "app" for the files
 * directly in src/ or components/. Each area has its own namespace in the
 * catalogue, named after its last part (components/editor: en/editor.ts).
 */
export function areaOf(rel) {
  const parts = rel.split(sep).join("/").split("/");
  if (parts.length === 1) return "app";
  if (parts[0] === "components") return parts.length > 2 ? `components/${parts[1]}` : "app";
  return parts[0];
}

/** The catalogue namespace of an area: components/editor → editor. */
export function namespaceOf(area) {
  return area.split("/").pop();
}

function walk(dir, out = []) {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) walk(path, out);
    else out.push(path);
  }
  return out;
}

/** Scans src/. Returns { files: Map<rel, findings>, errors: [{ file, line, column, message }] }. */
export function scanTree(src = SRC) {
  const files = new Map();
  const errors = [];
  for (const path of walk(src).sort()) {
    const rel = relative(src, path).split(sep).join("/");
    if (!isScanned(rel)) continue;
    const result = scanSource(rel, readFileSync(path, "utf8"));
    if (result.findings.length > 0) files.set(rel, result.findings);
    for (const e of result.errors) errors.push({ file: rel, ...e });
  }
  return { files, errors };
}

const slug = (area) => area.replace(/\//g, "-");

/** Reads the allowlist: file → allowed count. */
export function readAllowlist(dir = ALLOWLIST_DIR) {
  const allowed = new Map();
  if (!existsSync(dir)) return allowed;
  for (const name of readdirSync(dir).sort()) {
    if (!name.endsWith(".json")) continue;
    const data = JSON.parse(readFileSync(join(dir, name), "utf8"));
    for (const [file, count] of Object.entries(data)) allowed.set(file, count);
  }
  return allowed;
}

/** Writes the allowlist from a scan: one file per area, sorted, areas without strings removed. */
export function writeAllowlist(files, dir = ALLOWLIST_DIR) {
  mkdirSync(dir, { recursive: true });
  const byArea = new Map();
  for (const [file, findings] of files) {
    const area = areaOf(file);
    if (!byArea.has(area)) byArea.set(area, {});
    byArea.get(area)[file] = findings.length;
  }
  for (const name of readdirSync(dir)) {
    if (name.endsWith(".json")) rmSync(join(dir, name));
  }
  for (const [area, entries] of byArea) {
    writeFileSync(join(dir, `${slug(area)}.json`), `${JSON.stringify(entries, null, 2)}\n`);
  }
}

/**
 * Compares a scan with the allowlist. Returns { failures, notes }:
 * failures are files over their count (with their findings) and module
 * level t() calls; notes are files under their count or gone.
 */
export function checkAgainst({ files, errors }, allowed) {
  const failures = [];
  const notes = [];
  for (const e of errors) failures.push(`${e.file}:${e.line}:${e.column}: ${e.message}`);
  for (const [file, findings] of files) {
    const max = allowed.get(file) ?? 0;
    if (findings.length > max) {
      const lines = findings.map((f) => `    ${file}:${f.line}:${f.column} ${f.kind} ${JSON.stringify(f.text)}`);
      failures.push(
        `${file}: ${findings.length} hard-coded UI string(s), the allowlist allows ${max}. ` +
          `Put the text in the catalogue (src/i18n/en/) and use t(), or mark a string that is not UI text with an i18n-ignore comment:\n${lines.join("\n")}`,
      );
    } else if (findings.length < max) {
      notes.push(`${file}: ${findings.length} of ${max} allowed; lower the count (npm run i18n:update)`);
    }
  }
  for (const [file, max] of allowed) {
    if (!files.has(file) && max > 0) notes.push(`${file}: no hard-coded strings left (or the file is gone); remove it (npm run i18n:update)`);
  }
  return { failures, notes };
}

/** Counts per area: [{ area, files: [[file, count]], count }], biggest first. */
export function summarize(files) {
  const areas = new Map();
  for (const [file, findings] of files) {
    const area = areaOf(file);
    if (!areas.has(area)) areas.set(area, { area, files: [], count: 0 });
    const a = areas.get(area);
    a.files.push([file, findings.length]);
    a.count += findings.length;
  }
  const out = [...areas.values()];
  for (const a of out) a.files.sort((x, y) => y[1] - x[1] || x[0].localeCompare(y[0]));
  return out.sort((x, y) => y.count - x.count || x.area.localeCompare(y.area));
}

function main(argv) {
  const scan = scanTree();
  if (argv.includes("--update")) {
    writeAllowlist(scan.files);
    console.log(`i18n-strings: wrote the allowlist for ${scan.files.size} file(s) to ${relative(FRONTEND, ALLOWLIST_DIR)}/`);
  }
  const summary = summarize(scan.files);
  const total = summary.reduce((n, a) => n + a.count, 0);
  console.log(`Hard-coded UI strings: ${total} in ${scan.files.size} file(s)\n`);
  for (const a of summary) {
    console.log(`${String(a.count).padStart(5)}  ${a.area} (${a.files.length} file${a.files.length === 1 ? "" : "s"})`);
    if (argv.includes("--files") || argv.includes("--list")) {
      for (const [file, count] of a.files) {
        console.log(`${String(count).padStart(11)}  ${file}`);
        if (argv.includes("--list")) {
          for (const f of scan.files.get(file)) console.log(`${" ".repeat(15)}${f.line}:${f.column} ${f.kind} ${JSON.stringify(f.text)}`);
        }
      }
    }
  }
  const { failures, notes } = checkAgainst(scan, readAllowlist());
  if (notes.length > 0) console.log(`\nNotes:\n  ${notes.join("\n  ")}`);
  if (failures.length > 0) {
    console.error(`\ni18n-strings: FAILED\n${failures.join("\n")}`);
    return 1;
  }
  console.log("\ni18n-strings: ok");
  return 0;
}

if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) {
  process.exit(main(process.argv.slice(2)));
}
