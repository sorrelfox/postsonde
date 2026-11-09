import { useT } from "../../i18n/react";
import { BASE_URL_VARIABLE, relativeUrl } from "../../lib/baseUrl";
import { scopeText } from "../../lib/templateVariables";
import { useTemplateVariables } from "../common/TemplateVariables";
import styles from "./RelativeUrlHint.module.css";

/**
 * Under the URL of an HTTP or GraphQL request: where a relative URL
 * ("/users") is sent (COL-08), the {{baseUrl}} of the active environment
 * (or of the scopes that define it) followed by the path, and which scope
 * the base comes from; or that no scope defines baseUrl. Nothing for an
 * absolute URL, or before the request's variables are loaded.
 */
export function RelativeUrlHint({ url }: { url: string }) {
  const t = useT();
  const variables = useTemplateVariables()?.variables ?? null;
  if (variables === null) return null;
  const found = relativeUrl(url, variables);
  if (found === null) return null;
  const { base } = found;
  if (base === undefined || !base.resolved) {
    return (
      <p className={styles.hint} data-missing="true" role="note">
        {base === undefined
          ? t("editor.relativeUrl.noBase", { name: BASE_URL_VARIABLE })
          : t("editor.relativeUrl.baseUnresolved", { name: BASE_URL_VARIABLE })}
      </p>
    );
  }
  return (
    <p className={styles.hint} role="note" title={t("editor.relativeUrl.title", { name: BASE_URL_VARIABLE })}>
      <span className={styles.arrow} aria-hidden="true">
        →
      </span>
      <span className={styles.url}>{found.url}</span>
      <span className={styles.scope}>{t("editor.relativeUrl.from", { name: BASE_URL_VARIABLE, scope: scopeText(base) })}</span>
    </p>
  );
}
