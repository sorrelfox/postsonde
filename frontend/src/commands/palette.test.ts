import { afterEach, describe, expect, it, vi } from "vitest";
import { getShortcut, matchesShortcut, setShortcutOverrides, shortcutName, SHORTCUTS, type ShortcutDefinition } from "../lib/shortcuts";
import { actionAvailability, NOT_IN_PALETTE, openCommandPalette, onCommandPaletteRequest, paletteItems, runPaletteAction } from "./palette";
import { createShortcutBindings, SCOPE_ATTRIBUTE } from "./shortcuts";
import { EDIT_HISTORY_ATTRIBUTE } from "./undoRedo";

afterEach(() => {
  document.body.innerHTML = "";
  setShortcutOverrides({});
});

/** A focusable element inside the scope `def` applies to: where focus was before the palette opened. */
function focusInScopeOf(def: ShortcutDefinition): HTMLElement {
  const host = document.createElement("div");
  if (def.scope === "request") host.setAttribute(EDIT_HISTORY_ATTRIBUTE, "r1");
  else if (def.scope !== "global") host.setAttribute(SCOPE_ATTRIBUTE, def.scope);
  const inner = document.createElement("button");
  host.appendChild(inner);
  document.body.appendChild(host);
  inner.focus();
  return inner;
}

describe.each([
  ["macOS", true],
  ["Windows and Linux", false],
] as const)("the command palette on %s (NAV-05)", (_name, mac) => {
  it("lists every registered action and runs each one where it applies", () => {
    const listed = SHORTCUTS.filter((def) => !NOT_IN_PALETTE.includes(def.id));
    expect(listed.length).toBe(SHORTCUTS.length - NOT_IN_PALETTE.length);

    for (const def of listed) {
      const bindings = createShortcutBindings();
      const handler = vi.fn();
      if (def.scope === "global") bindings.bind(def.id, handler);
      const target = def.scope === "global" ? null : focusInScopeOf(def);
      const pressed: string[] = [];
      target?.addEventListener("keydown", (e) => {
        if (matchesShortcut(e, def.id, mac)) pressed.push(def.id);
      });

      // Listed by name, available here.
      const items = paletteItems(shortcutName(def), target, [], { bindings, mac });
      const item = items.find((i) => i.def.id === def.id);
      expect(item, def.id).toBeDefined();
      expect(item?.available, def.id).toBe(true);
      expect(paletteItems("", target, [], { bindings, mac }).some((i) => i.def.id === def.id), def.id).toBe(true);

      // Runs: the handler bound to a global action, the chord where focus was for a scoped one.
      expect(runPaletteAction(def, target, { bindings, mac }), def.id).toBe("ran");
      if (def.scope === "global") expect(handler, def.id).toHaveBeenCalledTimes(1);
      else {
        expect(pressed, def.id).toEqual([def.id]);
        expect(document.activeElement).toBe(target);
      }
      document.body.innerHTML = "";
    }
  });

  it("presses the custom chord of a remapped scoped action", () => {
    setShortcutOverrides({ "graphql.fillFields": ["Mod+Shift+L"] });
    const def = getShortcut("graphql.fillFields");
    const target = focusInScopeOf(def);
    const seen: KeyboardEvent[] = [];
    target.addEventListener("keydown", (e) => seen.push(e));
    expect(runPaletteAction(def, target, { bindings: createShortcutBindings(), mac })).toBe("ran");
    expect(seen).toHaveLength(1);
    expect(seen[0]?.key.toLowerCase()).toBe("l");
    expect(seen[0]?.shiftKey).toBe(true);
    expect(matchesShortcut(seen[0]!, "graphql.fillFields", mac)).toBe(true);
  });
});

describe("availability", () => {
  it("disables an unbound global action with what it needs, and a placeholder as coming soon", () => {
    const bindings = createShortcutBindings();
    expect(actionAvailability(getShortcut("request.send"), null, bindings)).toEqual({
      available: false,
      reason: "Open an HTTP, GraphQL or SSE request first",
    });
    expect(actionAvailability(getShortcut("sidebar.toggle"), null, bindings)).toEqual({ available: false, reason: "Not available here" });
    const placeholder: ShortcutDefinition = { ...getShortcut("sidebar.toggle"), placeholder: {} };
    bindings.bind("sidebar.toggle", () => {});
    expect(actionAvailability(placeholder, null, bindings)).toEqual({ available: false, reason: "Coming soon" });
    expect(runPaletteAction(getShortcut("request.send"), null, { bindings })).toBe("unavailable");
  });

  it("disables a scoped action outside its scope, e.g. Fill fields outside a GraphQL query editor", () => {
    const outside = document.createElement("input");
    document.body.appendChild(outside);
    const fill = getShortcut("graphql.fillFields");
    expect(actionAvailability(fill, outside)).toEqual({ available: false, reason: "Only in a GraphQL query editor" });
    expect(actionAvailability(fill, null).available).toBe(false);
    expect(runPaletteAction(fill, outside)).toBe("unavailable");

    // Undo of a request edit: in the request's editor, but not in one of its code editors.
    const request = document.createElement("div");
    request.setAttribute(EDIT_HISTORY_ATTRIBUTE, "r1");
    request.innerHTML = `<input id="plainfield" /><div class="cm-editor"><div id="insidecode" contenteditable="true"></div></div>`;
    document.body.appendChild(request);
    const undo = getShortcut("request.undo");
    expect(actionAvailability(undo, request.querySelector("#plainfield")).available).toBe(true);
    expect(actionAvailability(undo, request.querySelector("#insidecode"))).toEqual({
      available: false,
      reason: "Only in a request editor, outside its code editors",
    });
  });

  it("reports a handler that had nothing to do", () => {
    const bindings = createShortcutBindings();
    bindings.bind("environment.switch", () => false);
    expect(runPaletteAction(getShortcut("environment.switch"), null, { bindings })).toBe("declined");
  });
});

describe("search", () => {
  it("matches labels fuzzily, available actions first", () => {
    const bindings = createShortcutBindings();
    bindings.bind("tab.close", () => {});
    const items = paletteItems("close", null, [], { bindings, mac: true });
    expect(items[0]?.def.id).toBe("tab.close");
    expect(items[0]?.hits).toEqual([0, 1, 2, 3, 4]);
    const firstUnavailable = items.findIndex((i) => !i.available);
    expect(items.slice(firstUnavailable).every((i) => !i.available)).toBe(true);
    expect(paletteItems("fill fields", null, [], { bindings }).map((i) => i.def.id)).toContain("graphql.fillFields");
    expect(paletteItems("zzzz", null, [], { bindings })).toEqual([]);
  });

  it("lists recently run actions first before anything is typed", () => {
    const bindings = createShortcutBindings();
    bindings.bind("tab.close", () => {});
    bindings.bind("console.toggle", () => {});
    const items = paletteItems("", null, ["console.toggle", "tab.close"], { bindings });
    expect(items.slice(0, 2).map((i) => i.def.id)).toEqual(["console.toggle", "tab.close"]);
    expect(items.some((i) => i.def.id === "palette.open")).toBe(false);
  });
});

describe("opening from elsewhere", () => {
  it("hands the query to the mounted palette", () => {
    expect(openCommandPalette("x")).toBe(false);
    const listener = vi.fn();
    const off = onCommandPaletteRequest(listener);
    expect(openCommandPalette("fill")).toBe(true);
    expect(listener).toHaveBeenCalledWith("fill");
    off();
    expect(openCommandPalette()).toBe(false);
  });
});
