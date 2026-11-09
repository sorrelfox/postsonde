// Calls into the Go SettingsService (internal/app/services/settings): the
// settings file (SET-01) and where it is. See docs/settings.md.
//
// goSettingsPersistence adapts the service to SettingsPersistence
// (./persist), so commands/settings.ts and the settings store keep their
// flat keys (`splitRatio`) while the file groups them in sections
// (`ui.splitRatio`).
import { Events } from "@wailsio/runtime";
import type { ErrorDTO } from "../../bindings/github.com/sorrelfox/postsonde/internal/app/apperr";
import {
  SettingsService,
  type AllowedDirDTO,
  type CABundleDTO,
  type ChangedEventDTO,
  type ClientCertDTO,
  type ClientCertInputDTO,
  type EditorSettingsPatchDTO,
  type FileAccessDTO,
  type LocationDTO,
  type NetworkSettingsPatchDTO,
  type ProxyDTO,
  type ProxyInputDTO,
  type ProxyTestDTO,
  type SettingsDTO,
  type SettingsPatchDTO,
  type UISettingsPatchDTO,
} from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/settings";
import type { CancellablePromise } from "./calls";
import { formatError, toAppError, type AppError } from "./errors";
import type { PersistedSettings, SettingsPersistence } from "./persist";

export type {
  AllowedDirDTO,
  CABundleDTO,
  ChangedEventDTO,
  ClientCertDTO,
  ClientCertInputDTO,
  FileAccessDTO,
  LocationDTO,
  ProxyDTO,
  ProxyInputDTO,
  ProxyTestDTO,
  SettingsDTO,
  SettingsPatchDTO,
};

/** Sent by Go after every change: an update (`source: "app"`) or a hand edit of the file (`"file"`). */
export const SETTINGS_CHANGED_EVENT = "settings:changed";

/** Returns the settings in effect. */
export function getSettings(): CancellablePromise<SettingsDTO> {
  return SettingsService.Get();
}

/**
 * Applies the non-null fields of `patch` and saves the file. Rejects with an
 * `invalid_argument` error (code `settings.invalid_value`) and changes
 * nothing when a value is invalid.
 */
export function updateSettings(patch: SettingsPatchDTO): CancellablePromise<SettingsDTO> {
  return SettingsService.Update(patch);
}

/** Returns what is wrong with the settings file (invalid values, a broken or newer file); `[]` when nothing is. */
export async function getSettingsProblems(): Promise<AppError[]> {
  return toProblems(await SettingsService.Problems());
}

/** Calls `callback` with every settings:changed event. Returns the unsubscribe function. */
export function onSettingsChanged(callback: (event: ChangedEventDTO) => void): () => void {
  return Events.On(SETTINGS_CHANGED_EVENT, (ev) => callback(ev.data));
}

/** Returns where the settings file and the data directory are. */
export function getSettingsLocation(): CancellablePromise<LocationDTO> {
  return SettingsService.Location();
}

/**
 * Opens the settings file in the default app for JSON files, writing it
 * first if it does not exist yet. Rejects with an AppError (code
 * `settings.open_failed`) when the OS cannot open it.
 */
export async function openSettingsFile(): Promise<void> {
  try {
    await SettingsService.OpenFile();
  } catch (err: unknown) {
    throw toAppError(err);
  }
}

/** Shows the settings file in the file manager (Finder, Explorer). Rejects as openSettingsFile. */
export async function revealSettingsFile(): Promise<void> {
  try {
    await SettingsService.RevealFile();
  } catch (err: unknown) {
    throw toAppError(err);
  }
}

/**
 * The custom CA bundles (NET-04, network.caBundles): PEM files every TLS
 * connection trusts on top of the system roots, each as Go reads it now
 * (certificates, warnings such as an expired CA, or the error that stops
 * it from being used). Rejects with an AppError.
 */
export async function listCABundles(): Promise<CABundleDTO[]> {
  try {
    return (await SettingsService.CABundles()) ?? [];
  } catch (err: unknown) {
    throw toAppError(err);
  }
}

/**
 * Shows the native file dialog and trusts the chosen PEM file. Resolves
 * with the bundles afterwards (unchanged when the dialog is cancelled);
 * rejects with an AppError when the file is not usable, e.g.
 * `tlsconf.ca_no_certificates` for a file that is not PEM.
 */
export async function addCABundle(): Promise<CABundleDTO[]> {
  try {
    return (await SettingsService.AddCABundle()) ?? [];
  } catch (err: unknown) {
    throw toAppError(err);
  }
}

/**
 * Trusts the PEM file at `path` without a dialog (the local servers' CA,
 * SRV-05), validated like a chosen one. Resolves with the bundles
 * afterwards; a path already trusted changes nothing.
 */
export async function trustCABundle(path: string): Promise<CABundleDTO[]> {
  try {
    return (await SettingsService.TrustCABundle(path)) ?? [];
  } catch (err: unknown) {
    throw toAppError(err);
  }
}

/** Stops trusting the bundle at `path`. Resolves with the bundles afterwards. */
export async function removeCABundle(path: string): Promise<CABundleDTO[]> {
  try {
    return (await SettingsService.RemoveCABundle(path)) ?? [];
  } catch (err: unknown) {
    throw toAppError(err);
  }
}

/**
 * The allowed directories for file access (PRV-10, privacy.allowedDirs):
 * the folders whose files {{$file PATH}} may insert, besides the open
 * project's, each with the error that stops it from allowing anything (a
 * folder that is gone). Rejects with an AppError.
 */
export async function listAllowedDirs(): Promise<AllowedDirDTO[]> {
  try {
    return (await SettingsService.AllowedDirs()) ?? [];
  } catch (err: unknown) {
    throw toAppError(err);
  }
}

/**
 * Shows the native folder dialog and allows the chosen folder. Resolves
 * with the list afterwards (unchanged when the dialog is cancelled).
 */
export async function addAllowedDir(): Promise<AllowedDirDTO[]> {
  try {
    return (await SettingsService.AddAllowedDir()) ?? [];
  } catch (err: unknown) {
    throw toAppError(err);
  }
}

/**
 * Whether the features limited to the allowed directories (PRV-10: a JWT
 * key, schema, .proto or certificate file) may read `path`, as stored in a
 * project (absolute, `~/…`, or relative to the open project), and which
 * folder would allow it. Reads nothing. Rejects with an AppError.
 */
export async function checkFileAccess(path: string): Promise<FileAccessDTO> {
  try {
    return await SettingsService.CheckFileAccess(path);
  } catch (err: unknown) {
    throw toAppError(err);
  }
}

/**
 * Allows the folder at `path` (the offer of a file picker after
 * checkFileAccess). Resolves with the list afterwards; rejects with an
 * AppError (settings.locked when an administrator manages the list).
 */
export async function allowDirectory(path: string): Promise<AllowedDirDTO[]> {
  try {
    return (await SettingsService.AllowDirectory(path)) ?? [];
  } catch (err: unknown) {
    throw toAppError(err);
  }
}

/** Stops allowing the folder at `path`. Resolves with the list afterwards. */
export async function removeAllowedDir(path: string): Promise<AllowedDirDTO[]> {
  try {
    return (await SettingsService.RemoveAllowedDir(path)) ?? [];
  } catch (err: unknown) {
    throw toAppError(err);
  }
}

/**
 * The per-host client certificates for mTLS (AUTH-30, network.clientCerts):
 * a host pattern and a PEM certificate and key or a PKCS#12 file each, as
 * Go reads them now (subject, expiry, warnings, or the error that stops
 * one from being used). Passphrases stay in Go: only `hasPassphrase`
 * comes back. Rejects with an AppError.
 */
export async function listClientCerts(): Promise<ClientCertDTO[]> {
  try {
    return (await SettingsService.ClientCerts()) ?? [];
  } catch (err: unknown) {
    throw toAppError(err);
  }
}

/** Which file PickClientCertFile asks for. */
export type ClientCertFileKind = "cert" | "key" | "pfx";

/** Shows the native file dialog for a client certificate file; resolves with its path, `""` when cancelled. */
export async function pickClientCertFile(kind: ClientCertFileKind): Promise<string> {
  try {
    return await SettingsService.PickClientCertFile(kind);
  } catch (err: unknown) {
    throw toAppError(err);
  }
}

/**
 * Checks and adds a client certificate (the files are read now: they must
 * decode and the key must match). Rejects with an AppError, e.g.
 * `pkcs12.wrong_password`, `tlsconf.client_key_mismatch` or
 * `tlsconf.host_pattern_invalid`; nothing is saved then.
 */
export async function addClientCert(input: ClientCertInputDTO): Promise<ClientCertDTO[]> {
  try {
    return (await SettingsService.AddClientCert(input)) ?? [];
  } catch (err: unknown) {
    throw toAppError(err);
  }
}

/** Replaces the client certificate `id`, checked as addClientCert; a null passphrase keeps the stored one. */
export async function updateClientCert(id: string, input: ClientCertInputDTO): Promise<ClientCertDTO[]> {
  try {
    return (await SettingsService.UpdateClientCert(id, input)) ?? [];
  } catch (err: unknown) {
    throw toAppError(err);
  }
}

/** Removes the client certificate `id` and its stored passphrase; the files are not touched. */
export async function removeClientCert(id: string): Promise<ClientCertDTO[]> {
  try {
    return (await SettingsService.RemoveClientCert(id)) ?? [];
  } catch (err: unknown) {
    throw toAppError(err);
  }
}

/** Where the proxy comes from (network.proxyMode, SOCKS-04; "system", the OS's settings, NET-01). */
export type ProxyMode = "system" | "env" | "manual" | "off";

/**
 * The proxy setting (network.proxyMode, network.proxyUrl) and the proxy
 * connections go through now, from the settings or the ALL_PROXY
 * environment variable. The manual proxy's password stays in Go: only
 * `hasPassword` comes back. Rejects with an AppError.
 */
export async function getProxy(): Promise<ProxyDTO> {
  try {
    return await SettingsService.Proxy();
  } catch (err: unknown) {
    throw toAppError(err);
  }
}

/**
 * Changes the proxy setting. Go checks the URL first
 * (`netx.proxy_invalid_url`, detail `field` "url" or "password") and
 * moves a password, typed into the URL or the password field, to its
 * local secret storage. A null password keeps the stored one, "" removes
 * it. Rejects with an AppError; nothing is saved then.
 */
export async function setProxy(input: ProxyInputDTO): Promise<ProxyDTO> {
  try {
    return await SettingsService.SetProxy(input);
  } catch (err: unknown) {
    throw toAppError(err);
  }
}

/**
 * Replaces the proxy bypass list (network.proxyBypass, SOCKS-09). Go
 * checks every entry first (`netx.proxy_invalid_bypass`, details `entry`
 * and `field` "bypass"). Rejects with an AppError; nothing is saved then.
 */
export async function setProxyBypass(entries: string[]): Promise<ProxyDTO> {
  try {
    return await SettingsService.SetProxyBypass(entries);
  } catch (err: unknown) {
    throw toAppError(err);
  }
}

/**
 * Tests the proxy in effect (SOCKS-11): Go connects to `target`
 * (host:port) through it and closes the connection. A failed test resolves
 * with `ok: false`, the step it failed at and the error; the promise
 * rejects (with an AppError) only when the test could not run.
 */
export async function testProxy(target: string): Promise<ProxyTestDTO> {
  try {
    return await SettingsService.TestProxy({ target });
  } catch (err: unknown) {
    throw toAppError(err);
  }
}

/**
 * The admin policy (SET-02): settings locked by an administrator in a
 * system-wide policy file. Read once by Go at startup.
 */
export interface SettingsPolicy {
  /** True when the policy locks or requires anything. */
  active: boolean;
  /** Where the policy file is looked for ("" when it is not). */
  path: string;
  /** The locked setting keys as in settings.json, e.g. `network.verifyTls`. */
  locked: readonly string[];
  /** No request or connection may turn certificate verification off. */
  requireTlsVerification: boolean;
  /** What is wrong with the policy file (`settings.policy_invalid`). */
  problems: readonly AppError[];
}

/** No policy: nothing locked. */
export const NO_POLICY: SettingsPolicy = { active: false, path: "", locked: [], requireTlsVerification: false, problems: [] };

/** Fetches the admin policy; rejects with an AppError. */
export async function getSettingsPolicy(): Promise<SettingsPolicy> {
  try {
    const dto = await SettingsService.Policy();
    return {
      active: dto.active,
      path: dto.path,
      locked: dto.locked ?? [],
      requireTlsVerification: dto.requireTlsVerification,
      problems: toProblems(dto.problems),
    };
  } catch (err: unknown) {
    throw toAppError(err);
  }
}

/**
 * The key in settings.json (`section.field`) of each of the store's flat
 * setting keys, as toPatchDTO maps them. The admin policy names settings
 * by these keys.
 */
export const SETTING_FILE_KEYS: Readonly<Record<string, string>> = {
  theme: "ui.theme",
  sidebarCollapsed: "ui.sidebarCollapsed",
  splitRatio: "ui.splitRatio",
  responsePosition: "ui.responsePosition",
  splitRatioBelow: "ui.splitRatioBelow",
  customTheme: "ui.customTheme",
  locale: "ui.locale",
  defaultTimeoutMs: "network.defaultTimeoutMs",
  verifyTls: "network.verifyTls",
  maxResponseBodyMb: "network.maxResponseBodyMb",
  editorFontFamily: "editor.fontFamily",
  editorFontSize: "editor.fontSize",
  editorTabSize: "editor.tabSize",
  editorWordWrap: "editor.wordWrap",
  editorLineNumbers: "editor.lineNumbers",
  editorMatchBrackets: "editor.matchBrackets",
};

/** The settings.json key of a store key; a key that already has a section is returned as it is. */
export function settingFileKey(key: string): string {
  return SETTING_FILE_KEYS[key] ?? key;
}

/** Converts the problems Go sends into AppErrors. */
export function toProblems(dtos: readonly ErrorDTO[] | null | undefined): AppError[] {
  return (dtos ?? []).map((dto) => toAppError({ ...dto, details: { ...dto.details }, causes: dto.causes ?? [] }));
}

/** The settings as the store's flat keys (see `settings/load`). */
export function toPersisted(settings: SettingsDTO): PersistedSettings {
  const { theme, sidebarCollapsed, splitRatio, responsePosition, splitRatioBelow } = settings.ui;
  // A backend older than custom themes sends none: "" (none) is its meaning.
  const customTheme = (settings.ui as Partial<SettingsDTO["ui"]>).customTheme ?? "";
  // One older than the UI language (UI-10): "system".
  const locale = (settings.ui as Partial<SettingsDTO["ui"]>).locale ?? "system";
  const { defaultTimeoutMs, verifyTls, maxResponseBodyMb } = settings.network;
  // A backend older than the editor section sends none: the store keeps its values.
  const editor: Partial<SettingsDTO["editor"]> = settings.editor ?? {};
  return {
    theme,
    customTheme,
    locale,
    sidebarCollapsed,
    splitRatio,
    responsePosition,
    splitRatioBelow,
    defaultTimeoutMs,
    verifyTls,
    maxResponseBodyMb,
    editorFontFamily: editor.fontFamily,
    editorFontSize: editor.fontSize,
    editorTabSize: editor.tabSize,
    editorWordWrap: editor.wordWrap,
    editorLineNumbers: editor.lineNumbers,
    editorMatchBrackets: editor.matchBrackets,
    // null (no custom shortcuts) is {}; a backend without the section sends none: the store keeps its value.
    shortcuts: settings.shortcuts === undefined ? undefined : (settings.shortcuts ?? {}),
  };
}

const isRecord = (v: unknown): v is Record<string, unknown> => typeof v === "object" && v !== null && !Array.isArray(v);

const allNull = (section: object) => Object.values(section).every((v) => v === null);

/**
 * Turns a patch of the store's flat keys into the service's patch, with
 * null for every field left as it is. Keys it does not know and values of
 * the wrong type are dropped; Go validates the rest. Returns null when
 * nothing is left to save.
 */
export function toPatchDTO(patch: PersistedSettings): SettingsPatchDTO | null {
  const str = (v: unknown) => (typeof v === "string" ? v : null);
  const num = (v: unknown) => (typeof v === "number" ? v : null);
  const bool = (v: unknown) => (typeof v === "boolean" ? v : null);
  const ui: UISettingsPatchDTO = {
    theme: str(patch.theme),
    customTheme: str(patch.customTheme),
    locale: str(patch.locale),
    sidebarCollapsed: bool(patch.sidebarCollapsed),
    splitRatio: num(patch.splitRatio),
    responsePosition: str(patch.responsePosition),
    splitRatioBelow: num(patch.splitRatioBelow),
  };
  const network: NetworkSettingsPatchDTO = {
    defaultTimeoutMs: num(patch.defaultTimeoutMs),
    verifyTls: bool(patch.verifyTls),
    maxResponseBodyMb: num(patch.maxResponseBodyMb),
  };

  const editor: EditorSettingsPatchDTO = {
    fontFamily: str(patch.editorFontFamily),
    fontSize: num(patch.editorFontSize),
    tabSize: num(patch.editorTabSize),
    wordWrap: bool(patch.editorWordWrap),
    lineNumbers: bool(patch.editorLineNumbers),
    matchBrackets: bool(patch.editorMatchBrackets),
  };
  // The custom shortcuts (UI-07) are replaced as a whole.
  const shortcuts = isRecord(patch.shortcuts) ? (patch.shortcuts as SettingsPatchDTO["shortcuts"]) : null;
  if (allNull(ui) && allNull(network) && allNull(editor) && shortcuts === null) return null;
  const dto: Partial<SettingsPatchDTO> = {
    ui: allNull(ui) ? null : ui,
    network: allNull(network) ? null : network,
    editor: allNull(editor) ? null : editor,
  };
  // Left out rather than null when unchanged: Go reads both as "leave as it is".
  if (shortcuts !== null) dto.shortcuts = shortcuts;
  return dto as SettingsPatchDTO;
}

export interface GoSettingsOptions {
  /**
   * Called with the file's problems when they change: after `load()` and on
   * settings:changed events, with `[]` once they are fixed. Default: a
   * console warning per problem.
   */
  onProblems?: (problems: readonly AppError[]) => void;
}

/** Logs each problem as a console warning. */
export function warnSettingsProblems(problems: readonly AppError[]): void {
  for (const p of problems) {
    console.warn(`settings: ${formatError(p)}`, p);
  }
}

/**
 * The settings file, through SettingsService, as a SettingsPersistence:
 * `load()` is Get (and reports Problems), `save(patch)` is Update, and
 * `subscribe` follows settings:changed events for changes made to the file
 * outside the app.
 */
export function goSettingsPersistence(options: GoSettingsOptions = {}): SettingsPersistence {
  const onProblems = options.onProblems ?? warnSettingsProblems;

  // Report problems only when they differ from the last report, so a
  // lasting one (a file from a newer version) is not repeated on every save.
  // Starts at "none", so a fine file reports nothing.
  let lastReport = "[]";
  const report = (problems: AppError[]) => {
    const key = JSON.stringify(problems.map((p) => [p.code, p.message, p.details]));
    if (key === lastReport) return;
    lastReport = key;
    onProblems(problems);
  };

  return {
    async load() {
      try {
        const [settings, problems] = await Promise.all([SettingsService.Get(), SettingsService.Problems()]);
        report(toProblems(problems));
        return toPersisted(settings);
      } catch (err: unknown) {
        console.warn("settings: could not load the settings", toAppError(err));
        return {};
      }
    },

    async save(patch) {
      const dto = toPatchDTO(patch);
      if (dto === null) return;
      try {
        await SettingsService.Update(dto);
      } catch (err: unknown) {
        throw toAppError(err);
      }
    },

    subscribe(onChange) {
      return onSettingsChanged((ev) => {
        report(toProblems(ev.problems));
        // "app" events echo this window's own saves, which the store already
        // has. Applying one late could undo a newer change still being saved.
        if (ev.source !== "app") onChange(toPersisted(ev.settings));
      });
    },
  };
}
