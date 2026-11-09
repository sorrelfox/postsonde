import { act, fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const http = vi.hoisted(() => ({
  SaveBody: vi.fn<(input: { bodyId: string }) => Promise<unknown>>(),
  SaveExchange: vi.fn<(input: { bodyId: string; name: string }) => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/http", () => ({
  HTTPService: http,
}));

import { httpResponse } from "../../test/httpResponse";
import { ResponseView } from "./ResponseView";

const saveButton = () => screen.queryByRole("button", { name: "Save the body to a file" });
const exchangeButton = () => screen.queryByRole("button", { name: "Save the exchange as HAR" });

describe("save body to file (HTTP-34)", () => {
  beforeEach(() => {
    http.SaveBody.mockReset();
  });

  it("asks Go to save the kept body and says where it went", async () => {
    http.SaveBody.mockResolvedValue({ saved: true, path: "/tmp/out/report.pdf", size: 3 * 1024 * 1024 });
    render(<ResponseView response={httpResponse({ bodyId: "body_1", fileName: "report.pdf", size: 3 * 1024 * 1024 })} />);
    const button = saveButton();
    expect(button?.getAttribute("title")).toContain("as report.pdf");
    await act(async () => {
      fireEvent.click(button as HTMLElement);
      await Promise.resolve();
    });
    expect(http.SaveBody).toHaveBeenCalledWith({ bodyId: "body_1" });
    expect(screen.getByRole("status").textContent).toBe("Saved 3.0 MB to /tmp/out/report.pdf");
  });

  it("shows nothing when the dialog is cancelled, and Go's error when saving fails", async () => {
    http.SaveBody.mockResolvedValueOnce({ saved: false, path: "", size: 0 });
    render(<ResponseView response={httpResponse({ bodyId: "body_2" })} />);
    await act(async () => {
      fireEvent.click(saveButton() as HTMLElement);
      await Promise.resolve();
    });
    expect(screen.queryByRole("status")).toBeNull();
    expect(screen.queryByRole("alert")).toBeNull();

    http.SaveBody.mockRejectedValueOnce({
      kind: "not_found",
      code: "http.body_gone",
      message: "This response's body is no longer kept.",
      hint: "Send the request again to save its body.",
      details: {},
      causes: [],
    });
    await act(async () => {
      fireEvent.click(saveButton() as HTMLElement);
      await Promise.resolve();
    });
    expect(screen.getByRole("alert").textContent).toBe("This response's body is no longer kept. Send the request again to save its body.");
  });

  it("has no save button without a kept body or for an empty body", () => {
    const { rerender } = render(<ResponseView response={httpResponse({ bodyId: "" })} />);
    expect(saveButton()).toBeNull();
    rerender(<ResponseView response={httpResponse({ bodyId: "body_3", body: "", size: 0 })} />);
    expect(saveButton()).toBeNull();
  });
});

describe("save the exchange (EXP-06)", () => {
  beforeEach(() => {
    http.SaveExchange.mockReset();
  });

  it("asks Go to save the request and the whole response, named after the request, and says it was masked", async () => {
    http.SaveExchange.mockResolvedValue({ saved: true, path: "/tmp/out/List items.har", size: 2048, masked: true });
    render(<ResponseView response={httpResponse({ bodyId: "body_1" })} name="List items" />);
    await act(async () => {
      fireEvent.click(exchangeButton() as HTMLElement);
      await Promise.resolve();
    });
    expect(http.SaveExchange).toHaveBeenCalledWith({ bodyId: "body_1", name: "List items" });
    expect(screen.getByRole("status").textContent).toBe("Saved 2.0 KB to /tmp/out/List items.har, secrets masked");
  });

  it("is offered for an empty body too, and not without a kept response", () => {
    const { rerender } = render(<ResponseView response={httpResponse({ bodyId: "body_2", body: "", size: 0 })} />);
    expect(exchangeButton()).not.toBeNull();
    rerender(<ResponseView response={httpResponse({ bodyId: "" })} />);
    expect(exchangeButton()).toBeNull();
  });
});
