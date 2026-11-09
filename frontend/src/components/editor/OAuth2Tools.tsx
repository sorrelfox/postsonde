import { Fragment, useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import {
  clearOAuth2Token,
  getOAuth2Token,
  isBrowserGrant,
  oauth2Token,
  onOAuth2TokenChanged,
  type OAuth2IDTokenDTO,
  type OAuth2TokenDTO,
} from "../../lib/auth";
import { isCancelled, toAppError } from "../../lib/errors";
import { formatDate } from "../../i18n";
import { Trans, useT } from "../../i18n/react";
import type { RequestId } from "../../stores";
import { IconWarning } from "../common/icons";
import styles from "./AuthEditor.module.css";

type StatusState = { status: "loading" } | { status: "loaded"; token: OAuth2TokenDTO } | { status: "error"; message: string };

type Busy = null | "get" | "refresh" | "clear";

type Call = { cancel: () => unknown };

/** How long after an edit the status is asked for again. */
const REFETCH_DELAY_MS = 400;

/** The longest setTimeout delay (about 24.8 days). */
const MAX_TIMER_MS = 2 ** 31 - 1;

const TIME: Intl.DateTimeFormatOptions = { dateStyle: "medium", timeStyle: "short" };

/**
 * The stored token of a request's OAuth 2.0 auth (AUTH-11, AUTH-13): how
 * long it is valid, masked by Go (the token itself never reaches the UI),
 * and Get new token, Refresh and Clear; an OpenID Connect ID token's claims
 * (AUTH-14, IdTokenClaims). Sends get, refresh and store the
 * token by themselves; this is for looking and for doing it by hand.
 * Tokens are kept on this computer, never in the project.
 *
 * `revision` changes with the auth's fields, whose values make the token's
 * key: the status is asked for again a moment after it changes, and
 * whenever Go says a stored token changed.
 */
export function OAuth2TokenPanel({ id, revision }: { id: RequestId; revision: string }) {
  const t = useT();
  const [state, setState] = useState<StatusState>({ status: "loading" });
  const [busy, setBusy] = useState<Busy>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [tick, setTick] = useState(0);
  const live = useRef(true);
  const action = useRef<Call | null>(null);

  useEffect(() => {
    live.current = true;
    return () => {
      live.current = false;
      void action.current?.cancel();
    };
  }, []);
  // Another request drops what was shown.
  useEffect(() => {
    setState({ status: "loading" });
    setActionError(null);
  }, [id]);

  const refetch = useCallback(() => setTick((n) => n + 1), []);

  useEffect(() => {
    let call: Call | null = null;
    const timer = setTimeout(() => {
      const c = oauth2Token(id);
      call = c;
      c.then(
        (token) => setState({ status: "loaded", token }),
        (err: unknown) => {
          if (!isCancelled(err)) setState({ status: "error", message: toAppError(err).message });
        },
      );
    }, REFETCH_DELAY_MS);
    return () => {
      clearTimeout(timer);
      void call?.cancel();
    };
  }, [id, revision, tick]);

  useEffect(() => onOAuth2TokenChanged(refetch), [refetch]);

  // Shown as expired once it expires, without an edit.
  const expiresAt = state.status === "loaded" && state.token.status === "valid" ? state.token.expiresAt : 0;
  useEffect(() => {
    if (expiresAt === 0) return;
    const timer = setTimeout(refetch, Math.min(Math.max(expiresAt - Date.now(), 0) + 1000, MAX_TIMER_MS));
    return () => clearTimeout(timer);
  }, [expiresAt, refetch]);

  const run = (kind: Exclude<Busy, null>) => {
    setBusy(kind);
    setActionError(null);
    const c = kind === "clear" ? clearOAuth2Token(id) : getOAuth2Token(id, kind === "refresh");
    action.current = c;
    c.then(
      (token) => {
        if (!live.current) return;
        setState({ status: "loaded", token });
        setBusy(null);
      },
      (err: unknown) => {
        if (!live.current) return;
        setBusy(null);
        if (!isCancelled(err)) setActionError(toAppError(err).message);
      },
    );
  };
  const cancel = () => {
    void action.current?.cancel();
  };

  if (state.status === "loaded" && !state.token.available) return null;
  if (state.status === "loading") return null;

  let text: ReactNode;
  let scope: ReactNode = null;
  let token: OAuth2TokenDTO | null = null;
  if (state.status === "error") {
    text = t("editor.oauth2Token.unavailable", { error: state.message });
  } else {
    token = state.token;
    const shown = <code>{token.token}</code>;
    const time = token.expiresAt > 0 ? formatDate(new Date(token.expiresAt), TIME) : "";
    if (token.status === "none") {
      text = t("editor.oauth2Token.none");
    } else if (token.status === "expired") {
      text = token.refreshable ? (
        <Trans k="editor.oauth2Token.expiredRefresh" values={{ token: shown, time }} />
      ) : (
        <Trans k="editor.oauth2Token.expired" values={{ token: shown, time }} />
      );
    } else if (time === "") {
      text = <Trans k="editor.oauth2Token.validNoExpiry" values={{ token: shown }} />;
    } else {
      text = <Trans k="editor.oauth2Token.validUntil" values={{ token: shown, time }} />;
    }
    if (token.status !== "none" && token.scope !== "") scope = t("editor.oauth2Token.scope", { scope: token.scope });
  }
  const has = token !== null && token.status !== "none";

  return (
    <>
      <span className={styles.label}>{t("editor.oauth2Token.label")}</span>
      <div className={styles.jwtTools} data-oauth2-token={token?.status ?? "error"}>
        <p className={styles.note} aria-live="polite">
          {text}
          {scope !== null && (
            <>
              <br />
              {scope}
            </>
          )}
        </p>
        {token !== null && token.status !== "none" && token.idToken !== null && (
          <IdTokenClaims id={token.idToken} sent={token.sendsIdToken} />
        )}
        <div className={styles.jwtActions}>
          <button type="button" className={styles.action} disabled={busy !== null || token === null} onClick={() => run("get")}>
            {busy === "get"
              ? token !== null && isBrowserGrant(token.grant)
                ? t("editor.oauth2Token.signingIn")
                : t("editor.oauth2Token.getting")
              : t("editor.oauth2Token.get")}
          </button>
          <button
            type="button"
            className={styles.action}
            disabled={busy !== null || token === null || !token.refreshable}
            title={token !== null && has && !token.refreshable ? t("editor.oauth2Token.noRefresh") : undefined}
            onClick={() => run("refresh")}
          >
            {busy === "refresh" ? t("editor.oauth2Token.refreshing") : t("editor.oauth2Token.refresh")}
          </button>
          <button type="button" className={styles.action} disabled={busy !== null || !has} onClick={() => run("clear")}>
            {t("editor.oauth2Token.clear")}
          </button>
          {(busy === "get" || busy === "refresh") && (
            <button type="button" className={styles.action} onClick={cancel}>
              {t("editor.oauth2Token.cancel")}
            </button>
          )}
        </div>
        {actionError !== null && (
          <p className={styles.jwtError} role="alert">
            <IconWarning size={12} />
            <span>{actionError}</span>
          </p>
        )}
        <p className={styles.note}>
          {token !== null && token.folderName !== ""
            ? t("editor.oauth2Token.keptFolder", { folder: token.folderName })
            : t("editor.oauth2Token.kept")}
        </p>
      </div>
    </>
  );
}

/**
 * The claims of a stored token's OpenID Connect ID token (AUTH-14), as Go
 * decoded them: the token itself is masked, like the access token. Its
 * claims were checked when it arrived; its signature is not verified
 * (docs/auth.md).
 */
function IdTokenClaims({ id, sent }: { id: OAuth2IDTokenDTO; sent: boolean }) {
  const t = useT();
  if (id.error !== "") {
    return <p className={styles.note}>{t("editor.oauth2Token.idToken.unreadable", { error: id.error })}</p>;
  }
  const expires = id.expiresAt > 0 ? formatDate(new Date(id.expiresAt), TIME) : "";
  const rows: { label: string; value: string }[] = [
    { label: t("editor.oauth2Token.idToken.issuer"), value: id.issuer },
    { label: t("editor.oauth2Token.idToken.subject"), value: id.subject },
    { label: t("editor.oauth2Token.idToken.audience"), value: (id.audience ?? []).join(", ") },
    { label: t("editor.oauth2Token.idToken.expires"), value: id.expired ? t("editor.oauth2Token.idToken.expired", { time: expires }) : expires },
  ];
  if (id.name !== "") rows.push({ label: t("editor.oauth2Token.idToken.name"), value: id.name });
  if (id.email !== "") rows.push({ label: t("editor.oauth2Token.idToken.email"), value: id.email });
  return (
    <div className={styles.idToken} data-oauth2-id-token={id.expired ? "expired" : "valid"}>
      <p className={styles.note}>
        <Trans
          k={sent ? "editor.oauth2Token.idToken.sent" : "editor.oauth2Token.idToken.value"}
          values={{ token: <code>{id.token}</code>, algorithm: <code>{id.algorithm}</code> }}
        />
      </p>
      <dl className={styles.idClaims} aria-label={t("editor.oauth2Token.idToken.label")}>
        {rows.map((r) => (
          <Fragment key={r.label}>
            <dt>{r.label}</dt>
            <dd>{r.value}</dd>
          </Fragment>
        ))}
      </dl>
      <p className={styles.note}>{t("editor.oauth2Token.idToken.unverified")}</p>
    </div>
  );
}
