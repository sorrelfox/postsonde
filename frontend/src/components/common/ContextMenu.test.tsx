import { act, fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { ContextMenu, type ContextMenuItem } from "./ContextMenu";

function Harness({ items }: { items: ContextMenuItem[] }) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button type="button" onClick={() => setOpen(true)}>
        Open
      </button>
      <span>outside</span>
      {open && <ContextMenu label="Actions" x={10} y={20} items={items} onClose={() => setOpen(false)} />}
    </>
  );
}

function setup() {
  const selected: string[] = [];
  const item = (id: string, label: string, disabled = false): ContextMenuItem => ({
    id,
    label,
    disabled,
    onSelect: () => selected.push(id),
  });
  const items = [item("copy", "Copy"), item("cut", "Cut", true), item("paste", "Paste"), item("delete", "Delete", false)];
  render(<Harness items={items} />);
  const opener = screen.getByRole("button", { name: "Open" });
  act(() => opener.focus());
  fireEvent.click(opener);
  return { selected, opener };
}

const active = () => (document.activeElement as HTMLElement).textContent;
const press = (key: string) => fireEvent.keyDown(document.activeElement as Element, { key });

describe("ContextMenu", () => {
  it("renders a menu with menuitems at the point, focusing the first enabled item", () => {
    setup();
    const menu = screen.getByRole("menu", { name: "Actions" });
    expect(menu.parentElement).toBe(document.body);
    expect(menu.style.left).toBe("10px");
    expect(screen.getAllByRole("menuitem").map((el) => el.textContent)).toEqual(["Copy", "Cut", "Paste", "Delete"]);
    expect(screen.getByRole("menuitem", { name: "Cut" }).getAttribute("aria-disabled")).toBe("true");
    expect(active()).toBe("Copy");
  });

  it("moves with the arrows (skipping disabled items, wrapping), Home, End and letters", () => {
    setup();
    press("ArrowDown");
    expect(active()).toBe("Paste");
    press("ArrowDown");
    press("ArrowDown");
    expect(active()).toBe("Copy");
    press("ArrowUp");
    expect(active()).toBe("Delete");
    press("Home");
    expect(active()).toBe("Copy");
    press("End");
    expect(active()).toBe("Delete");
    press("p");
    expect(active()).toBe("Paste");
    press("c"); // Cut is disabled
    expect(active()).toBe("Copy");
  });

  it("chooses with Enter or a click, closes and gives focus back", () => {
    const { selected, opener } = setup();
    press("ArrowDown");
    press("Enter");
    expect(selected).toEqual(["paste"]);
    expect(screen.queryByRole("menu")).toBeNull();
    expect(document.activeElement).toBe(opener);

    fireEvent.click(opener);
    fireEvent.click(screen.getByRole("menuitem", { name: "Cut" })); // disabled
    expect(selected).toEqual(["paste"]);
    fireEvent.click(screen.getByRole("menuitem", { name: "Delete" }));
    expect(selected).toEqual(["paste", "delete"]);
  });

  it("closes on Escape, Tab and a pointer down outside, without choosing", () => {
    const { selected, opener } = setup();
    press("Escape");
    expect(screen.queryByRole("menu")).toBeNull();
    expect(document.activeElement).toBe(opener);

    fireEvent.click(opener);
    press("Tab");
    expect(screen.queryByRole("menu")).toBeNull();

    fireEvent.click(opener);
    fireEvent.pointerDown(screen.getByText("outside"));
    expect(screen.queryByRole("menu")).toBeNull();
    expect(selected).toEqual([]);
  });

  it("closes when the window loses focus", () => {
    const onClose = vi.fn();
    render(<ContextMenu label="Actions" x={0} y={0} items={[]} onClose={onClose} />);
    fireEvent.blur(window);
    expect(onClose).toHaveBeenCalled();
  });
});
