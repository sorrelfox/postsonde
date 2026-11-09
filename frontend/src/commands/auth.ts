// Auth tab commands (AUTH-01…06, AUTH-08, AUTH-12, AUTH-15…19). Every change is an auto-saved edit

// of the request's `auth` (editRequest); Go splits typed secrets off into
// local storage when it saves (docs/auth.md).

import { useEffect, useState } from "react";
import type { AuthDTO } from "../lib/collection";
import {
  authType,
  awsOf,
  getInheritedAuth,
  hawkOf,
  jwtOf,
  oauth1Of,
  oauth2Of,
  secretMembers,
  type AWSAuthDTO,
  type HawkAuthDTO,
  type InheritedAuthDTO,
  type JWTAuthDTO,
  type OAuth1AuthDTO,
  type OAuth2AuthDTO,
} from "../lib/auth";
import { isCancelled, toAppError } from "../lib/errors";
import type { RequestId, Stores } from "../stores";
import { editRequest } from "./autosave";

/** The auth of request `id`'s draft, or undefined without a draft. */
export function draftAuth(stores: Stores, id: RequestId): AuthDTO | undefined {
  // Drafts hold RequestDTOs (loadRequestDraft).
  return stores.drafts.getState().byId[id]?.request.auth as AuthDTO | undefined;
}

/**
 * Switches request `id`'s auth to `type`. The members of the other types
 * stay in the draft, so switching back while the tab is open restores
 * them; Go leaves them out of the file (and drops a local secret of the old
 * type).
 *
 * `extra` replaces the unknown members: the members of a type of a newer
 * PostSonde belong to that type and should not follow a switch away from
 * it (the Auth tab passes "" when leaving one and its members when going
 * back).
 */
export function setAuthType(stores: Stores, id: RequestId, type: string, extra?: string): void {
  const auth = draftAuth(stores, id);
  if (auth === undefined || authType(auth) === type) return;
  editRequest(stores, id, { auth: { ...auth, type, extra: extra ?? auth.extra } }, { history: "separate" });
}

/** The auth fields the Auth tab edits. */
export type AuthFields = Partial<
  Pick<
    AuthDTO,
    | "username"
    | "password"
    | "prefix"
    | "token"
    | "key"
    | "value"
    | "in"
    | "secret"
    | "consumerSecret"
    | "tokenSecret"
    | "secretKey"
    | "sessionToken"
    | "hawkKey"
    | "clientSecret"
    | "netrcFile"
    | "disabled"
  >
>;

/**
 * Sets fields of request `id`'s auth. Editing a secret member of the
 * current type removes its local-secret mark: the value typed now is what
 * counts, and Go marks it again if it is stored locally. Other edits keep
 * the marks, so a secret missing on this computer stays marked.
 */
export function setAuthFields(stores: Stores, id: RequestId, fields: AuthFields): void {
  const auth = draftAuth(stores, id);
  if (auth === undefined) return;
  const changed = (Object.keys(fields) as (keyof AuthFields)[]).some((k) => fields[k] !== auth[k]);
  if (!changed) return;
  const next: AuthDTO = { ...auth, ...fields };
  const edited = secretMembers(authType(auth)).filter((m) => m in fields && fields[m] !== auth[m]);
  if (edited.length > 0) {
    next.localSecrets = (auth.localSecrets ?? []).filter((m) => !(edited as string[]).includes(m));
  }
  editRequest(stores, id, { auth: next });
}

/**
 * Sets members of request `id`'s jwt auth (algorithm, key file, header,
 * payload, claims). A jwt auth without them gets the defaults first
 * (jwtOf).
 */
export function setJwtFields(stores: Stores, id: RequestId, fields: Partial<JWTAuthDTO>): void {
  const auth = draftAuth(stores, id);
  if (auth === undefined) return;
  const jwt = jwtOf(auth);
  const changed = auth.jwt == null || (Object.keys(fields) as (keyof JWTAuthDTO)[]).some((k) => fields[k] !== jwt[k]);
  if (!changed) return;
  editRequest(stores, id, { auth: { ...auth, jwt: { ...jwt, ...fields } } });
}

/**
 * Sets members of request `id`'s oauth1 auth (consumer key, token,
 * signature method, callback, verifier, realm, version, nonce, timestamp,
 * body hash). An oauth1 auth without them gets the defaults first
 * (oauth1Of).
 */
export function setOAuth1Fields(stores: Stores, id: RequestId, fields: Partial<OAuth1AuthDTO>): void {
  const auth = draftAuth(stores, id);
  if (auth === undefined) return;
  const oauth1 = oauth1Of(auth);
  const changed = auth.oauth1 == null || (Object.keys(fields) as (keyof OAuth1AuthDTO)[]).some((k) => fields[k] !== oauth1[k]);
  if (!changed) return;
  editRequest(stores, id, { auth: { ...auth, oauth1: { ...oauth1, ...fields } } });
}

/**
 * Sets members of request `id`'s awsv4 auth (access key, region, service,
 * unsigned payload, expiry). An awsv4 auth without them gets the defaults
 * first (awsOf).
 */
export function setAWSFields(stores: Stores, id: RequestId, fields: Partial<AWSAuthDTO>): void {
  const auth = draftAuth(stores, id);
  if (auth === undefined) return;
  const aws = awsOf(auth);
  const changed = auth.aws == null || (Object.keys(fields) as (keyof AWSAuthDTO)[]).some((k) => fields[k] !== aws[k]);
  if (!changed) return;
  editRequest(stores, id, { auth: { ...auth, aws: { ...aws, ...fields } } });
}

/**
 * Sets members of request `id`'s hawk auth (ID, algorithm, ext, app, dlg,
 * payload hash). A hawk auth without them gets the defaults first
 * (hawkOf).
 */
export function setHawkFields(stores: Stores, id: RequestId, fields: Partial<HawkAuthDTO>): void {
  const auth = draftAuth(stores, id);
  if (auth === undefined) return;
  const hawk = hawkOf(auth);
  const changed = auth.hawk == null || (Object.keys(fields) as (keyof HawkAuthDTO)[]).some((k) => fields[k] !== hawk[k]);
  if (!changed) return;
  editRequest(stores, id, { auth: { ...auth, hawk: { ...hawk, ...fields } } });
}

/**
 * Sets members of request `id`'s oauth2 auth (grant, token URL, client,
 * client authentication, scope, audience, resource, state, extra token
 * parameters). An oauth2 auth without them gets the defaults first
 * (oauth2Of).
 */
export function setOAuth2Fields(stores: Stores, id: RequestId, fields: Partial<OAuth2AuthDTO>): void {
  const auth = draftAuth(stores, id);
  if (auth === undefined) return;
  const oauth2 = oauth2Of(auth);
  const changed = auth.oauth2 == null || (Object.keys(fields) as (keyof OAuth2AuthDTO)[]).some((k) => fields[k] !== oauth2[k]);
  if (!changed) return;
  editRequest(stores, id, { auth: { ...auth, oauth2: { ...oauth2, ...fields } } });
}

export type InheritedAuthState =

  | { status: "loading" }
  | { status: "loaded"; inherited: InheritedAuthDTO }
  | { status: "error"; message: string };

/**
 * Loads the auth request `id` inherits (CollectionService.InheritedAuth)
 * while `enabled`, again when `enabled` turns on. A call still running when
 * the request changes or the tab closes is cancelled.
 */
export function useInheritedAuth(id: RequestId, enabled: boolean): InheritedAuthState {
  const [state, setState] = useState<InheritedAuthState>({ status: "loading" });
  useEffect(() => {
    if (!enabled) return;
    setState({ status: "loading" });
    const call = getInheritedAuth(id);
    let live = true;
    call.then(
      (inherited) => {
        if (live) setState({ status: "loaded", inherited });
      },
      (err: unknown) => {
        if (live && !isCancelled(err)) setState({ status: "error", message: toAppError(err).message });
      },
    );
    return () => {
      live = false;
      void call.cancel();
    };
  }, [id, enabled]);
  return state;
}
