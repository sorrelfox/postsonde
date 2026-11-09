import { useEffect, useRef, useState, type ReactNode } from "react";
import { netrcCheck, netrcFile, type NetrcCheckDTO, type NetrcFileDTO } from "../../lib/auth";
import { isCancelled, toAppError } from "../../lib/errors";
import { Trans, useT } from "../../i18n/react";
import type { RequestId } from "../../stores";
import { IconInfo, IconWarning } from "../common/icons";
import styles from "./AuthEditor.module.css";

type FileState = { status: "loading" } | { status: "loaded"; file: NetrcFileDTO } | { status: "error"; message: string };

/**
 * Which netrc file a netrc auth reads (AUTH-19), under its File field: Go
 * resolves the path (the field with its variables, NETRC, or the home
 * directory's file) without reading the file. A folder's typed path is
 * shown as typed, since the requests inside resolve it.
 */
export function NetrcFileNote({ id, file }: { id: RequestId | null; file: string }) {
  const t = useT();
  const [state, setState] = useState<FileState>({ status: "loading" });
  const typedInFolder = id === null && file.trim() !== "";

  useEffect(() => {
    if (typedInFolder) return;
    let call: { cancel: () => unknown } | null = null;
    // Asked again a moment after typing stops.
    const timer = setTimeout(() => {
      const c = netrcFile(id ?? "", file);
      call = c;
      c.then(
        (f) => setState({ status: "loaded", file: f }),
        (err: unknown) => {
          if (!isCancelled(err)) setState({ status: "error", message: toAppError(err).message });
        },
      );
    }, 250);
    return () => {
      clearTimeout(timer);
      void call?.cancel();
    };
  }, [id, file, typedInFolder]);

  let text: ReactNode = t("editor.netrc.finding");
  if (typedInFolder) {
    text = <Trans k="editor.netrc.readsInFolder" values={{ file: <code>{file}</code> }} />;
  } else if (state.status === "loaded") {
    const f = state.file;
    const env = f.source === "env";
    const path = <code>{f.path}</code>;
    const fallback = <code>{f.fallback}</code>;
    if (f.fallback === "") {
      text = env ? (
        <Trans k="editor.netrc.readsEnv" values={{ path }} />
      ) : (
        <Trans k="editor.netrc.reads" values={{ path }} />
      );
    } else {
      text = env ? (
        <Trans k="editor.netrc.readsEnvFallback" values={{ path, fallback }} />
      ) : (
        <Trans k="editor.netrc.readsFallback" values={{ path, fallback }} />
      );
    }
  } else if (state.status === "error") {
    text = state.message;
  }
  const failed = !typedInFolder && state.status === "error";
  return (
    <>
      <span />
      <p className={styles.hint} data-storage={failed ? "missing" : undefined}>
        {failed ? <IconWarning size={12} /> : <IconInfo size={12} />}
        <span>{text}</span>
      </p>
    </>
  );
}

type CheckState =
  | { status: "idle" }
  | { status: "loading" }
  | { status: "loaded"; check: NetrcCheckDTO }
  | { status: "error"; message: string };

/**
 * The Check button of a netrc auth (AUTH-19): only on a click, Go reads the
 * file and names the entry the host of the request's URL matches, with its
 * login. The password never comes back; the tab says only whether the
 * entry has one.
 */
export function NetrcCheck({ id }: { id: RequestId }) {
  const t = useT();
  const [state, setState] = useState<CheckState>({ status: "idle" });
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

  const check = () => {
    setState({ status: "loading" });
    const c = netrcCheck(id);
    call.current = c;
    c.then(
      (result) => {
        if (live.current) setState({ status: "loaded", check: result });
      },
      (err: unknown) => {
        if (live.current && !isCancelled(err)) setState({ status: "error", message: toAppError(err).message });
      },
    );
  };

  return (
    <>
      <span className={styles.label}>{t("editor.netrc.entry")}</span>
      <div className={styles.jwtTools}>
        <div className={styles.jwtActions}>
          <button type="button" className={styles.action} onClick={check} disabled={state.status === "loading"}>
            {state.status === "loaded" || state.status === "error" ? t("editor.netrc.checkAgain") : t("editor.netrc.check")}
          </button>
        </div>
        {state.status === "idle" && <p className={styles.note}>{t("editor.netrc.checkNote")}</p>}
        {state.status === "error" && (
          <p className={styles.jwtError} role="alert">
            <IconWarning size={12} />
            <span>{state.message}</span>
          </p>
        )}
        {state.status === "loaded" && <CheckResult check={state.check} />}
      </div>
    </>
  );
}

function CheckResult({ check }: { check: NetrcCheckDTO }) {
  const t = useT();
  const unresolved = check.unresolved ?? [];
  if (check.urlCredentials) {
    return (
      <p className={styles.note} role="status">
        {t("editor.netrc.urlCredentials")}
      </p>
    );
  }
  const values = {
    host: <code>{check.host}</code>,
    entry:
      check.entry === "default" ? (
        <Trans k="editor.netrc.defaultEntry" values={{ default: <code>default</code> }} />
      ) : (
        <code>machine {check.entry}</code>
      ),
    line: check.line,
    path: <code>{check.path}</code>,
    login: check.login === "" ? t("editor.netrc.noLogin") : <code>{check.login}</code>,
    password: check.hasPassword ? t("editor.netrc.withPassword") : t("editor.netrc.noPassword"),
  };
  return (
    <p className={styles.note} role="status">
      {check.folderName !== "" ? (
        <Trans k="editor.netrc.matchFolder" values={{ ...values, folder: check.folderName }} />
      ) : (
        <Trans k="editor.netrc.match" values={values} />
      )}
      {check.insecure && <span className={styles.warn}> {t("editor.netrc.insecure")}</span>}
      {unresolved.length > 0 && <span className={styles.warn}> {t("editor.unresolvedVariables", { names: unresolved.join(", ") })}</span>}
    </p>
  );
}
