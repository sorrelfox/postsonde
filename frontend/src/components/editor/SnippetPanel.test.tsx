import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const http = vi.hoisted(() => ({
  Snippet: vi.fn<(input: { requestId: string; language: string; mode: string; revealSecrets: boolean }) => Promise<unknown>>(),
  SnippetLanguages: vi.fn<() => Promise<unknown>>(),
}));
const collection = vi.hoisted(() => ({
  Flush: vi.fn<(id: string) => Promise<void>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/http", () => ({
  HTTPService: http,
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/collection", () => ({
  CollectionService: collection,
}));

import { resetSnippetLanguages, type SnippetDTO } from "../../commands/snippets";
import { SNIPPET_DELAY_MS } from "../../commands/useSnippet";
import { toaster } from "../../commands/toasts";
import { createStores, StoreProvider } from "../../stores";
import { expectAccessible } from "../../test/a11y";
import { fakeRuntime, resetFakeWails, settle } from "../../test/fakeWails";
import { SnippetPanel } from "./SnippetPanel";

const MASK = "••••••";
const SECRET = "t0ken-s3cret";

type Input = { requestId: string; language: string; mode: string; revealSecrets: boolean };

/** What the fake Go writes for an input: the token masked unless revealed, a placeholder when templated. */
function answer(input: Input): SnippetDTO {
  if (input.mode === "templated") {
    return {
      language: input.language,
      mode: "templated",
      code: "curl '{{base}}/me' \\\n  -H 'Authorization: Bearer {{token}}'",
      masked: false,
      revealed: false,
      unresolved: [],
    };
  }
  const token = input.revealSecrets ? SECRET : MASK;
  return {
    language: input.language,
    mode: "resolved",
    code: `curl https://api.test/me \\\n  -H 'Authorization: Bearer ${token}'`,
    masked: !input.revealSecrets,
    revealed: input.revealSecrets,
    unresolved: ["missing"],
  };
}

const cancellable = <T,>(p: Promise<T>) => Object.assign(p, { cancel: () => undefined });

beforeEach(() => {
  resetFakeWails();
  resetSnippetLanguages();
  toaster.clear();
  localStorage.clear();
  http.Snippet.mockReset().mockImplementation((input) => cancellable(Promise.resolve(answer(input))));
  http.SnippetLanguages.mockReset().mockImplementation(() =>
    cancellable(
      Promise.resolve([
        { id: "curl", name: "cURL", syntax: "shell" },
        { id: "python", name: "Python (Go's name)", syntax: "text" },
        { id: "kotlin", name: "Kotlin OkHttp", syntax: "text" },
      ]),
    ),
  );
  collection.Flush.mockReset().mockResolvedValue(undefined);
  fakeRuntime.Clipboard.SetText.mockReset().mockResolvedValue(undefined);
});

async function setup(props: { request?: unknown; unavailable?: string | null } = {}) {
  const stores = createStores();
  const view = render(
    <StoreProvider stores={stores}>
      <SnippetPanel id="req_1" request={props.request ?? { url: "a" }} unavailable={props.unavailable ?? null} />
    </StoreProvider>,
  );
  await act(settle);
  return { stores, view };
}

const code = () => screen.getByLabelText("cURL code");
const modeButton = (name: string) => within(screen.getByRole("group", { name: "Values" })).getByRole("button", { name });
const showSecrets = () => screen.getByRole("button", { name: "Show secrets" });
const status = () => screen.getByRole("status");
const lastToast = () => {
  const all = toaster.toasts();
  return all[all.length - 1]?.message;
};

describe("SnippetPanel", () => {
  it("shows the resolved cURL command with secrets masked by default", async () => {
    await setup();
    expect(http.Snippet).toHaveBeenCalledWith({ requestId: "req_1", language: "curl", mode: "resolved", revealSecrets: false });
    expect(code().textContent).toContain(`Bearer ${MASK}`);
    expect(code().textContent).not.toContain(SECRET);
    expect(modeButton("Resolved").getAttribute("aria-pressed")).toBe("true");
    expect(showSecrets().getAttribute("aria-pressed")).toBe("false");
    expect(status().textContent).toContain("Secrets are masked.");
    expect(status().textContent).toContain("{{missing}} is not defined and left as written.");
    expectAccessible();
  });

  it("lists the languages Go writes, and names those it has no text for as Go does", async () => {
    await setup();
    const select = screen.getByRole("combobox", { name: "Language" }) as HTMLSelectElement;
    expect([...select.options].map((o) => o.textContent)).toEqual(["cURL", "Python requests", "Kotlin OkHttp"]);
    fireEvent.change(select, { target: { value: "kotlin" } });
    await act(settle);
    expect(http.Snippet).toHaveBeenLastCalledWith({ requestId: "req_1", language: "kotlin", mode: "resolved", revealSecrets: false });
    expect(screen.getByLabelText("Kotlin OkHttp code").textContent).toContain("curl");
  });

  it("reveals secrets for this view only, on request, and hides them again", async () => {
    await setup();
    fireEvent.click(showSecrets());
    await act(settle);
    expect(http.Snippet).toHaveBeenLastCalledWith({ requestId: "req_1", language: "curl", mode: "resolved", revealSecrets: true });
    expect(showSecrets().getAttribute("aria-pressed")).toBe("true");
    expect(code().textContent).toContain(`Bearer ${SECRET}`);
    expect(status().textContent).toContain("Secrets are shown in this view only.");

    // Hiding asks for the masked code, and never shows the revealed one meanwhile.
    let resolveMasked: (dto: SnippetDTO) => void = () => undefined;
    http.Snippet.mockImplementationOnce((input) =>
      cancellable(
        new Promise<SnippetDTO>((resolve) => {
          resolveMasked = () => resolve(answer(input));
        }),
      ),
    );
    fireEvent.click(showSecrets());
    await act(settle);
    expect(code().textContent).not.toContain(SECRET);
    await act(async () => {
      resolveMasked(answer({ requestId: "req_1", language: "curl", mode: "resolved", revealSecrets: false }));
      await settle();
    });
    expect(code().textContent).toContain(`Bearer ${MASK}`);
    expect(showSecrets().getAttribute("aria-pressed")).toBe("false");
  });

  it("never saves a reveal: a new panel starts masked, and nothing is stored", async () => {
    const { view } = await setup();
    fireEvent.click(showSecrets());
    await act(settle);
    expect(code().textContent).toContain(SECRET);
    view.unmount();
    expect(localStorage.length).toBe(0);
    http.Snippet.mockClear();
    await setup();
    expect(http.Snippet).toHaveBeenCalledTimes(1);
    expect(http.Snippet).toHaveBeenCalledWith({ requestId: "req_1", language: "curl", mode: "resolved", revealSecrets: false });
    expect(code().textContent).not.toContain(SECRET);
  });

  it("shows the templated view with placeholders, where there is nothing to reveal", async () => {
    await setup();
    fireEvent.click(showSecrets());
    await act(settle);
    fireEvent.click(modeButton("Templated"));
    await act(settle);
    // Switching mode drops the reveal: templated is never asked for secrets.
    expect(http.Snippet).toHaveBeenLastCalledWith({ requestId: "req_1", language: "curl", mode: "templated", revealSecrets: false });
    expect(code().textContent).toContain("Bearer {{token}}");
    expect(code().textContent).not.toContain(SECRET);
    expect(screen.queryByRole("button", { name: "Show secrets" })).toBeNull();
    expect(status().textContent).toBe("Variables are kept as {{placeholders}}.");
    fireEvent.click(modeButton("Resolved"));
    await act(settle);
    expect(http.Snippet).toHaveBeenLastCalledWith({ requestId: "req_1", language: "curl", mode: "resolved", revealSecrets: false });
    expect(code().textContent).toContain(MASK);
    expectAccessible();
  });

  it("copies the code shown and says whether secrets were masked", async () => {
    await setup();
    fireEvent.click(screen.getByRole("button", { name: "Copy" }));
    await act(settle);
    expect(fakeRuntime.Clipboard.SetText).toHaveBeenLastCalledWith(`curl https://api.test/me \\\n  -H 'Authorization: Bearer ${MASK}'`);
    expect(lastToast()).toBe("Copied, with secrets masked.");

    fireEvent.click(showSecrets());
    await act(settle);
    fireEvent.click(screen.getByRole("button", { name: "Copy" }));
    await act(settle);
    expect(fakeRuntime.Clipboard.SetText).toHaveBeenLastCalledWith(`curl https://api.test/me \\\n  -H 'Authorization: Bearer ${SECRET}'`);
    expect(lastToast()).toBe("Copied, with the real values of the secrets.");
  });

  it("asks again after an edit, once the edit reached Go, and keeps the view's reveal", async () => {
    vi.useFakeTimers();
    try {
      const stores = createStores();
      const view = render(
        <StoreProvider stores={stores}>
          <SnippetPanel id="req_1" request={{ url: "a" }} />
        </StoreProvider>,
      );
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(http.Snippet).toHaveBeenCalledTimes(1);
      view.rerender(
        <StoreProvider stores={stores}>
          <SnippetPanel id="req_1" request={{ url: "b" }} />
        </StoreProvider>,
      );
      await act(async () => {
        await vi.advanceTimersByTimeAsync(SNIPPET_DELAY_MS - 1);
      });
      expect(http.Snippet).toHaveBeenCalledTimes(1);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(1);
      });
      expect(http.Snippet).toHaveBeenCalledTimes(2);
    } finally {
      vi.useRealTimers();
    }
  });

  it("shows Go's error, and a note when the request cannot be written", async () => {
    http.Snippet.mockImplementation(() =>
      cancellable(Promise.reject({ kind: "invalid_argument", code: "snippet.nul_byte", message: "The body contains a NUL byte." })),
    );
    const { view } = await setup();
    expect(screen.getByRole("alert").textContent).toContain("The body contains a NUL byte.");
    view.unmount();
    http.Snippet.mockClear();
    await setup({ unavailable: "Runs over WebSocket." });
    expect(screen.getByRole("note").textContent).toBe("Runs over WebSocket.");
    expect(http.Snippet).not.toHaveBeenCalled();
  });
});
