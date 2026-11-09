import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const service = vi.hoisted(() => ({
  Status: vi.fn<() => Promise<unknown>>(),
  Enable: vi.fn<(input: unknown) => Promise<unknown>>(),
  Lock: vi.fn<() => Promise<unknown>>(),
  ChangePassword: vi.fn<(input: unknown) => Promise<unknown>>(),
  Disable: vi.fn<(input: unknown) => Promise<unknown>>(),
  SetIdleLock: vi.fn<(minutes: number) => Promise<unknown>>(),
  SetKeyStore: vi.fn<(input: unknown) => Promise<unknown>>(),
  Reset: vi.fn<(input: unknown) => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/secrets", () => ({
  SecretsService: service,
}));

import { applySecretsStatus, getSecretsState, resetSecretsState } from "../../commands/secrets";
import { expectAccessible } from "../../test/a11y";
import { SecretEncryptionRows } from "./SecretEncryption";

const off = {
  available: true,
  enabled: false,
  locked: false,
  migration: "",
  required: false,
  idleLockMinutes: 0,
  idleLockLocked: false,
  kdf: "",
  iterations: 0,
  minPasswordLength: 8,
  problem: null,
  keychain: { mode: "password", modeLocked: false, available: false, name: "", pending: false, problem: null },
};
const keychain = { mode: "keychain", modeLocked: false, available: true, name: "macOS Keychain", pending: false, problem: null };
const unlocked = { ...off, enabled: true, kdf: "pbkdf2-hmac-sha256", iterations: 600000 };
const locked = { ...unlocked, locked: true };

beforeEach(() => {
  resetSecretsState();
  service.Status.mockReset().mockResolvedValue(off);
  service.Enable.mockReset().mockResolvedValue(unlocked);
  service.Lock.mockReset().mockResolvedValue(locked);
  service.ChangePassword.mockReset().mockResolvedValue(unlocked);
  service.Disable.mockReset().mockResolvedValue(off);
  service.SetIdleLock.mockReset();
  service.SetKeyStore.mockReset();
  service.Reset.mockReset();
});

afterEach(() => resetSecretsState());

describe("SecretEncryptionRows", () => {
  it("turns encryption on with a password typed twice", async () => {
    render(<SecretEncryptionRows />);
    expect(await screen.findByText(/^Off\. Passwords, tokens and keys/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Set master password…" }));
    const dialog = screen.getByRole("dialog", { name: "Set a master password" });
    const confirm = within(dialog).getByRole("button", { name: "Encrypt secrets" });
    const password = within(dialog).getByLabelText("Master password");
    const repeat = within(dialog).getByLabelText("Repeat the new password");

    expectAccessible();
    fireEvent.change(password, { target: { value: "short" } });
    expect(within(dialog).getByText("At least 8 characters.")).toBeTruthy();
    expect(password.getAttribute("aria-invalid")).toBe("true");
    expect(confirm.hasAttribute("disabled")).toBe(true);

    fireEvent.change(password, { target: { value: "long enough" } });
    fireEvent.change(repeat, { target: { value: "long enougx" } });
    expect(within(dialog).getByText("The passwords are not the same.")).toBeTruthy();
    expect(confirm.hasAttribute("disabled")).toBe(true);

    fireEvent.change(repeat, { target: { value: "long enough" } });
    expect(confirm.hasAttribute("disabled")).toBe(false);
    fireEvent.click(confirm);
    await waitFor(() => expect(service.Enable).toHaveBeenCalledWith({ password: "long enough" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(screen.getByText(/^On and unlocked\..*600000 iterations/)).toBeTruthy();
    expect(getSecretsState().status?.enabled).toBe(true);
  });

  it("locks now, changes the password and turns encryption off", async () => {
    service.Status.mockResolvedValue(unlocked);
    render(<SecretEncryptionRows />);
    fireEvent.click(await screen.findByRole("button", { name: "Lock now" }));
    await waitFor(() => expect(service.Lock).toHaveBeenCalled());
    expect(await screen.findByRole("button", { name: "Unlock…" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Change password…" })).toBeNull();

    // Unlocked again (as Go's event says it), then change the password.
    act(() => applySecretsStatus(unlocked));
    fireEvent.click(screen.getByRole("button", { name: "Change password…" }));
    let dialog = screen.getByRole("dialog", { name: "Change the master password" });
    service.ChangePassword.mockRejectedValueOnce(
      Object.assign(new Error("x"), {
        cause: { kind: "permission_denied", code: "secretstore.wrong_password", message: "The master password is not correct.", hint: "", details: {}, causes: [] },
      }),
    );
    fireEvent.change(within(dialog).getByLabelText("Current password"), { target: { value: "old password" } });
    fireEvent.change(within(dialog).getByLabelText("New password"), { target: { value: "new password" } });
    fireEvent.change(within(dialog).getByLabelText("Repeat the new password"), { target: { value: "new password" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Change password" }));
    expect((await within(dialog).findByRole("alert")).textContent).toContain("The master password is not correct.");
    fireEvent.click(within(dialog).getByRole("button", { name: "Change password" }));
    await waitFor(() =>
      expect(service.ChangePassword).toHaveBeenLastCalledWith({ oldPassword: "old password", newPassword: "new password" }),
    );
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());

    fireEvent.click(screen.getByRole("button", { name: "Turn off…" }));
    dialog = screen.getByRole("dialog", { name: "Turn off encryption" });
    fireEvent.change(within(dialog).getByLabelText("Master password"), { target: { value: "new password" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Turn off" }));
    await waitFor(() => expect(service.Disable).toHaveBeenCalledWith({ password: "new password" }));
    await waitFor(() => expect(getSecretsState().status?.enabled).toBe(false));
  });

  it("sets the idle lock, and follows the administrator's locks", async () => {
    service.Status.mockResolvedValue(unlocked);
    service.SetIdleLock.mockResolvedValue({ ...unlocked, idleLockMinutes: 15 });
    const { unmount } = render(<SecretEncryptionRows />);
    const select = (await screen.findByLabelText("Lock after")) as HTMLSelectElement;
    expect(select.value).toBe("0");
    expectAccessible();
    expect(Array.from(select.options).map((o) => o.textContent)).toEqual([
      "Never",
      "5 minutes",
      "15 minutes",
      "30 minutes",
      "1 hour",
      "4 hours",
    ]);
    fireEvent.change(select, { target: { value: "15" } });
    await waitFor(() => expect(service.SetIdleLock).toHaveBeenCalledWith(15));
    await waitFor(() => expect(select.value).toBe("15"));
    unmount();

    resetSecretsState();
    service.Status.mockResolvedValue({ ...unlocked, required: true, idleLockLocked: true, idleLockMinutes: 5 });
    render(<SecretEncryptionRows />);
    expect(await screen.findByText(/Your administrator requires a master password\./)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Turn off…" }).hasAttribute("disabled")).toBe(true);
    const lockedSelect = screen.getByLabelText(/Lock after/) as HTMLSelectElement;
    expect(lockedSelect.closest("fieldset")?.disabled).toBe(true);
  });

  it("keeps the key in the OS keychain or behind the master password only", async () => {
    service.Status.mockResolvedValue({ ...unlocked, keychain });
    service.SetKeyStore.mockResolvedValue({ ...unlocked, keychain: { ...keychain, mode: "password" } });
    render(<SecretEncryptionRows />);
    const select = (await screen.findByLabelText(/Unlock with/)) as HTMLSelectElement;
    expect(select.value).toBe("keychain");
    expect(screen.getByText(/The key is also kept in macOS Keychain/)).toBeTruthy();
    expect([...select.options].map((o) => o.textContent)).toEqual(["macOS Keychain or master password", "Master password only"]);
    expectAccessible();

    fireEvent.change(select, { target: { value: "password" } });
    await waitFor(() => expect(service.SetKeyStore).toHaveBeenCalledWith({ mode: "password" }));
    await waitFor(() => expect(select.value).toBe("password"));
    expect(screen.getByText(/Only the master password unlocks the secrets/)).toBeTruthy();

    // A keychain that refuses: its error, and the setting as it was.
    service.SetKeyStore.mockRejectedValueOnce(
      Object.assign(new Error("No Secret Service (keychain) is running in this session."), {
        cause: { kind: "unsupported", code: "keychain.unavailable", message: "No Secret Service (keychain) is running in this session.", hint: "", details: {}, causes: [] },
      }),
    );
    fireEvent.change(select, { target: { value: "keychain" } });
    expect((await screen.findByRole("alert")).textContent).toContain("No Secret Service");
  });

  it("says why the keychain could not be used, and that there is none", async () => {
    const problem = { kind: "permission_denied", code: "keychain.failed", message: "Access was denied.", hint: "", details: {}, causes: [] };
    service.Status.mockResolvedValue({ ...locked, keychain: { ...keychain, problem } });
    const { unmount } = render(<SecretEncryptionRows />);
    expect(await screen.findByText(/macOS Keychain could not be used: Access was denied\./)).toBeTruthy();
    unmount();
    resetSecretsState();
    service.Status.mockResolvedValue(locked);
    render(<SecretEncryptionRows />);
    const select = (await screen.findByLabelText(/Unlock with/)) as HTMLSelectElement;
    expect(select.disabled).toBe(true);
    expect([...select.options].map((o) => o.value)).toEqual(["password"]);
    expect(screen.getByText(/This computer has no keychain PostSonde can use/)).toBeTruthy();
  });

  it("offers the reset only while encryption is on", async () => {
    const { unmount } = render(<SecretEncryptionRows />);
    await screen.findByRole("button", { name: "Set master password…" });
    expect(screen.queryByRole("button", { name: "Delete encrypted secrets…" })).toBeNull();
    unmount();
    resetSecretsState();
    service.Status.mockResolvedValue(locked);
    render(<SecretEncryptionRows />);
    fireEvent.click(await screen.findByRole("button", { name: "Delete encrypted secrets…" }));
    expect(screen.getByRole("dialog", { name: "Delete the encrypted secrets?" })).toBeTruthy();
  });

  it("shows nothing without Go, and says so without a data directory", async () => {
    service.Status.mockRejectedValue(new Error("no backend"));
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const { container, unmount } = render(<SecretEncryptionRows />);
    await waitFor(() => expect(service.Status).toHaveBeenCalled());
    expect(container.textContent).toBe("");
    unmount();
    warn.mockRestore();
    service.Status.mockResolvedValue({ ...off, available: false });
    render(<SecretEncryptionRows />);
    expect(await screen.findByText(/Nothing is stored on this computer in this mode/)).toBeTruthy();
    expect(screen.queryByRole("button")).toBeNull();
  });
});
