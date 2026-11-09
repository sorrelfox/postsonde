import { createContext, useCallback, useContext, useId, useState, type ChangeEvent, type ReactNode } from "react";
import {
  setAuthFields,
  setAuthType,
  setAWSFields,
  setHawkFields,
  setJwtFields,
  setOAuth1Fields,
  setOAuth2Fields,
  useInheritedAuth,
  type AuthFields,
} from "../../commands/auth";
import {
  AUTH_TYPE_NAMES,
  AUTH_TYPES,
  authType,
  awsOf,
  DEFAULT_AWS_EXPIRES,
  DEFAULT_BEARER_PREFIX,
  DEFAULT_HAWK_ALGORITHM,
  DEFAULT_JWT_ALGORITHM,
  DEFAULT_JWT_QUERY_PARAM,
  DEFAULT_OAUTH1_SIGNATURE_METHOD,
  DEFAULT_OAUTH2_GRANT,
  DEFAULT_OAUTH2_QUERY_PARAM,
  HAWK_ALGORITHMS,
  hawkOf,
  isHmacAlgorithm,
  isBrowserGrant,
  isRunnableGrant,
  JWT_ALGORITHMS,
  jwtOf,
  OAUTH1_SIGNATURE_METHODS,
  oauth1Of,
  OAUTH2_GRANTS,
  OAUTH2_IMPLICIT,
  OAUTH2_PASSWORD,
  oauth2Of,
  secretStorage,
  secretValue,
  type AWSAuthDTO,
  type HawkAuthDTO,
  type JWTAuthDTO,
  type OAuth1AuthDTO,
  type OAuth2AuthDTO,
  type SecretMember,
} from "../../lib/auth";
import { isCancelled, toAppError } from "../../lib/errors";
import { pickBodyFile } from "../../lib/workspace";
import type { AuthDTO, KeyValueDTO } from "../../lib/collection";
import { SECRET_MASK } from "../../lib/templateVariables";
import type { MessageKey } from "../../i18n";
import { Trans, useT, type Translate } from "../../i18n/react";
import { useStores, type RequestId } from "../../stores";
import { IconEye, IconEyeOff, IconInfo, IconLock, IconWarning } from "../common/icons";
import { FileAccessNotice, useFileAccessCheck } from "../common/FileAccessNotice";
import { TemplateText, templateTitle, useTemplateVariables } from "../common/TemplateVariables";
import { CodeEditor } from "./CodeEditor";
import { JwtDecoder, JwtPreview } from "./JwtTools";
import { NetrcCheck, NetrcFileNote } from "./NetrcTools";
import { OAuth2TokenPanel } from "./OAuth2Tools";
import styles from "./AuthEditor.module.css";

const AUTHORIZATION = "Authorization"; // i18n-ignore: a header name
const PEM_PRIVATE_KEY = "-----BEGIN PRIVATE KEY-----"; // i18n-ignore: a PEM marker

export interface AuthEditorProps {
  id: RequestId;
  auth: AuthDTO;
  readOnly: boolean;
  /** The auth of folder `id` (COL-03), which the requests inside inherit, rather than a request's. */
  folder?: boolean;
  /**
   * The protocol of a websocket, socketio or grpc request, whose auth goes
   * with its handshake (AUTH-31): the tab says when the type (its own or
   * the inherited one) cannot be sent there. Undefined for HTTP, GraphQL
   * and SSE requests and for folders.
   */
  protocol?: HandshakeProtocol;
}

/** A protocol whose handshake carries a request's auth (AUTH-31). */
export type HandshakeProtocol = "websocket" | "socketio" | "grpc";

/** The auth types that sign or answer an HTTP request, so no handshake can carry them (request.HandshakeApplies in Go). */
const NOT_ON_HANDSHAKES: ReadonlySet<string> = new Set(["digest", "oauth1", "awsv4", "hawk"]);

/** The types that can go in the query string, which gRPC does not have. */
const QUERY_TYPES: ReadonlySet<string> = new Set(["apikey", "jwt", "oauth2"]);

/** The handshake protocol of the request being edited, for the texts that name its tabs. */
const ProtocolContext = createContext<HandshakeProtocol | undefined>(undefined);

/**
 * The Auth section of the request editor (AUTH-01…06, AUTH-08, AUTH-12,
 * AUTH-15…19), as in the mockup: a type picker (Inherit, None, Bearer,
 * Basic, Digest, API key, JWT, OAuth 1.0, OAuth 2.0, AWS Signature, Hawk,
 * Netrc), then the type's fields. Text fields highlight `{{variables}}`; secrets are password

 * fields with a show button. Every change is an auto-saved edit
 * (commands/auth.ts); a typed secret is kept on this computer, not in the
 * project file (docs/auth.md).
 *
 * The folder editor uses it too (`folder`): a folder's auth is what the
 * requests inside with "Inherit" send, and a folder's "Inherit" passes on
 * the auth of the folder above.
 */
export function AuthEditor({ id, auth, readOnly, folder = false, protocol }: AuthEditorProps) {
  const t = useT();
  const stores = useStores();
  const type = authType(auth);
  const baseId = useId();
  const set = useCallback(
    (fields: AuthFields) => {
      if (!readOnly) setAuthFields(stores, id, fields);
    },
    [stores, id, readOnly],
  );
  const setJwt = useCallback(
    (fields: Partial<JWTAuthDTO>) => {
      if (!readOnly) setJwtFields(stores, id, fields);
    },
    [stores, id, readOnly],
  );
  const setOAuth1 = useCallback(
    (fields: Partial<OAuth1AuthDTO>) => {
      if (!readOnly) setOAuth1Fields(stores, id, fields);
    },
    [stores, id, readOnly],
  );
  const setAWS = useCallback(
    (fields: Partial<AWSAuthDTO>) => {
      if (!readOnly) setAWSFields(stores, id, fields);
    },
    [stores, id, readOnly],
  );
  const setHawk = useCallback(
    (fields: Partial<HawkAuthDTO>) => {
      if (!readOnly) setHawkFields(stores, id, fields);
    },
    [stores, id, readOnly],
  );
  const setOAuth2 = useCallback(
    (fields: Partial<OAuth2AuthDTO>) => {
      if (!readOnly) setOAuth2Fields(stores, id, fields);
    },
    [stores, id, readOnly],
  );

  // A type of a newer PostSonde stays offered after switching away, so it
  // can be picked again while the editor is open. Its unknown members go
  // and come back with it.
  const known = AUTH_TYPES.some((a) => a.type === type);
  const [unknown] = useState(known ? null : { type, extra: auth.extra });
  const types =
    unknown === null ? AUTH_TYPES : [...AUTH_TYPES, { type: unknown.type, label: t("editor.auth.quoted", { value: unknown.type }) }];
  const onType = (e: ChangeEvent<HTMLInputElement>) => {
    if (readOnly) return;
    const next = e.target.value;
    let extra: string | undefined;
    if (unknown !== null && next === unknown.type) extra = unknown.extra;
    else if (unknown !== null && type === unknown.type) extra = "";
    setAuthType(stores, id, next, extra);
  };

  return (
    <ProtocolContext.Provider value={protocol}>
      <div className={styles.auth} data-auth-type={type}>
        <span className={styles.label} id={`${baseId}-type`}>
          {t("editor.auth.type")}
        </span>
        <div className={styles.types} role="radiogroup" aria-labelledby={`${baseId}-type`}>
          {types.map((a) => (
            <label key={a.type} className={styles.typeOption} data-disabled={readOnly || undefined}>
              <input
                type="radio"
                name={`${baseId}-type`}
                value={a.type}
                checked={a.type === type}
                disabled={readOnly}
                onChange={onType}
              />
              {a.label}
            </label>
          ))}
        </div>

        {type === "inherit" && <InheritNote id={id} folder={folder} />}
        {type === "none" && <Note>{folder ? t("editor.auth.none.folder") : t("editor.auth.none.request")}</Note>}
        {type === "bearer" && <BearerFields auth={auth} readOnly={readOnly} set={set} folder={folder} />}
        {type === "basic" && <BasicFields auth={auth} readOnly={readOnly} set={set} folder={folder} />}
        {type === "digest" && <DigestFields auth={auth} readOnly={readOnly} set={set} folder={folder} />}
        {type === "apikey" && <ApiKeyFields auth={auth} readOnly={readOnly} set={set} folder={folder} />}
        {type === "jwt" && <JwtFields id={id} auth={auth} readOnly={readOnly} set={set} setJwt={setJwt} folder={folder} />}
        {type === "oauth1" && <OAuth1Fields auth={auth} readOnly={readOnly} set={set} setOAuth1={setOAuth1} folder={folder} />}
        {type === "oauth2" && <OAuth2Fields id={id} auth={auth} readOnly={readOnly} set={set} setOAuth2={setOAuth2} folder={folder} />}
        {type === "awsv4" && <AWSFields auth={auth} readOnly={readOnly} set={set} setAWS={setAWS} folder={folder} />}
        {type === "hawk" && <HawkFields auth={auth} readOnly={readOnly} set={set} setHawk={setHawk} folder={folder} />}
        {type === "netrc" && <NetrcFields id={id} auth={auth} readOnly={readOnly} set={set} folder={folder} />}
        {!known && <Note>{t("editor.auth.unknownType", { type })}</Note>}
        {protocol !== undefined && <HandshakeNote id={id} auth={auth} protocol={protocol} />}
      </div>
    </ProtocolContext.Provider>
  );
}

/**
 * What a websocket, socketio or grpc request's auth cannot do (AUTH-31):
 * a type that signs or answers an HTTP request, its own or the inherited
 * one, refuses the connect; so does a query parameter on gRPC. Said here
 * before connecting rather than left out silently.
 */
function HandshakeNote({ id, auth, protocol }: { id: RequestId; auth: AuthDTO; protocol: HandshakeProtocol }) {
  const t = useT();
  const own = authType(auth);
  const inherited = useInheritedAuth(id, own === "inherit");
  let effective: AuthDTO | null = auth;
  let folderName = "";
  if (own === "inherit") {
    if (inherited.status !== "loaded" || inherited.inherited.folderId === "" || inherited.inherited.auth == null) return null;
    effective = inherited.inherited.auth;
    folderName = inherited.inherited.folderName;
  }
  const type = authType(effective);
  if (effective.disabled) return null;
  const name = AUTH_TYPE_NAMES[type] ?? type;
  const protocolName = {
    websocket: t("editor.auth.handshake.websocket"),
    socketio: t("editor.auth.handshake.socketio"),
    grpc: t("editor.auth.handshake.grpc"),
  }[protocol];
  let text: string | null = null;
  if (NOT_ON_HANDSHAKES.has(type)) {
    text =
      folderName === ""
        ? t("editor.auth.handshake.notApplicable", { name, protocol: protocolName })
        : t("editor.auth.handshake.notApplicableInherited", { name, protocol: protocolName, folder: folderName });
  } else if (protocol === "grpc" && QUERY_TYPES.has(type) && effective.in === "query") {
    text = t("editor.auth.handshake.grpcQuery", { name });
  }
  if (text === null) return null;
  return (
    <>
      <span />
      <p className={styles.note} role="status">
        <IconWarning size={12} /> <span className={styles.warn}>{text}</span>
      </p>
    </>
  );
}

interface FieldsProps {
  auth: AuthDTO;
  readOnly: boolean;
  set: (fields: AuthFields) => void;
  folder?: boolean;
}

function BearerFields({ auth, readOnly, set, folder }: FieldsProps) {
  const t = useT();
  const prefix = auth.prefix ?? DEFAULT_BEARER_PREFIX;
  return (
    <>
      <SecretField label={t("editor.auth.field.token")} member="token" auth={auth} readOnly={readOnly} set={set} placeholder="{{token}}" />
      <TextField
        label={t("editor.auth.field.prefix")}
        value={prefix}
        readOnly={readOnly}
        placeholder={t("editor.auth.placeholder.noPrefix")}
        onChange={(v) => set({ prefix: v })}
      />
      <EnabledField auth={auth} readOnly={readOnly} set={set} />
      <Preview auth={auth} folder={folder}>
        <Trans k="editor.auth.bearer.preview" values={{ header: <code>Authorization: {prefix === "" ? "" : `${prefix} `}…</code> }} />
      </Preview>
    </>
  );
}

/** The username and password of Basic and Digest auth. */
function UserPasswordFields({ auth, readOnly, set }: FieldsProps) {
  const t = useT();
  return (
    <>
      <TextField
        label={t("editor.auth.field.username")}
        value={auth.username}
        readOnly={readOnly}
        placeholder="user" // i18n-ignore: an example user name
        onChange={(v) => set({ username: v })}
      />
      <SecretField
        label={t("editor.auth.field.password")}
        member="password"
        auth={auth}
        readOnly={readOnly}
        set={set}
        placeholder="{{password}}"
      />
    </>
  );
}

function BasicFields({ auth, readOnly, set, folder }: FieldsProps) {
  return (
    <>
      <UserPasswordFields auth={auth} readOnly={readOnly} set={set} />
      <EnabledField auth={auth} readOnly={readOnly} set={set} />
      <Preview auth={auth} folder={folder}>
        <Trans k="editor.auth.basic.preview" values={{ header: <code>Authorization: Basic …</code> }} />
      </Preview>
    </>
  );
}

function DigestFields({ auth, readOnly, set, folder }: FieldsProps) {
  return (
    <>
      <UserPasswordFields auth={auth} readOnly={readOnly} set={set} />
      <EnabledField auth={auth} readOnly={readOnly} set={set} />
      <Preview auth={auth} folder={folder}>
        <Trans k="editor.auth.digest.preview" values={{ header: <code>Authorization: Digest …</code> }} />
      </Preview>
    </>
  );
}

function ApiKeyFields({ auth, readOnly, set, folder }: FieldsProps) {
  const t = useT();
  const inQuery = auth.in === "query";
  const known = auth.in === "" || auth.in === "header" || inQuery;
  const name = auth.key === "" ? "<key>" : auth.key;
  return (
    <>
      <TextField label={t("editor.auth.field.key")} value={auth.key} readOnly={readOnly} placeholder="X-API-Key" onChange={(v) => set({ key: v })} />
      <SecretField label={t("editor.auth.field.value")} member="value" auth={auth} readOnly={readOnly} set={set} placeholder="{{apiKey}}" />
      <span className={styles.label}>{t("editor.auth.field.addTo")}</span>
      <select
        className={styles.select}
        aria-label={t("editor.auth.apiKey.label")}
        value={known ? (inQuery ? "query" : "header") : auth.in}
        disabled={readOnly}
        onChange={(e) => set({ in: e.target.value })}
      >
        <option value="header">{t("editor.auth.in.header")}</option>
        <option value="query">{t("editor.auth.in.query")}</option>
        {!known && <option value={auth.in}>{t("editor.auth.quoted", { value: auth.in })}</option>}
      </select>
      <EnabledField auth={auth} readOnly={readOnly} set={set} />
      <Preview auth={auth} folder={folder} wins={inQuery ? t("editor.auth.wins.query") : undefined}>
        {inQuery ? (
          <Trans k="editor.auth.apiKey.previewQuery" values={{ param: <code>?{name}=…</code> }} />
        ) : (
          <Trans k="editor.auth.apiKey.previewHeader" values={{ header: <code>{name}: …</code> }} />
        )}
      </Preview>
    </>
  );
}

interface JwtFieldsProps extends FieldsProps {
  id: RequestId;
  setJwt: (fields: Partial<JWTAuthDTO>) => void;
}

/** The note under a JWT private key: its kind, by algorithm. */
function jwtKeyNote(t: Translate, alg: string): string {
  if (alg.startsWith("ES")) {
    const curve = alg === "ES256" ? "P-256" : alg === "ES384" ? "P-384" : "P-521"; // i18n-ignore: curve names
    return t("editor.auth.jwt.keyNote.ec", { curve });
  }
  return alg === "EdDSA" ? t("editor.auth.jwt.keyNote.ed25519") : t("editor.auth.jwt.keyNote.rsa");
}

/**
 * A JWT built and signed at send time (AUTH-16): the algorithm, the key
 * (typed, stored on this computer like any secret, or read from a file),
 * the header and payload JSON with `{{variables}}`, the claims added at
 * send time, and where the token goes. Go builds and signs it
 * (internal/core/jwt); the preview asks Go for the token of this moment.
 */
function JwtFields({ id, auth, readOnly, set, setJwt, folder }: JwtFieldsProps) {
  const t = useT();
  const jwt = jwtOf(auth);
  const alg = jwt.algorithm === "" ? DEFAULT_JWT_ALGORITHM : jwt.algorithm;
  const hmac = isHmacAlgorithm(alg);
  const baseId = useId();
  const [fromFile, setFromFile] = useState(jwt.keyFile !== "");
  const [pickFailure, setPickFailure] = useState<string | null>(null);
  const access = useFileAccessCheck();
  const inQuery = auth.in === "query";
  const knownIn = auth.in === "" || auth.in === "header" || inQuery;
  const prefix = auth.prefix ?? DEFAULT_BEARER_PREFIX;
  const name = auth.key === "" ? (inQuery ? DEFAULT_JWT_QUERY_PARAM : AUTHORIZATION) : auth.key;

  const choose = () => {
    setPickFailure(null);
    pickBodyFile().then(
      (file) => {
        if (file.path === "") return;
        setJwt({ keyFile: file.path });
        // A key file outside the allowed directories is refused (PRV-10).
        access.check(file.path);
      },
      (err: unknown) => {
        if (!isCancelled(err)) setPickFailure(toAppError(err).message);
      },
    );
  };

  return (
    <>
      <span className={styles.label}>{t("editor.auth.field.algorithm")}</span>
      <select
        className={styles.select}
        aria-label={t("editor.auth.jwt.algorithm")}
        value={alg}
        disabled={readOnly}
        onChange={(e) => setJwt({ algorithm: e.target.value })}
      >
        {JWT_ALGORITHMS.map((a) => (
          <option key={a} value={a}>
            {a}
          </option>
        ))}
        {!JWT_ALGORITHMS.includes(alg) && <option value={alg}>{t("editor.auth.quoted", { value: alg })}</option>}
      </select>

      <span className={styles.label} id={`${baseId}-source`}>
        {t("editor.auth.field.keyFrom")}
      </span>
      <div className={styles.inline} role="radiogroup" aria-labelledby={`${baseId}-source`}>
        <label className={styles.check}>
          <input
            type="radio"
            name={`${baseId}-source`}
            checked={!fromFile}
            disabled={readOnly}
            onChange={() => {
              setFromFile(false);
              setJwt({ keyFile: "" });
            }}
          />
          {hmac ? t("editor.auth.jwt.typedSecret") : t("editor.auth.jwt.typedKey")}
        </label>
        <label className={styles.check}>
          <input type="radio" name={`${baseId}-source`} checked={fromFile} disabled={readOnly} onChange={() => setFromFile(true)} />
          {t("editor.auth.jwt.file")}
        </label>
      </div>
      {fromFile ? (
        <>
          <span className={styles.label}>{t("editor.auth.field.keyFile")}</span>
          <div className={styles.secret}>
            <div className={styles.field}>
              <CodeEditor
                className={styles.editor}
                singleLine
                ariaLabel={t("editor.auth.field.keyFile")}
                placeholder={hmac ? "keys/secret.txt" : "keys/private.pem"}
                value={jwt.keyFile}
                readOnly={readOnly}
                onChange={readOnly ? undefined : (v) => setJwt({ keyFile: v })}
              />
            </div>
            <button type="button" className={styles.action} disabled={readOnly} onClick={choose}>
              {t("editor.auth.jwt.choose")}
            </button>
          </div>
          <span />
          <p className={styles.hint} data-storage={pickFailure === null ? undefined : "missing"}>
            {pickFailure === null ? <IconInfo size={12} /> : <IconWarning size={12} />}
            <span>{pickFailure ?? (hmac ? t("editor.auth.jwt.keyFileHint.secret") : t("editor.auth.jwt.keyFileHint.pem"))}</span>
          </p>
          <FileAccessNotice access={access} className={styles.fieldNotice} />
        </>
      ) : (
        <SecretField
          label={hmac ? t("editor.auth.field.secret") : t("editor.auth.field.privateKey")}
          member="secret"
          auth={auth}
          readOnly={readOnly}
          set={set}
          multiline={!hmac}
          placeholder={hmac ? "{{jwtSecret}}" : PEM_PRIVATE_KEY}
        />
      )}
      {hmac && (
        <>
          <span />
          <label className={styles.check}>
            <input
              type="checkbox"
              checked={jwt.secretBase64}
              disabled={readOnly}
              onChange={(e) => setJwt({ secretBase64: e.target.checked })}
            />
            {t("editor.auth.jwt.secretBase64")}
          </label>
        </>
      )}
      {!hmac && (
        <>
          <span />
          <p className={styles.note}>{jwtKeyNote(t, alg)}</p>
        </>
      )}

      <span className={styles.label}>{t("editor.auth.field.header")}</span>
      <div className={styles.codeBox}>
        <CodeEditor
          language="json"
          ariaLabel={t("editor.auth.jwt.header")}
          value={jwt.header}
          readOnly={readOnly}
          onChange={readOnly ? undefined : (v) => setJwt({ header: v })}
        />
      </div>
      <span />
      <p className={styles.note}>
        <Trans
          k="editor.auth.jwt.headerNote"
          values={{
            example: <code>{'{"kid": "key-1"}'}</code>,
            alg: <code>alg</code>,
            typ: <code>typ</code>,
            jwt: <code>JWT</code>,
          }}
        />
      </p>

      <span className={styles.label}>{t("editor.auth.field.payload")}</span>
      <div className={styles.codeBox}>
        <CodeEditor
          language="json"
          ariaLabel={t("editor.auth.jwt.payload")}
          value={jwt.payload}
          readOnly={readOnly}
          onChange={readOnly ? undefined : (v) => setJwt({ payload: v })}
        />
      </div>

      <span className={styles.label}>{t("editor.auth.field.addClaims")}</span>
      <div className={styles.inline}>
        <label className={styles.check}>
          <input type="checkbox" checked={jwt.issuedAt} disabled={readOnly} onChange={(e) => setJwt({ issuedAt: e.target.checked })} />
          <Trans k="editor.auth.jwt.claimNow" values={{ claim: <code>iat</code> }} />
        </label>
        <label className={styles.check}>
          <input type="checkbox" checked={jwt.notBefore} disabled={readOnly} onChange={(e) => setJwt({ notBefore: e.target.checked })} />
          <Trans k="editor.auth.jwt.claimNow" values={{ claim: <code>nbf</code> }} />
        </label>
        <label className={styles.check}>
          <input type="checkbox" checked={jwt.jwtId} disabled={readOnly} onChange={(e) => setJwt({ jwtId: e.target.checked })} />
          <Trans k="editor.auth.jwt.claimRandom" values={{ claim: <code>jti</code> }} />
        </label>
        <label className={styles.check}>
          <Trans
            k="editor.auth.jwt.claimIn"
            values={{
              claim: <code>exp</code>,
              input: (
                <input
                  className={styles.number}
                  type="number"
                  min={0}
                  step={60}
                  aria-label={t("editor.auth.jwt.expiresIn")}
                  value={jwt.expiresIn}
                  readOnly={readOnly}
                  onChange={(e) => setJwt({ expiresIn: Math.max(0, Math.floor(Number(e.target.value) || 0)) })}
                />
              ),
            }}
          />
        </label>
      </div>
      <span />
      <p className={styles.note}>
        <Trans k="editor.auth.jwt.claimsNote" values={{ exp: <code>exp</code>, example: <code>{"{{$timestamp}}"}</code> }} />
      </p>

      <span className={styles.label}>{t("editor.auth.field.addTo")}</span>
      <select
        className={styles.select}
        aria-label={t("editor.auth.jwt.sendIn")}
        value={knownIn ? (inQuery ? "query" : "header") : auth.in}
        disabled={readOnly}
        onChange={(e) => set({ in: e.target.value })}
      >
        <option value="header">{t("editor.auth.in.header")}</option>
        <option value="query">{t("editor.auth.in.query")}</option>
        {!knownIn && <option value={auth.in}>{t("editor.auth.quoted", { value: auth.in })}</option>}
      </select>
      <TextField
        key={inQuery ? "param" : "header"}
        label={inQuery ? t("editor.auth.field.parameter") : t("editor.auth.field.headerName")}
        value={auth.key}
        readOnly={readOnly}
        placeholder={inQuery ? DEFAULT_JWT_QUERY_PARAM : AUTHORIZATION}
        onChange={(v) => set({ key: v })}
      />
      {!inQuery && (
        <TextField
          label={t("editor.auth.field.prefix")}
          value={prefix}
          readOnly={readOnly}
          placeholder={t("editor.auth.placeholder.noPrefix")}
          onChange={(v) => set({ prefix: v })}
        />
      )}
      <EnabledField auth={auth} readOnly={readOnly} set={set} />
      <Preview auth={auth} folder={folder} wins={inQuery ? t("editor.auth.wins.query") : undefined}>
        {inQuery ? (
          <Trans k="editor.auth.jwt.previewQuery" values={{ param: <code>?{name}=…</code> }} />
        ) : (
          <Trans
            k="editor.auth.jwt.previewHeader"
            values={{ header: <code>{`${name}: ${prefix === "" ? "" : `${prefix} `}<jwt>`}</code> }}
          />
        )}
      </Preview>
      {!folder && !auth.disabled && <JwtPreview id={id} />}
      <JwtDecoder />
    </>
  );
}

interface OAuth1FieldsProps extends FieldsProps {
  setOAuth1: (fields: Partial<OAuth1AuthDTO>) => void;
}

/**
 * OAuth 1.0 (AUTH-15): the client and token credentials, the signature
 * method and where the protocol parameters go. Go signs the request as it
 * is sent (internal/core/oauth1), with a new nonce and timestamp each time
 * unless they are set here; the secrets are kept on this computer like any
 * other.
 */
function OAuth1Fields({ auth, readOnly, set, setOAuth1, folder }: OAuth1FieldsProps) {
  const t = useT();
  const o = oauth1Of(auth);
  const method = o.signatureMethod === "" ? DEFAULT_OAUTH1_SIGNATURE_METHOD : o.signatureMethod;
  const rsa = method === "RSA-SHA1";
  const where = auth.in === "" ? "header" : auth.in;
  const knownIn = where === "header" || where === "query" || where === "body";
  const then =
    where === "query" ? (
      <Trans k="editor.auth.oauth1.toQuery" values={{ param: <code>?oauth_consumer_key=…&amp;oauth_signature=…</code> }} />
    ) : where === "body" ? (
      t("editor.auth.oauth1.toBody")
    ) : (
      <Trans
        k="editor.auth.oauth1.toHeader"
        values={{
          header: <code>Authorization: OAuth {o.realm === "" ? "" : "realm=…, "}oauth_consumer_key=…, oauth_signature=…</code>,
        }}
      />
    );
  return (
    <>
      <span className={styles.label}>{t("editor.auth.field.signature")}</span>
      <select
        className={styles.select}
        aria-label={t("editor.auth.oauth1.signatureMethod")}
        value={method}
        disabled={readOnly}
        onChange={(e) => setOAuth1({ signatureMethod: e.target.value })}
      >
        {OAUTH1_SIGNATURE_METHODS.map((m) => (
          <option key={m} value={m}>
            {m}
          </option>
        ))}
        {!OAUTH1_SIGNATURE_METHODS.includes(method) && <option value={method}>{t("editor.auth.quoted", { value: method })}</option>}
      </select>
      <TextField
        label={t("editor.auth.field.consumerKey")}
        value={o.consumerKey}
        readOnly={readOnly}
        placeholder="{{consumerKey}}"
        onChange={(v) => setOAuth1({ consumerKey: v })}
      />
      <SecretField
        key={rsa ? "rsa" : "secret"}
        label={rsa ? t("editor.auth.field.privateKey") : t("editor.auth.field.consumerSecret")}
        member="consumerSecret"
        auth={auth}
        readOnly={readOnly}
        set={set}
        multiline={rsa}
        placeholder={rsa ? PEM_PRIVATE_KEY : "{{consumerSecret}}"}
      />
      {rsa && <Note>{t("editor.auth.oauth1.rsaNote")}</Note>}
      <TextField
        label={t("editor.auth.field.accessToken")}
        value={o.accessToken}
        readOnly={readOnly}
        placeholder={t("editor.auth.placeholder.temporaryCredentials")}
        onChange={(v) => setOAuth1({ accessToken: v })}
      />
      {!rsa && (
        <SecretField
          label={t("editor.auth.field.tokenSecret")}
          member="tokenSecret"
          auth={auth}
          readOnly={readOnly}
          set={set}
          placeholder="{{tokenSecret}}"
        />
      )}

      <span className={styles.label}>{t("editor.auth.field.addTo")}</span>
      <select
        className={styles.select}
        aria-label={t("editor.auth.oauth1.sendIn")}
        value={where}
        disabled={readOnly}
        onChange={(e) => set({ in: e.target.value })}
      >
        <option value="header">{t("editor.auth.in.authorizationHeader")}</option>
        <option value="query">{t("editor.auth.in.query")}</option>
        <option value="body">{t("editor.auth.in.formBody")}</option>
        {!knownIn && <option value={where}>{t("editor.auth.quoted", { value: where })}</option>}
      </select>
      <TextField
        label={t("editor.auth.field.callback")}
        value={o.callback}
        readOnly={readOnly}
        placeholder="oob" // i18n-ignore: the OAuth out-of-band callback
        onChange={(v) => setOAuth1({ callback: v })}
      />
      <TextField label={t("editor.auth.field.verifier")} value={o.verifier} readOnly={readOnly} placeholder="" onChange={(v) => setOAuth1({ verifier: v })} />
      {where === "header" && (
        <TextField label={t("editor.auth.field.realm")} value={o.realm} readOnly={readOnly} placeholder="" onChange={(v) => setOAuth1({ realm: v })} />
      )}
      <TextField
        label={t("editor.auth.field.nonce")}
        value={o.nonce}
        readOnly={readOnly}
        placeholder={t("editor.auth.placeholder.nonce")}
        onChange={(v) => setOAuth1({ nonce: v })}
      />
      <TextField
        label={t("editor.auth.field.timestamp")}
        value={o.timestamp}
        readOnly={readOnly}
        placeholder={t("editor.auth.placeholder.timestamp")}
        onChange={(v) => setOAuth1({ timestamp: v })}
      />
      <span />
      <div className={styles.inline}>
        <label className={styles.check}>
          <input
            type="checkbox"
            checked={!o.omitVersion}
            disabled={readOnly}
            onChange={(e) => setOAuth1({ omitVersion: !e.target.checked })}
          />
          <Trans k="editor.auth.oauth1.sendVersion" values={{ param: <code>oauth_version=1.0</code> }} />
        </label>
        <label className={styles.check}>
          <input type="checkbox" checked={o.bodyHash} disabled={readOnly} onChange={(e) => setOAuth1({ bodyHash: e.target.checked })} />
          <Trans k="editor.auth.oauth1.addBodyHash" values={{ param: <code>oauth_body_hash</code> }} />
        </label>
      </div>
      <EnabledField auth={auth} readOnly={readOnly} set={set} />
      <Preview
        auth={auth}
        folder={folder}
        wins={where === "header" ? undefined : where === "query" ? t("editor.auth.wins.query") : ""}
      >
        {o.bodyHash ? (
          <Trans k="editor.auth.oauth1.previewBodyHash" values={{ method, then }} />
        ) : (
          <Trans k="editor.auth.oauth1.preview" values={{ method, then }} />
        )}
      </Preview>
    </>
  );
}

interface AWSFieldsProps extends FieldsProps {
  setAWS: (fields: Partial<AWSAuthDTO>) => void;
}

/**
 * AWS Signature Version 4 (AUTH-17): the credentials, the region and
 * service of the credential scope, and where the signature goes. Go signs
 * the request as it is sent (internal/core/awssigv4), with the time of the
 * send; the secret key and session token are kept on this computer like
 * any other secret.
 */
function AWSFields({ auth, readOnly, set, setAWS, folder }: AWSFieldsProps) {
  const t = useT();
  const a = awsOf(auth);
  const where = auth.in === "" ? "header" : auth.in;
  const knownIn = where === "header" || where === "query";
  const s3 = a.service === "s3";
  const payload = a.unsignedPayload || (s3 && where === "query") ? <code>UNSIGNED-PAYLOAD</code> : t("editor.auth.aws.bodyHash");
  const headers = (
    <>
      <code>X-Amz-Date</code>
      {auth.sessionToken || (auth.localSecrets ?? []).includes("sessionToken") ? (
        <>
          , <code>X-Amz-Security-Token</code>
        </>
      ) : null}
      {s3 || a.unsignedPayload ? (
        <>
          , <code>X-Amz-Content-Sha256</code>
        </>
      ) : null}
    </>
  );
  const then =
    where === "query" ? (
      <Trans
        k="editor.auth.aws.toQuery"
        values={{ param: <code>?X-Amz-Algorithm=…&amp;X-Amz-Credential=…&amp;X-Amz-Signature=…</code> }}
      />
    ) : (
      <Trans
        k="editor.auth.aws.toHeader"
        values={{ header: <code>Authorization: AWS4-HMAC-SHA256 Credential=…, SignedHeaders=…, Signature=…</code>, headers }}
      />
    );
  return (
    <>
      <TextField
        label={t("editor.auth.field.accessKey")}
        value={a.accessKey}
        readOnly={readOnly}
        placeholder="{{awsAccessKeyId}}"
        onChange={(v) => setAWS({ accessKey: v })}
      />
      <SecretField
        label={t("editor.auth.field.secretKey")}
        member="secretKey"
        auth={auth}
        readOnly={readOnly}
        set={set}
        placeholder="{{awsSecretAccessKey}}"
      />
      <SecretField
        label={t("editor.auth.field.sessionToken")}
        member="sessionToken"
        auth={auth}
        readOnly={readOnly}
        set={set}
        placeholder={t("editor.auth.placeholder.sessionToken")}
      />
      <TextField label={t("editor.auth.field.region")} value={a.region} readOnly={readOnly} placeholder="us-east-1" onChange={(v) => setAWS({ region: v })} />
      <TextField
        label={t("editor.auth.field.service")}
        value={a.service}
        readOnly={readOnly}
        placeholder="execute-api, s3, …" // i18n-ignore: AWS service names
        onChange={(v) => setAWS({ service: v })}
      />

      <span className={styles.label}>{t("editor.auth.field.addTo")}</span>
      <select
        className={styles.select}
        aria-label={t("editor.auth.aws.sendIn")}
        value={where}
        disabled={readOnly}
        onChange={(e) => set({ in: e.target.value })}
      >
        <option value="header">{t("editor.auth.in.authorizationHeader")}</option>
        <option value="query">{t("editor.auth.in.presigned")}</option>
        {!knownIn && <option value={where}>{t("editor.auth.quoted", { value: where })}</option>}
      </select>
      <span />
      <div className={styles.inline}>
        <label className={styles.check}>
          <input
            type="checkbox"
            checked={a.unsignedPayload}
            disabled={readOnly}
            onChange={(e) => setAWS({ unsignedPayload: e.target.checked })}
          />
          {t("editor.auth.aws.unsignedPayload")}
        </label>
        {where === "query" && (
          <label className={styles.check}>
            <Trans
              k="editor.auth.aws.expiresIn"
              values={{
                input: (
                  <input
                    className={styles.number}
                    type="number"
                    min={0}
                    max={604800}
                    step={60}
                    aria-label={t("editor.auth.aws.expiresInLabel")}
                    value={a.expires === 0 ? DEFAULT_AWS_EXPIRES : a.expires}
                    readOnly={readOnly}
                    onChange={(e) => setAWS({ expires: Math.max(0, Math.floor(Number(e.target.value) || 0)) })}
                  />
                ),
              }}
            />
          </label>
        )}
      </div>
      <EnabledField auth={auth} readOnly={readOnly} set={set} />
      <Preview auth={auth} folder={folder} wins={where === "header" ? undefined : t("editor.auth.wins.query")}>
        {s3 ? (
          <Trans k="editor.auth.aws.previewS3" values={{ payload, then }} />
        ) : (
          <Trans k="editor.auth.aws.preview" values={{ payload, then }} />
        )}
      </Preview>
    </>
  );
}

interface OAuth2FieldsProps extends FieldsProps {
  id: RequestId;
  setOAuth2: (fields: Partial<OAuth2AuthDTO>) => void;
}

/** The labels of the grant picker, by grant type. */
const GRANT_LABELS: Readonly<Record<string, MessageKey>> = {
  client_credentials: "editor.auth.oauth2.grant.clientCredentials",
  authorization_code: "editor.auth.oauth2.grant.authorizationCode",
  password: "editor.auth.oauth2.grant.password",
  implicit: "editor.auth.oauth2.grant.implicit",
};

/**
 * OAuth 2.0 (AUTH-07…10, AUTH-12, AUTH-14): the grant, the token endpoint
 * and the client, how the client authenticates (Basic header or body), the
 * scopes, audience, resource and extra parameters of the token request,
 * the password grant's username and password, the redirect grants'
 * authorization and redirect URLs, the OpenID Connect issuer and whether
 * the ID token is sent instead of the access token, and where the token
 * goes. Go gets the token when the request is sent, through the
 * app's HTTP client, and keeps it for the session until it expires; the
 * exchange is in the Timeline tab (AUTH-33). The client secret is kept on
 * this computer like any other secret; tokens are stored on this computer
 * too, never in the project, and a request's tab shows its token
 * (OAuth2TokenPanel, AUTH-13).
 */
function OAuth2Fields({ id, auth, readOnly, set, setOAuth2, folder }: OAuth2FieldsProps) {
  const t = useT();
  const o = oauth2Of(auth);
  const grant = o.grantType === "" ? DEFAULT_OAUTH2_GRANT : o.grantType;
  const inQuery = auth.in === "query";
  const knownIn = auth.in === "" || auth.in === "header" || inQuery;
  const prefix = auth.prefix ?? DEFAULT_BEARER_PREFIX;
  const name = auth.key === "" ? (inQuery ? DEFAULT_OAUTH2_QUERY_PARAM : AUTHORIZATION) : auth.key;
  const clientAuth = o.clientAuth === "" ? "header" : o.clientAuth;
  const params = o.tokenParams ?? [];
  const setParams = (rows: KeyValueDTO[]) => setOAuth2({ tokenParams: rows });
  const setParam = (i: number, row: Partial<KeyValueDTO>) => setParams(params.map((p, j) => (j === i ? { ...p, ...row } : p)));
  const implicit = grant === OAUTH2_IMPLICIT;
  const browser = isBrowserGrant(grant);

  return (
    <>
      <span className={styles.label}>{t("editor.auth.field.grant")}</span>
      <select
        className={styles.select}
        aria-label={t("editor.auth.oauth2.grantLabel")}
        value={grant}
        disabled={readOnly}
        onChange={(e) => setOAuth2({ grantType: e.target.value })}
      >
        {OAUTH2_GRANTS.map((g) => (
          <option key={g} value={g}>
            {t(GRANT_LABELS[g] ?? "editor.auth.oauth2.grant.clientCredentials")}
          </option>
        ))}
        {!OAUTH2_GRANTS.includes(grant) && <option value={grant}>{t("editor.auth.quoted", { value: grant })}</option>}
      </select>
      {!isRunnableGrant(grant) && (
        <>
          <span />
          <p className={styles.hint} data-storage="missing">
            <IconWarning size={12} />
            <span>{t("editor.auth.oauth2.grantNotRun")}</span>
          </p>
        </>
      )}
      {implicit && (
        <>
          <span />
          <p className={styles.hint} data-storage="missing">
            <IconWarning size={12} />
            <span>{t("editor.auth.oauth2.implicitNote")}</span>
          </p>
        </>
      )}
      {browser && (
        <TextField
          label={t("editor.auth.field.authUrl")}
          value={o.authUrl}
          readOnly={readOnly}
          placeholder={t("editor.auth.placeholder.authUrl")}
          onChange={(v) => setOAuth2({ authUrl: v })}
        />
      )}
      {!implicit && (
        <TextField
          label={t("editor.auth.field.tokenUrl")}
          value={o.tokenUrl}
          readOnly={readOnly}
          placeholder={t("editor.auth.placeholder.tokenUrl")}
          onChange={(v) => setOAuth2({ tokenUrl: v })}
        />
      )}
      {browser && (
        <>
          <TextField
            label={t("editor.auth.field.redirectUrl")}
            value={o.redirectUrl}
            readOnly={readOnly}
            placeholder={t("editor.auth.placeholder.redirectUrl")}
            onChange={(v) => setOAuth2({ redirectUrl: v })}
          />
          <span />
          <p className={styles.hint}>
            <IconInfo size={12} />
            <span>{implicit ? t("editor.auth.oauth2.implicitSignInNote") : t("editor.auth.oauth2.signInNote")}</span>
          </p>
        </>
      )}
      <TextField
        label={t("editor.auth.field.clientId")}
        value={o.clientId}
        readOnly={readOnly}
        placeholder="{{clientId}}"
        onChange={(v) => setOAuth2({ clientId: v })}
      />
      {!implicit && (
        <>
          <SecretField
            label={t("editor.auth.field.clientSecret")}
            member="clientSecret"
            auth={auth}
            readOnly={readOnly}
            set={set}
            placeholder="{{clientSecret}}"
          />
          <span className={styles.label}>{t("editor.auth.field.clientAuth")}</span>
          <select
            className={styles.select}
            aria-label={t("editor.auth.oauth2.clientAuthLabel")}
            value={clientAuth}
            disabled={readOnly}
            onChange={(e) => setOAuth2({ clientAuth: e.target.value === "header" ? "" : e.target.value })}
          >
            <option value="header">{t("editor.auth.oauth2.clientAuth.header")}</option>
            <option value="body">{t("editor.auth.oauth2.clientAuth.body")}</option>
            {clientAuth !== "header" && clientAuth !== "body" && (
              <option value={clientAuth}>{t("editor.auth.quoted", { value: clientAuth })}</option>
            )}
          </select>
        </>
      )}
      {grant === OAUTH2_PASSWORD && (
        <>
          <TextField
            label={t("editor.auth.field.username")}
            value={auth.username}
            readOnly={readOnly}
            placeholder="{{username}}"
            onChange={(v) => set({ username: v })}
          />
          <SecretField
            label={t("editor.auth.field.password")}
            member="password"
            auth={auth}
            readOnly={readOnly}
            set={set}
            placeholder="{{password}}"
          />
        </>
      )}
      <TextField
        label={t("editor.auth.field.scope")}
        value={o.scope}
        readOnly={readOnly}
        placeholder={t("editor.auth.placeholder.scope")}
        onChange={(v) => setOAuth2({ scope: v })}
      />
      <TextField
        label={t("editor.auth.field.audience")}
        value={o.audience}
        readOnly={readOnly}
        placeholder={t("editor.auth.placeholder.optional")}
        onChange={(v) => setOAuth2({ audience: v })}
      />
      <TextField
        label={t("editor.auth.field.resource")}
        value={o.resource}
        readOnly={readOnly}
        placeholder={t("editor.auth.placeholder.optional")}
        onChange={(v) => setOAuth2({ resource: v })}
      />
      {browser && (
        <TextField
          label={t("editor.auth.field.state")}
          value={o.state}
          readOnly={readOnly}
          placeholder={t("editor.auth.placeholder.state")}
          onChange={(v) => setOAuth2({ state: v })}
        />
      )}
      <TextField
        label={t("editor.auth.field.issuer")}
        value={o.issuer}
        readOnly={readOnly}
        placeholder={t("editor.auth.placeholder.issuer")}
        onChange={(v) => setOAuth2({ issuer: v })}
      />
      <span />
      <label className={styles.check}>
        <input type="checkbox" checked={o.idToken} disabled={readOnly} onChange={(e) => setOAuth2({ idToken: e.target.checked })} />
        {t("editor.auth.oauth2.sendIdToken")}
      </label>
      <span />
      <p className={styles.hint}>
        <IconInfo size={12} />
        <span>{t("editor.auth.oauth2.idTokenNote")}</span>
      </p>

      {!implicit && <OAuth2Params params={params} readOnly={readOnly} setParam={setParam} setParams={setParams} />}
      <span className={styles.label}>{t("editor.auth.field.addTo")}</span>
      <select
        className={styles.select}
        aria-label={t("editor.auth.oauth2.sendIn")}
        value={knownIn ? (inQuery ? "query" : "header") : auth.in}
        disabled={readOnly}
        onChange={(e) => set({ in: e.target.value })}
      >
        <option value="header">{t("editor.auth.in.header")}</option>
        <option value="query">{t("editor.auth.in.query")}</option>
        {!knownIn && <option value={auth.in}>{t("editor.auth.quoted", { value: auth.in })}</option>}
      </select>
      <TextField
        key={inQuery ? "param" : "header"}
        label={inQuery ? t("editor.auth.field.parameter") : t("editor.auth.field.headerName")}
        value={auth.key}
        readOnly={readOnly}
        placeholder={inQuery ? DEFAULT_OAUTH2_QUERY_PARAM : AUTHORIZATION}
        onChange={(v) => set({ key: v })}
      />
      {!inQuery && (
        <TextField
          label={t("editor.auth.field.prefix")}
          value={prefix}
          readOnly={readOnly}
          placeholder={t("editor.auth.placeholder.noPrefix")}
          onChange={(v) => set({ prefix: v })}
        />
      )}
      <EnabledField auth={auth} readOnly={readOnly} set={set} />
      <Preview auth={auth} folder={folder} wins={inQuery ? t("editor.auth.wins.query") : undefined}>
        {inQuery ? (
          <Trans
            k={implicit ? "editor.auth.oauth2.previewQueryImplicit" : "editor.auth.oauth2.previewQuery"}
            values={{ param: <code>?{name}=…</code> }}
          />
        ) : (
          <Trans
            k={implicit ? "editor.auth.oauth2.previewHeaderImplicit" : "editor.auth.oauth2.previewHeader"}
            values={{ header: <code>{`${name}: ${prefix === "" ? "" : `${prefix} `}<token>`}</code> }}
          />
        )}
      </Preview>
      {!folder && !auth.disabled && <OAuth2TokenPanel id={id} revision={JSON.stringify(auth)} />}
    </>
  );
}

interface OAuth2ParamsProps {
  params: KeyValueDTO[];
  readOnly: boolean;
  setParam: (i: number, row: Partial<KeyValueDTO>) => void;
  setParams: (rows: KeyValueDTO[]) => void;
}

/** The extra parameters of an oauth2 auth's token request. */
function OAuth2Params({ params, readOnly, setParam, setParams }: OAuth2ParamsProps) {
  const t = useT();
  return (
    <>
      <span className={styles.label}>{t("editor.auth.field.tokenParams")}</span>
      <div className={styles.paramList} role="group" aria-label={t("editor.auth.oauth2.paramsLabel")}>
        {params.map((p, i) => (
          <div key={i} className={styles.paramRow}>
            <input
              type="checkbox"
              checked={p.enabled}
              disabled={readOnly}
              aria-label={t("editor.auth.oauth2.paramEnabled", { number: i + 1 })}
              onChange={(e) => setParam(i, { enabled: e.target.checked })}
            />
            <input
              className={styles.paramInput}
              value={p.name}
              readOnly={readOnly}
              placeholder={t("editor.auth.oauth2.paramName")}
              aria-label={t("editor.auth.oauth2.paramNameLabel", { number: i + 1 })}
              spellCheck={false}
              onChange={(e) => setParam(i, { name: e.target.value })}
            />
            <input
              className={styles.paramInput}
              value={p.value}
              readOnly={readOnly}
              placeholder={t("editor.auth.oauth2.paramValue")}
              aria-label={t("editor.auth.oauth2.paramValueLabel", { number: i + 1 })}
              spellCheck={false}
              onChange={(e) => setParam(i, { value: e.target.value })}
            />
            <button
              type="button"
              className={styles.action}
              disabled={readOnly}
              aria-label={t("editor.auth.oauth2.paramRemove", { number: i + 1 })}
              onClick={() => setParams(params.filter((_, j) => j !== i))}
            >
              {t("editor.auth.oauth2.remove")}
            </button>
          </div>
        ))}
        <div>
          <button
            type="button"
            className={styles.action}
            disabled={readOnly}
            onClick={() => setParams([...params, { name: "", value: "", description: "", enabled: true, extra: "" }])}
          >
            {t("editor.auth.oauth2.paramAdd")}
          </button>
        </div>
      </div>
    </>
  );
}

interface HawkFieldsProps extends FieldsProps {
  setHawk: (fields: Partial<HawkAuthDTO>) => void;
}

/**
 * Hawk (AUTH-18): the credentials, the MAC algorithm, the optional ext,
 * app and dlg, and the payload hash. Go signs the request as it is sent
 * (internal/core/hawk) with a new timestamp and nonce each time; the key is
 * kept on this computer like any other secret.
 */
function HawkFields({ auth, readOnly, set, setHawk, folder }: HawkFieldsProps) {
  const t = useT();
  const h = hawkOf(auth);
  const algorithm = h.algorithm === "" ? DEFAULT_HAWK_ALGORITHM : h.algorithm;
  const header = (
    <code>
      Authorization: Hawk id=…, ts=…, nonce=…{h.payloadHash ? ", hash=…" : ""}
      {h.ext !== "" ? ", ext=…" : ""}, mac=…
    </code>
  );
  return (
    <>
      <TextField label={t("editor.auth.field.hawkId")} value={h.id} readOnly={readOnly} placeholder="{{hawkId}}" onChange={(v) => setHawk({ id: v })} />
      <SecretField label={t("editor.auth.field.hawkKey")} member="hawkKey" auth={auth} readOnly={readOnly} set={set} placeholder="{{hawkKey}}" />
      <span className={styles.label}>{t("editor.auth.field.algorithm")}</span>
      <select
        className={styles.select}
        aria-label={t("editor.auth.hawk.algorithm")}
        value={algorithm}
        disabled={readOnly}
        onChange={(e) => setHawk({ algorithm: e.target.value })}
      >
        {HAWK_ALGORITHMS.map((m) => (
          <option key={m} value={m}>
            {m}
          </option>
        ))}
        {!HAWK_ALGORITHMS.includes(algorithm) && <option value={algorithm}>{t("editor.auth.quoted", { value: algorithm })}</option>}
      </select>
      {/* Ext, App and Dlg are Hawk's own parameter names (ext, app, dlg). */}
      <TextField
        label="Ext" // i18n-ignore: a Hawk parameter name
        value={h.ext}
        readOnly={readOnly}
        placeholder={t("editor.auth.placeholder.ext")}
        onChange={(v) => setHawk({ ext: v })}
      />
      <TextField
        label="App" // i18n-ignore: a Hawk parameter name
        value={h.app}
        readOnly={readOnly}
        placeholder={t("editor.auth.placeholder.app")}
        onChange={(v) => setHawk({ app: v })}
      />
      {h.app !== "" && (
        <TextField
          label="Dlg" // i18n-ignore: a Hawk parameter name
          value={h.dlg}
          readOnly={readOnly}
          placeholder={t("editor.auth.placeholder.dlg")}
          onChange={(v) => setHawk({ dlg: v })}
        />
      )}
      <span />
      <div className={styles.inline}>
        <label className={styles.check}>
          <input type="checkbox" checked={h.payloadHash} disabled={readOnly} onChange={(e) => setHawk({ payloadHash: e.target.checked })} />
          {t("editor.auth.hawk.hashPayload")}
        </label>
      </div>
      <EnabledField auth={auth} readOnly={readOnly} set={set} />
      <Preview auth={auth} folder={folder}>
        {h.payloadHash ? (
          <Trans k="editor.auth.hawk.previewPayload" values={{ algorithm: algorithm.toUpperCase(), header }} />
        ) : (
          <Trans k="editor.auth.hawk.preview" values={{ algorithm: algorithm.toUpperCase(), header }} />
        )}
      </Preview>
    </>
  );
}

/**
 * A netrc auth (AUTH-19): the file to read (empty for NETRC or the home
 * directory's .netrc), which file that is, and on a click the entry the
 * URL's host matches. Go reads the file only when the request is sent or
 * checked; nothing from it is saved.
 */
function NetrcFields({ id, auth, readOnly, set, folder }: FieldsProps & { id: RequestId }) {
  const t = useT();
  const file = auth.netrcFile ?? "";
  return (
    <>
      <TextField
        label={t("editor.auth.field.file")}
        value={file}
        readOnly={readOnly}
        placeholder={t("editor.auth.placeholder.netrcFile")}
        onChange={(v) => set({ netrcFile: v })}
      />
      <NetrcFileNote id={folder ? null : id} file={file} />
      <EnabledField auth={auth} readOnly={readOnly} set={set} />
      <Preview auth={auth} folder={folder}>
        <Trans
          k="editor.auth.netrc.preview"
          values={{ header: <code>Authorization: Basic …</code>, machine: <code>machine</code>, default: <code>default</code> }}
        />
      </Preview>
      {!folder && !auth.disabled && <NetrcCheck id={id} />}
    </>
  );
}

function Note({ children }: { children: ReactNode }) {
  return (
    <>
      <span />
      <p className={styles.note}>{children}</p>
    </>
  );
}

/** What the auth sends, and what overrides it (docs/auth.md). */
function Preview({ auth, wins, folder, children }: { auth: AuthDTO; wins?: string; folder?: boolean; children: ReactNode }) {
  const t = useT();
  const protocol = useContext(ProtocolContext);
  if (auth.disabled) {
    return <Note>{folder ? t("editor.auth.disabled.folder") : t("editor.auth.disabled.request")}</Note>;
  }
  const header = folder
    ? t("editor.auth.wins.header.folder")
    : protocol === "grpc"
      ? t("editor.auth.wins.metadata")
      : t("editor.auth.wins.header.request");
  return (
    <Note>
      {children} {wins ?? header}
    </Note>
  );
}

function InheritNote({ id, folder }: { id: RequestId; folder: boolean }) {
  const t = useT();
  const state = useInheritedAuth(id, true);
  const item = folder ? "folder" : "request";
  let text: ReactNode = t("editor.auth.inherit.default");
  if (state.status === "loaded") {
    const { inherited } = state;
    const name = AUTH_TYPE_NAMES[inherited.type] ?? t("editor.auth.quoted", { value: inherited.type });
    const params = { name, folder: inherited.folderName };
    text =
      inherited.folderId === ""
        ? folder
          ? t("editor.auth.inherit.none.folder")
          : t("editor.auth.inherit.none.request")
        : inherited.disabled
          ? t("editor.auth.inherit.fromDisabled", params)
          : t("editor.auth.inherit.from", params);
  } else if (state.status === "error") {
    text = (
      <>
        {t("editor.auth.inherit.default")} <span className={styles.warn}>{state.message}</span>
      </>
    );
  }
  return (
    <>
      <Note>
        <span role="status">{text}</span>
      </Note>
      {state.status === "loaded" && state.inherited.folderId !== "" && state.inherited.auth != null && (
        <InheritedFields auth={state.inherited.auth} folderName={state.inherited.folderName} item={item} />
      )}
      {!folder && state.status === "loaded" && state.inherited.type === "jwt" && !state.inherited.disabled && <JwtPreview id={id} />}
      {!folder && state.status === "loaded" && state.inherited.type === "netrc" && !state.inherited.disabled && <NetrcCheck id={id} />}
      {!folder && state.status === "loaded" && state.inherited.type === "oauth2" && !state.inherited.disabled && (
        <OAuth2TokenPanel id={id} revision={JSON.stringify(state.inherited.auth)} />
      )}
    </>
  );
}

interface InheritedField {
  label: string;
  value: string;
  /** The secret member: shown masked unless it is {{references}}. */
  secret?: SecretMember;
}

function inheritedFields(t: Translate, auth: AuthDTO): InheritedField[] {
  switch (authType(auth)) {
    case "bearer":
      return [
        { label: t("editor.auth.field.token"), value: auth.token, secret: "token" },
        { label: t("editor.auth.field.prefix"), value: auth.prefix ?? DEFAULT_BEARER_PREFIX },
      ];
    case "basic":
    case "digest":
      return [
        { label: t("editor.auth.field.username"), value: auth.username },
        { label: t("editor.auth.field.password"), value: auth.password, secret: "password" },
      ];
    case "apikey":
      return [
        { label: t("editor.auth.field.key"), value: auth.key },
        { label: t("editor.auth.field.value"), value: auth.value, secret: "value" },
        { label: t("editor.auth.field.addTo"), value: auth.in === "query" ? t("editor.auth.in.query") : t("editor.auth.in.header") },
      ];
    case "oauth1": {
      const o = oauth1Of(auth);
      const method = o.signatureMethod === "" ? DEFAULT_OAUTH1_SIGNATURE_METHOD : o.signatureMethod;
      const rsa = method === "RSA-SHA1";
      const fields: InheritedField[] = [
        { label: t("editor.auth.field.signature"), value: method },
        { label: t("editor.auth.field.consumerKey"), value: o.consumerKey },
        {
          label: rsa ? t("editor.auth.field.privateKey") : t("editor.auth.field.consumerSecret"),
          value: auth.consumerSecret ?? "",
          secret: "consumerSecret",
        },
        { label: t("editor.auth.field.accessToken"), value: o.accessToken },
      ];
      if (!rsa) fields.push({ label: t("editor.auth.field.tokenSecret"), value: auth.tokenSecret ?? "", secret: "tokenSecret" });
      fields.push({
        label: t("editor.auth.field.addTo"),
        value:
          auth.in === "query"
            ? t("editor.auth.in.query")
            : auth.in === "body"
              ? t("editor.auth.in.formBody")
              : t("editor.auth.in.authorizationHeader"),
      });
      if (o.callback !== "") fields.push({ label: t("editor.auth.field.callback"), value: o.callback });
      if (o.verifier !== "") fields.push({ label: t("editor.auth.field.verifier"), value: o.verifier });
      if (o.realm !== "") fields.push({ label: t("editor.auth.field.realm"), value: o.realm });
      return fields;
    }
    case "awsv4": {
      const a = awsOf(auth);
      const fields: InheritedField[] = [
        { label: t("editor.auth.field.accessKey"), value: a.accessKey },
        { label: t("editor.auth.field.secretKey"), value: auth.secretKey ?? "", secret: "secretKey" },
      ];
      if ((auth.sessionToken ?? "") !== "" || (auth.localSecrets ?? []).includes("sessionToken")) {
        fields.push({ label: t("editor.auth.field.sessionToken"), value: auth.sessionToken ?? "", secret: "sessionToken" });
      }
      fields.push(
        { label: t("editor.auth.field.region"), value: a.region },
        { label: t("editor.auth.field.service"), value: a.service },
        {
          label: t("editor.auth.field.addTo"),
          value: auth.in === "query" ? t("editor.auth.in.presigned") : t("editor.auth.in.authorizationHeader"),
        },
      );
      if (a.unsignedPayload) fields.push({ label: t("editor.auth.field.payload"), value: t("editor.auth.inherited.unsigned") });
      return fields;
    }
    case "hawk": {
      const h = hawkOf(auth);
      const fields: InheritedField[] = [
        { label: t("editor.auth.field.hawkId"), value: h.id },
        { label: t("editor.auth.field.hawkKey"), value: auth.hawkKey ?? "", secret: "hawkKey" },
        { label: t("editor.auth.field.algorithm"), value: h.algorithm === "" ? DEFAULT_HAWK_ALGORITHM : h.algorithm },
      ];
      // i18n-ignore-start: Hawk's own parameter names
      if (h.ext !== "") fields.push({ label: "Ext", value: h.ext });
      if (h.app !== "") fields.push({ label: "App", value: h.app });
      if (h.app !== "" && h.dlg !== "") fields.push({ label: "Dlg", value: h.dlg });
      // i18n-ignore-end
      if (h.payloadHash) fields.push({ label: t("editor.auth.field.payload"), value: t("editor.auth.inherited.hashed") });
      return fields;
    }
    case "netrc":
      return [{ label: t("editor.auth.field.file"), value: (auth.netrcFile ?? "") || t("editor.auth.placeholder.netrcFile") }];
    case "oauth2": {
      const o = oauth2Of(auth);
      const grant = o.grantType === "" ? DEFAULT_OAUTH2_GRANT : o.grantType;
      const inQuery = auth.in === "query";
      const implicit = grant === OAUTH2_IMPLICIT;
      const fields: InheritedField[] = [
        { label: t("editor.auth.field.grant"), value: GRANT_LABELS[grant] === undefined ? grant : t(GRANT_LABELS[grant]) },
      ];
      if (!implicit) fields.push({ label: t("editor.auth.field.tokenUrl"), value: o.tokenUrl });
      if (isBrowserGrant(grant)) {
        fields.push(
          { label: t("editor.auth.field.authUrl"), value: o.authUrl },
          { label: t("editor.auth.field.redirectUrl"), value: o.redirectUrl === "" ? t("editor.auth.placeholder.redirectUrl") : o.redirectUrl },
        );
      }
      fields.push({ label: t("editor.auth.field.clientId"), value: o.clientId });
      if (!implicit) {
        fields.push(
          { label: t("editor.auth.field.clientSecret"), value: auth.clientSecret ?? "", secret: "clientSecret" },
          {
            label: t("editor.auth.field.clientAuth"),
            value: o.clientAuth === "body" ? t("editor.auth.oauth2.clientAuth.body") : t("editor.auth.oauth2.clientAuth.header"),
          },
        );
      }
      if (grant === OAUTH2_PASSWORD) {
        fields.push(
          { label: t("editor.auth.field.username"), value: auth.username },
          { label: t("editor.auth.field.password"), value: auth.password, secret: "password" },
        );
      }
      if (o.issuer !== "") fields.push({ label: t("editor.auth.field.issuer"), value: o.issuer });
      if (o.idToken) fields.push({ label: t("editor.auth.field.sends"), value: t("editor.auth.oauth2.idToken") });
      if (o.scope !== "") fields.push({ label: t("editor.auth.field.scope"), value: o.scope });
      if (o.audience !== "") fields.push({ label: t("editor.auth.field.audience"), value: o.audience });
      if (o.resource !== "") fields.push({ label: t("editor.auth.field.resource"), value: o.resource });
      const extra = (o.tokenParams ?? []).filter((p) => p.enabled && p.name !== "");
      if (extra.length > 0) fields.push({ label: t("editor.auth.field.tokenParams"), value: extra.map((p) => `${p.name}=${p.value}`).join(", ") });
      fields.push({
        label: t("editor.auth.field.addTo"),
        value: inQuery
          ? t("editor.auth.in.queryParam", { name: auth.key === "" ? DEFAULT_OAUTH2_QUERY_PARAM : auth.key })
          : t("editor.auth.in.headerWithPrefix", {
              name: auth.key === "" ? AUTHORIZATION : auth.key,
              prefix: auth.prefix ?? DEFAULT_BEARER_PREFIX,
            }),
      });
      return fields;
    }
    case "jwt": {
      const jwt = jwtOf(auth);
      const inQuery = auth.in === "query";
      // Claim names, not text.
      const claims = [
        jwt.issuedAt ? "iat" : "",
        jwt.notBefore ? "nbf" : "",
        jwt.jwtId ? "jti" : "",
        jwt.expiresIn > 0 ? `exp +${jwt.expiresIn}s` : "",
      ].filter((c) => c !== "");
      return [
        { label: t("editor.auth.field.algorithm"), value: jwt.algorithm === "" ? DEFAULT_JWT_ALGORITHM : jwt.algorithm },
        jwt.keyFile !== ""
          ? { label: t("editor.auth.field.keyFile"), value: jwt.keyFile }
          : {
              label: isHmacAlgorithm(jwt.algorithm) ? t("editor.auth.field.secret") : t("editor.auth.field.privateKey"),
              value: auth.secret ?? "",
              secret: "secret",
            },
        { label: t("editor.auth.field.header"), value: jwt.header },
        { label: t("editor.auth.field.payload"), value: jwt.payload },
        { label: t("editor.auth.field.addClaims"), value: claims.join(", ") },
        {
          label: t("editor.auth.field.addTo"),
          value: inQuery
            ? t("editor.auth.in.queryParam", { name: auth.key === "" ? DEFAULT_JWT_QUERY_PARAM : auth.key })
            : t("editor.auth.in.headerWithPrefix", {
                name: auth.key === "" ? AUTHORIZATION : auth.key,
                prefix: auth.prefix ?? DEFAULT_BEARER_PREFIX,
              }),
        },
      ];
    }
  }
  return [];
}

/**
 * The fields of the inherited auth, read-only (AUTH-01, AUTH-32): their
 * `{{variables}}` drawn as chips resolved with this item's variables, the
 * way they are sent (a folder's auth is rendered with the scopes of the
 * request that sends it). Go never sends a typed secret here.
 */
function InheritedFields({ auth, folderName, item }: { auth: AuthDTO; folderName: string; item: string }) {
  const t = useT();
  const variables = useTemplateVariables()?.variables ?? null;
  const fields = inheritedFields(t, auth);
  if (fields.length === 0) return null;
  const vars = <code>{"{{variables}}"}</code>;
  return (
    <>
      {fields.map((f) => {
        const hidden = f.secret !== undefined && f.value === "";
        const stored = hidden && (auth.localSecrets ?? []).includes(f.secret as string);
        return (
          <div
            key={f.label}
            className={styles.inheritedRow}
            role="group"
            aria-label={t("editor.auth.inherited.label", { field: f.label })}
          >
            <span className={styles.label}>{f.label}</span>
            <span className={styles.inheritedValue} title={hidden ? undefined : templateTitle(f.value, variables)}>
              {stored ? (
                <span className={styles.inheritedHidden}>{t("editor.auth.inherited.stored", { mask: SECRET_MASK, folder: folderName })}</span>
              ) : f.value === "" ? (
                <span className={styles.inheritedHidden}>{t("editor.auth.inherited.empty")}</span>
              ) : (
                <TemplateText text={f.value} variables={variables} />
              )}
            </span>
          </div>
        );
      })}
      <Note>
        {item === "request" ? (
          <Trans k="editor.auth.inherited.note.request" values={{ folder: folderName, variables: vars }} />
        ) : (
          <Trans k="editor.auth.inherited.note.folder" values={{ folder: folderName, variables: vars }} />
        )}
      </Note>
    </>
  );
}

interface TextFieldProps {
  label: string;
  value: string;
  readOnly: boolean;
  placeholder: string;
  onChange: (value: string) => void;
}

/** A one-line template field (the URL bar's CodeEditor): `{{variables}}` highlighted. */
function TextField({ label, value, readOnly, placeholder, onChange }: TextFieldProps) {
  return (
    <>
      <span className={styles.label}>{label}</span>
      <div className={styles.field}>
        <CodeEditor
          className={styles.editor}
          singleLine
          ariaLabel={label}
          placeholder={placeholder}
          value={value}
          readOnly={readOnly}
          onChange={readOnly ? undefined : onChange}
        />
      </div>
    </>
  );
}

interface SecretFieldProps extends FieldsProps {
  label: string;
  member: SecretMember;
  placeholder: string;
  /** A multi-line value (a PEM private key): hidden as a masked box rather than a password field. */
  multiline?: boolean;
}

/** How the notes and the show button name each secret: "Show the {what}". */
const SECRET_NAMES: Readonly<Record<SecretMember, MessageKey>> = {
  password: "editor.auth.secret.password",
  token: "editor.auth.secret.token",
  value: "editor.auth.secret.value",
  secret: "editor.auth.secret.secret",
  consumerSecret: "editor.auth.secret.consumerSecret",
  tokenSecret: "editor.auth.secret.tokenSecret",
  secretKey: "editor.auth.secret.secretKey",
  sessionToken: "editor.auth.secret.sessionToken",
  hawkKey: "editor.auth.secret.hawkKey",
  clientSecret: "editor.auth.secret.clientSecret",
};

/**
 * A secret: a password field, or with the eye the template field. A typed
 * value starts hidden; an empty one or a `{{variable}}` starts shown.
 */
function SecretField({ label, member, auth, readOnly, set, placeholder, multiline = false }: SecretFieldProps) {
  const t = useT();
  const value = secretValue(auth, member);
  const storage = secretStorage(auth, member);
  const [shown, setShown] = useState(storage !== "local");
  const onChange = (v: string) => set({ [member]: v });
  const what = t(SECRET_NAMES[member]);
  const variable = <code>{"{{variable}}"}</code>;
  const toggle = shown ? t("editor.auth.secret.hide", { what }) : t("editor.auth.secret.show", { what });

  return (
    <>
      <span className={styles.label}>{label}</span>
      <div className={styles.secret}>
        {multiline ? (
          shown ? (
            <div className={styles.codeBox}>
              <CodeEditor ariaLabel={label} value={value} readOnly={readOnly} onChange={readOnly ? undefined : onChange} />
            </div>
          ) : (
            <div className={styles.maskedBox} aria-label={label} role="textbox" aria-readonly="true">
              {value === ""
                ? placeholder
                : t("editor.auth.secret.lines", { mask: SECRET_MASK, count: value.split("\n").filter((l) => l.trim() !== "").length })}
            </div>
          )
        ) : (
        <div className={styles.field}>
          {shown ? (
            <CodeEditor
              className={styles.editor}
              singleLine
              ariaLabel={label}
              placeholder={placeholder}
              value={value}
              readOnly={readOnly}
              onChange={readOnly ? undefined : onChange}
            />
          ) : (
            <input
              className={styles.password}
              type="password"
              aria-label={label}
              value={value}
              readOnly={readOnly}
              autoComplete="off"
              spellCheck={false}
              onChange={(e) => onChange(e.target.value)}
            />
          )}
        </div>
        )}
        <button
          type="button"
          className={styles.reveal}
          aria-label={toggle}
          aria-pressed={shown}
          title={toggle}
          onClick={() => setShown((s) => !s)}
        >
          {shown ? <IconEyeOff size={13} /> : <IconEye size={13} />}
        </button>
      </div>
      <span />
      <p className={styles.hint} data-storage={storage}>
        {storage === "missing" ? (
          <IconWarning size={12} />
        ) : storage === "local" ? (
          <IconLock size={12} />
        ) : (
          <IconInfo size={12} />
        )}
        <span>
          {storage === "missing" && <Trans k="editor.auth.secret.missing" values={{ what, variable }} />}
          {storage === "local" && <Trans k="editor.auth.secret.local" values={{ variable }} />}
          {storage === "reference" && t("editor.auth.secret.reference")}
          {storage === "empty" && <Trans k="editor.auth.secret.empty" values={{ what, variable }} />}
        </span>
      </p>
    </>
  );
}

function EnabledField({ auth, readOnly, set }: FieldsProps) {
  const t = useT();
  return (
    <>
      <span />
      <label className={styles.check}>
        <input type="checkbox" checked={!auth.disabled} disabled={readOnly} onChange={(e) => set({ disabled: !e.target.checked })} />
        {t("editor.auth.send")}
      </label>
    </>
  );
}
