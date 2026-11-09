import { act, fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const http = vi.hoisted(() => ({
  ReadBody: vi.fn<(input: { bodyId: string; offset: number; charset: string; hex: boolean }) => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/http", () => ({
  HTTPService: http,
}));

import { EditorView } from "@codemirror/view";
import { httpResponse } from "../../test/httpResponse";
import { ResponseView } from "./ResponseView";

const MiB = 1024 * 1024;
const SIZE = 200 * MiB;

/** A 200 MiB text body of which the response carries the first MiB (HTTP-37). */
const big = () =>
  httpResponse({
    bodyId: "body_big",
    contentType: "text/plain",
    bodyLanguage: "text",
    body: "first page",
    bodyPretty: "",
    prettyState: "",
    bodyClipped: true,
    bodyShown: MiB,
    size: SIZE,
    bodySize: SIZE,
  });

const bodyText = () => (EditorView.findFromDOM(document.querySelector(".cm-editor") as HTMLElement) as EditorView).state.doc.toString();
const button = (name: string) => screen.getByRole("button", { name }) as HTMLButtonElement;
const click = async (name: string) => {
  await act(async () => {
    fireEvent.click(button(name));
    await Promise.resolve();
    await Promise.resolve();
  });
};

describe("large body pages (HTTP-37)", () => {
  beforeEach(() => {
    http.ReadBody.mockReset();
    http.ReadBody.mockImplementation(({ offset }) =>
      Promise.resolve({ text: `page@${offset}`, start: offset, end: Math.min(offset + MiB, SIZE), size: SIZE, binary: false }),
    );
  });

  it("shows where it is and reads the other pages from Go", async () => {
    render(<ResponseView response={big()} />);
    expect(screen.getByText(/Showing 0 B–1\.0 MB of 200\.0 MB/)).toBeTruthy();
    expect(button("Start").disabled).toBe(true);
    expect(bodyText()).toBe("first page");

    await click("Load more");
    expect(http.ReadBody).toHaveBeenLastCalledWith({ bodyId: "body_big", offset: MiB, charset: "UTF-8", hex: false });
    expect(bodyText()).toBe(`first pagepage@${MiB}`);
    expect(screen.getByText(/Showing 0 B–2\.0 MB of 200\.0 MB/)).toBeTruthy();

    await click("End");
    expect(http.ReadBody).toHaveBeenLastCalledWith(expect.objectContaining({ offset: SIZE - MiB }));
    expect(bodyText()).toBe(`page@${SIZE - MiB}`);
    expect(button("Next").disabled).toBe(true);

    await click("Previous");
    expect(http.ReadBody).toHaveBeenLastCalledWith(expect.objectContaining({ offset: SIZE - 2 * MiB }));

    await click("Start");
    expect(bodyText()).toBe("first page");
  });

  it("stops loading more at the cap and pages from there", async () => {
    render(<ResponseView response={big()} />);
    for (let i = 0; i < 7; i++) await click("Load more");
    expect(screen.getByText(/Showing 0 B–8\.0 MB/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Load more" })).toBeNull();
    await click("Next");
    expect(bodyText()).toBe(`page@${8 * MiB}`);
  });

  it("says why when Go no longer keeps the body", async () => {
    http.ReadBody.mockRejectedValueOnce({ kind: "not_found", code: "http.body_gone", message: "This response's body is no longer kept.", hint: "", details: {}, causes: [] });
    render(<ResponseView response={big()} />);
    await click("Next");
    expect(screen.getByText(/no longer kept/)).toBeTruthy();
    expect(bodyText()).toBe("first page");
  });

  it("has no pager for a body that came whole, or one Go does not keep", () => {
    const { rerender } = render(<ResponseView response={httpResponse({ bodyId: "body_small" })} />);
    expect(screen.queryByRole("button", { name: "Next" })).toBeNull();
    rerender(<ResponseView response={{ ...big(), bodyId: "" }} />);
    expect(screen.queryByRole("button", { name: "Next" })).toBeNull();
    expect(screen.getByText(/Only the first 1\.0 MB of the body are shown/)).toBeTruthy();
  });

  it("names the limit of a truncated body", () => {
    render(<ResponseView response={{ ...big(), bodyTruncated: true, bodyLimit: 512 * MiB }} />);
    expect(screen.getByText(/larger than the 512\.0 MB limit \(Settings › Network\)/)).toBeTruthy();
  });
});
