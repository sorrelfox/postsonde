import { act, fireEvent, render, waitFor, within } from "@testing-library/react";
import { EditorView } from "@codemirror/view";
import { beforeEach, describe, expect, it, vi } from "vitest";

const service = vi.hoisted(() => ({
  Request: vi.fn<(id: string) => Promise<unknown>>(),
  UpdateRequest: vi.fn<(input: { request: unknown; revision: number }) => Promise<void>>(),
  Flush: vi.fn<(id: string) => Promise<void>>(),
  InheritedAuth: vi.fn<(id: string) => unknown>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/collection", () => ({
  CollectionService: service,
}));
const http = vi.hoisted(() => ({
  JWTPreview: vi.fn<(input: { requestId: string }) => unknown>(),
  NetrcFile: vi.fn<(input: { requestId: string; file: string }) => unknown>(),
  NetrcCheck: vi.fn<(input: { requestId: string }) => unknown>(),
  OAuth2Token: vi.fn<(input: { requestId: string }) => unknown>(),
  OAuth2GetToken: vi.fn<(input: { requestId: string; refresh: boolean }) => unknown>(),
  OAuth2ClearToken: vi.fn<(input: { requestId: string }) => unknown>(),
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/http", () => ({
  HTTPService: http,
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/format", () => ({
  FormatService: {
    ValidateJSON: () => Object.assign(Promise.resolve({ valid: true, error: null }), { cancel: () => undefined }),
  },
}));

import { isTemplateReference, OAUTH2_TOKEN_CHANGED_EVENT, secretStorage, type OAuth2TokenDTO } from "../../lib/auth";
import type { AuthDTO, RequestDTO } from "../../lib/collection";
import { createStores, StoreProvider, type Stores, type TreeNode } from "../../stores";
import { emit, FakeCancelError, resetFakeWails, settle } from "../../test/fakeWails";
import { TabPanel } from "../tabs/TabPanel";
import { AuthEditor } from "./AuthEditor";

const cancellable = <T,>(value: T) => Object.assign(Promise.resolve(value), { cancel: vi.fn() });

const auth = (over: Partial<AuthDTO> = {}): AuthDTO => ({
  type: "inherit",
  username: "",
  password: "",
  prefix: null,
  token: "",
  key: "",
  value: "",
  in: "",
  disabled: false,
  localSecrets: [],
  extra: "",
  ...over,
});

function request(over: Partial<AuthDTO> = {}): RequestDTO {
  return {
    id: "req",
    kind: "http",
    name: "Orders",
    description: "",
    method: "GET",
    url: "/orders",
    params: null,
    pathParams: null,
    headers: null,
    body: { mode: "none", type: "", text: "", fields: null, parts: null, path: "", contentType: "", extra: "" },
    query: "",
    variables: "",
    operationName: "",
    connectionParams: "",
    schemaFile: "",
    address: "",
    sendMode: "",
    lineEnding: "",
    message: "",
    subprotocols: null,
    messageFormat: "",
    service: "",
    proto: { files: [], importPaths: [], inherit: false, extra: "" },
    metadata: [],
    bodyExamples: [],
    auth: auth(over),
    settings: { timeoutMs: null, followRedirects: null, maxRedirects: null, followOriginalMethod: null, forwardAuthorization: null, verifyTls: null, connectTimeoutMs: null, urlEncoding: "", httpVersion: "", sendCookies: null, storeCookies: null, tls: null, deadlineMs: null, extra: "" },
    extra: "",
  };
}

const node = (over: Partial<TreeNode> = {}): TreeNode =>
  ({ id: "req", kind: "request", parentId: null, name: "Orders", path: "req.json", fileKind: "http", readOnly: "", method: "GET", ...over }) as TreeNode;

async function setup(dto: RequestDTO = request(), opts: { readOnly?: string } = {}): Promise<Stores> {
  service.Request.mockImplementation(() => Promise.resolve(structuredClone(dto)));
  const stores = createStores();
  stores.collection.dispatch({ type: "collection/loading", workspaceId: "ws_1" });
  stores.collection.dispatch({ type: "collection/load", workspaceId: "ws_1", nodes: [node({ readOnly: opts.readOnly ?? "" })], rootIds: ["req"] });
  stores.tabs.dispatch({ type: "tabs/restore", order: ["req"], activeId: "req" });
  stores.drafts.dispatch({ type: "drafts/setSubTab", id: "req", subTab: "auth" });
  render(
    <StoreProvider stores={stores}>
      <TabPanel />
    </StoreProvider>,
  );
  await act(settle);
  return stores;
}

const section = () => document.querySelector("[data-auth-type]") as HTMLElement;
const radio = (name: string) => within(section()).getByRole("radio", { name }) as HTMLInputElement;
const textbox = (name: string) => within(section()).getByRole("textbox", { name }) as HTMLElement;
const viewOf = (name: string): EditorView => {
  const view = EditorView.findFromDOM(textbox(name));
  if (view === null) throw new Error(`${name} is not a CodeMirror field`);
  return view;
};
/** The password input labelled `name` (not a textbox role). */
const password = (name: string) => section().querySelector(`input[type="password"][aria-label="${name}"]`) as HTMLInputElement | null;
const type = (name: string, text: string) =>
  act(() => {
    const view = viewOf(name);
    view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: text }, userEvent: "input.type" });
  });
/** The auth of the last request sent to UpdateRequest. */
const saved = (): AuthDTO => {
  const calls = service.UpdateRequest.mock.calls;
  return (calls[calls.length - 1]?.[0].request as RequestDTO).auth;
};

beforeEach(() => {
  resetFakeWails();
  service.Request.mockReset();
  service.UpdateRequest.mockReset().mockResolvedValue(undefined);
  service.Flush.mockReset().mockResolvedValue(undefined);
  service.InheritedAuth.mockReset().mockImplementation(() =>
    cancellable({ type: "bearer", disabled: false, folderId: "fld_users", folderName: "Users", auth: auth({ type: "bearer", token: "{{token}}" }) }),
  );
  http.OAuth2Token.mockReset().mockImplementation(() => cancellable(tokenDTO()));
  http.OAuth2GetToken.mockReset();
  http.OAuth2ClearToken.mockReset();
});

/** The stored token of an oauth2 auth, as Go's OAuth2Token returns it. */
const tokenDTO = (over: Partial<OAuth2TokenDTO> = {}): OAuth2TokenDTO => ({
  available: true,
  key: "k1",
  grant: "client_credentials",
  status: "none",
  token: "",
  tokenType: "",
  scope: "",
  expiresAt: 0,
  obtainedAt: 0,
  refreshable: false,
  folderName: "",
  sendsIdToken: false,
  idToken: null,
  ...over,
});

describe("AuthEditor", () => {
  it("shows the inherited auth without saving anything", async () => {
    await setup();
    expect(radio("Inherit").checked).toBe(true);
    expect(["Inherit", "None", "Bearer", "Basic", "Digest", "API key"].map((n) => radio(n).value)).toEqual([
      "inherit",
      "none",
      "bearer",
      "basic",
      "digest",
      "apikey",
    ]);
    expect(service.InheritedAuth).toHaveBeenCalledWith("req");
    expect(within(section()).getByRole("status").textContent).toContain("Bearer token, inherited from “Users”");
    expect(service.UpdateRequest).not.toHaveBeenCalled();
  });

  it("lists the inherited auth's fields with their variables as chips (AUTH-32)", async () => {
    await setup();
    const token = within(section()).getByRole("group", { name: "Inherited Token" });
    expect(token.textContent).toBe("Token{{token}}");
    expect(token.querySelector("[class*='chip']")?.textContent).toBe("{{token}}");
    expect(within(section()).getByRole("group", { name: "Inherited Prefix" }).textContent).toBe("PrefixBearer");
    expect(section().textContent).toContain("resolve with this request's variables");

    // A typed secret stays in the folder: masked, never sent to the request's view.
    service.InheritedAuth.mockImplementation(() =>
      cancellable({
        type: "apikey",
        disabled: false,
        folderId: "fld_users",
        folderName: "Users",
        auth: auth({ type: "apikey", key: "{{keyName}}", value: "", in: "query", localSecrets: ["value"] }),
      }),
    );
    fireEvent.click(radio("None"));
    await act(settle);
    fireEvent.click(radio("Inherit"));
    await act(settle);
    expect(within(section()).getByRole("group", { name: "Inherited Key" }).textContent).toBe("Key{{keyName}}");
    expect(within(section()).getByRole("group", { name: "Inherited Value" }).textContent).toContain("stored with “Users”");
    expect(within(section()).getByRole("group", { name: "Inherited Add to" }).textContent).toBe("Add toQuery params");
  });

  it("says when no folder sets auth, and shows a failed lookup", async () => {
    service.InheritedAuth.mockImplementation(() => cancellable({ type: "none", disabled: false, folderId: "", folderName: "", auth: auth({ type: "none" }) }));
    await setup();
    expect(within(section()).getByRole("status").textContent).toContain("No folder above this request sets one");
  });

  it("edits a bearer token, its prefix and the disable toggle", async () => {
    await setup();
    fireEvent.click(radio("Bearer"));
    await act(settle);
    expect(saved().type).toBe("bearer");
    expect(section().dataset.authType).toBe("bearer");

    // An empty token is shown as a template field; the prefix defaults to Bearer.
    expect(viewOf("Prefix").state.doc.toString()).toBe("Bearer");
    type("Token", "{{token}}");
    await act(settle);
    expect(saved()).toMatchObject({ type: "bearer", token: "{{token}}", prefix: null });
    expect(section().textContent).toContain("Saved in the project as a reference");
    expect(section().textContent).toContain("Authorization: Bearer …");

    type("Prefix", "");
    await act(settle);
    expect(saved().prefix).toBe("");
    expect(section().textContent).toContain("Authorization: …");

    const send = within(section()).getByRole("checkbox", { name: "Send this auth" }) as HTMLInputElement;
    expect(send.checked).toBe(true);
    fireEvent.click(send);
    await act(settle);
    expect(saved().disabled).toBe(true);
    expect(section().textContent).toContain("Disabled: nothing is sent");
  });

  it("masks a typed password, shows it on request, and drops the local mark when it is edited", async () => {
    await setup(request({ type: "basic", username: "zoë", password: "hunter2", localSecrets: ["password"] }));
    expect(viewOf("Username").state.doc.toString()).toBe("zoë");
    const field = password("Password");
    expect(field?.value).toBe("hunter2");
    expect(section().textContent).toContain("Stored only on this computer");

    // Editing another field keeps the mark.
    type("Username", "zoe");
    await act(settle);
    expect(saved()).toMatchObject({ username: "zoe", password: "hunter2", localSecrets: ["password"] });

    fireEvent.change(field as HTMLInputElement, { target: { value: "{{password}}" } });
    await act(settle);
    expect(saved()).toMatchObject({ password: "{{password}}", localSecrets: [] });

    fireEvent.click(within(section()).getByRole("button", { name: "Show the password" }));
    expect(password("Password")).toBeNull();
    expect(viewOf("Password").state.doc.toString()).toBe("{{password}}");
    fireEvent.click(within(section()).getByRole("button", { name: "Hide the password" }));
    expect(password("Password")).not.toBeNull();
  });

  it("edits Digest credentials, the password kept like a Basic one (AUTH-06)", async () => {
    await setup();
    fireEvent.click(radio("Digest"));
    await act(settle);
    expect(saved().type).toBe("digest");
    type("Username", "Mufasa");
    type("Password", "{{password}}");
    await act(settle);
    expect(saved()).toMatchObject({ type: "digest", username: "Mufasa", password: "{{password}}" });
    expect(section().textContent).toContain("Authorization: Digest …");
    expect(section().textContent).toContain("Saved in the project as a reference");
  });

  it("warns about a secret typed on another computer", async () => {
    await setup(request({ type: "bearer", localSecrets: ["token"] }));
    expect(section().querySelector('[data-storage="missing"]')?.textContent).toContain("typed on another computer");
  });

  it("edits an API key and where it is sent", async () => {
    await setup(request({ type: "apikey" }));
    type("Key", "api_key");
    await act(settle);
    type("Value", "{{apiKey}}");
    await act(settle);
    expect(section().textContent).toContain("Sent as the header api_key: …");
    const where = within(section()).getByRole("combobox", { name: "Add the API key to" }) as HTMLSelectElement;
    expect(where.value).toBe("header");
    fireEvent.change(where, { target: { value: "query" } });
    await act(settle);
    expect(saved()).toMatchObject({ type: "apikey", key: "api_key", value: "{{apiKey}}", in: "query" });
    expect(section().textContent).toContain("?api_key=…");
  });

  it("keeps the members of other types while switching, and an unknown type as it is", async () => {
    await setup(request({ type: "edgegrid", extra: '{"realm":"r"}', username: "u" }));
    expect(radio("“edgegrid”").checked).toBe(true);
    expect(section().textContent).toContain("from a newer version of PostSonde");
    fireEvent.click(radio("Basic"));
    await act(settle);
    expect(saved()).toMatchObject({ type: "basic", username: "u", extra: "" });
    fireEvent.click(radio("Bearer"));
    await act(settle);
    fireEvent.click(radio("“edgegrid”"));
    await act(settle);
    expect(saved()).toMatchObject({ type: "edgegrid", username: "u", extra: '{"realm":"r"}' });
  });

  it("edits a JWT auth: algorithm, key, payload, claims and where it goes (AUTH-16)", async () => {
    await setup();
    fireEvent.click(radio("JWT"));
    await act(settle);
    expect(saved().type).toBe("jwt");
    const alg = within(section()).getByRole("combobox", { name: "JWT algorithm" }) as HTMLSelectElement;
    expect(alg.value).toBe("HS256");
    expect(section().textContent).toContain("Authorization: Bearer <jwt>");

    type("Secret", "{{jwtSecret}}");
    await act(settle);
    expect(saved()).toMatchObject({ type: "jwt", secret: "{{jwtSecret}}" });
    expect(section().textContent).toContain("Saved in the project as a reference");

    type("JWT payload", '{"sub": "{{user}}"}');
    await act(settle);
    fireEvent.click(within(section()).getByRole("checkbox", { name: "iat now" }));
    await act(settle);
    fireEvent.change(within(section()).getByRole("spinbutton", { name: "Expires in seconds" }), { target: { value: "300" } });
    await act(settle);
    expect(saved().jwt).toMatchObject({ algorithm: "HS256", payload: '{"sub": "{{user}}"}', issuedAt: true, expiresIn: 300 });

    // A private-key algorithm asks for a PEM key: a multi-line field.
    fireEvent.change(alg, { target: { value: "ES256" } });
    await act(settle);
    expect(saved().jwt?.algorithm).toBe("ES256");
    expect(section().textContent).toContain("An EC private key on P-256");
    expect(within(section()).queryByRole("checkbox", { name: "Secret is base64-encoded" })).toBeNull();

    // A key file instead.
    fireEvent.click(within(section()).getByRole("radio", { name: "File" }));
    type("Key file", "keys/ec.pem");
    await act(settle);
    expect(saved().jwt?.keyFile).toBe("keys/ec.pem");
    fireEvent.click(within(section()).getByRole("radio", { name: "Typed key" }));
    await act(settle);
    expect(saved().jwt?.keyFile).toBe("");

    const where = within(section()).getByRole("combobox", { name: "Send the JWT in" }) as HTMLSelectElement;
    fireEvent.change(where, { target: { value: "query" } });
    await act(settle);
    expect(saved().in).toBe("query");
    expect(section().textContent).toContain("?access_token=…");
  });

  it("masks a typed PEM key until shown", async () => {
    const pem = "-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n";
    await setup(request({ type: "jwt", secret: pem, localSecrets: ["secret"], jwt: { algorithm: "RS256", keyFile: "", secretBase64: false, header: "", payload: "{}", issuedAt: false, notBefore: false, jwtId: false, expiresIn: 0 } }));
    const masked = within(section()).getByRole("textbox", { name: "Private key" });
    expect(masked.textContent).toContain("3 lines");
    expect(masked.textContent).not.toContain("AAAA");
    expect(section().textContent).toContain("Stored only on this computer");
    fireEvent.click(within(section()).getByRole("button", { name: "Show the key" }));
    expect(viewOf("Private key").state.doc.toString()).toBe(pem);
  });

  it("previews the signed token and decodes pasted ones", async () => {
    const token =
      "eyJ0eXAiOiJKV1QiLA0KICJhbGciOiJIUzI1NiJ9.eyJpc3MiOiJqb2UiLA0KICJleHAiOjEzMDA4MTkzODAsDQogImh0dHA6Ly9leGFtcGxlLmNvbS9pc19yb290Ijp0cnVlfQ.dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk";
    http.JWTPreview.mockImplementation(() =>
      cancellable({ token, header: "{}", payload: "{}", algorithm: "HS256", folderName: "", unresolved: [] }),
    );
    await setup(request({ type: "jwt", secret: "{{s}}" }));
    expect(within(section()).queryByLabelText("Signed token")).toBeNull();
    fireEvent.click(within(section()).getByRole("button", { name: "Preview token" }));
    await act(settle);
    expect(http.JWTPreview).toHaveBeenCalledWith({ requestId: "req" });
    expect(within(section()).getByLabelText("Signed token").textContent).toBe(token);
    expect(section().textContent).toContain('"iss": "joe"');
    expect(section().textContent).toContain("Expires (exp): 2011-03-22T18:43:00Z");
    fireEvent.click(within(section()).getByRole("button", { name: "Hide" }));
    expect(within(section()).queryByLabelText("Signed token")).toBeNull();

    const input = within(section()).getByRole("textbox", { name: "Token" }) as HTMLTextAreaElement;
    fireEvent.change(input, { target: { value: "not a token" } });
    expect(section().textContent).toContain("three parts");
    fireEvent.change(input, { target: { value: `Bearer ${token}` } });
    expect(section().textContent).toContain('"typ": "JWT"');
    expect(service.UpdateRequest).not.toHaveBeenCalled();
  });

  it("edits an OAuth 1.0 auth with two secrets, each marked on its own (AUTH-15)", async () => {
    await setup(request({ type: "oauth1", consumerSecret: "typed", tokenSecret: "", localSecrets: ["consumerSecret", "tokenSecret"] }));
    expect(radio("OAuth 1.0").checked).toBe(true);
    expect(password("Consumer secret")?.value).toBe("typed");
    expect(section().querySelector('[data-storage="missing"]')?.textContent).toContain("token secret was typed on another computer");

    type("Consumer key", "dpf43f3p2l4k3l03");
    await act(settle);
    expect(saved().oauth1).toMatchObject({ consumerKey: "dpf43f3p2l4k3l03", signatureMethod: "HMAC-SHA1", omitVersion: false });
    expect(saved().localSecrets).toEqual(["consumerSecret", "tokenSecret"]);

    // Editing one secret drops only its own mark.
    type("Token secret", "{{tokenSecret}}");
    await act(settle);
    expect(saved()).toMatchObject({ tokenSecret: "{{tokenSecret}}", consumerSecret: "typed", localSecrets: ["consumerSecret"] });

    type("Access token", "nnch734d00sl2jdk");
    type("Realm", "Photos");
    await act(settle);
    expect(saved().oauth1).toMatchObject({ accessToken: "nnch734d00sl2jdk", realm: "Photos" });
    expect(section().textContent).toContain('Authorization: OAuth realm=…, oauth_consumer_key=…');

    const method = within(section()).getByRole("combobox", { name: "Signature method" }) as HTMLSelectElement;
    fireEvent.change(method, { target: { value: "HMAC-SHA256" } });
    fireEvent.click(within(section()).getByRole("checkbox", { name: "Send oauth_version=1.0" }));
    fireEvent.click(within(section()).getByRole("checkbox", { name: "Add oauth_body_hash" }));
    await act(settle);
    expect(saved().oauth1).toMatchObject({ signatureMethod: "HMAC-SHA256", omitVersion: true, bodyHash: true });

    const where = within(section()).getByRole("combobox", { name: "Send the OAuth parameters in" }) as HTMLSelectElement;
    fireEvent.change(where, { target: { value: "body" } });
    await act(settle);
    expect(saved().in).toBe("body");
    expect(within(section()).queryByRole("textbox", { name: "Realm" })).toBeNull();
    expect(section().textContent).toContain("added to the form body");

    // RSA-SHA1: the consumer secret is the private key; no token secret.
    fireEvent.change(method, { target: { value: "RSA-SHA1" } });
    await act(settle);
    expect(within(section()).getByRole("textbox", { name: "Private key" })).toBeTruthy();
    expect(within(section()).queryByRole("textbox", { name: "Token secret" })).toBeNull();
    expect(password("Token secret")).toBeNull();
  });

  it("edits an AWS Signature v4 auth with its two secrets (AUTH-17)", async () => {
    await setup(request({ type: "awsv4", secretKey: "typed", localSecrets: ["secretKey", "sessionToken"] }));
    expect(radio("AWS Signature").checked).toBe(true);
    expect(password("Secret key")?.value).toBe("typed");
    expect(section().querySelector('[data-storage="missing"]')?.textContent).toContain("session token was typed on another computer");

    type("Access key", "AKIDEXAMPLE");
    type("Region", "us-east-1");
    type("Service", "s3");
    await act(settle);
    expect(saved().aws).toMatchObject({ accessKey: "AKIDEXAMPLE", region: "us-east-1", service: "s3", unsignedPayload: false });
    expect(saved().localSecrets).toEqual(["secretKey", "sessionToken"]);
    expect(section().textContent).toContain("X-Amz-Content-Sha256");

    type("Session token", "{{sessionToken}}");
    await act(settle);
    expect(saved()).toMatchObject({ sessionToken: "{{sessionToken}}", secretKey: "typed", localSecrets: ["secretKey"] });

    fireEvent.click(within(section()).getByRole("checkbox", { name: "Unsigned payload" }));
    const where = within(section()).getByRole("combobox", { name: "Send the AWS signature in" }) as HTMLSelectElement;
    fireEvent.change(where, { target: { value: "query" } });
    await act(settle);
    expect(saved()).toMatchObject({ in: "query", aws: { unsignedPayload: true } });
    const expires = within(section()).getByRole("spinbutton", { name: "Presigned URL expires in seconds" }) as HTMLInputElement;
    expect(expires.value).toBe("3600");
    fireEvent.change(expires, { target: { value: "86400" } });
    await act(settle);
    expect(saved().aws).toMatchObject({ expires: 86400 });
    expect(section().textContent).toContain("X-Amz-Signature=…");
  });

  it("edits a Hawk auth (AUTH-18)", async () => {
    await setup(request({ type: "hawk" }));
    expect(radio("Hawk").checked).toBe(true);
    type("Hawk ID", "dh37fgj492je");
    type("Hawk key", "werxhqb98rpaxn39848xrunpaw3489ruxnpa98w4rxn");
    type("Ext", "some-app-ext-data");
    await act(settle);
    expect(saved()).toMatchObject({ hawkKey: "werxhqb98rpaxn39848xrunpaw3489ruxnpa98w4rxn", hawk: { id: "dh37fgj492je", algorithm: "sha256", ext: "some-app-ext-data" } });
    expect(within(section()).queryByRole("textbox", { name: "Dlg" })).toBeNull();
    type("App", "app-1");
    await act(settle);
    expect(within(section()).getByRole("textbox", { name: "Dlg" })).toBeTruthy();

    const algorithm = within(section()).getByRole("combobox", { name: "Hawk algorithm" }) as HTMLSelectElement;
    fireEvent.change(algorithm, { target: { value: "sha1" } });
    fireEvent.click(within(section()).getByRole("checkbox", { name: "Hash the payload" }));
    await act(settle);
    expect(saved().hawk).toMatchObject({ algorithm: "sha1", payloadHash: true, app: "app-1" });
    expect(section().textContent).toContain("Authorization: Hawk id=…, ts=…, nonce=…, hash=…, ext=…, mac=…");
  });

  it("edits an OAuth 2.0 client credentials auth (AUTH-08, AUTH-12)", async () => {
    await setup(request({ type: "oauth2" }));
    expect(radio("OAuth 2.0").checked).toBe(true);
    type("Token URL", "{{idp}}/oauth/token");
    type("Client ID", "shop");
    type("Client secret", "typed-client-secret");
    type("Scope", "orders:read");
    type("Audience", "https://api.shop");
    await act(settle);
    expect(saved()).toMatchObject({
      clientSecret: "typed-client-secret",
      oauth2: { grantType: "client_credentials", tokenUrl: "{{idp}}/oauth/token", clientId: "shop", scope: "orders:read", audience: "https://api.shop" },
    });
    expect(section().textContent).toContain("Authorization: Bearer <token>");
    expect(section().textContent).toContain("Timeline tab");

    const clientAuth = within(section()).getByRole("combobox", { name: "Send the client credentials in" }) as HTMLSelectElement;
    fireEvent.change(clientAuth, { target: { value: "body" } });
    fireEvent.click(within(section()).getByRole("button", { name: "Add parameter" }));
    await act(settle);
    const params = within(section()).getByRole("group", { name: "Extra parameters of the token request" });
    fireEvent.change(within(params).getByRole("textbox", { name: "Parameter 1 name" }), { target: { value: "tenant" } });
    await act(settle);
    fireEvent.change(within(params).getByRole("textbox", { name: "Parameter 1 value" }), { target: { value: "t1" } });
    await act(settle);
    expect(saved().oauth2).toMatchObject({ clientAuth: "body", tokenParams: [{ name: "tenant", value: "t1", enabled: true }] });

    const where = within(section()).getByRole("combobox", { name: "Send the token in" }) as HTMLSelectElement;
    fireEvent.change(where, { target: { value: "query" } });
    await act(settle);
    expect(saved().in).toBe("query");
    expect(section().textContent).toContain("?access_token=…");

    fireEvent.click(within(params).getByRole("button", { name: "Remove parameter 1" }));
    const grant = within(section()).getByRole("combobox", { name: "OAuth 2.0 grant type" }) as HTMLSelectElement;
    fireEvent.change(grant, { target: { value: "authorization_code" } });
    await act(settle);
    expect(saved().oauth2).toMatchObject({ grantType: "authorization_code", tokenParams: [] });
    // The authorization code grant is run (AUTH-07): its URLs, and no
    // warning.
    expect(section().querySelector('[data-storage="missing"]')).toBeNull();
    expect(section().textContent).toContain("opens your browser to sign in");
    type("Authorization URL", "{{idp}}/authorize");
    type("Redirect URL", "http://127.0.0.1:8765/callback");
    await act(settle);
    expect(saved().oauth2).toMatchObject({ authUrl: "{{idp}}/authorize", redirectUrl: "http://127.0.0.1:8765/callback" });
    // The password grant (AUTH-09): the username, and the password as a
    // secret kept on this computer.
    fireEvent.change(grant, { target: { value: "password" } });
    await act(settle);
    expect(section().querySelector('[data-storage="missing"]')).toBeNull();
    expect(within(section()).queryByRole("textbox", { name: "Authorization URL" })).toBeNull();
    type("Username", "{{user}}");
    await act(settle);
    type("Password", "typed-password");
    await act(settle);
    expect(saved()).toMatchObject({ username: "{{user}}", password: "typed-password", oauth2: { grantType: "password" } });
    // The implicit grant (AUTH-10): deprecated, signs in in the browser, no
    // token URL, client secret or token request parameters.
    fireEvent.change(grant, { target: { value: "implicit" } });
    await act(settle);
    expect(section().querySelector('[data-storage="missing"]')?.textContent).toContain("deprecated");
    expect(within(section()).getByRole("textbox", { name: "Authorization URL" })).toBeTruthy();
    expect(within(section()).queryByRole("textbox", { name: "Token URL" })).toBeNull();
    expect(within(section()).queryByRole("combobox", { name: "Send the client credentials in" })).toBeNull();
    expect(within(section()).queryByRole("group", { name: "Extra parameters of the token request" })).toBeNull();
    expect(section().textContent).toContain("a page there hands it to PostSonde");
    expect(section().textContent).toContain("signing in in the browser");
    // OpenID Connect (AUTH-14): the issuer, and the ID token sent instead.
    type("Issuer (OpenID Connect)", "https://idp.test");
    await act(settle);
    fireEvent.click(within(section()).getByRole("checkbox", { name: "Send the ID token instead of the access token" }));
    await act(settle);
    expect(saved().oauth2).toMatchObject({ grantType: "implicit", issuer: "https://idp.test", idToken: true });
    expect(section().textContent).toContain("its signature is not verified");
  });

  it("shows the claims of the stored ID token, masked (AUTH-14)", async () => {
    http.OAuth2Token.mockImplementation(() =>
      cancellable(
        tokenDTO({
          grant: "implicit",
          status: "valid",
          token: "••••••wxyz",
          sendsIdToken: true,
          idToken: {
            token: "••••••sig0",
            algorithm: "RS256",
            issuer: "https://idp.test",
            subject: "user-42",
            audience: ["desk"],
            expiresAt: Date.UTC(2026, 8, 23, 13, 0),
            issuedAt: Date.UTC(2026, 8, 23, 12, 0),
            name: "Ada Lovelace",
            email: "ada@example.test",
            expired: false,
            error: "",
          },
        }),
      ),
    );
    await setup(request({ type: "oauth2", oauth2: { grantType: "implicit", tokenUrl: "", authUrl: "https://idp.test/authorize", redirectUrl: "", clientId: "desk", clientAuth: "", scope: "openid", audience: "", resource: "", state: "", tokenParams: [], issuer: "", idToken: true } }));
    const claims = () => section().querySelector("[data-oauth2-id-token]") as HTMLElement | null;
    await waitFor(() => expect(claims()).not.toBeNull());
    expect(claims()?.dataset.oauth2IdToken).toBe("valid");
    expect(claims()?.querySelector("code")?.textContent).toBe("••••••sig0");
    expect(claims()?.textContent).toContain("Sent instead of the access token.");
    const list = within(claims() as HTMLElement).getByLabelText("ID token");
    expect(list.textContent).toContain("Issuerhttps://idp.test");
    expect(list.textContent).toContain("Subjectuser-42");
    expect(list.textContent).toContain("Audiencedesk");
    expect(list.textContent).toContain("Emailada@example.test");
    expect(claims()?.textContent).toContain("Its signature is not verified");
    // The implicit grant signs in in the browser too.
    http.OAuth2GetToken.mockImplementation(() => Object.assign(new Promise(() => undefined), { cancel: vi.fn() }));
    const panel = section().querySelector("[data-oauth2-token]") as HTMLElement;
    fireEvent.click(within(panel).getByRole("button", { name: "Get new token" }));
    await act(settle);
    expect(panel.textContent).toContain("Waiting for the sign-in in the browser…");
  });

  it("says a sign-in is waiting in the browser for the authorization code grant", async () => {
    http.OAuth2Token.mockImplementation(() => cancellable(tokenDTO({ grant: "authorization_code" })));
    await setup(request({ type: "oauth2", oauth2: { grantType: "authorization_code", tokenUrl: "https://idp.test/token", authUrl: "https://idp.test/authorize", redirectUrl: "", clientId: "desk", clientAuth: "", scope: "", audience: "", resource: "", state: "", tokenParams: [], issuer: "", idToken: false } }));
    const panel = () => section().querySelector("[data-oauth2-token]") as HTMLElement | null;
    await waitFor(() => expect(panel()).not.toBeNull());
    http.OAuth2GetToken.mockImplementation(() => Object.assign(new Promise(() => undefined), { cancel: vi.fn() }));
    fireEvent.click(within(panel() as HTMLElement).getByRole("button", { name: "Get new token" }));
    await act(settle);
    expect(panel()?.textContent).toContain("Waiting for the sign-in in the browser…");
    expect(within(section()).getByRole("textbox", { name: "Authorization URL" })).toBeTruthy();
  });

  it("shows the stored OAuth 2.0 token masked, and gets, refreshes and clears it (AUTH-11, AUTH-13)", async () => {
    const expiresAt = Date.UTC(2026, 8, 23, 13, 0);
    http.OAuth2Token.mockImplementation(() => cancellable(tokenDTO()));
    await setup(request({ type: "oauth2", oauth2: { grantType: "client_credentials", tokenUrl: "https://idp.test/token", authUrl: "", redirectUrl: "", clientId: "shop", clientAuth: "", scope: "", audience: "", resource: "", state: "", tokenParams: [], issuer: "", idToken: false } }));
    const panel = () => section().querySelector("[data-oauth2-token]") as HTMLElement | null;
    await waitFor(() => expect(panel()?.dataset.oauth2Token).toBe("none"));
    expect(http.OAuth2Token).toHaveBeenLastCalledWith({ requestId: "req" });
    expect(panel()?.textContent).toContain("No token yet: the next send gets one.");
    expect(panel()?.textContent).toContain("never in the project files");
    const button = (name: string) => within(panel() as HTMLElement).getByRole("button", { name }) as HTMLButtonElement;
    expect(button("Refresh").disabled).toBe(true);
    expect(button("Clear").disabled).toBe(true);

    const valid = tokenDTO({ status: "valid", token: "••••••wxyz", tokenType: "Bearer", scope: "orders:read", expiresAt, refreshable: true });
    http.OAuth2GetToken.mockImplementation(() => cancellable(valid));
    fireEvent.click(button("Get new token"));
    await act(settle);
    expect(http.OAuth2GetToken).toHaveBeenLastCalledWith({ requestId: "req", refresh: false });
    expect(panel()?.dataset.oauth2Token).toBe("valid");
    expect(panel()?.querySelector("code")?.textContent).toBe("••••••wxyz");
    expect(panel()?.textContent).toContain("is valid until");
    expect(panel()?.textContent).toContain("Scope: orders:read");

    fireEvent.click(button("Refresh"));
    await act(settle);
    expect(http.OAuth2GetToken).toHaveBeenLastCalledWith({ requestId: "req", refresh: true });

    // An error is shown, and the token kept.
    http.OAuth2ClearToken.mockImplementation(() => Object.assign(Promise.reject(new Error("disk full")), { cancel: vi.fn() }));
    fireEvent.click(button("Clear"));
    await act(settle);
    expect(within(panel() as HTMLElement).getByRole("alert").textContent).toContain("disk full");
    expect(panel()?.dataset.oauth2Token).toBe("valid");
    http.OAuth2ClearToken.mockImplementation(() => cancellable(tokenDTO()));
    fireEvent.click(button("Clear"));
    await act(settle);
    expect(panel()?.dataset.oauth2Token).toBe("none");

    // Go says a token changed (a send stored one): the status is asked again.
    http.OAuth2Token.mockImplementation(() => cancellable(tokenDTO({ status: "expired", token: "••••••wxyz", expiresAt, refreshable: true })));
    act(() => emit(OAUTH2_TOKEN_CHANGED_EVENT, { key: "k1" }));
    await waitFor(() => expect(panel()?.dataset.oauth2Token).toBe("expired"));
    expect(panel()?.textContent).toContain("The next send refreshes it.");
  });

  it("cancels getting an OAuth 2.0 token", async () => {
    await setup(request({ type: "oauth2", oauth2: { grantType: "client_credentials", tokenUrl: "https://idp.test/token", authUrl: "", redirectUrl: "", clientId: "shop", clientAuth: "", scope: "", audience: "", resource: "", state: "", tokenParams: [], issuer: "", idToken: false } }));
    const panel = () => section().querySelector("[data-oauth2-token]") as HTMLElement | null;
    await waitFor(() => expect(panel()).not.toBeNull());
    let reject: (err: unknown) => void = () => undefined;
    const cancel = vi.fn(() => reject(new FakeCancelError()));
    http.OAuth2GetToken.mockImplementation(() => Object.assign(new Promise((_, r) => (reject = r)), { cancel }));
    fireEvent.click(within(panel() as HTMLElement).getByRole("button", { name: "Get new token" }));
    await act(settle);
    expect(panel()?.textContent).toContain("Getting a token…");
    fireEvent.click(within(panel() as HTMLElement).getByRole("button", { name: "Cancel" }));
    await act(settle);
    expect(cancel).toHaveBeenCalled();
    expect(within(panel() as HTMLElement).queryByRole("alert")).toBeNull();
    expect(within(panel() as HTMLElement).getByRole("button", { name: "Get new token" })).toBeTruthy();
  });

  it("lists an inherited OAuth 2.0 auth without its typed client secret", async () => {
    service.InheritedAuth.mockImplementation(() =>
      cancellable({
        type: "oauth2",
        disabled: false,
        folderId: "fld_api",
        folderName: "API",
        auth: auth({
          type: "oauth2",
          clientSecret: "",
          localSecrets: ["clientSecret"],
          oauth2: {
            grantType: "client_credentials",
            tokenUrl: "https://idp.test/token",
            authUrl: "",
            redirectUrl: "",
            clientId: "shop",
            clientAuth: "",
            scope: "read",
            audience: "",
            resource: "",
            state: "",
            tokenParams: [],
            issuer: "",
            idToken: false,
          },
        }),
      }),
    );
    await setup();
    expect(within(section()).getByRole("status").textContent).toContain("OAuth 2.0, inherited from “API”");
    expect(within(section()).getByRole("group", { name: "Inherited Token URL" }).textContent).toBe("Token URLhttps://idp.test/token");
    expect(within(section()).getByRole("group", { name: "Inherited Client secret" }).textContent).toContain("stored with “API”");
    expect(within(section()).getByRole("group", { name: "Inherited Grant type" }).textContent).toBe("Grant typeClient credentials");
  });

  it("lists an inherited AWS auth without its typed secret", async () => {
    service.InheritedAuth.mockImplementation(() =>
      cancellable({
        type: "awsv4",
        disabled: false,
        folderId: "fld_aws",
        folderName: "AWS",
        auth: auth({
          type: "awsv4",
          secretKey: "",
          sessionToken: "{{st}}",
          localSecrets: ["secretKey"],
          aws: { accessKey: "AKID", region: "eu-west-1", service: "execute-api", unsignedPayload: false, expires: 0 },
        }),
      }),
    );
    await setup();
    expect(within(section()).getByRole("status").textContent).toContain("AWS Signature v4, inherited from “AWS”");
    expect(within(section()).getByRole("group", { name: "Inherited Access key" }).textContent).toBe("Access keyAKID");
    expect(within(section()).getByRole("group", { name: "Inherited Secret key" }).textContent).toContain("stored with “AWS”");
    expect(within(section()).getByRole("group", { name: "Inherited Session token" }).textContent).toBe("Session token{{st}}");
    expect(within(section()).getByRole("group", { name: "Inherited Region" }).textContent).toBe("Regioneu-west-1");
  });

  it("lists an inherited OAuth 1.0 auth without its typed secrets", async () => {

    service.InheritedAuth.mockImplementation(() =>
      cancellable({
        type: "oauth1",
        disabled: false,
        folderId: "fld_users",
        folderName: "Users",
        auth: auth({
          type: "oauth1",
          consumerSecret: "",
          tokenSecret: "{{ts}}",
          localSecrets: ["consumerSecret"],
          oauth1: { consumerKey: "ck", accessToken: "", signatureMethod: "", callback: "", verifier: "", realm: "", omitVersion: false, nonce: "", timestamp: "", bodyHash: false },
        }),
      }),
    );
    await setup();
    expect(within(section()).getByRole("status").textContent).toContain("OAuth 1.0, inherited from “Users”");
    expect(within(section()).getByRole("group", { name: "Inherited Consumer key" }).textContent).toBe("Consumer keyck");
    expect(within(section()).getByRole("group", { name: "Inherited Consumer secret" }).textContent).toContain("stored with “Users”");
    expect(within(section()).getByRole("group", { name: "Inherited Token secret" }).textContent).toBe("Token secret{{ts}}");
    expect(within(section()).getByRole("group", { name: "Inherited Signature" }).textContent).toBe("SignatureHMAC-SHA1");
  });

  it("edits a netrc auth, names its file, and checks the entry only on request (AUTH-19)", async () => {
    http.NetrcFile.mockReset().mockImplementation(({ file }) =>
      cancellable({ path: file === "" ? "/home/u/.netrc" : "/home/u/.netrc-work", fallback: "", source: file === "" ? "home" : "request" }),
    );
    http.NetrcCheck.mockReset().mockImplementation(() =>
      cancellable({
        path: "/home/u/.netrc-work",
        source: "request",
        host: "api.example.com",
        entry: "api.example.com",
        line: 3,
        login: "alice",
        hasPassword: true,
        insecure: true,
        urlCredentials: false,
        folderName: "",
        unresolved: [],
      }),
    );
    await setup(request({ type: "netrc" }));
    expect(radio("Netrc").checked).toBe(true);
    await act(() => new Promise((r) => setTimeout(r, 300)));
    expect(http.NetrcFile).toHaveBeenLastCalledWith({ requestId: "req", file: "" });
    expect(section().textContent).toContain("Reads /home/u/.netrc when the request is sent");
    expect(section().textContent).toContain("Authorization: Basic …");

    type("File", "~/.netrc-work");
    await act(settle);
    expect(saved().netrcFile).toBe("~/.netrc-work");
    await act(() => new Promise((r) => setTimeout(r, 300)));
    expect(http.NetrcFile).toHaveBeenLastCalledWith({ requestId: "req", file: "~/.netrc-work" });
    expect(section().textContent).toContain("Reads /home/u/.netrc-work");

    // The file is read only on a click.
    expect(http.NetrcCheck).not.toHaveBeenCalled();
    fireEvent.click(within(section()).getByRole("button", { name: "Check" }));
    await act(settle);
    expect(http.NetrcCheck).toHaveBeenCalledWith({ requestId: "req" });
    const result = section().textContent ?? "";
    expect(result).toContain("api.example.com matches machine api.example.com (line 3 of /home/u/.netrc-work): login alice, with a password.");
    expect(result).toContain("chmod 600");

    http.NetrcCheck.mockImplementation(() =>
      Object.assign(Promise.reject(new Error("The .netrc file has no entry for api.example.com.")), { cancel: vi.fn() }),
    );
    fireEvent.click(within(section()).getByRole("button", { name: "Check again" }));
    await act(settle);
    expect(within(section()).getByRole("alert").textContent).toContain("no entry for api.example.com");
  });

  it("lists an inherited netrc auth with its Check button", async () => {
    http.NetrcCheck.mockReset();
    service.InheritedAuth.mockImplementation(() =>
      cancellable({ type: "netrc", disabled: false, folderId: "fld_users", folderName: "Users", auth: auth({ type: "netrc" }) }),
    );
    await setup();
    expect(within(section()).getAllByRole("status")[0].textContent).toContain("Netrc file, inherited from “Users”");
    expect(within(section()).getByRole("group", { name: "Inherited File" }).textContent).toContain("Default: NETRC");
    expect(within(section()).getByRole("button", { name: "Check" })).toBeTruthy();
    expect(http.NetrcCheck).not.toHaveBeenCalled();
  });

  it("is read-only for a read-only request", async () => {
    await setup(request({ type: "basic", username: "u", password: "p" }), { readOnly: "newer_version" });
    for (const name of ["Inherit", "None", "Bearer", "Basic", "Digest", "API key"]) expect(radio(name).disabled).toBe(true);
    expect(viewOf("Username").state.readOnly).toBe(true);
    expect(password("Password")?.readOnly).toBe(true);
    expect((within(section()).getByRole("checkbox", { name: "Send this auth" }) as HTMLInputElement).disabled).toBe(true);
    fireEvent.click(radio("None"));
    fireEvent.change(password("Password") as HTMLInputElement, { target: { value: "x" } });
    await act(settle);
    expect(service.UpdateRequest).not.toHaveBeenCalled();
    // Showing the value still works.
    fireEvent.click(within(section()).getByRole("button", { name: "Show the password" }));
    expect(viewOf("Password").state.doc.toString()).toBe("p");
  });
});

describe("AuthEditor on a handshake (AUTH-31)", () => {
  function renderOn(a: AuthDTO, protocol: "websocket" | "socketio" | "grpc") {
    const stores = createStores();
    return render(
      <StoreProvider stores={stores}>
        <AuthEditor id="req" auth={a} readOnly={false} protocol={protocol} />
      </StoreProvider>,
    );
  }

  it("says when the type cannot be sent on the handshake instead of leaving it out", async () => {
    const { unmount } = renderOn(auth({ type: "digest", username: "u" }), "websocket");
    expect(within(section()).getByRole("status").textContent).toContain("Digest auth cannot be sent on a WebSocket handshake");
    unmount();

    // A query parameter on gRPC, which has no query string.
    const grpc = renderOn(auth({ type: "apikey", key: "k", in: "query" }), "grpc");
    expect(section().textContent).toContain("gRPC has no query string");
    grpc.unmount();

    // A header on gRPC is metadata, and the Metadata tab wins.
    const md = renderOn(auth({ type: "bearer", token: "{{t}}" }), "grpc");
    expect(section().textContent).toContain("An enabled row of the same key in the Metadata tab wins.");
    expect(within(section()).queryByRole("status")).toBeNull();
    md.unmount();

    // The inherited type counts too.
    service.InheritedAuth.mockImplementation(() =>
      cancellable({ type: "hawk", disabled: false, folderId: "fld_api", folderName: "API", auth: auth({ type: "hawk" }) }),
    );
    renderOn(auth({ type: "inherit" }), "socketio");
    await act(settle);
    expect(section().textContent).toContain("The auth inherited from “API” is Hawk");
  });
});

describe("auth helpers", () => {
  it("tells references from typed values like Go", () => {
    const cases: [string, boolean][] = [
      ["", false],
      ["{{token}}", true],
      [" {{ a }} {{b}} ", true],
      ["abc{{x}}", false],
      ["\\{{x}}", false],
      ["{{x", false],
      ["Bearer {{t}}", false],
    ];
    for (const [text, want] of cases) expect([text, isTemplateReference(text)]).toEqual([text, want]);
  });

  it("says where a secret is kept", () => {
    expect(secretStorage(auth({ type: "bearer" }), "token")).toBe("empty");
    expect(secretStorage(auth({ type: "bearer", localSecrets: ["token"] }), "token")).toBe("missing");
    expect(secretStorage(auth({ type: "bearer", token: "{{t}}" }), "token")).toBe("reference");
    expect(secretStorage(auth({ type: "bearer", token: "t" }), "token")).toBe("local");
  });
});
