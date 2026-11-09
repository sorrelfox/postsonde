import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const settings = vi.hoisted(() => ({
  CheckFileAccess: vi.fn<(path: string) => Promise<unknown>>(),
  AllowDirectory: vi.fn<(path: string) => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/settings", () => ({
  SettingsService: settings,
}));

import { settle } from "../../test/fakeWails";
import { FileAccessNotice, useFileAccessCheck } from "./FileAccessNotice";

/** A file picker that "picks" what the test passes. */
function Picker({ paths }: { paths: string[] }) {
  const access = useFileAccessCheck();
  return (
    <div>
      <button type="button" onClick={() => access.check(paths)}>
        Pick
      </button>
      <FileAccessNotice access={access} />
    </div>
  );
}

const outside = (folder: string, locked = false) => ({ allowed: false, folder, locked });
const allowed = { allowed: true, folder: "", locked: false };

async function pick(paths: string[]) {
  render(<Picker paths={paths} />);
  fireEvent.click(screen.getByRole("button", { name: "Pick" }));
  await act(settle);
}

beforeEach(() => {
  settings.CheckFileAccess.mockReset().mockResolvedValue(allowed);
  settings.AllowDirectory.mockReset().mockResolvedValue([]);
});

describe("FileAccessNotice (PRV-10)", () => {
  it("shows nothing for a file the project may read", async () => {
    await pick(["keys/private.pem"]);
    expect(settings.CheckFileAccess).toHaveBeenCalledWith("keys/private.pem");
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("offers to allow the folder of a file outside, and allows it", async () => {
    settings.CheckFileAccess.mockResolvedValue(outside("/Users/ann/keys"));
    await pick(["/Users/ann/keys/private.pem"]);
    const status = screen.getByRole("status");
    expect(status.textContent).toContain("“private.pem” is outside the directories allowed for file access");
    const allow = screen.getByRole("button", { name: "Allow “keys”" });
    expect(allow.getAttribute("title")).toContain("/Users/ann/keys");
    fireEvent.click(allow);
    await act(settle);
    expect(settings.AllowDirectory).toHaveBeenCalledWith("/Users/ann/keys");
    expect(status.textContent).toContain("“keys” is allowed now.");
    expect(screen.queryByRole("button", { name: "Allow “keys”" })).toBeNull();
  });

  it("groups the files of one folder, and dismisses", async () => {
    settings.CheckFileAccess.mockImplementation(async (path) =>
      path.startsWith("/protos/") ? outside("/protos") : path.startsWith("/vendor/") ? outside("/vendor") : allowed,
    );
    await pick(["/protos/a.proto", "/protos/b.proto", "api/c.proto", "/vendor/d.proto"]);
    const status = screen.getByRole("status");
    expect(status.textContent).toContain("2 chosen files are outside");
    expect(status.textContent).toContain("“d.proto” is outside");
    expect(screen.getByRole("button", { name: "Allow “protos”" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Allow “vendor”" })).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Dismiss" }));
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("says the list is managed when an administrator locks it, without an offer", async () => {
    settings.CheckFileAccess.mockResolvedValue(outside("/etc/certs", true));
    await pick(["/etc/certs/server.pem"]);
    const status = screen.getByRole("status");
    expect(status.textContent).toContain("“server.pem” is outside");
    expect(status.textContent).toContain("managed by your administrator");
    expect(screen.queryByRole("button", { name: /^Allow/ })).toBeNull();
  });

  it("shows why allowing failed", async () => {
    settings.CheckFileAccess.mockResolvedValue(outside("/Volumes/usb"));
    settings.AllowDirectory.mockRejectedValue({
      kind: "permission_denied",
      code: "settings.locked",
      message: "This setting is managed by your administrator.",
    });
    await pick(["/Volumes/usb/key.pem"]);
    fireEvent.click(screen.getByRole("button", { name: "Allow “usb”" }));
    await waitFor(() => expect(screen.getByRole("status").textContent).toContain("managed by your administrator"));
    expect(screen.getByRole("button", { name: "Allow “usb”" })).toBeTruthy();
  });

  it("shows nothing without a Go backend", async () => {
    settings.CheckFileAccess.mockRejectedValue(new Error("no backend"));
    await pick(["/x/y.pem"]);
    expect(screen.queryByRole("status")).toBeNull();
  });
});
