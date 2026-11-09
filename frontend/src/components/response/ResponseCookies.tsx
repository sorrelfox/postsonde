import { Fragment } from "react";
import type { MessageKey } from "../../i18n";
import { Trans, useT } from "../../i18n/react";
import type { CookieDTO } from "../../lib/http";
import styles from "./ResponseView.module.css";

const COLUMNS: readonly MessageKey[] = [
  "response.cookies.column.name",
  "response.cookies.column.value",
  "response.cookies.column.domain",
  "response.cookies.column.path",
  "response.cookies.column.expires",
  "response.cookies.column.flags",
  "response.cookies.column.jar",
];

/**
 * The response's Cookies tab (HTTP-32): each Set-Cookie header parsed by Go
 * (internal/core/cookies, like the jar), one row per header in the order
 * received: name, value, domain, path, expiry and flags, then what the
 * project's cookie jar did with it. A value that is not a cookie at all
 * keeps its row, with the reason; so does one the jar ignored. Attributes
 * that were ignored are listed under their cookie.
 */
export function ResponseCookies({ cookies }: { cookies: CookieDTO[] }) {
  const t = useT();
  if (cookies.length === 0) return <p className={styles.empty}>{t("response.cookies.empty")}</p>;
  return (
    <div className={`${styles.headersBox} ${styles.cookiesBox}`}>
      <table className={styles.cookies} aria-label={t("response.cookies.label")}>
        <thead>
          <tr>
            {COLUMNS.map((c) => (
              <th key={c} scope="col">
                {t(c)}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {cookies.map((c, i) => (
            <Fragment key={i}>
              <tr className={styles.cookieRow} data-cookie-state={c.error !== "" ? "invalid" : c.jar}>
                <th scope="row" className={styles.cookieName} title={c.raw}>
                  {c.name !== "" ? c.name : <span className={styles.cookieQuiet}>{t("response.cookies.noName")}</span>}
                </th>
                <td className={styles.cookieValue}>{c.value}</td>
                <td>{c.domain !== "" ? c.domain : <Quiet title={t("response.cookies.hostOnlyTitle")}>{t("response.cookies.hostOnly")}</Quiet>}</td>
                <td>{c.path !== "" ? c.path : <Quiet title={t("response.cookies.defaultPathTitle")}>{t("response.cookies.defaultPath")}</Quiet>}</td>
                <td>
                  <Expiry cookie={c} />
                </td>
                <td>
                  <Flags cookie={c} />
                </td>
                <td>
                  <JarState cookie={c} />
                </td>
              </tr>
              {(c.error !== "" || c.jarMessage !== "" || (c.notes ?? []).length > 0) && (
                <tr className={styles.cookieDetail}>
                  <td colSpan={COLUMNS.length}>
                    {c.error !== "" && (
                      <p className={styles.cookieProblem} role="note">
                        <Trans k="response.cookies.notACookie" values={{ error: c.error, raw: <code className={styles.cookieRaw}>{c.raw}</code> }} />
                      </p>
                    )}
                    {c.error === "" && c.jarMessage !== "" && (
                      <p className={styles.cookieProblem} role="note">
                        {t("response.cookies.notStored", { message: c.jarMessage })}
                      </p>
                    )}
                    {(c.notes ?? []).map((n, j) => (
                      <p key={j} className={styles.cookieNote}>
                        {n}
                      </p>
                    ))}
                  </td>
                </tr>
              )}
            </Fragment>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function Quiet({ children, title }: { children: string; title?: string }) {
  return (
    <span className={styles.cookieQuiet} title={title}>
      {children}
    </span>
  );
}

/** "2021-06-09 10:18:14 UTC" from Go's RFC 3339 UTC time. */
function formatExpires(rfc3339: string): string {
  return rfc3339.replace("T", " ").replace(/Z$/, " UTC");
}

/** Max-Age wins over Expires; neither is a session cookie. */
function Expiry({ cookie }: { cookie: CookieDTO }) {
  const t = useT();
  if (cookie.hasMaxAge) {
    const title = cookie.expires !== "" ? t("response.cookies.maxAgeWins", { expires: formatExpires(cookie.expires) }) : undefined;
    return (
      <span title={title}>
        {cookie.maxAge <= 0 ? t("response.cookies.maxAgeDelete", { maxAge: cookie.maxAge }) : t("response.cookies.maxAge", { maxAge: cookie.maxAge })}
      </span>
    );
  }
  if (cookie.expires !== "") return <span>{formatExpires(cookie.expires)}</span>;
  return <Quiet title={t("response.cookies.sessionTitle")}>{t("response.cookies.session")}</Quiet>;
}

function Flags({ cookie }: { cookie: CookieDTO }) {
  const t = useT();
  const flags: string[] = [];
  // i18n-ignore: Set-Cookie attribute names, as the header has them
  if (cookie.secure) flags.push("Secure");
  if (cookie.httpOnly) flags.push("HttpOnly");
  if (cookie.sameSite !== "") flags.push(`SameSite=${cookie.sameSite}`);
  if (flags.length === 0) return <Quiet>{t("response.cookies.noFlags")}</Quiet>;
  return (
    <span className={styles.cookieFlags}>
      {flags.map((f) => (
        <span key={f} className={styles.cookieFlag}>
          {f}
        </span>
      ))}
    </span>
  );
}

const JAR_LABELS: Record<string, { label: MessageKey; title: MessageKey }> = {
  stored: { label: "response.cookies.jar.stored", title: "response.cookies.jar.storedTitle" },
  removed: { label: "response.cookies.jar.removed", title: "response.cookies.jar.removedTitle" },
  rejected: { label: "response.cookies.jar.rejected", title: "response.cookies.jar.rejectedTitle" },
  skipped: { label: "response.cookies.jar.skipped", title: "response.cookies.jar.skippedTitle" },
};

function JarState({ cookie }: { cookie: CookieDTO }) {
  const t = useT();
  const state = JAR_LABELS[cookie.jar];
  if (state === undefined) return <Quiet title={t("response.cookies.jar.noneTitle")}>—</Quiet>;
  return (
    <span className={styles.cookieJar} data-state={cookie.jar} title={cookie.jarMessage !== "" ? cookie.jarMessage : t(state.title)}>
      {t(state.label)}
    </span>
  );
}
