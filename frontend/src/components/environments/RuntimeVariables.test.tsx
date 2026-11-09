import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, within } from "@testing-library/react";

const service = vi.hoisted(() => ({
  List: vi.fn<() => Promise<unknown>>(),
  Set: vi.fn<(input: unknown) => Promise<unknown>>(),
  Delete: vi.fn<(name: string) => Promise<unknown>>(),
  Clear: vi.fn<() => Promise<unknown>>(),
}));
const environments = vi.hoisted(() => ({
  List: vi.fn<() => Promise<unknown>>(),
  SetActive: vi.fn<(id: string) => Promise<unknown>>(),
  GlobalVariables: vi.fn<() => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/runtimevars", () => ({
  RuntimeVarsService: service,
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/environments", () => ({
  EnvironmentsService: environments,
}));

import { environmentsWorkspaceKey } from "../../commands/environments";
import { DEFAULT_LOCALE, PSEUDO_LOCALE, setLocale, t } from "../../i18n";
import type { RuntimeVariableDTO } from "../../lib/runtimeVariables";
import { createStores, initialEnvironmentsState, StoreProvider, type WorkspaceInfo } from "../../stores";
import { expectAccessible } from "../../test/a11y";
import { emit, settle } from "../../test/fakeWails";
import { englishLeftovers, isPseudo, visibleTexts } from "../../test/pseudoLocale";
import { ConfirmHost } from "../common/Dialog";
import { EnvironmentsPanel } from "./EnvironmentsPanel";

const shop: WorkspaceInfo = {
  id: "ws_1",
  name: "Shop",
  path: "/p/shop",
  localDir: "/data/ws_1",
  localStatus: "existing",
  previousPath: "",
  newerFormat: false,
};

const userId: RuntimeVariableDTO = { name: "userId", value: "u-42", secret: false, persist: false };
const authToken: RuntimeVariableDTO = { name: "authToken", value: "tok-s3cret", secret: true, persist: true };

const listOf = (...variables: RuntimeVariableDTO[]) => ({ variables });

function setup(label = "Environments") {
  const stores = createStores({
    environments: { ...initialEnvironmentsState, status: "ready", workspaceKey: environmentsWorkspaceKey(shop) as string },
  });
  stores.workspace.dispatch({ type: "workspace/setCurrent", workspace: shop });
  render(
    <StoreProvider stores={stores}>
      <aside aria-label={label}>
        <EnvironmentsPanel label={label} />
      </aside>
      <ConfirmHost />
    </StoreProvider>,
  );
  return stores;
}

/** Opens the Runtime variables row and waits for the list. */
async function openRuntime() {
  fireEvent.click(screen.getByRole("button", { name: "Runtime variables" }));
  await act(settle);
  return screen.getByRole("region", { name: "Runtime variables" });
}

const form = () => screen.getByRole("form");
const nameField = () => within(form()).getByRole("textbox", { name: "Name" }) as HTMLInputElement;
const valueField = () => form().querySelectorAll("input")[1] as HTMLInputElement;

beforeEach(() => {
  for (const fn of [...Object.values(service), ...Object.values(environments)]) fn.mockReset();
  service.List.mockResolvedValue(listOf(authToken, userId));
});

afterEach(() => {
  setLocale(DEFAULT_LOCALE);
});

describe("Runtime variables (ENV-15)", () => {
  it("lists them with a masked secret, their lifetime and the precedence", async () => {
    setup();
    const order = screen.getByRole("list", { name: "Variable precedence, lowest first" });
    const runtime = within(order).getByText("Runtime");
    expect(runtime.hasAttribute("data-unavailable")).toBe(false);
    expect(runtime.getAttribute("title")).toContain("A folder run's own variables win over them.");
    expect(service.List).not.toHaveBeenCalled();

    const section = await openRuntime();
    expect(service.List).toHaveBeenCalledTimes(1);
    const items = within(within(section).getByRole("list", { name: "Runtime variables" })).getAllByRole("listitem");
    expect(items.map((li) => li.textContent)).toEqual(["authToken••••••Kept", "userIdu-42Session"]);
    expect(section.textContent).not.toContain("tok-s3cret");

    fireEvent.click(within(items[0]).getByRole("button", { name: "Show the value of authToken" }));
    expect(items[0].textContent).toContain("tok-s3cret");
    expect(within(items[0]).getByRole("button", { name: "Secret: authToken" }).getAttribute("aria-pressed")).toBe("true");
    expect(
      within(items[1]).getByRole("button", { name: "Keep userId after closing the project" }).getAttribute("aria-pressed"),
    ).toBe("false");
    expectAccessible(document.body);
  });

  it("sets a new one from the form, secret and kept", async () => {
    setup();
    await openRuntime();
    const submit = within(form()).getByRole("button", { name: "Set" }) as HTMLButtonElement;
    expect(submit.disabled).toBe(true);

    const added: RuntimeVariableDTO = { name: "sessionId", value: "abc", secret: true, persist: true };
    service.Set.mockResolvedValue(listOf(authToken, added, userId));
    fireEvent.change(nameField(), { target: { value: " sessionId " } });
    fireEvent.change(valueField(), { target: { value: "abc" } });
    fireEvent.click(within(form()).getByRole("checkbox", { name: "Secret: mask the value everywhere" }));
    expect(valueField().type).toBe("password");
    fireEvent.click(within(form()).getByRole("checkbox", { name: "Keep after closing the project" }));
    fireEvent.click(submit);
    await act(settle);

    expect(service.Set).toHaveBeenCalledWith({ previousName: "", name: "sessionId", value: "abc", secret: true, persist: true });
    expect(screen.getAllByRole("listitem").some((li) => li.textContent === "sessionId••••••Kept")).toBe(true);
    expect(nameField().value).toBe("");
  });

  it("edits, toggles and deletes one", async () => {
    setup();
    await openRuntime();

    // Edit: the name opens it in the form; saving renames.
    fireEvent.click(screen.getByRole("button", { name: "Edit userId" }));
    expect(screen.getByText("Edit userId", { selector: "p" })).toBeTruthy();
    expect(nameField().value).toBe("userId");
    expect(valueField().value).toBe("u-42");
    service.Set.mockResolvedValue(listOf(authToken, { ...userId, name: "accountId", value: "u-43" }));
    fireEvent.change(nameField(), { target: { value: "accountId" } });
    fireEvent.change(valueField(), { target: { value: "u-43" } });
    fireEvent.click(within(form()).getByRole("button", { name: "Save" }));
    await act(settle);
    expect(service.Set).toHaveBeenLastCalledWith({ previousName: "userId", name: "accountId", value: "u-43", secret: false, persist: false });
    expect(screen.queryByRole("button", { name: "Edit userId" })).toBeNull();

    // The pin keeps it after closing the project; the lock makes it secret.
    service.Set.mockResolvedValue(listOf({ ...authToken, persist: false }));
    fireEvent.click(screen.getByRole("button", { name: "Keep authToken after closing the project" }));
    await act(settle);
    expect(service.Set).toHaveBeenLastCalledWith({ previousName: "authToken", name: "authToken", value: "tok-s3cret", secret: true, persist: false });
    fireEvent.click(screen.getByRole("button", { name: "Secret: authToken" }));
    await act(settle);
    expect(service.Set).toHaveBeenLastCalledWith({ previousName: "authToken", name: "authToken", value: "tok-s3cret", secret: false, persist: false });

    service.Delete.mockResolvedValue(listOf());
    fireEvent.click(screen.getByRole("button", { name: "Delete authToken" }));
    await act(settle);
    expect(service.Delete).toHaveBeenCalledWith("authToken");
    expect(screen.getByText(/No runtime variables/)).toBeTruthy();
  });

  it("clears them all after asking, and shows Go's errors", async () => {
    setup();
    await openRuntime();

    service.Set.mockRejectedValue({
      message: "The name of a runtime variable cannot contain {{ or }}.",
      kind: "invalid_argument",
      code: "runtimevars.invalid_name",
    });
    fireEvent.change(nameField(), { target: { value: "a}}b" } });
    fireEvent.click(within(form()).getByRole("button", { name: "Set" }));
    await act(settle);
    expect(screen.getByRole("alert").textContent).toContain("cannot contain");
    expect(nameField().getAttribute("aria-invalid")).toBe("true");

    service.Clear.mockResolvedValue(listOf());
    fireEvent.click(screen.getByRole("button", { name: "Clear all" }));
    await act(settle);
    const dialog = screen.getByRole("dialog", { name: "Clear all runtime variables?" });
    fireEvent.click(within(dialog).getByRole("button", { name: "Clear all" }));
    await act(settle);
    expect(service.Clear).toHaveBeenCalledTimes(1);
    expect(screen.queryAllByRole("listitem").filter((li) => li.closest("[aria-label='Runtime variables']"))).toEqual([]);
  });

  it("reloads when Go says they changed (a value taken from a response)", async () => {
    setup();
    const section = await openRuntime();
    service.List.mockResolvedValue(listOf(authToken, userId, { name: "orderId", value: "o-7", secret: false, persist: false }));
    act(() => emit("runtimevars:changed", { count: 3 }));
    await act(settle);
    expect(service.List).toHaveBeenCalledTimes(2);
    expect(within(section).getByRole("button", { name: "Edit orderId" })).toBeTruthy();
  });

  it("offers Clear all when the saved ones cannot be read", async () => {
    service.List.mockRejectedValue({
      message: "The saved runtime variables could not be read.",
      kind: "parse",
      code: "runtimevars.local_data_failed",
    });
    setup();
    await openRuntime();
    expect(screen.getByRole("alert").textContent).toContain("could not be read");
    service.Clear.mockResolvedValue(listOf());
    const alert = screen.getByRole("alert");
    fireEvent.click(within(alert).getByRole("button", { name: "Clear all" }));
    await act(settle);
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Clear all" }));
    await act(settle);
    expect(service.Clear).toHaveBeenCalledTimes(1);
    expect(screen.getByText(/No runtime variables/)).toBeTruthy();
  });

  it("shows every text from the catalogue (pseudo-locale)", async () => {
    setLocale(PSEUDO_LOCALE);
    setup(t("environments.panel.list"));
    fireEvent.click(screen.getByRole("button", { name: t("environments.runtime.label") }));
    await act(settle);
    fireEvent.click(screen.getByRole("button", { name: t("environments.runtime.edit", { name: "userId" }) }));
    const roots = [document.body];
    expect(englishLeftovers(roots, ["environments"])).toEqual([]);
    expect(visibleTexts(roots).filter(isPseudo).length).toBeGreaterThan(10);
  });
});
