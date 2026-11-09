import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, within } from "@testing-library/react";

const service = vi.hoisted(() => ({
  Create: vi.fn<(input: unknown) => Promise<unknown>>(),
  Start: vi.fn<(id: string) => Promise<unknown>>(),
  UpdateServer: vi.fn<(id: string, input: unknown) => Promise<unknown>>(),
  PickTLSFile: vi.fn<(kind: string) => Promise<unknown>>(),
  LocalCA: vi.fn<() => Promise<unknown>>(),
  RevealLocalCA: vi.fn<() => Promise<unknown>>(),
}));
const settings = vi.hoisted(() => ({
  TrustCABundle: vi.fn<(path: string) => Promise<unknown>>(),
  CheckFileAccess: vi.fn<(path: string) => Promise<unknown>>(),
  AllowDirectory: vi.fn<(path: string) => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/servers", () => ({
  ServersService: service,
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/settings", () => ({
  SettingsService: settings,
}));

import { keepTls, toLocalServer } from "../../commands/servers";
import type { ServerDTO } from "../../lib/servers";
import { createStores, StoreProvider, type Stores } from "../../stores";
import { resetFakeWails, settle } from "../../test/fakeWails";
import { runningDTO, serverDTO } from "../../test/servers";
import { FolderServerTab } from "./FolderServerTab";
import { ServersPanel } from "./ServersPanel";
import { ServerDetails } from "./ServerView";
import { tlsDraftChanged, tlsDraftOf, tlsInputOf } from "./TlsSettings";

const WS = "ws_1";
const FOLDER = "fld_api";
const CA_PATH = "/data/servers/tls/ca.pem";

function localCA(trusted: boolean) {
  return { path: CA_PATH, subject: "CN=PostSonde local CA", notAfter: "2036-09-19T00:00:00Z", sha256: "ab".repeat(32), pem: "-----BEGIN CERTIFICATE-----", trusted };
}

function savedDTO(over: Partial<ServerDTO> = {}): ServerDTO {
  return serverDTO({
    id: "srv_saved",
    type: "echo_http",
    host: "localhost",
    port: 8443,
    folder: { workspaceId: WS, folderId: FOLDER, folderName: "API" },
    ...over,
  });
}

function renderFolder(dto: ServerDTO): Stores {
  const stores = createStores();
  stores.collection.dispatch({ type: "collection/loading", workspaceId: WS });
  stores.collection.dispatch({
    type: "collection/load",
    workspaceId: WS,
    nodes: [{ id: FOLDER, kind: "folder", parentId: null, name: "API", path: "api", fileKind: "", readOnly: "", childIds: [] }] as never,
    rootIds: [FOLDER],
  });
  stores.servers.dispatch({ type: "servers/loaded", servers: [toLocalServer(dto)], types: [] });
  render(
    <StoreProvider stores={stores}>
      <FolderServerTab folderId={FOLDER} readOnly={false} />
    </StoreProvider>,
  );
  return stores;
}

beforeEach(() => {
  resetFakeWails();
  for (const fn of Object.values(service)) fn.mockReset();
  settings.TrustCABundle.mockReset();
  settings.CheckFileAccess.mockReset().mockResolvedValue({ allowed: true, folder: "", locked: false });
  settings.AllowDirectory.mockReset().mockResolvedValue([]);
  service.LocalCA.mockResolvedValue(localCA(false));
});

describe("TLS drafts", () => {
  it("turns a draft into the input Go takes", () => {
    const off = tlsDraftOf(null);
    expect(tlsInputOf(off)).toBeNull();
    expect(tlsDraftChanged(off, null)).toBe(false);
    const generated = { ...off, enabled: true };
    expect(tlsInputOf(generated)).toEqual({ certificate: "generated", certFile: "", keyFile: "", pfxFile: "", passphrase: null });
    const pfx = { ...generated, certificate: "files" as const, source: "pkcs12" as const, certFile: "/stale.pem", pfxFile: " /c/dev.p12 " };
    expect(tlsInputOf(pfx)).toEqual({ certificate: "files", certFile: "", keyFile: "", pfxFile: "/c/dev.p12", passphrase: null });
    expect(tlsInputOf({ ...pfx, passphrase: "s3cret", passphraseTouched: true })?.passphrase).toBe("s3cret");

    const saved = { certificate: "files" as const, certFile: "", keyFile: "", pfxFile: "/c/dev.p12", hasPassphrase: true };
    expect(tlsDraftOf(saved).source).toBe("pkcs12");
    expect(tlsDraftChanged(tlsDraftOf(saved), saved)).toBe(false);
    expect(tlsDraftChanged({ ...tlsDraftOf(saved), passphraseTouched: true }, saved)).toBe(true);
    expect(keepTls(saved)).toEqual({ certificate: "files", certFile: "", keyFile: "", pfxFile: "/c/dev.p12", passphrase: null });
  });
});

describe("folder Server tab TLS", () => {
  it("turns on the generated certificate at once and offers to trust the local CA", async () => {
    service.UpdateServer.mockResolvedValue(savedDTO({ tls: { certificate: "generated", certFile: "", keyFile: "", pfxFile: "", hasPassphrase: false } }));
    settings.TrustCABundle.mockResolvedValue([]);
    renderFolder(savedDTO());
    fireEvent.click(screen.getByRole("checkbox", { name: "Serve over TLS (HTTPS)" }));
    await act(settle);
    expect(service.UpdateServer).toHaveBeenCalledWith("srv_saved", {
      name: "",
      host: "localhost",
      port: 8443,
      allowLan: false,
      tls: { certificate: "generated", certFile: "", keyFile: "", pfxFile: "", passphrase: null },
    });
    expect(screen.getByText(CA_PATH)).toBeTruthy();
    expect(screen.getByLabelText("Serves HTTPS")).toBeTruthy();

    service.LocalCA.mockResolvedValue(localCA(true));
    fireEvent.click(screen.getByRole("button", { name: "Trust in PostSonde" }));
    await act(settle);
    expect(settings.TrustCABundle).toHaveBeenCalledWith(CA_PATH);
    expect(screen.getByText(/requests trust this CA/)).toBeTruthy();
  });

  it("applies a PKCS#12 file chosen in the dialog with its passphrase, and shows why Go refused it", async () => {
    service.PickTLSFile.mockResolvedValue("/certs/dev.p12");
    service.UpdateServer.mockRejectedValue({
      kind: "invalid_argument",
      code: "pkcs12.wrong_password",
      message: "The passphrase of /certs/dev.p12 is wrong.",
      hint: "Enter the passphrase of the file.",
    });
    renderFolder(savedDTO());
    fireEvent.click(screen.getByRole("checkbox", { name: "Serve over TLS (HTTPS)" }));
    await act(settle);
    service.UpdateServer.mockClear();
    fireEvent.click(screen.getByRole("radio", { name: "My certificate" }));
    fireEvent.click(screen.getByRole("radio", { name: "PKCS#12 (.p12, .pfx)" }));
    expect(service.UpdateServer).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Choose PKCS#12 file" }));
    await act(settle);
    expect(service.PickTLSFile).toHaveBeenCalledWith("pfx");
    expect((screen.getByRole("textbox", { name: "PKCS#12 file" }) as HTMLInputElement).value).toBe("/certs/dev.p12");
    fireEvent.change(screen.getByLabelText("Passphrase"), { target: { value: "wrong" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply TLS settings" }));
    await act(settle);
    expect(service.UpdateServer).toHaveBeenCalledWith("srv_saved", expect.objectContaining({
      tls: { certificate: "files", certFile: "", keyFile: "", pfxFile: "/certs/dev.p12", passphrase: "wrong" },
    }));
    const section = screen.getByRole("region", { name: "TLS" });
    expect(within(section).getByRole("alert").textContent).toContain("The passphrase of /certs/dev.p12 is wrong.");
  });

  it("offers to allow the folder of a certificate file outside the allowed directories (PRV-10)", async () => {
    service.PickTLSFile.mockResolvedValue("/certs/dev.p12");
    settings.CheckFileAccess.mockResolvedValue({ allowed: false, folder: "/certs", locked: false });
    renderFolder(savedDTO());
    fireEvent.click(screen.getByRole("checkbox", { name: "Serve over TLS (HTTPS)" }));
    await act(settle);
    fireEvent.click(screen.getByRole("radio", { name: "My certificate" }));
    fireEvent.click(screen.getByRole("radio", { name: "PKCS#12 (.p12, .pfx)" }));
    fireEvent.click(screen.getByRole("button", { name: "Choose PKCS#12 file" }));
    await act(settle);
    expect(settings.CheckFileAccess).toHaveBeenCalledWith("/certs/dev.p12");
    const section = screen.getByRole("region", { name: "TLS" });
    expect(within(section).getByRole("status").textContent).toContain("“dev.p12” is outside the directories allowed");
    fireEvent.click(within(section).getByRole("button", { name: "Allow “certs”" }));
    await act(settle);
    expect(settings.AllowDirectory).toHaveBeenCalledWith("/certs");
    expect(within(section).getByRole("status").textContent).toContain("“certs” is allowed now.");
  });

  it("keeps TLS as it is when the name changes, with the stored passphrase", async () => {
    const tls = { certificate: "files", certFile: "/c/cert.pem", keyFile: "/c/key.pem", pfxFile: "", hasPassphrase: true };
    service.UpdateServer.mockResolvedValue(savedDTO({ tls, name: "API" }));
    renderFolder(savedDTO({ tls }));
    expect((screen.getByLabelText("Passphrase") as HTMLInputElement).placeholder).toBe("Stored (type to replace)");
    fireEvent.change(screen.getByRole("textbox", { name: "Name" }), { target: { value: "API" } });
    fireEvent.blur(screen.getByRole("textbox", { name: "Name" }));
    await act(settle);
    expect(service.UpdateServer).toHaveBeenCalledWith("srv_saved", {
      name: "API",
      host: "localhost",
      port: 8443,
      allowLan: false,
      tls: { certificate: "files", certFile: "/c/cert.pem", keyFile: "/c/key.pem", pfxFile: "", passphrase: null },
    });
  });
});

describe("TLS following the server", () => {
  it("takes TLS changed elsewhere unless a change is being edited", () => {
    const stores = renderFolder(savedDTO());
    const checkbox = () => screen.getByRole("checkbox", { name: "Serve over TLS (HTTPS)" }) as HTMLInputElement;
    expect(checkbox().checked).toBe(false);
    const generated = { certificate: "generated", certFile: "", keyFile: "", pfxFile: "", hasPassphrase: false };
    act(() => stores.servers.dispatch({ type: "servers/put", server: toLocalServer(savedDTO({ tls: generated })) }));
    expect(checkbox().checked).toBe(true);

    // A file being chosen here is kept when the server changes.
    fireEvent.click(screen.getByRole("radio", { name: "My certificate" }));
    fireEvent.change(screen.getByRole("textbox", { name: "Certificate" }), { target: { value: "/c/cert.pem" } });
    act(() => stores.servers.dispatch({ type: "servers/put", server: toLocalServer(savedDTO({ tls: null })) }));
    expect((screen.getByRole("textbox", { name: "Certificate" }) as HTMLInputElement).value).toBe("/c/cert.pem");
  });
});

describe("Servers view TLS", () => {
  it("creates a server with TLS from the new-server form", async () => {
    service.Create.mockResolvedValue(serverDTO({ type: "websocket" }));
    service.Start.mockResolvedValue(runningDTO({ type: "websocket" }));
    const stores = createStores();
    render(
      <StoreProvider stores={stores}>
        <ServersPanel label="Servers" />
      </StoreProvider>,
    );
    fireEvent.click(screen.getByRole("button", { name: "New server" }));
    fireEvent.click(screen.getByRole("radio", { name: "WebSocket" }));
    fireEvent.click(screen.getByRole("checkbox", { name: "Serve over TLS (wss)" }));
    await act(settle);
    fireEvent.submit(screen.getByRole("form", { name: "New server" }));
    await act(settle);
    expect(service.Create).toHaveBeenCalledWith(expect.objectContaining({
      type: "websocket",
      tls: { certificate: "generated", certFile: "", keyFile: "", pfxFile: "", passphrase: null },
    }));
  });

  it("shows the certificate a running TLS server presents", () => {
    const dto = runningDTO({
      tls: { certificate: "generated", certFile: "", keyFile: "", pfxFile: "", hasPassphrase: false },
      certificate: {
        subject: "CN=localhost,O=PostSonde local server",
        issuer: "CN=PostSonde local CA",
        notBefore: "2026-09-19T00:00:00Z",
        notAfter: "2027-09-19T00:00:00Z",
        sha256: "cd".repeat(32),
        names: ["localhost", "127.0.0.1", "::1"],
      },
    });
    const stores = createStores();
    stores.servers.dispatch({ type: "servers/loaded", servers: [toLocalServer(dto)], types: [] });
    render(
      <StoreProvider stores={stores}>
        <ServerDetails server={toLocalServer(dto)} />
      </StoreProvider>,
    );
    const details = screen.getByTestId("server-details");
    expect(details.textContent).toContain("Generated certificate (PostSonde local CA)");
    expect(details.textContent).toContain("localhost, 127.0.0.1, ::1");
    expect(details.textContent).toContain("Sep 19, 2027");
    expect(details.textContent).toContain("cd".repeat(32));
    expect(screen.getByText("Stop the server to change its TLS settings.")).toBeTruthy();
    expect((screen.getByRole("checkbox", { name: "Serve over TLS (HTTPS)" }) as HTMLInputElement).disabled).toBe(true);
  });
});
