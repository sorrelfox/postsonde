import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { createPromptQueue } from "../../commands/prompts";
import { PromptHost } from "./PromptHost";

const dialog = () => screen.queryByRole("dialog");

afterEach(() => {
  document.body.style.overflow = "";
});

describe("PromptHost (DYN-07)", () => {
  it("shows one field per value, with defaults and secret fields masked, and answers with the values", async () => {
    const queue = createPromptQueue();
    render(<PromptHost queue={queue} />);
    expect(dialog()).toBeNull();

    let answer!: Promise<Record<string, string> | null>;
    act(() => {
      answer = queue.request({
        fields: [
          { label: "API token", default: "", secret: true },
          { label: "Item id", default: "42", secret: false },
        ],
      });
    });
    const d = dialog()!;
    expect(within(d).getByRole("heading").textContent).toBe("Values for this send");
    expect(d.textContent).toContain("not saved");
    const token = within(d).getByLabelText("API token") as HTMLInputElement;
    const id = within(d).getByLabelText("Item id") as HTMLInputElement;
    expect(token.type).toBe("password");
    expect(id.type).toBe("text");
    expect(id.value).toBe("42");
    expect(document.activeElement).toBe(token);

    fireEvent.change(token, { target: { value: "t0k" } });
    fireEvent.keyDown(token, { key: "Enter" });
    await expect(answer).resolves.toEqual({ "API token": "t0k", "Item id": "42" });
    expect(dialog()).toBeNull();
  });

  it("cancels with Esc or Cancel, and says a run asks once", async () => {
    const queue = createPromptQueue();
    render(<PromptHost queue={queue} />);

    let answer!: Promise<Record<string, string> | null>;
    act(() => {
      answer = queue.request({ fields: [{ label: "Token", default: "", secret: false }], purpose: "run" });
    });
    expect(within(dialog()!).getByRole("heading").textContent).toBe("Values for this run");
    expect(within(dialog()!).getByRole("button", { name: "Start" })).toBeTruthy();
    fireEvent.change(within(dialog()!).getByLabelText("Token"), { target: { value: "typed" } });
    fireEvent.keyDown(dialog()!, { key: "Escape" });
    await expect(answer).resolves.toBeNull();
    expect(dialog()).toBeNull();

    act(() => {
      answer = queue.request({ fields: [{ label: "Token", default: "", secret: false }] });
    });
    // A new request starts empty: nothing typed before is kept.
    expect((within(dialog()!).getByLabelText("Token") as HTMLInputElement).value).toBe("");
    fireEvent.click(within(dialog()!).getByRole("button", { name: "Cancel" }));
    await expect(answer).resolves.toBeNull();
  });

  it("treats a request without a mounted host as cancelled", async () => {
    const queue = createPromptQueue();
    const errorSpy = console.error;
    console.error = () => {};
    try {
      await expect(queue.request({ fields: [] })).resolves.toBeNull();
    } finally {
      console.error = errorSpy;
    }
  });
});
