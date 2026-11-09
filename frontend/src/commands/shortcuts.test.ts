import { afterEach, describe, expect, it, vi } from "vitest";

const windowService = vi.hoisted(() => ({
  NewWindow: vi.fn<() => Promise<void>>(() => Promise.resolve()),
}));
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/window", () => ({
  WindowService: windowService,
}));

import { createStores, type Stores, type TreeNode } from "../stores";
import {
  createShortcutBindings,
  handleShortcutKeyDown,
  installShortcuts,
  isEditableTarget,
  scopesOf,
  type ShortcutBindings,
} from "./shortcuts";
import { setShortcutOverrides } from "../lib/shortcuts";
import { closeTabs } from "./tabs";

const cleanups: (() => void)[] = [];
afterEach(() => {
  cleanups.splice(0).forEach((c) => c());
  document.body.innerHTML = "";
});

function mount(html: string): HTMLElement {
  const host = document.createElement("div");
  host.innerHTML = html;
  document.body.appendChild(host);
  return host;
}

type Mods = Partial<Pick<KeyboardEventInit, "metaKey" | "ctrlKey" | "altKey" | "shiftKey" | "repeat" | "isComposing">>;

/** Dispatches a keydown on `target` and runs the handler on it, like the window capture listener. */
function press(
  target: EventTarget,
  key: string,
  code: string,
  mods: Mods,
  options: { bindings: ShortcutBindings; mac: boolean },
): { id: string | null; event: KeyboardEvent } {
  const event = new KeyboardEvent("keydown", { key, code, bubbles: true, cancelable: true, ...mods });
  let id: string | null = null;
  const listener = (e: Event) => {
    id = handleShortcutKeyDown(e as KeyboardEvent, options);
  };
  window.addEventListener("keydown", listener, true);
  target.dispatchEvent(event);
  window.removeEventListener("keydown", listener, true);
  return { id, event };
}

describe("isEditableTarget and scopesOf", () => {
  it("knows text fields from other controls", () => {
    const host = mount(`
      <input id="text" /><input id="search" type="search" /><input id="check" type="checkbox" />
      <textarea id="area"></textarea><select id="sel"></select><button id="btn">b</button>
      <div contenteditable="true"><span id="rich">x</span></div>
      <div contenteditable="false"><span id="ro">x</span></div>`);
    const el = (id: string) => host.querySelector(`#${id}`);
    expect(["text", "search", "area", "sel", "rich"].map((id) => isEditableTarget(el(id)))).toEqual([true, true, true, true, true]);
    expect(["check", "btn", "ro"].map((id) => isEditableTarget(el(id)))).toEqual([false, false, false]);
    expect(isEditableTarget(window)).toBe(false);
    expect(isEditableTarget(null)).toBe(false);
  });

  it("lists the scopes around an element, innermost first", () => {
    const host = mount(`<div data-shortcut-scope="editor"><div data-shortcut-scope="table"><input id="x" /></div></div>`);
    expect(scopesOf(host.querySelector("#x"))).toEqual(["table", "editor"]);
    expect(scopesOf(document.body)).toEqual([]);
    expect(scopesOf(window)).toEqual([]);
  });
});

describe.each([
  ["macOS", true],
  ["Windows and Linux", false],
] as const)("handleShortcutKeyDown on %s", (_name, mac) => {
  const mod: Mods = mac ? { metaKey: true } : { ctrlKey: true };
  const other: Mods = mac ? { ctrlKey: true } : { metaKey: true };

  function setup() {
    const bindings = createShortcutBindings();
    const calls: string[] = [];
    for (const id of ["tab.close", "help.toggle", "sidebar.toggle", "environment.switch"]) {
      bindings.bind(id, () => {
        calls.push(id);
      });
    }
    return { bindings, calls, options: { bindings, mac } };
  }

  it("runs a bound global shortcut with the platform's modifier and stops the event", () => {
    const { calls, options } = setup();
    const { id, event } = press(document.body, "w", "KeyW", mod, options);
    expect(id).toBe("tab.close");
    expect(calls).toEqual(["tab.close"]);
    expect(event.defaultPrevented).toBe(true);

    expect(press(document.body, "w", "KeyW", other, options).id).toBeNull();
    expect(calls).toEqual(["tab.close"]);
  });

  it("works on a Russian layout through the key position", () => {
    const { calls, options } = setup();
    press(document.body, "ц", "KeyW", mod, options);
    press(document.body, "у", "KeyE", mod, options);
    expect(calls).toEqual(["tab.close", "environment.switch"]);
  });

  it("lets plain keys type in text fields but runs Mod chords there", () => {
    const { calls, options } = setup();
    const host = mount(`<input id="field" /><div contenteditable="true"><p id="cm">x</p></div><button id="btn">b</button>`);
    const field = host.querySelector("#field")!;
    const { event } = press(field, "?", "Slash", { shiftKey: true }, options);
    expect(event.defaultPrevented).toBe(false);
    press(host.querySelector("#cm")!, "?", "Slash", { shiftKey: true }, options);
    expect(calls).toEqual([]);

    press(host.querySelector("#btn")!, "?", "Slash", { shiftKey: true }, options);
    expect(calls).toEqual(["help.toggle"]);
    press(field, "/", "Slash", mod, options);
    press(field, "b", "KeyB", mod, options);
    expect(calls).toEqual(["help.toggle", "help.toggle", "sidebar.toggle"]);
  });

  it("ignores IME composition and events already handled", () => {
    const { calls, options } = setup();
    press(document.body, "w", "KeyW", { ...mod, isComposing: true }, options);
    const handled = new KeyboardEvent("keydown", { key: "w", code: "KeyW", cancelable: true, ...mod });
    handled.preventDefault();
    expect(handleShortcutKeyDown(handled, options)).toBeNull();
    expect(calls).toEqual([]);
  });

  it("leaves every key press to a shortcut recorder (UI-07)", () => {
    const { calls, options } = setup();
    const host = mount(`<button id="rec" data-shortcut-scope="record">Press the new keys</button>`);
    const { id, event } = press(host.querySelector("#rec")!, "w", "KeyW", mod, options);
    expect([id, event.defaultPrevented]).toEqual([null, false]);
    expect(calls).toEqual([]);
  });

  it("follows custom chords (UI-07)", () => {
    const { calls, options } = setup();
    setShortcutOverrides({ "sidebar.toggle": ["Mod+Alt+B"] });
    try {
      expect(press(document.body, "b", "KeyB", mod, options).id).toBeNull();
      expect(press(document.body, "∫", "KeyB", { ...mod, altKey: true }, options).id).toBe("sidebar.toggle");
      expect(calls).toEqual(["sidebar.toggle"]);
    } finally {
      setShortcutOverrides({});
    }
  });

  it("leaves a chord to the component when its scope defines it", () => {
    const { bindings, options } = setup();
    const send = vi.fn();
    bindings.bind("request.send", send);
    const host = mount(`
      <div data-shortcut-scope="table"><input id="cell" /></div>
      <div data-shortcut-scope="editor"><div contenteditable="true"><p id="line">x</p></div></div>
      <input id="url" />`);
    const inTable = press(host.querySelector("#cell")!, "Enter", "Enter", mod, options);
    expect([inTable.id, inTable.event.defaultPrevented]).toEqual([null, false]);
    const inEditor = press(host.querySelector("#line")!, "/", "Slash", mod, options);
    expect([inEditor.id, inEditor.event.defaultPrevented]).toEqual([null, false]);
    // A global shortcut the scope does not define still works inside it.
    expect(press(host.querySelector("#line")!, "w", "KeyW", mod, options).id).toBe("tab.close");
    expect(send).not.toHaveBeenCalled();
    // Outside the table, ⌘Enter is send (HTTP-20).
    const inUrl = press(host.querySelector("#url")!, "Enter", "Enter", mod, options);
    expect([inUrl.id, inUrl.event.defaultPrevented]).toEqual(["request.send", true]);
    expect(send).toHaveBeenCalledTimes(1);
  });

  it("swallows placeholders and lets unbound shortcuts and declined handlers through", () => {
    const bindings = createShortcutBindings();
    const options = { bindings, mac };
    // Send is bound only while an HTTP request editor is shown (HTTP-20).
    expect(press(document.body, "Enter", "Enter", mod, options).event.defaultPrevented).toBe(false);

    // The quick switcher is built (NAV-04): unbound (no title bar mounted), it lets the key through.
    expect(press(document.body, "p", "KeyP", mod, options).event.defaultPrevented).toBe(false);

    // New request is built (COL-04): unbound (no sidebar mounted), it lets the key through.
    expect(press(document.body, "n", "KeyN", mod, options).event.defaultPrevented).toBe(false);

    expect(press(document.body, "w", "KeyW", mod, options).event.defaultPrevented).toBe(false);
    // Beautify is bound only while a JSON body editor is shown.
    expect(press(document.body, "F", "KeyF", { ...mod, shiftKey: true }, options).event.defaultPrevented).toBe(false);
    const beautify = vi.fn();
    bindings.bind("request.beautify", beautify);
    expect(press(document.body, "F", "KeyF", { ...mod, shiftKey: true }, options).id).toBe("request.beautify");
    expect(beautify).toHaveBeenCalledTimes(1);
    // Inside a GraphQL query editor the same chord is the editor's Prettify (GQL-18).
    const graphql = mount(`<div data-shortcut-scope="graphql"><div contenteditable="true" id="query">q</div></div>`);
    const query = graphql.querySelector("#query")!;
    const inEditor = press(query, "F", "KeyF", { ...mod, shiftKey: true }, options);
    expect(inEditor.id).toBeNull();
    expect(inEditor.event.defaultPrevented).toBe(false);
    expect(beautify).toHaveBeenCalledTimes(1);
    bindings.bind("environment.switch", () => false);
    expect(press(document.body, "e", "KeyE", mod, options).event.defaultPrevented).toBe(false);
  });

  it("runs only inDialog shortcuts inside a dialog", () => {
    const { calls, options } = setup();
    const host = mount(`<div data-shortcut-scope="dialog"><button id="close">x</button></div>`);
    const button = host.querySelector("#close")!;
    expect(press(button, "w", "KeyW", mod, options).event.defaultPrevented).toBe(false);
    press(button, "/", "Slash", mod, options);
    expect(calls).toEqual(["help.toggle"]);
  });

  it("swallows key repeat without running the shortcut again", () => {
    const { calls, options } = setup();
    press(document.body, "w", "KeyW", mod, options);
    const { event } = press(document.body, "w", "KeyW", { ...mod, repeat: true }, options);
    expect(event.defaultPrevented).toBe(true);
    expect(calls).toEqual(["tab.close"]);
  });
});

describe("createShortcutBindings", () => {
  it("uses the latest binding and falls back when it is removed", () => {
    const bindings = createShortcutBindings();
    const a = vi.fn();
    const b = vi.fn();
    const event = new KeyboardEvent("keydown");
    const unbindA = bindings.bind("tab.close", a);
    const unbindB = bindings.bind("tab.close", b);
    bindings.get("tab.close")?.(event);
    expect([a.mock.calls.length, b.mock.calls.length]).toEqual([0, 1]);
    unbindB();
    unbindB(); // twice is harmless
    bindings.get("tab.close")?.(event);
    expect([a.mock.calls.length, b.mock.calls.length]).toEqual([1, 1]);
    unbindA();
    expect(bindings.get("tab.close")).toBeUndefined();
  });
});

const node = (id: string): TreeNode =>
  ({ id, kind: "request", parentId: null, name: id, path: `${id}.json`, fileKind: "http", readOnly: "", method: "GET" }) as TreeNode;

describe("installShortcuts", () => {
  function openTabs(ids: string[], active: string): Stores {
    const stores = createStores();
    stores.collection.dispatch({ type: "collection/loading", workspaceId: "ws" });
    stores.collection.dispatch({ type: "collection/load", workspaceId: "ws", nodes: ids.map(node), rootIds: ids, expandedIds: [] });
    stores.tabs.dispatch({ type: "tabs/restore", order: ids, activeId: active });
    return stores;
  }

  it.each([
    ["macOS", true, { metaKey: true }],
    ["Windows and Linux", false, { ctrlKey: true }],
  ] as const)("closes the active tab with the close shortcut on %s", (_name, mac, mods) => {
    const stores = openTabs(["a", "b", "c"], "b");
    const bindings = createShortcutBindings();
    cleanups.push(installShortcuts(stores, { bindings, mac }));

    const press = () => {
      const e = new KeyboardEvent("keydown", { key: "w", code: "KeyW", bubbles: true, cancelable: true, ...mods });
      document.body.dispatchEvent(e);
      return e;
    };
    expect(press().defaultPrevented).toBe(true);
    expect(stores.tabs.getState()).toMatchObject({ order: ["a", "c"], activeId: "c" });
    press();
    press();
    expect(stores.tabs.getState()).toMatchObject({ order: [], activeId: null });
    // With no tab open the key is still taken, so the window does not close.
    expect(press().defaultPrevented).toBe(true);
  });

  it("reopens the last closed tab with Mod+Shift+T", () => {
    const stores = openTabs(["a", "b", "c"], "b");
    const bindings = createShortcutBindings();
    cleanups.push(installShortcuts(stores, { bindings, mac: false }));
    const press = () => {
      const e = new KeyboardEvent("keydown", { key: "T", code: "KeyT", bubbles: true, cancelable: true, ctrlKey: true, shiftKey: true });
      document.body.dispatchEvent(e);
      return e;
    };
    // Nothing closed yet: the key is still taken.
    expect(press().defaultPrevented).toBe(true);
    closeTabs(stores, ["b"]);
    press();
    expect(stores.tabs.getState()).toMatchObject({ order: ["a", "b", "c"], activeId: "b" });
  });

  it("opens a new window with Mod+Shift+N (APP-06)", () => {
    const stores = openTabs(["a"], "a");
    const bindings = createShortcutBindings();
    cleanups.push(installShortcuts(stores, { bindings, mac: false }));
    const e = new KeyboardEvent("keydown", { key: "N", code: "KeyN", bubbles: true, cancelable: true, ctrlKey: true, shiftKey: true });
    document.body.dispatchEvent(e);
    expect(e.defaultPrevented).toBe(true);
    expect(windowService.NewWindow).toHaveBeenCalledTimes(1);
  });

  it("stops listening and unbinds when stopped", () => {
    const stores = openTabs(["a"], "a");
    const bindings = createShortcutBindings();
    const stop = installShortcuts(stores, { bindings, mac: false });
    expect(bindings.get("tab.close")).toBeDefined();
    stop();
    expect(bindings.get("tab.close")).toBeUndefined();
    document.body.dispatchEvent(new KeyboardEvent("keydown", { key: "w", code: "KeyW", bubbles: true, ctrlKey: true }));
    expect(stores.tabs.getState().order).toEqual(["a"]);
  });
});
