import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { installShortcuts } from "../../commands/shortcuts";
import { createToaster, DEFAULT_TOAST_DURATION_MS, MAX_TOASTS, type Toaster } from "../../commands/toasts";
import { createStores } from "../../stores";
import { ToastHost } from "./Toast";

let toaster: Toaster;

beforeEach(() => {
  vi.useFakeTimers();
  toaster = createToaster();
});

afterEach(() => {
  vi.useRealTimers();
});

function setup() {
  return render(
    <>
      <button type="button">outside</button>
      <ToastHost toaster={toaster} />
    </>,
  );
}

const show = (...args: Parameters<Toaster["show"]>) => {
  let id = "";
  act(() => {
    id = toaster.show(...args);
  });
  return id;
};
const advance = (ms: number) =>
  act(() => {
    vi.advanceTimersByTime(ms);
  });
const messages = () => [...document.querySelectorAll("[data-toast-id]")].map((t) => t.querySelector("span[id]")?.textContent);

describe("ToastHost", () => {
  it("announces toasts in a polite live region that is always there", () => {
    const { container } = setup();
    const live = container.querySelector("[aria-live]")!;
    expect(live.getAttribute("aria-live")).toBe("polite");
    expect(screen.queryByRole("region", { name: "Notifications" })).toBeNull();
    show({ message: "Deleted “Cancel order”" });
    expect(container.querySelector("[aria-live]")).toBe(live);
    expect(live.textContent).toContain("Deleted “Cancel order”");
    expect(screen.getByRole("region", { name: "Notifications" })).toBeTruthy();
  });

  it("shows the icon, the action, the shortcut hint and a dismiss button", () => {
    setup();
    const run = vi.fn();
    show({ message: "Deleted “Cancel order”", icon: <svg data-testid="icon" />, action: { label: "Undo", run }, shortcut: "⌘Z" });
    const region = screen.getByRole("region", { name: "Notifications" });
    expect(within(region).getByTestId("icon")).toBeTruthy();
    const undo = within(region).getByRole("button", { name: "Undo" });
    expect(document.getElementById(undo.getAttribute("aria-describedby")!)?.textContent).toBe("Deleted “Cancel order”");
    const kbd = region.querySelector("kbd")!;
    expect(kbd.textContent).toBe("⌘Z");
    expect(kbd.getAttribute("aria-hidden")).toBe("true");
    expect(within(region).getByRole("button", { name: "Dismiss notification" })).toBeTruthy();
  });

  it("dismisses itself after the duration, 8 s by default", () => {
    setup();
    show({ message: "default" });
    show({ message: "short", duration: 2000 });
    show({ message: "sticky", duration: 0 });
    advance(1999);
    expect(messages()).toEqual(["default", "short", "sticky"]);
    advance(1);
    expect(messages()).toEqual(["default", "sticky"]);
    advance(DEFAULT_TOAST_DURATION_MS - 2000);
    expect(messages()).toEqual(["sticky"]);
    advance(60_000);
    expect(messages()).toEqual(["sticky"]);
    // A sticky toast has no countdown bar.
    expect(document.querySelector("[data-toast-id] [aria-hidden] > div")).toBeNull();
  });

  it("pauses the countdown while the pointer is over the toasts", () => {
    setup();
    show({ message: "hover me", duration: 5000 });
    advance(3000);
    const region = screen.getByRole("region", { name: "Notifications" });
    fireEvent.pointerEnter(region);
    advance(10_000);
    expect(messages()).toEqual(["hover me"]);
    fireEvent.pointerLeave(region);
    advance(1999);
    expect(messages()).toEqual(["hover me"]);
    advance(1);
    expect(messages()).toEqual([]);
  });

  it("pauses the countdown while focus is in the toasts", () => {
    setup();
    show({ message: "focus me", duration: 5000 });
    const dismiss = screen.getByRole("button", { name: "Dismiss notification" });
    act(() => dismiss.focus());
    advance(10_000);
    expect(messages()).toEqual(["focus me"]);
    act(() => screen.getByRole("button", { name: "outside" }).focus());
    advance(4999);
    expect(messages()).toEqual(["focus me"]);
    advance(1);
    expect(messages()).toEqual([]);
  });

  it("runs the action once and dismisses the toast", () => {
    setup();
    const run = vi.fn();
    show({ message: "Deleted", action: { label: "Undo", run } });
    fireEvent.click(screen.getByRole("button", { name: "Undo" }));
    expect(run).toHaveBeenCalledTimes(1);
    expect(messages()).toEqual([]);
  });

  it("dismisses with the dismiss button and with dismiss(id)", () => {
    setup();
    const a = show({ message: "a" });
    show({ message: "b" });
    act(() => toaster.dismiss(a));
    expect(messages()).toEqual(["b"]);
    fireEvent.click(screen.getByRole("button", { name: "Dismiss notification" }));
    expect(messages()).toEqual([]);
  });

  it(`keeps at most ${MAX_TOASTS}, newest at the bottom`, () => {
    setup();
    for (const m of ["1", "2", "3", "4"]) show({ message: m });
    expect(messages()).toEqual(["2", "3", "4"]);
  });

  it("focuses the newest toast with F6 and gives focus back when it goes away", () => {
    const stop = installShortcuts(createStores());
    try {
      setup();
      const outside = screen.getByRole("button", { name: "outside" });
      act(() => outside.focus());
      // Nothing to focus: F6 goes through.
      expect(fireEvent.keyDown(outside, { key: "F6" })).toBe(true);

      show({ message: "older" });
      show({ message: "newer", action: { label: "Undo", run: () => {} } });
      fireEvent.keyDown(outside, { key: "F6" });
      const undo = screen.getByRole("button", { name: "Undo" });
      expect(document.activeElement).toBe(undo);

      // The focused toast goes: focus moves to the next one…
      fireEvent.click(undo);
      expect(document.activeElement).toBe(screen.getByRole("button", { name: "Dismiss notification" }));
      // …and back to where it came from after the last.
      fireEvent.click(document.activeElement!);
      expect(messages()).toEqual([]);
      expect(document.activeElement).toBe(outside);
    } finally {
      stop();
    }
  });

  it("clears the toasts when it unmounts", () => {
    const { unmount } = setup();
    show({ message: "bye" });
    unmount();
    expect(toaster.toasts()).toEqual([]);
  });
});

describe("createToaster", () => {
  it("returns ids, ignores unknown ids and reports failing actions", async () => {
    const t = createToaster();
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const id = t.show({ message: "x", action: { label: "Undo", run: () => Promise.reject(new Error("boom")) } });
    expect(t.toasts()[0]).toMatchObject({ id, duration: DEFAULT_TOAST_DURATION_MS });
    t.dismiss("nope");
    t.runAction("nope");
    expect(t.toasts()).toHaveLength(1);
    t.runAction(id);
    expect(t.toasts()).toEqual([]);
    await vi.runAllTimersAsync();
    expect(warn).toHaveBeenCalled();
    warn.mockRestore();
  });
});
