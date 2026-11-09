import { describe, expect, it } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { KeyValuePreview } from "./KeyValuePreview";

describe("KeyValuePreview", () => {
  it("shows params and headers tables with sample data", () => {
    render(<KeyValuePreview />);
    expect(screen.getByRole("grid", { name: "Query params" })).toBeTruthy();
    expect(screen.getByRole("grid", { name: "Headers" })).toBeTruthy();
    expect((screen.getAllByRole("textbox", { name: "Key, row 1" })[0] as HTMLInputElement).value).toBe("limit");
    expect(screen.getByText("{{token}}").tagName).toBe("SPAN");
    expect(screen.getByRole("heading", { name: "Params · 2" })).toBeTruthy();
  });

  it("updates the enabled count when a row is toggled", () => {
    render(<KeyValuePreview />);
    fireEvent.click(screen.getAllByRole("checkbox", { name: "Enabled, row 3" })[0]);
    expect(screen.getByRole("heading", { name: "Params · 3" })).toBeTruthy();
  });
});
