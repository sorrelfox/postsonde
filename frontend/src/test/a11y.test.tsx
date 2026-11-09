// The audit helper itself (test/a11y.ts): each rule finds what it is for
// and leaves correct markup alone.
import { act, render } from "@testing-library/react";
import { useState } from "react";
import { describe, expect, it } from "vitest";
import { onTabListKeyDown } from "../components/common/tabList";
import { accessibleName, auditA11y, pressTab, tabStops } from "./a11y";

const rules = (html: string) => {
  document.body.innerHTML = html;
  const out = auditA11y().map((v) => v.rule);
  document.body.innerHTML = "";
  return out;
};

describe("auditA11y", () => {
  it("passes labelled, well-formed markup", () => {
    expect(
      rules(`
        <button type="button" aria-label="Close"><svg aria-hidden="true"></svg></button>
        <label for="n">Name</label><input id="n">
        <div role="tablist" aria-label="Sections">
          <button role="tab" id="t1" aria-selected="true" aria-controls="p1" tabindex="0">One</button>
          <button role="tab" id="t2" aria-selected="false" tabindex="-1">Two</button>
        </div>
        <div role="tabpanel" id="p1" aria-labelledby="t1">…</div>
        <div role="tree" aria-label="Files"><div role="treeitem" aria-selected="true" tabindex="0">a</div><div role="treeitem" aria-selected="false" tabindex="-1">b</div></div>
      `),
    ).toEqual([]);
  });

  it("finds each kind of problem", () => {
    expect(rules(`<button type="button"><svg aria-hidden="true"></svg></button>`)).toEqual(["name-missing"]);
    expect(rules(`<input placeholder="Search">`)).toEqual(["label-placeholder-only"]);
    expect(rules(`<button tabindex="2">A</button>`)).toEqual(["tabindex-positive"]);
    expect(rules(`<button aria-controls="nope">A</button>`)).toEqual(["idref-missing"]);
    expect(rules(`<div id="x"></div><div id="x"></div>`)).toEqual(["duplicate-id"]);
    expect(rules(`<div role="bogus"></div>`)).toEqual(["role-invalid"]);
    expect(rules(`<div aria-hidden="true"><button>A</button></div>`)).toEqual(["aria-hidden-focusable"]);
    expect(rules(`<button role="tab" aria-selected="true">A</button>`)).toEqual(["role-context"]);
    expect(rules(`<div role="tablist" aria-label="T"><button role="tab" aria-selected="true">A</button><button>More</button></div>`)).toContain("role-children");
    expect(rules(`<div role="tablist" aria-label="T"><button role="tab" aria-selected="true">A</button><button role="tab" aria-selected="false">B</button></div>`)).toEqual(["roving-tabindex"]);
    expect(rules(`<a href="#a" role="link">A <button>B</button></a>`)).toEqual(["nested-interactive"]);
    expect(rules(`<div role="tabpanel" aria-label="P"></div>`)).toEqual(["tabpanel-label"]);
  });

  it("ignores what is hidden from assistive technology", () => {
    expect(rules(`<div hidden><button></button></div><input list="l" aria-label="X"><datalist id="l"><option value="a"></option></datalist>`)).toEqual([]);
  });

  it("computes names from labels, content and references", () => {
    document.body.innerHTML = `
      <span id="l">Base URL</span><input id="a" aria-labelledby="l">
      <label>Port <input id="b"></label>
      <button id="c">Send <span aria-hidden="true">⌘↩</span></button>
      <button id="d" title="More"></button>`;
    expect(["a", "b", "c", "d"].map((id) => accessibleName(document.getElementById(id)!))).toEqual(["Base URL", "Port", "Send", "More"]);
    document.body.innerHTML = "";
  });
});

describe("pressTab", () => {
  it("moves through the Tab stops in document order, wrapping, and backwards with Shift", () => {
    render(
      <div>
        <button type="button">A</button>
        <button type="button" tabIndex={-1}>skipped</button>
        <input aria-label="B" />
        <button type="button" disabled>
          disabled
        </button>
        <a href="#c">C</a>
      </div>,
    );
    expect(tabStops().map((el) => accessibleName(el))).toEqual(["A", "B", "C"]);
    expect(accessibleName(pressTab()!)).toBe("A");
    expect(accessibleName(pressTab()!)).toBe("B");
    expect(accessibleName(pressTab()!)).toBe("C");
    expect(accessibleName(pressTab()!)).toBe("A");
    expect(accessibleName(pressTab(true)!)).toBe("C");
  });
});

function Tabs({ vertical = false }: { vertical?: boolean }) {
  const [tab, setTab] = useState("one");
  return (
    <div role="tablist" aria-label="T" aria-orientation={vertical ? "vertical" : undefined} onKeyDown={onTabListKeyDown}>
      {["one", "two", "three"].map((t) => (
        <button key={t} type="button" role="tab" aria-selected={t === tab} tabIndex={t === tab ? 0 : -1} onClick={() => setTab(t)}>
          {t}
        </button>
      ))}
    </div>
  );
}

describe("onTabListKeyDown", () => {
  const key = (k: string) => document.activeElement?.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true, cancelable: true }));
  const selected = () => document.querySelector("[aria-selected='true']")?.textContent;

  it("moves focus and selection with the arrows, Home and End, wrapping", () => {
    render(<Tabs />);
    act(() => document.querySelector<HTMLElement>("[role='tab']")?.focus());
    act(() => void key("ArrowRight"));
    expect([document.activeElement?.textContent, selected()]).toEqual(["two", "two"]);
    act(() => void key("End"));
    expect(selected()).toBe("three");
    act(() => void key("ArrowRight"));
    expect(selected()).toBe("one");
    act(() => void key("ArrowLeft"));
    expect(selected()).toBe("three");
    act(() => void key("Home"));
    expect(selected()).toBe("one");
    // Up and Down belong to a vertical list.
    act(() => void key("ArrowDown"));
    expect(selected()).toBe("one");
  });

  it("uses Up and Down in a vertical list", () => {
    render(<Tabs vertical />);
    act(() => document.querySelector<HTMLElement>("[role='tab']")?.focus());
    act(() => void key("ArrowDown"));
    expect(selected()).toBe("two");
    act(() => void key("ArrowRight"));
    expect(selected()).toBe("two");
  });
});
