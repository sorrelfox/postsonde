import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { SplitPane, type SplitPaneProps } from "./SplitPane";

// jsdom has no layout: the container reports the size set here, the handle 0.
let size = { width: 1000, height: 600 };

function rect(width: number, height: number): DOMRect {
  return { x: 0, y: 0, left: 0, top: 0, right: width, bottom: height, width, height, toJSON: () => ({}) };
}

// Animation frames and ResizeObserver, driven by the tests.
let frames: FrameRequestCallback[] = [];
const flushFrames = () =>
  act(() => {
    const pending = frames;
    frames = [];
    pending.forEach((cb) => cb(0));
  });

let observers: Array<() => void> = [];
const resizeTo = (width: number, height = size.height) =>
  act(() => {
    size = { width, height };
    observers.forEach((cb) => cb());
  });

beforeEach(() => {
  size = { width: 1000, height: 600 };
  frames = [];
  observers = [];
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    return this.hasAttribute("data-split-pane") ? rect(size.width, size.height) : rect(0, 0);
  });
  vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => frames.push(cb));
  vi.stubGlobal("cancelAnimationFrame", (id: number) => {
    frames.splice(id - 1, 1, () => {});
  });
  vi.stubGlobal(
    "ResizeObserver",
    class {
      constructor(private readonly cb: () => void) {}
      observe() {
        observers.push(this.cb);
      }
      disconnect() {
        observers = observers.filter((cb) => cb !== this.cb);
      }
    },
  );
});

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

type Props = Partial<Omit<SplitPaneProps, "children">>;

/** A parent that owns the ratio, like WorkArea does through the store. */
function renderSplit(props: Props = {}) {
  const onRatioChange = vi.fn();
  const onRatioCommit = vi.fn();
  function Parent() {
    const [ratio, setRatio] = useState(props.ratio ?? 0.5);
    return (
      <SplitPane
        {...props}
        ratio={ratio}
        onRatioChange={onRatioChange}
        onRatioCommit={(r) => {
          onRatioCommit(r);
          setRatio(r);
        }}
      >
        {[<p key="a">first pane</p>, <p key="b">second pane</p>]}
      </SplitPane>
    );
  }
  render(<Parent />);
  const handle = screen.getByRole("separator");
  const container = handle.parentElement as HTMLElement;
  return { handle, container, onRatioChange, onRatioCommit };
}

const valueNow = (el: HTMLElement) => el.getAttribute("aria-valuenow");
const cssRatio = (el: HTMLElement) => el.style.getPropertyValue("--split-ratio");

describe("SplitPane", () => {
  it("renders an accessible separator between the panes", () => {
    const { handle, container } = renderSplit({ ratio: 0.3, minRatio: 0.2, maxRatio: 0.8, label: "Resize" });
    expect(handle.getAttribute("aria-label")).toBe("Resize");
    expect(handle.getAttribute("aria-orientation")).toBe("vertical");
    expect(handle.tabIndex).toBe(0);
    expect(valueNow(handle)).toBe("30");
    expect(handle.getAttribute("aria-valuemin")).toBe("20");
    expect(handle.getAttribute("aria-valuemax")).toBe("80");
    expect(cssRatio(container)).toBe("0.3");
    const controlled = document.getElementById(handle.getAttribute("aria-controls") ?? "");
    expect(controlled?.textContent).toBe("first pane");
    expect(container.textContent).toBe("first panesecond pane");
  });

  it("steps with the arrow keys and jumps with Home, End and Enter", () => {
    const { handle, container, onRatioChange, onRatioCommit } = renderSplit({ minRatio: 0.2, maxRatio: 0.8 });
    fireEvent.keyDown(handle, { key: "ArrowRight" });
    expect(valueNow(handle)).toBe("52");
    expect(cssRatio(container)).toBe("0.52");
    fireEvent.keyDown(handle, { key: "ArrowRight", shiftKey: true });
    expect(valueNow(handle)).toBe("62");
    fireEvent.keyDown(handle, { key: "ArrowLeft" });
    expect(valueNow(handle)).toBe("60");
    fireEvent.keyDown(handle, { key: "Home" });
    expect(valueNow(handle)).toBe("20");
    fireEvent.keyDown(handle, { key: "End" });
    expect(valueNow(handle)).toBe("80");
    fireEvent.keyDown(handle, { key: "Enter" });
    expect(valueNow(handle)).toBe("50");
    expect(onRatioCommit.mock.calls.map(([r]) => r)).toEqual([0.52, 0.62, 0.6, 0.2, 0.8, 0.5]);
    expect(onRatioChange).toHaveBeenCalledTimes(6);
  });

  it("clamps keyboard steps and commits nothing at the limits", () => {
    const { handle, onRatioCommit } = renderSplit({ ratio: 0.75, minRatio: 0.2, maxRatio: 0.8 });
    fireEvent.keyDown(handle, { key: "ArrowRight", shiftKey: true });
    expect(valueNow(handle)).toBe("80");
    fireEvent.keyDown(handle, { key: "ArrowRight" });
    fireEvent.keyDown(handle, { key: "End" });
    expect(onRatioCommit).toHaveBeenCalledTimes(1);
    fireEvent.keyDown(handle, { key: "Home" });
    fireEvent.keyDown(handle, { key: "ArrowLeft" });
    expect(valueNow(handle)).toBe("20");
    expect(onRatioCommit).toHaveBeenCalledTimes(2);
  });

  it("uses the up and down arrows when stacked", () => {
    const { handle, onRatioCommit } = renderSplit({ orientation: "vertical" });
    expect(handle.getAttribute("aria-orientation")).toBe("horizontal");
    fireEvent.keyDown(handle, { key: "ArrowRight" });
    expect(onRatioCommit).not.toHaveBeenCalled();
    fireEvent.keyDown(handle, { key: "ArrowDown" });
    expect(valueNow(handle)).toBe("52");
    fireEvent.keyDown(handle, { key: "ArrowUp", shiftKey: true });
    expect(valueNow(handle)).toBe("42");
  });

  it("resets to the default ratio on double-click", () => {
    const { handle, onRatioCommit } = renderSplit({ ratio: 0.7 });
    fireEvent.doubleClick(handle);
    expect(valueNow(handle)).toBe("50");
    expect(onRatioCommit).toHaveBeenCalledWith(0.5);
  });

  it("follows a pointer drag once per frame and commits once, on pointerup", () => {
    const { handle, container, onRatioChange, onRatioCommit } = renderSplit();
    fireEvent.pointerDown(handle, { pointerId: 1, button: 0, clientX: 500 });
    expect(container.hasAttribute("data-dragging")).toBe(true);
    expect(document.activeElement).toBe(handle);

    fireEvent.pointerMove(handle, { pointerId: 1, clientX: 600 });
    expect(valueNow(handle)).toBe("50"); // nothing until the frame runs
    flushFrames();
    expect(valueNow(handle)).toBe("60");
    expect(cssRatio(container)).toBe("0.6");

    // Two moves in one frame: one update, with the latest position.
    fireEvent.pointerMove(handle, { pointerId: 1, clientX: 680 });
    fireEvent.pointerMove(handle, { pointerId: 1, clientX: 750 });
    flushFrames();
    expect(cssRatio(container)).toBe("0.75");
    expect(onRatioChange.mock.calls.map(([r]) => r)).toEqual([0.6, 0.75]);
    expect(onRatioCommit).not.toHaveBeenCalled();

    fireEvent.pointerMove(handle, { pointerId: 1, clientX: 700 });
    fireEvent.pointerUp(handle, { pointerId: 1, clientX: 700 });
    fireEvent.lostPointerCapture(handle, { pointerId: 1 });
    expect(onRatioCommit).toHaveBeenCalledTimes(1);
    expect(onRatioCommit).toHaveBeenCalledWith(0.7);
    expect(valueNow(handle)).toBe("70");
    expect(container.hasAttribute("data-dragging")).toBe(false);
  });

  it("ignores a click without movement and other pointers", () => {
    const { handle, onRatioCommit } = renderSplit();
    fireEvent.pointerDown(handle, { pointerId: 1, button: 0, clientX: 500 });
    fireEvent.pointerMove(handle, { pointerId: 2, clientX: 900 });
    flushFrames();
    fireEvent.pointerUp(handle, { pointerId: 1, clientX: 500 });
    expect(onRatioCommit).not.toHaveBeenCalled();

    fireEvent.pointerDown(handle, { pointerId: 1, button: 2, clientX: 500 });
    fireEvent.pointerMove(handle, { pointerId: 1, clientX: 900 });
    flushFrames();
    expect(valueNow(handle)).toBe("50");
  });

  it("reverts a cancelled drag without committing", () => {
    const { handle, container, onRatioCommit } = renderSplit();
    fireEvent.pointerDown(handle, { pointerId: 1, button: 0, clientX: 500 });
    fireEvent.pointerMove(handle, { pointerId: 1, clientX: 800 });
    flushFrames();
    expect(valueNow(handle)).toBe("80");
    fireEvent.pointerCancel(handle, { pointerId: 1 });
    expect(onRatioCommit).not.toHaveBeenCalled();
    expect(valueNow(handle)).toBe("50");
    expect(cssRatio(container)).toBe("0.5");
  });

  it("keeps both panes at their minimum size while dragging", () => {
    const { handle, onRatioCommit } = renderSplit({ minStart: 300, minEnd: 200 });
    expect(handle.getAttribute("aria-valuemin")).toBe("30");
    expect(handle.getAttribute("aria-valuemax")).toBe("80");
    fireEvent.pointerDown(handle, { pointerId: 1, button: 0, clientX: 500 });
    fireEvent.pointerMove(handle, { pointerId: 1, clientX: 0 });
    fireEvent.pointerUp(handle, { pointerId: 1, clientX: 0 });
    expect(onRatioCommit).toHaveBeenLastCalledWith(0.3);
    fireEvent.pointerDown(handle, { pointerId: 1, button: 0, clientX: 300 });
    fireEvent.pointerMove(handle, { pointerId: 1, clientX: 2000 });
    fireEvent.pointerUp(handle, { pointerId: 1, clientX: 2000 });
    expect(onRatioCommit).toHaveBeenLastCalledWith(0.8);
  });

  it("re-clamps to the minimum sizes when the container is small or shrinks", () => {
    size = { width: 500, height: 600 };
    const { handle, container, onRatioCommit } = renderSplit({ ratio: 0.2, minStart: 200, minEnd: 200 });
    // 200px of 500 is 40%: the stored 20% is shown as 40%, not committed.
    expect(valueNow(handle)).toBe("40");
    expect(handle.getAttribute("aria-valuemin")).toBe("40");
    expect(handle.getAttribute("aria-valuemax")).toBe("60");

    resizeTo(1000);
    expect(valueNow(handle)).toBe("20");
    expect(cssRatio(container)).toBe("0.2");
    expect(handle.getAttribute("aria-valuemin")).toBe("20");

    resizeTo(400);
    expect(valueNow(handle)).toBe("50");
    expect(handle.getAttribute("aria-valuemax")).toBe("50");

    // Too small for both minimums: shared in proportion to them.
    resizeTo(300);
    expect(valueNow(handle)).toBe("50");
    expect(onRatioCommit).not.toHaveBeenCalled();
  });
});
