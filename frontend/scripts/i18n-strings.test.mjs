// @vitest-environment node
// Tests for i18n-strings.mjs (UI-10): what counts as a hard-coded UI
// string, and the check of src/ against the allowlist, which is what makes
// `npm test` (and scripts/check-frontend.sh) fail when one is added.

import { describe, expect, it } from "vitest";
import { areaOf, checkAgainst, isSentenceLike, isText, readAllowlist, scanSource, scanTree, summarize } from "./i18n-strings.mjs";

const texts = (source, file = "x.tsx") => scanSource(file, source).findings.map((f) => [f.kind, f.text]);

describe("scanSource", () => {
  it("finds JSX text and strings in UI attributes", () => {
    expect(
      texts(`export const A = () => (
        <div className="row" data-kind="x" role="status" aria-label="Status bar">
          Ready
          <button title={busy ? "Saving…" : "Save"} placeholder="Filter by name" confirmLabel="Delete it"><Icon /></button>
          <img alt="Logo" />
        </div>
      );`),
    ).toEqual([
      ["jsx-attr", "Status bar"],
      ["jsx-text", "Ready"],
      ["jsx-attr", "Saving…"],
      ["jsx-attr", "Save"],
      ["jsx-attr", "Filter by name"],
      ["jsx-attr", "Delete it"],
      ["jsx-attr", "Logo"],
    ]);
  });

  it("finds strings as JSX children, in UI properties and setters, and sentence-like values", () => {
    expect(
      texts(`function F({ n }) {
        const label = n > 1 ? "Hide sidebar" : "Show sidebar";
        showToast({ message: \`Deleted \${n}\` });
        setError("Could not save");
        return <p>{n === 0 ? "No items" : label}</p>;
      }`),
    ).toEqual([
      ["literal", "Hide sidebar"],
      ["literal", "Show sidebar"],
      ["ui-prop", "Deleted"],
      ["ui-call", "Could not save"],
      ["jsx-expr", "No items"],
    ]);
  });

  it("leaves code alone: keys, ids, class names, file names, protocol tokens, comparisons, logs and t() keys", () => {
    expect(
      texts(`import x from "./Thing";
        const KIND = "request";
        function F({ mode }) {
          if (mode === "Portable mode") return null;
          console.warn("settings: could not load");
          throw new Error("unreachable state");
          const file = "settings.json";
          const header = "Content-Type";
          const method = "GET";
          return <div className={styles.row} id="main" key="k" data-x="Some words">{t("shell.statusBar.ready")}<code>npm run build</code></div>;
        }`),
    ).toEqual([]);
  });

  it("skips a line marked i18n-ignore, the line after one, a marked block and a marked file", () => {
    expect(texts(`const a = "Plain words"; // i18n-ignore: not UI\n// i18n-ignore\nconst b = "More words";`)).toEqual([]);
    expect(texts(`// i18n-ignore-start\nconst fonts = ["Fira Code", "Source Code Pro"];\n// i18n-ignore-end\nconst c = "Real text";`)).toEqual([
      ["literal", "Real text"],
    ]);
    expect(texts(`// i18n-ignore-file: a demo\nconst a = <p>Hello there</p>;`)).toEqual([]);
  });

  it("reports t() at module level, which fixes the language at import time", () => {
    const { errors } = scanSource("x.ts", `const LABEL = t("shell.x");\nfunction ok() { return t("shell.y"); }`);
    expect(errors).toHaveLength(1);
    expect(errors[0]).toMatchObject({ line: 1 });
  });
});

describe("text heuristics", () => {
  it.each(["Ready", "Save failed", "Saving…", "Loading...", "OK", "dev"])("%s is text", (s) => {
    expect(isText(s)).toBe(true);
  });
  it.each(["settings.json", "Content-Type", "PostSonde", "GET", "shell.statusBar.ready", "  ", "—", "42"])("%s is not text", (s) => {
    expect(isText(s)).toBe(false);
  });
  it("tells sentences from values in general positions", () => {
    expect(isSentenceLike("Hide sidebar")).toBe(true);
    expect(isSentenceLike("Ready")).toBe(true);
    expect(isSentenceLike("ready")).toBe(false);
    expect(isSentenceLike("1px solid var(--line)")).toBe(false);
    expect(isSentenceLike('{"a": 1}')).toBe(false);
  });
});

describe("areas", () => {
  it("groups files by component folder, else by top folder", () => {
    expect(areaOf("components/editor/BodyEditor.tsx")).toBe("components/editor");
    expect(areaOf("components/StreamDemo.tsx")).toBe("app");
    expect(areaOf("App.tsx")).toBe("app");
    expect(areaOf("lib/shortcuts/registry.ts")).toBe("lib");
  });
});

describe("checkAgainst", () => {
  const finding = { line: 1, column: 1, kind: "jsx-text", text: "Ready" };
  it("fails on a file over its count or without an entry, notes one under it", () => {
    const scan = { files: new Map([["a.tsx", [finding, finding]], ["b.tsx", [finding]], ["c.tsx", [finding]]]), errors: [] };
    const { failures, notes } = checkAgainst(scan, new Map([["a.tsx", 1], ["b.tsx", 1], ["c.tsx", 3], ["gone.tsx", 2]]));
    expect(failures).toHaveLength(1);
    expect(failures[0]).toContain("a.tsx: 2 hard-coded UI string(s), the allowlist allows 1");
    expect(notes).toEqual([
      "c.tsx: 1 of 3 allowed; lower the count (npm run i18n:update)",
      "gone.tsx: no hard-coded strings left (or the file is gone); remove it (npm run i18n:update)",
    ]);
    expect(checkAgainst({ files: new Map([["d.tsx", [finding]]]), errors: [] }, new Map()).failures).toHaveLength(1);
  });
});

describe("src/", () => {
  // Parsing every source file takes a few seconds, more when other test
  // files run beside it, so it gets more than the default 5 s.
  it("has no hard-coded UI strings beyond the allowlist (src/i18n/allowlist/), and no module-level t()", { timeout: 60_000 }, () => {
    const scan = scanTree();
    const { failures } = checkAgainst(scan, readAllowlist());
    expect(failures).toEqual([]);
    // The converted slice has none left.
    const areas = new Map(summarize(scan.files).map((a) => [a.area, a.count]));
    for (const done of ["components/shell", "components/settings", "components/common"]) expect(areas.get(done)).toBeUndefined();
  });
});
