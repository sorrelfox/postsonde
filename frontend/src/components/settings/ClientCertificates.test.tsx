import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const service = vi.hoisted(() => ({
  ClientCerts: vi.fn<() => Promise<unknown>>(),
  PickClientCertFile: vi.fn<(kind: string) => Promise<string>>(),
  AddClientCert: vi.fn<(input: unknown) => Promise<unknown>>(),
  UpdateClientCert: vi.fn<(id: string, input: unknown) => Promise<unknown>>(),
  RemoveClientCert: vi.fn<(id: string) => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/settings", () => ({
  SettingsService: service,
}));

import { emit } from "../../test/fakeWails";
import { ClientCertRows, commonName, describeClientCert } from "./ClientCertificates";

const pfx = {
  id: "cc_1",
  host: "*.example.com",
  kind: "pkcs12",
  certFile: "",
  keyFile: "",
  pfxFile: "/Users/ann/certs/client.p12",
  hasPassphrase: true,
  subject: "CN=api client,O=Corp",
  issuer: "CN=Corp CA",
  notBefore: "2026-01-01T00:00:00Z",
  notAfter: "2027-03-01T00:00:00Z",
  expired: false,
  sha256: "ab",
  chainLength: 1,
  warnings: [],
  error: null,
};

const expired = {
  ...pfx,
  id: "cc_2",
  host: "localhost:8443",
  kind: "pem",
  pfxFile: "",
  certFile: "/Users/ann/certs/old.crt",
  keyFile: "/Users/ann/certs/old.key",
  hasPassphrase: false,
  subject: "CN=old",
  notAfter: "2025-01-01T00:00:00Z",
  expired: true,
  warnings: [{ code: "tlsconf.client_cert_expired", message: "The client certificate expired on 2025-01-01.", hint: "Renew it." }],
};

function renderRows() {
  render(
    <div>
      <ClientCertRows shorten={(p) => p} />
    </div>,
  );
}

beforeEach(() => {
  service.ClientCerts.mockReset().mockResolvedValue([]);
  service.PickClientCertFile.mockReset().mockResolvedValue("");
  service.AddClientCert.mockReset().mockResolvedValue([]);
  service.UpdateClientCert.mockReset().mockResolvedValue([]);
  service.RemoveClientCert.mockReset().mockResolvedValue([]);
});

describe("describeClientCert", () => {
  it("names the subject, the format and the expiry", () => {
    expect(describeClientCert(pfx)).toBe("api client, PKCS#12, expires 2027-03-01");
    expect(describeClientCert(expired)).toBe("old, PEM, expired 2025-01-01");
    expect(commonName("O=Corp")).toBe("O=Corp");
    expect(commonName("CN=a\\, b,O=x")).toBe("a, b");
  });
});

describe("ClientCertRows", () => {
  it("adds a PKCS#12 certificate with its passphrase through the dialog", async () => {
    renderRows();
    await waitFor(() => expect(service.ClientCerts).toHaveBeenCalled());
    expect(screen.getByText("None")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Add…" }));
    const dialog = await screen.findByRole("dialog");
    expect(dialog).toBeTruthy();
    fireEvent.change(screen.getByLabelText("Host"), { target: { value: " *.example.com " } });
    fireEvent.click(screen.getByRole("button", { name: "PKCS#12 (.p12, .pfx)" }));
    service.PickClientCertFile.mockResolvedValueOnce("/Users/ann/certs/client.p12");
    fireEvent.click(screen.getByRole("button", { name: "Choose the PKCS#12 file" }));
    await waitFor(() => expect((screen.getByLabelText("PKCS#12 file") as HTMLInputElement).value).toBe("/Users/ann/certs/client.p12"));
    expect(service.PickClientCertFile).toHaveBeenCalledWith("pfx");
    fireEvent.change(screen.getByLabelText("Passphrase"), { target: { value: "s3cret" } });

    service.AddClientCert.mockResolvedValueOnce([pfx]);
    fireEvent.click(screen.getByRole("button", { name: "Add" }));
    expect(await screen.findByText("*.example.com")).toBeTruthy();
    expect(service.AddClientCert).toHaveBeenCalledWith({
      host: "*.example.com",
      certFile: "",
      keyFile: "",
      pfxFile: "/Users/ann/certs/client.p12",
      passphrase: "s3cret",
    });
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(screen.getByText("api client, PKCS#12, expires 2027-03-01")).toBeTruthy();
    expect(screen.queryByText("s3cret")).toBeNull();
  });

  it("keeps the dialog open with Go's reason when a certificate is refused", async () => {
    renderRows();
    fireEvent.click(await screen.findByRole("button", { name: "Add…" }));
    fireEvent.change(screen.getByLabelText("Host"), { target: { value: "api.example.com" } });
    fireEvent.change(screen.getByLabelText("Certificate"), { target: { value: "/c.pem" } });
    fireEvent.change(screen.getByLabelText("Private key"), { target: { value: "/k.pem" } });
    service.AddClientCert.mockRejectedValueOnce({
      kind: "invalid_argument",
      code: "tlsconf.client_key_mismatch",
      message: "The private key does not belong to the client certificate.",
      hint: "Pick the private key that was created together with this certificate.",
      details: { field: "keyFile" },
      causes: [],
    });
    fireEvent.click(screen.getByRole("button", { name: "Add" }));
    expect((await screen.findByRole("alert")).textContent).toContain("does not belong to the client certificate");
    expect(screen.getByRole("dialog")).toBeTruthy();
    expect(screen.getByLabelText("Private key").getAttribute("aria-invalid")).toBe("true");
    expect(service.AddClientCert.mock.calls[0]![0]).toMatchObject({ passphrase: "", pfxFile: "" });
  });

  it("edits without resending the stored passphrase, lists warnings and removes", async () => {
    service.ClientCerts.mockResolvedValue([pfx, expired]);
    renderRows();
    expect(await screen.findByText("The client certificate expired on 2025-01-01.")).toBeTruthy();
    expect(screen.getByText("old, PEM, expired 2025-01-01")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Edit the client certificate for *.example.com" }));
    expect((screen.getByLabelText("Passphrase") as HTMLInputElement).placeholder).toContain("Stored");
    fireEvent.change(screen.getByLabelText("Host"), { target: { value: "api.example.com" } });
    service.UpdateClientCert.mockResolvedValueOnce([{ ...pfx, host: "api.example.com" }, expired]);
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByText("api.example.com")).toBeTruthy();
    expect(service.UpdateClientCert).toHaveBeenCalledWith("cc_1", {
      host: "api.example.com",
      certFile: "",
      keyFile: "",
      pfxFile: "/Users/ann/certs/client.p12",
      passphrase: null,
    });

    service.RemoveClientCert.mockResolvedValueOnce([expired]);
    fireEvent.click(screen.getByRole("button", { name: "Remove the client certificate for api.example.com" }));
    await waitFor(() => expect(screen.queryByText("api.example.com")).toBeNull());
    expect(service.RemoveClientCert).toHaveBeenCalledWith("cc_1");
  });

  it("shows a certificate that can no longer be used, and follows hand edits", async () => {
    service.ClientCerts.mockResolvedValueOnce([
      {
        ...pfx,
        subject: "",
        notAfter: "",
        error: { kind: "invalid_argument", code: "pkcs12.wrong_password", message: "The password of the PKCS#12 file is wrong.", hint: "Enter it again.", details: {}, causes: [] },
      },
    ]);
    renderRows();
    expect((await screen.findByRole("alert")).textContent).toContain("password of the PKCS#12 file is wrong");

    service.ClientCerts.mockResolvedValueOnce([]);
    act(() => emit("settings:changed", { settings: {}, problems: [], source: "file" }));
    expect(await screen.findByText("None")).toBeTruthy();
  });
});
