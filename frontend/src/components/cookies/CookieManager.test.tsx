import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, within } from "@testing-library/react";

const service = vi.hoisted(() => ({
  List: vi.fn<() => Promise<unknown>>(),
  Set: vi.fn<(input: { key: string; cookie: Record<string, unknown> }) => Promise<unknown>>(),
  Delete: vi.fn<(key: string) => Promise<unknown>>(),
  DeleteDomain: vi.fn<(domain: string) => Promise<unknown>>(),
  Clear: vi.fn<() => Promise<unknown>>(),
  SetPerEnvironment: vi.fn<(on: boolean) => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/cookies", () => ({
  CookiesService: service,
}));

import { emit, resetFakeWails } from "../../test/fakeWails";
import type { CookieDTO, JarDTO } from "../../lib/cookies";
import { ConfirmHost } from "../common/Dialog";
import { CookiesButton } from "./CookiesButton";

function cookie(over: Partial<CookieDTO>): CookieDTO {
  return {
    key: `key-${over.name ?? "n"}`,
    name: "n",
    value: "v",
    domain: "example.com",
    hostOnly: true,
    path: "/",
    secure: false,
    httpOnly: false,
    sameSite: "",
    expires: "",
    created: "2030-01-01T00:00:00Z",
    lastAccess: "2030-01-01T00:00:00Z",
    binary: false,
    ...over,
  };
}

const sharedJar: JarDTO = { workspace: "Shop API", perEnvironment: false, environmentId: "", environment: "" };
const stagingJar: JarDTO = { workspace: "Shop API", perEnvironment: true, environmentId: "env_staging", environment: "Staging" };

const session = cookie({ name: "session", value: "s1", httpOnly: true, secure: true, sameSite: "Lax" });
const theme = cookie({ name: "theme", value: "dark", domain: "shop.test", hostOnly: false, path: "/app" });

async function openManager() {
  render(
    <>
      <CookiesButton />
      <ConfirmHost />
    </>,
  );
  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name: "Cookies" }));
  });
  return screen.getByRole("dialog", { name: "Cookies" });
}

beforeEach(() => {
  vi.clearAllMocks();
  resetFakeWails();
  service.List.mockResolvedValue({ cookies: [session, theme], problem: null, jar: sharedJar });
  service.Set.mockImplementation(async ({ cookie: c }) => ({ ...c, key: "new" }));
  service.Delete.mockResolvedValue(undefined);
  service.DeleteDomain.mockResolvedValue(1);
  service.Clear.mockResolvedValue(undefined);
});

describe("CookieManager", () => {
  it("lists cookies grouped by domain", async () => {
    const dialog = await openManager();
    expect(within(dialog).getByText("2 cookies")).toBeTruthy();
    const example = within(dialog).getByRole("region", { name: "example.com" });
    const row = within(example).getByText("session").closest("tr") as HTMLElement;
    expect(within(row).getByText("s1")).toBeTruthy();
    expect(within(row).getByText("Session")).toBeTruthy();
    expect(within(row).getByText("Lax")).toBeTruthy();
    expect(within(row).getAllByRole("img", { name: "Yes" })).toHaveLength(2);
    const shop = within(dialog).getByRole("region", { name: "shop.test" });
    expect(within(shop).getByText("+ subdomains")).toBeTruthy();
    expect(within(shop).getByText("/app")).toBeTruthy();

    fireEvent.change(within(dialog).getByRole("searchbox", { name: "Filter cookies" }), { target: { value: "dark" } });
    expect(within(dialog).queryByRole("region", { name: "example.com" })).toBeNull();
  });

  it("edits a cookie through the service and reloads", async () => {
    const dialog = await openManager();
    await act(async () => {
      fireEvent.click(within(dialog).getByRole("button", { name: "Edit session" }));
    });
    const form = screen.getByRole("dialog", { name: "Edit cookie" });
    fireEvent.change(within(form).getByLabelText("Value"), { target: { value: "edited" } });
    service.List.mockResolvedValue({ cookies: [{ ...session, value: "edited" }, theme], problem: null });
    await act(async () => {
      fireEvent.click(within(form).getByRole("button", { name: "Save" }));
    });
    expect(service.Set).toHaveBeenCalledWith({
      key: "key-session",
      cookie: expect.objectContaining({ name: "session", value: "edited", domain: "example.com", hostOnly: true, secure: true, httpOnly: true, sameSite: "Lax", expires: "" }),
    });
    const list = screen.getByRole("dialog", { name: "Cookies" });
    expect(within(list).getByText("edited")).toBeTruthy();
  });

  it("adds a cookie to a domain and shows service errors under the field", async () => {
    const dialog = await openManager();
    await act(async () => {
      fireEvent.click(within(dialog).getByRole("button", { name: "Add cookie to shop.test" }));
    });
    const form = screen.getByRole("dialog", { name: "Add cookie" });
    expect((within(form).getByLabelText("Domain") as HTMLInputElement).value).toBe("shop.test");
    fireEvent.change(within(form).getByLabelText("Name"), { target: { value: "__Host-x" } });
    service.Set.mockRejectedValueOnce({
      kind: "invalid_argument",
      code: "cookies.invalid_cookie",
      message: "Needs Secure.",
      hint: "Turn on Secure.",
      details: { field: "name" },
    });
    await act(async () => {
      fireEvent.click(within(form).getByRole("button", { name: "Add cookie" }));
    });
    expect(service.Set).toHaveBeenCalledWith({ key: "", cookie: expect.objectContaining({ name: "__Host-x", domain: "shop.test" }) });
    expect(screen.getByRole("dialog", { name: "Add cookie" })).toBeTruthy();
    expect(within(form).getByLabelText("Name").getAttribute("aria-invalid")).toBe("true");

    // Back returns to the list without saving.
    await act(async () => {
      fireEvent.click(within(form).getByRole("button", { name: "Back" }));
    });
    expect(screen.getByRole("dialog", { name: "Cookies" })).toBeTruthy();
  });

  it("deletes a cookie and clears a domain after confirming", async () => {
    const dialog = await openManager();
    await act(async () => {
      fireEvent.click(within(dialog).getByRole("button", { name: "Delete session" }));
    });
    const ask = screen.getByRole("dialog", { name: "Delete cookie?" });
    await act(async () => {
      fireEvent.click(within(ask).getByRole("button", { name: "Delete" }));
    });
    expect(service.Delete).toHaveBeenCalledWith("key-session");

    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Clear cookies of shop.test" }));
    });
    const askDomain = screen.getByRole("dialog", { name: "Clear cookies of shop.test?" });
    await act(async () => {
      fireEvent.click(within(askDomain).getByRole("button", { name: "Cancel" }));
    });
    expect(service.DeleteDomain).not.toHaveBeenCalled();
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Clear cookies of shop.test" }));
    });
    await act(async () => {
      fireEvent.click(within(screen.getByRole("dialog", { name: "Clear cookies of shop.test?" })).getByRole("button", { name: "Clear" }));
    });
    expect(service.DeleteDomain).toHaveBeenCalledWith("shop.test");
  });

  it("shows an empty jar and a cookie file problem", async () => {
    service.List.mockResolvedValue({ cookies: null, problem: { code: "cookies.file_invalid", message: "Not saved.", hint: "Fix the file." } });
    const dialog = await openManager();
    expect(within(dialog).getByText("No cookies")).toBeTruthy();
    expect(within(dialog).getByText("Not saved. Fix the file.")).toBeTruthy();
    expect((within(dialog).getByRole("button", { name: "Clear all" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("names the jar it edits: the project's shared jar, or an environment's", async () => {
    const dialog = await openManager();
    const jar = within(dialog).getByLabelText("Cookie jar");
    expect(within(jar).getByText("Shop API")).toBeTruthy();
    expect(within(jar).getByText("Shared by all environments")).toBeTruthy();
    const setting = within(dialog).getByRole("checkbox", { name: "Separate cookies per environment" }) as HTMLInputElement;
    expect(setting.checked).toBe(false);

    // Switching the environment reloads the list: another jar.
    service.List.mockResolvedValue({ cookies: [], problem: null, jar: stagingJar });
    await act(async () => {
      emit("environments:changed", { activeId: "env_staging" });
    });
    expect(within(jar).getByText("Shop API · Staging")).toBeTruthy();
    expect(within(jar).getByText("Only this environment")).toBeTruthy();
    expect(setting.checked).toBe(true);
    expect(within(dialog).getByText("No cookies")).toBeTruthy();

    service.List.mockResolvedValue({ cookies: [], problem: null, jar: { ...stagingJar, environmentId: "", environment: "" } });
    await act(async () => {
      emit("environments:changed", { activeId: "" });
    });
    expect(within(jar).getByText("Shop API · No environment")).toBeTruthy();
    expect(within(jar).getByText("Used when no environment is active")).toBeTruthy();
  });

  it("turns separate cookies per environment on and off", async () => {
    const dialog = await openManager();
    service.SetPerEnvironment.mockResolvedValueOnce({ cookies: [], problem: null, jar: stagingJar });
    await act(async () => {
      fireEvent.click(within(dialog).getByRole("checkbox", { name: "Separate cookies per environment" }));
    });
    expect(service.SetPerEnvironment).toHaveBeenCalledWith(true);
    expect(within(dialog).getByText("Shop API · Staging")).toBeTruthy();
    expect((within(dialog).getByRole("checkbox", { name: "Separate cookies per environment" }) as HTMLInputElement).checked).toBe(true);

    service.SetPerEnvironment.mockRejectedValueOnce({ kind: "internal", code: "cookies.local_data_failed", message: "Not saved.", hint: "Check the disk.", details: {} });
    await act(async () => {
      fireEvent.click(within(dialog).getByRole("checkbox", { name: "Separate cookies per environment" }));
    });
    expect(service.SetPerEnvironment).toHaveBeenLastCalledWith(false);
    expect(within(dialog).getByRole("alert").textContent).toContain("Not saved. Check the disk.");
    expect((within(dialog).getByRole("checkbox", { name: "Separate cookies per environment" }) as HTMLInputElement).checked).toBe(true);
  });

  it("clears the jar it shows, naming it", async () => {
    service.List.mockResolvedValue({ cookies: [session], problem: null, jar: stagingJar });
    const dialog = await openManager();
    await act(async () => {
      fireEvent.click(within(dialog).getByRole("button", { name: "Clear all" }));
    });
    const ask = screen.getByRole("dialog", { name: "Clear all cookies?" });
    expect(within(ask).getByText("Every cookie of the Shop API · Staging jar will be deleted.")).toBeTruthy();
    await act(async () => {
      fireEvent.click(within(ask).getByRole("button", { name: "Clear all" }));
    });
    expect(service.Clear).toHaveBeenCalledTimes(1);
  });
});
