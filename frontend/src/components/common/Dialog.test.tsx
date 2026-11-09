import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { useRef, useState } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createConfirmQueue } from "../../commands/dialogs";
import { handleShortcutKeyDown } from "../../commands/shortcuts";
import { ConfirmHost, Dialog, type DialogProps } from "./Dialog";

function Harness({ startOpen = true, onCancel, ...props }: Partial<DialogProps> & { startOpen?: boolean }) {
  const [open, setOpen] = useState(startOpen);
  return (
    <div data-testid="app">
      <button type="button" onClick={() => setOpen(true)}>
        open
      </button>
      <Dialog
        title="New folder"
        confirmLabel="Create folder"
        {...props}
        open={open}
        onCancel={() => {
          onCancel?.();
          setOpen(false);
        }}
      />
    </div>
  );
}

const dialog = () => screen.queryByRole("dialog");
const button = (name: string) => within(dialog()!).getByRole("button", { name });

afterEach(() => {
  document.body.style.overflow = "";
});

describe("Dialog", () => {
  it("renders nothing when closed", () => {
    render(<Harness startOpen={false} onConfirm={vi.fn()} />);
    expect(dialog()).toBeNull();
  });

  it("is a labelled, described modal dialog in a portal on body", () => {
    render(
      <Harness onConfirm={vi.fn()} describedBy="desc">
        <p id="desc">Folders hold requests.</p>
      </Harness>,
    );
    const d = dialog()!;
    expect(d.getAttribute("aria-modal")).toBe("true");
    expect(document.getElementById(d.getAttribute("aria-labelledby")!)?.textContent).toBe("New folder");
    expect(d.getAttribute("aria-describedby")).toBe("desc");
    expect(screen.getByRole("dialog", { name: "New folder" })).toBe(d);
    // Portal: outside the app's tree, a direct child of body.
    expect(screen.getByTestId("app").contains(d)).toBe(false);
    expect(d.parentElement?.parentElement).toBe(document.body);
    expect(d.getAttribute("data-shortcut-scope")).toBe("dialog");
  });

  it("makes the rest of the page inert and locks scrolling while open, and undoes it on close", () => {
    const { container } = render(<Harness onConfirm={vi.fn()} />);
    expect(container.hasAttribute("inert")).toBe(true);
    expect(dialog()!.closest("[inert]")).toBeNull();
    expect(document.body.style.overflow).toBe("hidden");
    fireEvent.keyDown(dialog()!, { key: "Escape" });
    expect(dialog()).toBeNull();
    expect(container.hasAttribute("inert")).toBe(false);
    expect(document.body.style.overflow).toBe("");
  });

  it("uses the width prop, 440px by default", () => {
    const { unmount } = render(<Harness onConfirm={vi.fn()} />);
    expect(dialog()!.style.width).toBe("440px");
    unmount();
    render(<Harness onConfirm={vi.fn()} width={560} />);
    expect(dialog()!.style.width).toBe("560px");
  });

  describe("initial focus", () => {
    it("goes to the first field", () => {
      render(
        <Harness onConfirm={vi.fn()}>
          <input aria-label="Name" />
          <input aria-label="Other" />
        </Harness>,
      );
      expect(document.activeElement).toBe(screen.getByRole("textbox", { name: "Name" }));
    });

    it("goes to the confirm button without fields", () => {
      render(<Harness onConfirm={vi.fn()} />);
      expect(document.activeElement).toBe(button("Create folder"));
    });

    it("goes to Cancel in a danger dialog, even with fields", () => {
      render(
        <Harness onConfirm={vi.fn()} variant="danger">
          <input aria-label="Name" />
        </Harness>,
      );
      expect(document.activeElement).toBe(button("Cancel"));
    });

    it("goes to the initialFocus element when given", () => {
      function WithRef() {
        const ref = useRef<HTMLInputElement>(null);
        return (
          <Harness onConfirm={vi.fn()} initialFocus={ref}>
            <input aria-label="Name" />
            <input aria-label="Second" ref={ref} />
          </Harness>
        );
      }
      render(<WithRef />);
      expect(document.activeElement).toBe(screen.getByRole("textbox", { name: "Second" }));
    });

    it("goes to the close button in a dialog without a footer", () => {
      render(<Harness />);
      expect(document.activeElement).toBe(button("Close"));
    });
  });

  it("gives focus back to where it was when it closes", () => {
    render(<Harness startOpen={false} onConfirm={vi.fn()} />);
    const opener = screen.getByRole("button", { name: "open" });
    act(() => opener.focus());
    fireEvent.click(opener);
    expect(dialog()).toBeTruthy();
    expect(document.activeElement).not.toBe(opener);
    fireEvent.click(button("Cancel"));
    expect(dialog()).toBeNull();
    expect(document.activeElement).toBe(opener);
  });

  it("wraps Tab and Shift+Tab inside and pulls outside focus back in", () => {
    render(
      <Harness onConfirm={vi.fn()}>
        <input aria-label="Name" />
      </Harness>,
    );
    const close = button("Close");
    const name = screen.getByRole("textbox", { name: "Name" });
    const create = button("Create folder");
    act(() => create.focus());
    fireEvent.keyDown(create, { key: "Tab" });
    expect(document.activeElement).toBe(close);
    fireEvent.keyDown(close, { key: "Tab", shiftKey: true });
    expect(document.activeElement).toBe(create);
    // In the middle Tab is left to the browser.
    act(() => name.focus());
    const e = fireEvent.keyDown(name, { key: "Tab" });
    expect(e).toBe(true);

    act(() => screen.getByRole("button", { name: "open", hidden: true }).focus());
    expect(dialog()!.contains(document.activeElement)).toBe(true);
  });

  describe("keys", () => {
    it("cancels with Escape", () => {
      const onCancel = vi.fn();
      const onConfirm = vi.fn();
      render(
        <Harness onCancel={onCancel} onConfirm={onConfirm}>
          <input aria-label="Name" />
        </Harness>,
      );
      fireEvent.keyDown(screen.getByRole("textbox"), { key: "Escape" });
      expect(onCancel).toHaveBeenCalledTimes(1);
      expect(onConfirm).not.toHaveBeenCalled();
      expect(dialog()).toBeNull();
    });

    it("leaves an Escape a child handled alone", () => {
      const onCancel = vi.fn();
      render(
        <Harness onCancel={onCancel} onConfirm={vi.fn()}>
          <input aria-label="Name" onKeyDown={(e) => e.key === "Escape" && e.preventDefault()} />
        </Harness>,
      );
      fireEvent.keyDown(screen.getByRole("textbox"), { key: "Escape" });
      expect(onCancel).not.toHaveBeenCalled();
    });

    it("confirms with Enter in a field", () => {
      const onConfirm = vi.fn();
      render(
        <Harness onConfirm={onConfirm}>
          <input aria-label="Name" />
        </Harness>,
      );
      const e = fireEvent.keyDown(screen.getByRole("textbox"), { key: "Enter" });
      expect(onConfirm).toHaveBeenCalledTimes(1);
      expect(e).toBe(false);
    });

    it.each([
      ["in a text area", <textarea key="t" aria-label="Field" />],
      ["in a multi-line editor", <div key="c" aria-label="Field" role="textbox" contentEditable suppressContentEditableWarning />],
    ])("does not confirm with Enter %s", (_name, field) => {
      const onConfirm = vi.fn();
      render(<Harness onConfirm={onConfirm}>{field}</Harness>);
      fireEvent.keyDown(screen.getByRole("textbox", { name: "Field" }), { key: "Enter" });
      expect(onConfirm).not.toHaveBeenCalled();
    });

    it("does not confirm with Enter during IME composition, on key repeat or with modifiers", () => {
      const onConfirm = vi.fn();
      render(
        <Harness onConfirm={onConfirm}>
          <input aria-label="Name" />
        </Harness>,
      );
      const input = screen.getByRole("textbox");
      fireEvent.keyDown(input, { key: "Enter", isComposing: true });
      fireEvent.keyDown(input, { key: "Enter", keyCode: 229 });
      fireEvent.keyDown(input, { key: "Enter", repeat: true });
      fireEvent.keyDown(input, { key: "Enter", shiftKey: true });
      expect(onConfirm).not.toHaveBeenCalled();
      fireEvent.keyDown(input, { key: "Escape", isComposing: true });
      expect(dialog()).toBeTruthy();
    });

    it("leaves Enter on a button to the button", () => {
      const onConfirm = vi.fn();
      const onCancel = vi.fn();
      render(<Harness onConfirm={onConfirm} onCancel={onCancel} variant="danger" />);
      const e = fireEvent.keyDown(button("Cancel"), { key: "Enter" });
      expect(e).toBe(true);
      expect(onConfirm).not.toHaveBeenCalled();
    });

    it("does not confirm while the confirm button is disabled or busy", () => {
      const onConfirm = vi.fn();
      const onCancel = vi.fn();
      const { rerender } = render(
        <Harness onConfirm={onConfirm} onCancel={onCancel} confirmDisabled>
          <input aria-label="Name" />
        </Harness>,
      );
      fireEvent.keyDown(screen.getByRole("textbox"), { key: "Enter" });
      expect((button("Create folder") as HTMLButtonElement).disabled).toBe(true);
      expect(onConfirm).not.toHaveBeenCalled();

      rerender(
        <Harness onConfirm={onConfirm} onCancel={onCancel} busy>
          <input aria-label="Name" />
        </Harness>,
      );
      const create = button("Create folder");
      expect(create.getAttribute("aria-busy")).toBe("true");
      expect(create.getAttribute("aria-disabled")).toBe("true");
      fireEvent.keyDown(screen.getByRole("textbox"), { key: "Enter" });
      fireEvent.click(create);
      // Busy also blocks cancelling.
      fireEvent.keyDown(screen.getByRole("textbox"), { key: "Escape" });
      fireEvent.click(button("Cancel"));
      expect(onConfirm).not.toHaveBeenCalled();
      expect(onCancel).not.toHaveBeenCalled();
      expect(dialog()).toBeTruthy();
    });

    it("stops global shortcuts inside except the ones allowed in dialogs", () => {
      render(
        <Harness onConfirm={vi.fn()}>
          <input aria-label="Name" />
        </Harness>,
      );
      const bind = vi.fn();
      const bindings = { bind: () => () => {}, get: () => bind };
      const input = screen.getByRole("textbox");
      const sidebar = new KeyboardEvent("keydown", { key: "b", code: "KeyB", ctrlKey: true, bubbles: true });
      Object.defineProperty(sidebar, "target", { value: input });
      expect(handleShortcutKeyDown(sidebar, { bindings, mac: false })).toBeNull();
      const help = new KeyboardEvent("keydown", { key: "/", code: "Slash", ctrlKey: true, bubbles: true, cancelable: true });
      Object.defineProperty(help, "target", { value: input });
      expect(handleShortcutKeyDown(help, { bindings, mac: false })).toBe("help.toggle");
    });
  });

  describe("scrim", () => {
    it("cancels a default dialog", () => {
      const onCancel = vi.fn();
      render(<Harness onConfirm={vi.fn()} onCancel={onCancel} />);
      fireEvent.pointerDown(dialog()!);
      expect(onCancel).not.toHaveBeenCalled();
      fireEvent.pointerDown(dialog()!.parentElement!);
      expect(onCancel).toHaveBeenCalledTimes(1);
    });

    it("does nothing for a danger dialog", () => {
      const onCancel = vi.fn();
      render(<Harness onConfirm={vi.fn()} onCancel={onCancel} variant="danger" />);
      fireEvent.pointerDown(dialog()!.parentElement!);
      expect(onCancel).not.toHaveBeenCalled();
      expect(dialog()).toBeTruthy();
    });
  });

  it("draws the danger variant with the destructive button and its icon", () => {
    render(<Harness onConfirm={vi.fn()} variant="danger" confirmIcon={<svg data-testid="icon" />} />);
    const confirm = button("Create folder");
    expect(confirm.className).toMatch(/danger/);
    expect(within(confirm).getByTestId("icon")).toBeTruthy();
    expect(button("Cancel").className).toMatch(/secondary/);
  });

  it("has no footer without onConfirm", () => {
    render(<Harness />);
    expect(within(dialog()!).queryByRole("button", { name: "Cancel" })).toBeNull();
    fireEvent.click(button("Close"));
    expect(dialog()).toBeNull();
  });

  it("restores the outer dialog when a nested one closes", () => {
    function Nested() {
      const [inner, setInner] = useState(false);
      return (
        <Dialog open title="Outer" onCancel={() => {}}>
          <button type="button" onClick={() => setInner(true)}>
            more
          </button>
          <Dialog open={inner} title="Inner" onCancel={() => setInner(false)} />
        </Dialog>
      );
    }
    render(<Nested />);
    const more = screen.getByRole("button", { name: "more" });
    act(() => more.focus());
    fireEvent.click(more);
    const outer = screen.getByRole("dialog", { name: "Outer", hidden: true });
    expect(outer.closest("[inert]")).not.toBeNull();
    fireEvent.keyDown(screen.getByRole("dialog", { name: "Inner" }), { key: "Escape" });
    expect(screen.queryByRole("dialog", { name: "Inner" })).toBeNull();
    expect(outer.closest("[inert]")).toBeNull();
    expect(document.activeElement).toBe(more);
    expect(document.body.style.overflow).toBe("hidden");
  });
});

describe("confirm() and ConfirmHost", () => {
  it("resolves true on confirm and false on cancel, one request at a time", async () => {
    const queue = createConfirmQueue();
    render(<ConfirmHost queue={queue} />);

    let first: Promise<boolean> = Promise.resolve(false);
    let second: Promise<boolean> = Promise.resolve(true);
    act(() => {
      first = queue.request({ title: "Delete folder “Orders”?", message: "Its 3 requests move to the trash.", confirmLabel: "Delete folder", variant: "danger" });
      second = queue.request({ title: "Clear data?" });
    });
    const d = screen.getByRole("dialog", { name: "Delete folder “Orders”?" });
    expect(document.getElementById(d.getAttribute("aria-describedby")!)?.textContent).toBe("Its 3 requests move to the trash.");
    expect(document.activeElement).toBe(within(d).getByRole("button", { name: "Cancel" }));
    act(() => {
      fireEvent.click(within(d).getByRole("button", { name: "Delete folder" }));
    });
    await expect(first).resolves.toBe(true);

    const next = screen.getByRole("dialog", { name: "Clear data?" });
    expect(next.getAttribute("aria-describedby")).toBeNull();
    // Default labels; Enter confirms, Escape cancels.
    expect(within(next).getByRole("button", { name: "OK" })).toBeTruthy();
    act(() => {
      fireEvent.keyDown(next, { key: "Escape" });
    });
    await expect(second).resolves.toBe(false);
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("resolves false without a host, and for pending requests when the host unmounts", async () => {
    const queue = createConfirmQueue();
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    await expect(queue.request({ title: "Nobody asks" })).resolves.toBe(false);
    expect(error).toHaveBeenCalled();
    error.mockRestore();

    const { unmount } = render(<ConfirmHost queue={queue} />);
    let pending: Promise<boolean> = Promise.resolve(true);
    act(() => {
      pending = queue.request({ title: "Still there?" });
    });
    unmount();
    await expect(pending).resolves.toBe(false);
  });

  it("confirms with Enter", async () => {
    const queue = createConfirmQueue();
    render(<ConfirmHost queue={queue} />);
    let answer: Promise<boolean> = Promise.resolve(false);
    act(() => {
      answer = queue.request({ title: "Continue?" });
    });
    act(() => {
      fireEvent.keyDown(screen.getByRole("dialog"), { key: "Enter" });
    });
    await expect(answer).resolves.toBe(true);
  });
});
