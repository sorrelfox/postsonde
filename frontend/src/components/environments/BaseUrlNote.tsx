import { useT } from "../../i18n/react";
import { BASE_URL_VARIABLE } from "../../lib/baseUrl";
import { SECRET_MASK } from "../../lib/templateVariables";
import type { EnvironmentVariable } from "../../stores";
import styles from "./EnvironmentsPanel.module.css";

/** The baseUrl variable that counts among `variables`, as the scope uses it: the last enabled one, a secret one only with a value. */
function baseUrlOf(variables: readonly EnvironmentVariable[]): EnvironmentVariable | undefined {
  let found: EnvironmentVariable | undefined;
  for (const v of variables) {
    if (v.name === BASE_URL_VARIABLE && v.enabled && !(v.secret && v.value === "")) found = v;
  }
  return found;
}

function shown(v: EnvironmentVariable): string {
  return v.secret ? SECRET_MASK : (v.localValue ?? v.value);
}

export interface BaseUrlNoteProps {
  /** The variables of the environment (or the global ones) shown. */
  variables: readonly EnvironmentVariable[];
  /** The global variables it inherits; undefined for the global variables themselves, null while they load. */
  base?: readonly EnvironmentVariable[] | null;
}

/**
 * The base URL of an environment (COL-08): its baseUrl variable, where
 * requests whose URL starts with "/" are sent, the one it inherits from the
 * global variables, or how to set one. The convention is shown here because
 * baseUrl is an ordinary variable. See docs/environments.md#base-urls.
 */
export function BaseUrlNote({ variables, base }: BaseUrlNoteProps) {
  const t = useT();
  const own = baseUrlOf(variables);
  const inherited = base === undefined || base === null ? undefined : baseUrlOf(base);
  let text: string;
  if (own !== undefined) text = t("environments.baseUrl.own", { name: BASE_URL_VARIABLE, url: shown(own) });
  else if (inherited !== undefined) text = t("environments.baseUrl.inherited", { name: BASE_URL_VARIABLE, url: shown(inherited) });
  else if (base === undefined) text = t("environments.baseUrl.noneGlobal", { name: BASE_URL_VARIABLE });
  else text = t("environments.baseUrl.none", { name: BASE_URL_VARIABLE });
  return <p className={styles.note}>{text}</p>;
}
