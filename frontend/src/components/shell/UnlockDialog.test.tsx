import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const service = vi.hoisted(() => ({
  Status: vi.fn<() => Promise<unknown>>(),
  Unlock: vi.fn<(input: unknown) => Promise<unknown>>(),
  UnlockWithKeychain: vi.fn<() => Promise<unknown>>(),
  Lock: vi.fn<() => Promise<unknown>>(),
  Enable: vi.fn<(input: unknown) => Promise<unknown>>(),
  Reset: vi.fn<(input: unknown) => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/secrets", () => ({
  SecretsService: service,
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/environments", () => ({
  EnvironmentsService: { List: vi.fn(() => Promise.resolve({ environments: [], activeId: "", problems: [], readOnly: false })) },
}));

import { resetSecretsState } from "../../commands/secrets";
import { toAppError } from "../../lib/errors";
import { createStores, StoreProvider } from "../../stores";
import { expectAccessible } from "../../test/a11y";
import { emit, resetFakeWails } from "../../test/fakeWails";
import { SecretsStatus } from "./SecretsStatus";
import { SecretsHost } from "./UnlockDialog";

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
const locked = { ...off, enabled: true, locked: true, kdf: "pbkdf2-hmac-sha256", iterations: 600000 };
const unlocked = { ...locked, locked: false };

function goError(code: string, message: string, hint = "") {
  return Object.assign(new Error(message), {
    cause: { kind: "permission_denied", code, message, hint, details: {}, causes: [] },
  });
}

function renderHost() {
  return render(
    <StoreProvider stores={createStores()}>
      <SecretsHost />
      <SecretsStatus />
    </StoreProvider>,
  );
}

beforeEach(() => {
  resetFakeWails();
  resetSecretsState();
  service.Status.mockReset().mockResolvedValue(off);
  service.Unlock.mockReset();
  service.UnlockWithKeychain.mockReset();
  service.Lock.mockReset().mockResolvedValue(locked);
  service.Enable.mockReset();
  service.Reset.mockReset();
});

afterEach(() => {
  resetSecretsState();
});

describe("SecretsHost", () => {
  it("shows nothing while encryption is off", async () => {
    renderHost();
    await waitFor(() => expect(service.Status).toHaveBeenCalled());
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(screen.queryByTestId("secrets-status")).toBeNull();
  });

  it("asks for the password at startup, rejects a wrong one and unlocks with the right one", async () => {
    service.Status.mockResolvedValue(locked);
    renderHost();
    const dialog = await screen.findByRole("dialog", { name: "Unlock stored secrets" });
    expect(dialog).toBeTruthy();
    const unlock = screen.getByRole("button", { name: "Unlock" });
    expect(unlock.hasAttribute("disabled")).toBe(true);
    expectAccessible();

    service.Unlock.mockRejectedValueOnce(goError("secretstore.wrong_password", "The master password is not correct."));
    const field = screen.getByLabelText("Master password") as HTMLInputElement;
    expect(field.type).toBe("password");
    fireEvent.change(field, { target: { value: "wrong one" } });
    fireEvent.click(unlock);
    expect((await screen.findByRole("alert")).textContent).toContain("The master password is not correct.");
    expect(service.Unlock).toHaveBeenCalledWith({ password: "wrong one" });
    expectAccessible();
    // Typed again from an empty field.
    expect(field.value).toBe("");
    expect(screen.getByRole("dialog")).toBeTruthy();

    service.Unlock.mockResolvedValueOnce(unlocked);
    fireEvent.change(field, { target: { value: "correct horse" } });
    fireEvent.click(screen.getByRole("button", { name: "Unlock" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(screen.getByTestId("secrets-status").textContent).toContain("Unlocked");
  });

  it("waits for the keychain at startup and asks for the password only when it did not unlock", async () => {
    service.Status.mockResolvedValue({ ...locked, keychain: { ...keychain, pending: true } });
    renderHost();
    await waitFor(() => expect(service.Status).toHaveBeenCalled());
    await act(async () => {
      await Promise.resolve();
    });
    expect(screen.queryByRole("dialog")).toBeNull();

    // The keychain failed: the dialog opens, says why, and offers it again.
    const problem = { kind: "unsupported", code: "keychain.unavailable", message: "No Secret Service is running.", hint: "", details: {}, causes: [] };
    act(() => emit("secrets:changed", { ...locked, keychain: { ...keychain, problem } }));
    const dialog = await screen.findByRole("dialog", { name: "Unlock stored secrets" });
    expect(dialog.textContent).toContain("macOS Keychain could not unlock them: No Secret Service is running.");
    expectAccessible();

    service.UnlockWithKeychain.mockResolvedValueOnce({ ...unlocked, keychain });
    fireEvent.click(screen.getByRole("button", { name: "Unlock with macOS Keychain" }));
    await waitFor(() => expect(service.UnlockWithKeychain).toHaveBeenCalled());
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it("does not ask at startup when the keychain unlocked the secrets", async () => {
    service.Status.mockResolvedValue({ ...locked, keychain: { ...keychain, pending: true } });
    renderHost();
    await waitFor(() => expect(service.Status).toHaveBeenCalled());
    act(() => emit("secrets:changed", { ...unlocked, keychain }));
    await waitFor(() => expect(screen.getByTestId("secrets-status").textContent).toContain("Unlocked"));
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("opens again on a locked error, unless Not now was chosen", async () => {
    service.Status.mockResolvedValue(locked);
    renderHost();
    await screen.findByRole("dialog");
    fireEvent.click(screen.getByRole("button", { name: "Not now" }));
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(screen.getByTestId("secrets-status").textContent).toContain("Locked");

    // Dismissed: a send refused while locked does not bring it back.
    act(() => {
      toAppError(goError("secretstore.locked", "Your stored secrets are locked."));
    });
    await act(async () => {
      await Promise.resolve();
    });
    expect(screen.queryByRole("dialog")).toBeNull();

    // The status bar's lock opens it on request.
    fireEvent.click(screen.getByTestId("secrets-status"));
    expect(await screen.findByRole("dialog", { name: "Unlock stored secrets" })).toBeTruthy();
  });

  it("asks for the password when a call fails because the secrets got locked", async () => {
    service.Status.mockResolvedValue(unlocked);
    renderHost();
    await waitFor(() => expect(screen.getByTestId("secrets-status").textContent).toContain("Unlocked"));
    expect(screen.queryByRole("dialog")).toBeNull();

    // The idle lock: Go says so, then a send fails.
    service.Status.mockResolvedValue(locked);
    act(() => emit("secrets:changed", locked));
    act(() => {
      toAppError(goError("secretstore.locked", "Your stored secrets are locked."));
    });
    expect(await screen.findByRole("dialog", { name: "Unlock stored secrets" })).toBeTruthy();
  });

  it("locks now from the status bar", async () => {
    service.Status.mockResolvedValue(unlocked);
    renderHost();
    await waitFor(() => expect(screen.getByTestId("secrets-status").textContent).toContain("Unlocked"));
    fireEvent.click(screen.getByTestId("secrets-status"));
    await waitFor(() => expect(service.Lock).toHaveBeenCalled());
    await waitFor(() => expect(screen.getByTestId("secrets-status").textContent).toContain("Locked"));
  });

  it("deletes the encrypted secrets for a forgotten password after the typed confirmation", async () => {
    service.Status.mockResolvedValue(locked);
    service.Reset.mockResolvedValue(off);
    renderHost();
    await screen.findByRole("dialog");
    fireEvent.click(screen.getByRole("button", { name: "Forgot password?" }));
    const dialog = await screen.findByRole("dialog", { name: "Delete the encrypted secrets?" });
    expect(dialog).toBeTruthy();
    const confirm = screen.getByRole("button", { name: "Delete secrets" });
    expect(confirm.hasAttribute("disabled")).toBe(true);
    expectAccessible();
    const word = screen.getByLabelText("Type DELETE to confirm");
    fireEvent.change(word, { target: { value: "delete" } });
    expect(confirm.hasAttribute("disabled")).toBe(true);
    fireEvent.change(word, { target: { value: "DELETE" } });
    expect(confirm.hasAttribute("disabled")).toBe(false);
    fireEvent.click(confirm);
    await waitFor(() => expect(service.Reset).toHaveBeenCalledWith({ confirm: true }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(screen.queryByTestId("secrets-status")).toBeNull();
  });

  it("asks for a new master password when the administrator requires one", async () => {
    service.Status.mockResolvedValue({ ...off, required: true });
    service.Enable.mockResolvedValue({ ...unlocked, required: true });
    renderHost();
    const dialog = await screen.findByRole("dialog", { name: "Set a master password" });
    expect(dialog.textContent).toContain("Your administrator requires stored secrets to be encrypted.");
    fireEvent.change(screen.getByLabelText("Master password"), { target: { value: "long enough" } });
    fireEvent.change(screen.getByLabelText("Repeat the new password"), { target: { value: "long enough" } });
    fireEvent.click(screen.getByRole("button", { name: "Encrypt secrets" }));
    await waitFor(() => expect(service.Enable).toHaveBeenCalledWith({ password: "long enough" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });
});
