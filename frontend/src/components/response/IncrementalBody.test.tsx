import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { EditorView } from "@codemirror/view";
import { beforeEach, describe, expect, it, vi } from "vitest";

const graphqlService = vi.hoisted(() => ({
  Subscribe: vi.fn<(input: unknown) => Promise<unknown>>(),
  StopSubscription: vi.fn<(input: unknown) => Promise<unknown>>(),
}));
const http = vi.hoisted(() => ({
  Send: vi.fn<(input: { requestId: string }) => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/graphql", () => ({
  GraphQLService: graphqlService,
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/http", () => ({
  HTTPService: http,
}));
vi.mock(
  "../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/ops",
  async () => (await import("../../test/fakeWails")).fakeOpsBindings,
);

import { httpSends, resetHttpSends } from "../../commands/httpSends";
import type { IncrementalDTO, IncrementalPartDTO, ResponseDTO } from "../../lib/http";
import { createStores, StoreProvider } from "../../stores";
import { emit, opsCancel, resetFakeWails, settle } from "../../test/fakeWails";
import { httpResponse } from "../../test/httpResponse";
import { HttpResponsePanel } from "./HttpResponsePanel";
import { ResponseView } from "./ResponseView";

const PARTS: IncrementalPartDTO[] = [
  {
    index: 0,
    atMs: 8.25,
    text: '{\n  "data": {\n    "user": {\n      "id": "1"\n    }\n  },\n  "hasNext": true\n}',
    clipped: false,
    size: 40,
    entries: [{ kind: "initial", path: "", label: "", id: "", items: 0, errors: 0, problem: "" }],
    hasNext: true,
    problem: "",
  },
  {
    index: 1,
    atMs: 212.5,
    text: '{\n  "incremental": []\n}',
    clipped: false,
    size: 90,
    entries: [{ kind: "defer", path: "user", label: "Details", id: "", items: 0, errors: 0, problem: "" }],
    hasNext: true,
    problem: "",
  },
  {
    index: 2,
    atMs: 400,
    text: '{\n  "incremental": [],\n  "hasNext": false\n}',
    clipped: false,
    size: 80,
    entries: [{ kind: "stream", path: "user.friends", label: "", id: "1", items: 2, errors: 1, problem: "" }],
    hasNext: false,
    problem: "",
  },
];

const MERGED = '{\n  "data": {\n    "user": {\n      "id": "1",\n      "email": "a@example.com"\n    }\n  },\n  "extensions": {\n    "cost": 3\n  }\n}';

function incremental(over: Partial<IncrementalDTO> = {}): IncrementalDTO {
  return {
    format: "multipart",
    receiving: false,
    parts: PARTS,
    partCount: PARTS.length,
    merged: MERGED,
    mergedTooLarge: false,
    mergedSize: MERGED.length,
    hasNext: false,
    completed: true,
    pending: 0,
    errors: 0,
    ...over,
  };
}

const RAW = '\r\n---\r\nContent-Type: application/json\r\n\r\n{"data":{"user":{"id":"1"}},"hasNext":true}\r\n-----\r\n';

function multipart(inc: IncrementalDTO | null, over: Partial<ResponseDTO> = {}): ResponseDTO {
  return httpResponse({
    contentType: 'multipart/mixed; boundary="-"',
    headers: [{ name: "Content-Type", value: 'multipart/mixed; boundary="-"' }],
    body: RAW,
    bodyLanguage: "text",
    bodyPretty: "",
    prettyState: "",
    size: RAW.length,
    bodyShown: RAW.length,
    incremental: inc,
    ...over,
  });
}

const option = (name: string) => screen.queryByRole("button", { name });
const pressed = (name: string) => option(name)?.getAttribute("aria-pressed");
const editorText = () =>
  (EditorView.findFromDOM(document.querySelector(".cm-editor") as HTMLElement) as EditorView).state.doc.toString();
const partItems = () => within(screen.getByRole("list", { name: "Incremental parts" })).getAllByRole("listitem", { hidden: true }).filter((li) => li.hasAttribute("data-part"));

describe("ResponseView incremental responses (GQL-08)", () => {
  it("opens the merged result in the GraphQL view", () => {
    render(<ResponseView response={multipart(incremental())} graphql={{}} />);
    expect(pressed("GraphQL")).toBe("true");
    expect(pressed("Merged")).toBe("true");
    expect(option("Pretty")).toBeNull();
    expect(screen.getByRole("status").textContent).toBe("3 parts in 400 ms, complete");
    expect(editorText()).toBe('{\n  "data": {\n    "user": {\n      "id": "1",\n      "email": "a@example.com"\n    }\n  },\n  "extensions": {\n    "cost": 3\n  }\n}');
    expect(document.querySelector("[data-graphql-view]")?.textContent).toContain("No errors");
    // The merged result's extensions can be hidden like a plain response's.
    fireEvent.click(option("Hide extensions") as HTMLElement);
    expect(editorText()).not.toContain("cost");
  });

  it("lists the parts with their times and what each did", () => {
    render(<ResponseView response={multipart(incremental())} graphql={{}} />);
    fireEvent.click(option("Parts") as HTMLElement);
    expect(document.querySelector("[data-incremental-view]")?.getAttribute("data-incremental-view")).toBe("parts");
    const [first, second, third] = partItems();
    expect(first?.textContent).toContain("Part 1");
    expect(first?.textContent).toContain("8.3 ms");
    expect(first?.textContent).toContain("Initial result");
    expect(first?.textContent).toContain("hasNext: true");
    expect(first?.querySelector("pre")?.textContent).toBe(PARTS[0]?.text);
    expect(second?.textContent).toContain("213 ms");
    expect(second?.textContent).toContain("+204 ms");
    expect(second?.textContent).toContain("Deferred data at user (Details)");
    expect(third?.textContent).toContain("2 streamed items at user.friends, 1 error");
    expect(third?.textContent).toContain("hasNext: false");
    // Back to the merged result, and Raw shows the body as received.
    fireEvent.click(option("Merged") as HTMLElement);
    expect(editorText()).toContain('"email": "a@example.com"');
    fireEvent.click(option("Raw") as HTMLElement);
    expect(editorText()).toContain("-----");
  });

  it("keeps the chosen view while partial responses are replaced", () => {
    const partial = (n: number) => multipart(incremental({ receiving: true, completed: false, hasNext: true, parts: PARTS.slice(0, n), partCount: n }));
    const { rerender } = render(<ResponseView response={partial(1)} graphql={{}} />);
    expect(screen.getByRole("status").textContent).toBe("Receiving… 1 part");
    fireEvent.click(option("Parts") as HTMLElement);
    expect(partItems()).toHaveLength(1);
    rerender(<ResponseView response={partial(2)} graphql={{}} />);
    expect(partItems()).toHaveLength(2);
    fireEvent.click(option("Raw") as HTMLElement);
    rerender(<ResponseView response={partial(3)} graphql={{}} />);
    expect(pressed("Raw")).toBe("true");
    // The final response of the send keeps it too; the next send starts over.
    const final = multipart(incremental());
    rerender(<ResponseView response={final} graphql={{}} />);
    expect(pressed("Raw")).toBe("true");
    rerender(<ResponseView response={multipart(incremental({ receiving: true, parts: PARTS.slice(0, 1), partCount: 1 }))} graphql={{}} />);
    expect(pressed("GraphQL")).toBe("true");
    expect(pressed("Parts")).toBe("true");
  });

  it("says when the response ended early or the merged result is too large", () => {
    const { rerender } = render(<ResponseView response={multipart(incremental({ completed: false, hasNext: true, pending: 1 }))} graphql={{}} />);
    expect(screen.getByRole("status").textContent).toBe("3 parts, ended before hasNext: false, 1 pending");
    rerender(<ResponseView response={multipart(incremental({ merged: "", mergedTooLarge: true, mergedSize: 5 << 20 }))} graphql={{}} />);
    expect(screen.getByText(/The merged result is too large to show here/)).toBeTruthy();
  });

  it("marks parts that were not merged", () => {
    const bad: IncrementalPartDTO = { ...PARTS[0]!, index: 3, atMs: 500, text: "oops", entries: [], hasNext: null, problem: "The part is not valid JSON, so it was not merged." };
    render(<ResponseView response={multipart(incremental({ parts: [...PARTS, bad], partCount: 5 }))} graphql={{}} />);
    fireEvent.click(option("Parts") as HTMLElement);
    const items = partItems();
    expect(items[3]?.textContent).toContain("not merged");
    expect(items[3]?.textContent).not.toContain("hasNext");
    expect(screen.getByText("1 more part is not listed; Raw has it.")).toBeTruthy();
  });

  it("leaves a REST response's multipart body alone", () => {
    render(<ResponseView response={multipart(incremental())} />);
    expect(option("GraphQL")).toBeNull();
    expect(option("Merged")).toBeNull();
    expect(editorText()).toContain("-----");
  });
});

describe("HttpResponsePanel incremental responses (GQL-08)", () => {
  beforeEach(() => {
    resetFakeWails();
    resetHttpSends();
    http.Send.mockReset().mockResolvedValue({ opId: "op_1" });
  });

  it("shows the parts while they arrive, with Cancel, then the final response", async () => {
    render(
      <StoreProvider stores={createStores()}>
        <HttpResponsePanel id="req_1" graphql />
      </StoreProvider>,
    );
    act(() => httpSends().get("req_1").send());
    await act(settle);
    const partial = (n: number) => multipart(incremental({ receiving: true, completed: false, hasNext: true, parts: PARTS.slice(0, n), partCount: n }));
    act(() => emit("http:op:data", { opId: "op_1", seq: 2, items: [partial(1)] }));
    expect(screen.getByText("Receiving parts…")).toBeTruthy();
    expect(screen.getByText("Receiving… 1 part")).toBeTruthy();
    fireEvent.click(option("Parts") as HTMLElement);
    act(() => emit("http:op:data", { opId: "op_1", seq: 3, items: [partial(2)] }));
    expect(partItems()).toHaveLength(2);
    act(() => emit("http:op:data", { opId: "op_1", seq: 4, items: [multipart(incremental())] }));
    act(() => emit("http:op:done", { opId: "op_1", seq: 5, status: "ok", error: null, items: 3, batches: 3, elapsedMs: 400 }));
    expect(screen.queryByText("Receiving parts…")).toBeNull();
    // The view kept the Parts choice across the partial and final responses.
    expect(pressed("Parts")).toBe("true");
    expect(partItems()).toHaveLength(3);
    expect(screen.getByText("3 parts in 400 ms, complete")).toBeTruthy();
  });

  it("cancels from the receiving strip", async () => {
    render(
      <StoreProvider stores={createStores()}>
        <HttpResponsePanel id="req_1" graphql />
      </StoreProvider>,
    );
    act(() => httpSends().get("req_1").send());
    await act(settle);
    act(() => emit("http:op:data", { opId: "op_1", seq: 2, items: [multipart(incremental({ receiving: true, completed: false }))] }));
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await act(settle);
    expect(opsCancel).toHaveBeenCalledWith("op_1");
  });
});
