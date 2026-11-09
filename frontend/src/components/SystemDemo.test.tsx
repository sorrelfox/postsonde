import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
// Wails' response to SystemService.Wait(-1), written by
// internal/app/wails_errors_test.go.
import waitInvalidBody from "../../../internal/app/testdata/wails/wait_invalid_argument.golden?raw";
import SystemDemo from "./SystemDemo";

const INFO_METHOD_ID = 1487803373;

interface RuntimeCall {
  args: { methodID: number; args?: unknown[] };
}

function json(body: string, status: number): Response {
  return new Response(body, { status, headers: { "Content-Type": "application/json" } });
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("SystemDemo", () => {
  it("shows the structured error for bad input", async () => {
    const fetchMock = vi.fn(async (_url: unknown, init?: RequestInit) => {
      const { args } = JSON.parse(String(init?.body)) as RuntimeCall;
      if (args.methodID === INFO_METHOD_ID) {
        return json(
          JSON.stringify({ name: "PostSonde", version: "dev", os: "darwin", arch: "arm64", goVersion: "go1.27.0", wailsVersion: "v3" }),
          200,
        );
      }
      return json(waitInvalidBody, 422);
    });
    vi.stubGlobal("fetch", fetchMock);

    render(<SystemDemo />);
    await screen.findByText("PostSonde dev on darwin/arm64");
    fireEvent.click(screen.getByRole("button", { name: "Bad input" }));

    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("Invalid input");
    expect(alert.textContent).toContain("invalid_argument · system.wait_out_of_range");
    expect(alert.textContent).toContain("The wait time must be between 0 and 60000 ms, not -1 ms.");
    expect(alert.textContent).toContain("Enter a value from 0 to 60000 ms.");
    expect(screen.getByRole("status").textContent).toBe("Failed");

    const waitCall = fetchMock.mock.calls
      .map(([, init]) => JSON.parse(String(init?.body)) as RuntimeCall)
      .find((c) => c.args.methodID !== INFO_METHOD_ID);
    expect(waitCall?.args.args).toEqual([-1]);
  });
});
