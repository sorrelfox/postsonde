import { useEffect, useId, useRef, useState } from "react";
import { jwtPreview, type JWTPreviewDTO } from "../../lib/auth";
import { copyText } from "../../lib/clipboard";
import { isCancelled, toAppError } from "../../lib/errors";
import { decodeJwt, formatClaimTime, type DecodedJwt } from "../../lib/jwt";
import type { MessageKey } from "../../i18n";
import { useT } from "../../i18n/react";
import type { RequestId } from "../../stores";
import { IconCopy, IconWarning } from "../common/icons";
import styles from "./AuthEditor.module.css";

type PreviewState =
  | { status: "idle" }
  | { status: "loading" }
  | { status: "loaded"; preview: JWTPreviewDTO }
  | { status: "error"; message: string };

/**
 * The token preview of a jwt auth (AUTH-16): on request, Go signs the JWT
 * the request would send now (JWTPreview, with its variables and this
 * moment's claims) and the tab shows it with its decoded parts. The token
 * is a credential, so it is shown only on a click, never kept, and hidden
 * again with the button.
 */
export function JwtPreview({ id }: { id: RequestId }) {
  const t = useT();
  const [state, setState] = useState<PreviewState>({ status: "idle" });
  const [copied, setCopied] = useState(false);
  const live = useRef(true);
  const call = useRef<{ cancel: () => unknown } | null>(null);
  useEffect(() => {
    live.current = true;
    return () => {
      live.current = false;
      void call.current?.cancel();
    };
  }, []);
  // Another request (or a type switch) clears what was shown.
  useEffect(() => setState({ status: "idle" }), [id]);

  const build = () => {
    setState({ status: "loading" });
    setCopied(false);
    const c = jwtPreview(id);
    call.current = c;
    c.then(
      (preview) => {
        if (live.current) setState({ status: "loaded", preview });
      },
      (err: unknown) => {
        if (live.current && !isCancelled(err)) setState({ status: "error", message: toAppError(err).message });
      },
    );
  };

  const shown = state.status === "loaded";
  return (
    <>
      <span className={styles.label}>{t("editor.auth.field.token")}</span>
      <div className={styles.jwtTools}>
        <div className={styles.jwtActions}>
          <button type="button" className={styles.action} onClick={build} disabled={state.status === "loading"}>
            {shown ? t("editor.jwt.signAgain") : t("editor.jwt.preview")}
          </button>
          {shown && (
            <>
              <button
                type="button"
                className={styles.action}
                onClick={() => {
                  void copyText(state.preview.token).then(
                    () => live.current && setCopied(true),
                    () => undefined,
                  );
                }}
              >
                <IconCopy size={12} /> {copied ? t("editor.jwt.copied") : t("editor.jwt.copy")}
              </button>
              <button type="button" className={styles.action} onClick={() => setState({ status: "idle" })}>
                {t("editor.jwt.hide")}
              </button>
            </>
          )}
        </div>
        {state.status === "idle" && (
          <p className={styles.note}>{t("editor.jwt.previewNote")}</p>
        )}
        {state.status === "error" && (
          <p className={styles.jwtError} role="alert">
            <IconWarning size={12} />
            <span>{state.message}</span>
          </p>
        )}
        {shown && <PreviewBody preview={state.preview} />}
      </div>
    </>
  );
}

function PreviewBody({ preview }: { preview: JWTPreviewDTO }) {
  const t = useT();
  const decoded = decodeJwt(preview.token);
  return (
    <>
      <code className={styles.token} aria-label={t("editor.jwt.signedToken")}>
        {preview.token}
      </code>
      {decoded.ok && <Decoded jwt={decoded.jwt} />}
      <p className={styles.note}>
        {preview.folderName !== ""
          ? t("editor.jwt.signedWithFolder", { algorithm: preview.algorithm, folder: preview.folderName })
          : t("editor.jwt.signedWith", { algorithm: preview.algorithm })}
        {(preview.unresolved ?? []).length > 0 && (
          <span className={styles.warn}> {t("editor.unresolvedVariables", { names: (preview.unresolved ?? []).join(", ") })}</span>
        )}
      </p>
    </>
  );
}

/** A decoded token: header, payload and its time claims in UTC. */
function Decoded({ jwt }: { jwt: DecodedJwt }) {
  const t = useT();
  const times: [MessageKey, number | null][] = [
    ["editor.jwt.issuedAt", jwt.issuedAt],
    ["editor.jwt.notBefore", jwt.notBefore],
    ["editor.jwt.expires", jwt.expiresAt],
  ];
  const now = Date.now() / 1000;
  return (
    <div className={styles.decoded}>
      <div>
        <span className={styles.decodedLabel}>{t("editor.auth.field.header")}</span>
        <pre className={styles.json}>{jwt.header}</pre>
      </div>
      <div>
        <span className={styles.decodedLabel}>{t("editor.auth.field.payload")}</span>
        <pre className={styles.json}>{jwt.payload}</pre>
      </div>
      {times.some(([, v]) => v !== null) && (
        <ul className={styles.claims}>
          {times.map(([key, v]) =>
            v === null ? null : (
              <li key={key}>
                {t("editor.jwt.claimTime", { label: t(key), time: formatClaimTime(v) })}
                {key === "editor.jwt.expires" && v < now && <span className={styles.warn}> {t("editor.jwt.expired")}</span>}
              </li>
            ),
          )}
        </ul>
      )}
    </div>
  );
}

/**
 * A decoder for any JWT pasted in (AUTH-16): its header and payload, as a
 * JWT debugger shows them. Nothing is verified, sent or saved; the text
 * lives only while the tab is open.
 */
export function JwtDecoder() {
  const t = useT();
  const [text, setText] = useState("");
  const inputId = useId();
  const result = text.trim() === "" ? null : decodeJwt(text);
  return (
    <>
      <span />
      <details className={styles.decoder}>
        <summary>{t("editor.jwt.decode")}</summary>
        <label htmlFor={inputId} className={styles.decodedLabel}>
          {t("editor.auth.field.token")}
        </label>
        <textarea
          id={inputId}
          className={styles.decoderInput}
          value={text}
          rows={3}
          spellCheck={false}
          autoComplete="off"
          placeholder="eyJhbGciOi…" // i18n-ignore: the start of a token
          onChange={(e) => setText(e.target.value)}
        />
        {result !== null &&
          (result.ok ? (
            <Decoded jwt={result.jwt} />
          ) : (
            <p className={styles.jwtError} role="alert">
              <IconWarning size={12} />
              <span>{result.error}</span>
            </p>
          ))}
        <p className={styles.note}>{t("editor.jwt.decodeNote")}</p>
      </details>
    </>
  );
}
