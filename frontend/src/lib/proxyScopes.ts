// Calls into the Go ProxyScopesService (internal/app/services/proxyscopes):
// the proxies of the open project and of its environments (SOCKS-08), and
// the proxy in effect with the scope it comes from. Scope passwords stay
// in Go: only `hasPassword` comes back. See docs/network.md#proxy-scopes.
import { Events } from "@wailsio/runtime";
import {
  ProxyScopesService,
  type EffectiveDTO,
  type EnvironmentScopeDTO,
  type ScopeDTO,
  type ScopeInputDTO,
  type ScopesDTO,
} from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/proxyscopes";
import { toAppError } from "./errors";

export type { EffectiveDTO, EnvironmentScopeDTO, ScopeDTO, ScopeInputDTO, ScopesDTO };

/** A scope's mode: the next level out decides, its own proxy, or none. */
export type ScopeMode = "inherit" | "manual" | "off";

/** Sent by Go after a scope was changed. */
export const PROXY_SCOPES_CHANGED_EVENT = "proxyscopes:changed";

/**
 * The events after which the scopes or the proxy in effect may differ: a
 * scope saved, the settings (the app's proxy), the environments (another
 * active one, one renamed or deleted) and the open project.
 */
const INVALIDATING_EVENTS = [PROXY_SCOPES_CHANGED_EVENT, "settings:changed", "environments:changed", "workspace:changed"] as const;

/** The scopes of the open project and the proxy in effect. Rejects with an AppError. */
export async function getProxyScopes(): Promise<ScopesDTO> {
  try {
    return await ProxyScopesService.Scopes();
  } catch (err: unknown) {
    throw toAppError(err);
  }
}

/**
 * Changes one scope. Go checks the URL and the bypass list first (detail
 * `field` "url", "password" or "bypass") and moves a password to the
 * project's local secret storage; a null password keeps the stored one,
 * "" removes it. Rejects with an AppError; nothing is saved then.
 */
export async function setProxyScope(input: ScopeInputDTO): Promise<ScopesDTO> {
  try {
    return await ProxyScopesService.SetScope(input);
  } catch (err: unknown) {
    throw toAppError(err);
  }
}

/** Calls `callback` after every event that may change the scopes or the proxy in effect. Returns the unsubscribe function. */
export function onProxyScopesInvalidated(callback: () => void): () => void {
  const offs = INVALIDATING_EVENTS.map((name) => Events.On(name, () => callback()));
  return () => {
    for (const off of offs) off();
  };
}

/** Splits a typed bypass list at commas, semicolons, spaces and line breaks, as Go's NO_PROXY reader does. */
export function splitBypassList(text: string): string[] {
  return text.split(/[\s,;]+/).filter((entry) => entry !== "");
}
