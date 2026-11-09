import { useEffect, useId, useMemo, useRef, useState } from "react";
import {
  EMPTY_MOCK_CONFIG,
  applyMockRoute,
  exampleOfSource,
  previewMockRoute,
  type MockSource,
} from "../../commands/mockFromExample";
import type { MessageKey } from "../../i18n";
import { useT } from "../../i18n/react";
import { toAppError } from "../../lib/errors";
import type { MockExampleMode, MockExampleResultDTO } from "../../lib/mockFromExample";
import { MOCK_HTTP_TYPE } from "../../lib/servers";
import type { ResponseExampleDTO } from "../../lib/responseHistory";
import { shallowEqual, useCollection, useServers, useStores, type LocalServer, type MockConfig } from "../../stores";
import { Dialog } from "../common/Dialog";
import { IconInfo, IconWarning } from "../common/icons";
import { formatSize } from "../messagelog/format";
import { methodBadge } from "../tree/badges";
import { toMockConfig } from "../../commands/servers";
import styles from "./OpenApiRoutes.module.css";
import own from "./MockFromExample.module.css";

/** How long the path must stay unchanged before the preview is made again, in ms. */
export const PATH_DELAY_MS = 300;

/** The server select's value for a new mock server. */
const NEW_SERVER = "";

/** Why a header field is left out (Go's mockexample reasons). */
const DROP_REASONS: Readonly<Record<string, MessageKey>> = {
  hop_by_hop: "servers.fromExample.reason.hopByHop",
  content_length: "servers.fromExample.reason.contentLength",
  content_encoding: "servers.fromExample.reason.contentEncoding",
  date: "servers.fromExample.reason.date",
  invalid: "servers.fromExample.reason.invalid",
};

export interface MockFromExampleDialogProps {
  /** The response to mock; null: closed. */
  source: MockSource | null;
  /** The request's method and URL as typed: they give the route's method and path. */
  method: string;
  url: string;
  onClose: () => void;
}

/**
 * "Create mock route" (MOCK-03, docs/mock-routes.md#routes-from-a-response):
 * the route Go makes of a response (ServersService.MockRouteFromExample,
 * secrets masked again), for the mock server chosen or a new one, with
 * the path (editable), what is sent (status, headers, body) and what is
 * left out and why. When a route of the server already answers the method
 * and path, the response is added to it as a named response or replaces
 * its response. Nothing changes until the confirm button.
 */
export function MockFromExampleDialog(props: MockFromExampleDialogProps) {
  if (props.source === null) return null;
  return <Preview {...props} source={props.source} />;
}

/** The mock servers a route can go to (commands/mockFromExample.ts mockServerChoices, as a hook). */
function useMockServers(): LocalServer[] {
  const workspaceId = useCollection((s) => s.workspaceId);
  const all = useServers((s) => s.ids.map((id) => s.byId[id]), shallowEqual);
  return useMemo(
    () =>
      all.filter(
        (x): x is LocalServer =>
          x !== undefined && x.type === MOCK_HTTP_TYPE && (x.folder === null || x.folder.workspaceId === workspaceId),
      ),
    [all, workspaceId],
  );
}

function Preview({ source, method, url, onClose }: MockFromExampleDialogProps & { source: MockSource }) {
  const t = useT();
  const stores = useStores();
  const baseId = useId();
  const servers = useMockServers();
  const [example, setExample] = useState<ResponseExampleDTO | null>(null);
  const [serverId, setServerId] = useState<string>(() => {
    const running = servers.find((s) => s.state === "running");
    return (running ?? servers[0])?.id ?? NEW_SERVER;
  });
  const serverConfig = useServers((s) => (serverId === NEW_SERVER ? null : (s.mockDrafts[serverId] ?? s.byId[serverId]?.mock ?? null)));
  const config: MockConfig = serverConfig ?? EMPTY_MOCK_CONFIG;
  const [mode, setMode] = useState<MockExampleMode>("route");
  // Set once the user picks a mode: then it is not changed for them.
  const modeChosen = useRef(false);
  const choose = (next: MockExampleMode) => {
    modeChosen.current = true;
    setMode(next);
  };
  // The path typed (null: untouched, so Go makes it from the URL) and the one the preview was asked with.
  const [path, setPath] = useState<string | null>(null);
  const [sentPath, setSentPath] = useState<string | null>(null);
  const [name, setName] = useState("");
  const [result, setResult] = useState<MockExampleResultDTO | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [applying, setApplying] = useState(false);

  // The response, as an example: Go makes one of a history entry (masked).
  useEffect(() => {
    let live = true;
    exampleOfSource(source).then(
      (ex) => {
        if (live) setExample(ex);
      },
      (err: unknown) => {
        if (!live) return;
        const e = toAppError(err);
        setError(e.hint ? `${e.message} ${e.hint}` : e.message);
        setLoading(false);
      },
    );
    return () => {
      live = false;
    };
  }, [source]);

  // A typed path is sent once typing pauses.
  useEffect(() => {
    if (path === null || path === sentPath) return;
    const timer = setTimeout(() => setSentPath(path), PATH_DELAY_MS);
    return () => clearTimeout(timer);
  }, [path, sentPath]);

  useEffect(() => {
    if (example === null) return;
    setLoading(true);
    const call = previewMockRoute({ example, method, url, path: sentPath, mode, name, config });
    let live = true;
    call.then(
      (res) => {
        if (!live) return;
        setResult(res);
        setError(null);
        setLoading(false);
        // A route already answers: add the response to it, unless the user chose.
        if (res.existing !== null && mode === "route" && !modeChosen.current) setMode("response");
      },
      (err: unknown) => {
        if (!live) return;
        const e = toAppError(err);
        setError(e.hint ? `${e.message} ${e.hint}` : e.message);
        setResult(null);
        setLoading(false);
      },
    );
    return () => {
      live = false;
      call.cancel();
    };
  }, [example, method, url, sentPath, mode, name, config]);

  const pending = loading || (path !== null && path !== sentPath);
  const status = result?.status ?? "";
  const canApply = result !== null && !pending && !applying && (status === "added" || status === "response_added" || status === "replaced");
  const serverName = servers.find((s) => s.id === serverId)?.name ?? t("servers.fromExample.newServerName");

  const apply = () => {
    if (!canApply || result === null) return;
    const next = toMockConfig(result.config);
    if (next === null) return;
    setApplying(true);
    applyMockRoute(stores, serverId === NEW_SERVER ? null : serverId, result, next).then(
      () => onClose(),
      (err: unknown) => {
        setApplying(false);
        setError(toAppError(err).message);
      },
    );
  };

  const confirmLabel =
    status === "response_added"
      ? t("servers.fromExample.addResponse")
      : status === "replaced"
        ? t("servers.fromExample.replace")
        : serverId === NEW_SERVER
          ? t("servers.fromExample.createServer")
          : t("servers.fromExample.addRoute");

  const response = result?.response ?? null;
  const badge = methodBadge(result?.method ?? method);
  const notes = result?.notes ?? [];
  const dropped = result?.dropped ?? [];
  const diagnostics = result?.diagnostics ?? [];

  return (
    <Dialog
      open
      title={t("servers.fromExample.title")}
      width={680}
      confirmLabel={confirmLabel}
      confirmDisabled={!canApply}
      busy={applying}
      onCancel={onClose}
      onConfirm={apply}
    >
      <div className={styles.body} data-testid="mock-from-example">
        <div className={styles.row}>
          <label className={styles.label} htmlFor={`${baseId}-server`}>
            {t("servers.fromExample.server")}
          </label>
          <select
            id={`${baseId}-server`}
            className={own.select}
            value={serverId}
            onChange={(e) => {
              setServerId(e.target.value);
              setMode("route");
              modeChosen.current = false;
            }}
          >
            {servers.map((s) => (
              <option key={s.id} value={s.id}>
                {s.state === "running" && s.boundPort > 0
                  ? t("servers.fromExample.serverRunning", { name: s.name, port: s.boundPort })
                  : t("servers.fromExample.serverStopped", { name: s.name })}
              </option>
            ))}
            <option value={NEW_SERVER}>{t("servers.fromExample.newServer")}</option>
          </select>
        </div>

        <div className={styles.row}>
          <label className={styles.label} htmlFor={`${baseId}-path`}>
            {t("servers.fromExample.path")}
          </label>
          <span className={styles.badge} data-tone={badge.tone}>
            {badge.label}
          </span>
          <input
            id={`${baseId}-path`}
            className={styles.input}
            spellCheck={false}
            value={path ?? result?.path ?? ""}
            onChange={(e) => setPath(e.target.value)}
          />
          {result !== null && path !== null && path !== result.derivedPath && (
            <button type="button" className={styles.link} onClick={() => setPath(result.derivedPath)}>
              {t("servers.fromExample.useDerived", { path: result.derivedPath })}
            </button>
          )}
        </div>
        <p className={styles.hint}>{t("servers.fromExample.pathHint")}</p>

        {result?.existing != null && (
          <div className={styles.modes} role="radiogroup" aria-label={t("servers.fromExample.existing")}>
            <p className={styles.summary}>
              {t("servers.fromExample.existingNote", { route: `${result.existing.method} ${result.existing.path}` })}
            </p>
            <label className={styles.mode}>
              <input type="radio" name={`${baseId}-mode`} checked={mode === "response"} onChange={() => choose("response")} />
              {t("servers.fromExample.asResponse")}
            </label>
            {mode === "response" && (
              <div className={styles.row}>
                <label className={styles.label} htmlFor={`${baseId}-name`}>
                  {t("servers.fromExample.responseName")}
                </label>
                <input
                  id={`${baseId}-name`}
                  className={styles.input}
                  value={name}
                  placeholder={example?.name ?? ""}
                  onChange={(e) => setName(e.target.value)}
                />
              </div>
            )}
            {mode === "response" && result.responseName !== "" && (
              <p className={styles.hint}>{t("servers.fromExample.selectHint", { name: result.responseName })}</p>
            )}
            <label className={styles.mode}>
              <input type="radio" name={`${baseId}-mode`} checked={mode === "replace"} onChange={() => choose("replace")} />
              {t("servers.fromExample.replaceResponse")}
            </label>
            <label className={styles.mode}>
              <input type="radio" name={`${baseId}-mode`} checked={mode === "route"} onChange={() => choose("route")} />
              {t("servers.fromExample.keep")}
            </label>
          </div>
        )}

        {loading && result === null && error === null && <p className={styles.hint}>{t("servers.fromExample.loading")}</p>}
        {error !== null && (
          <p className={styles.error} role="alert">
            {error}
          </p>
        )}

        {result !== null && response !== null && (
          <>
            <p className={styles.summary} aria-live="polite">
              {summary(t, result, serverName)}
            </p>
            <section className={own.section} aria-labelledby={`${baseId}-sent`}>
              <h3 className={own.heading} id={`${baseId}-sent`}>
                {t("servers.fromExample.sent", { status: response.status })}
              </h3>
              {(response.headers ?? []).length > 0 && (
                <table className={own.table}>
                  <caption className={own.caption}>{t("servers.fromExample.headers")}</caption>
                  <thead>
                    <tr>
                      <th scope="col">{t("servers.fromExample.headerName")}</th>
                      <th scope="col">{t("servers.fromExample.headerValue")}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {(response.headers ?? []).map((h, i) => (
                      <tr key={`${h.name}-${i}`}>
                        <td>{h.name}</td>
                        <td>{h.value}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
              <p className={styles.hint}>{bodyLine(t, response.body)}</p>
            </section>
            {dropped.length > 0 && (
              <section className={own.section} aria-labelledby={`${baseId}-dropped`}>
                <h3 className={own.heading} id={`${baseId}-dropped`}>
                  {t("servers.fromExample.dropped")}
                </h3>
                <ul className={styles.diagnostics}>
                  {dropped.map((d, i) => (
                    <li key={`${d.name}-${i}`} className={styles.diagnostic} data-severity="info">
                      <IconInfo size={12} />
                      <span>
                        <span className={own.field}>{d.name}</span>{" "}
                        {t(DROP_REASONS[d.reason] ?? "servers.fromExample.reason.invalid")}
                      </span>
                    </li>
                  ))}
                </ul>
              </section>
            )}
            {(notes.length > 0 || diagnostics.length > 0) && (
              <ul className={styles.diagnostics} aria-label={t("servers.fromExample.notes")}>
                {diagnostics.map((d, i) => (
                  <li key={`d-${i}`} className={styles.diagnostic} data-severity="error">
                    <IconWarning size={12} />
                    <span>{d.message}</span>
                  </li>
                ))}
                {notes.map((n, i) => (
                  <li key={`n-${i}`} className={styles.diagnostic} data-severity={n.severity}>
                    {n.severity === "info" ? <IconInfo size={12} /> : <IconWarning size={12} />}
                    <span>{n.message}</span>
                  </li>
                ))}
              </ul>
            )}
          </>
        )}
      </div>
    </Dialog>
  );
}

type Translate = ReturnType<typeof useT>;

function summary(t: Translate, r: MockExampleResultDTO, server: string): string {
  const route = `${r.method} ${r.path}`;
  switch (r.status) {
    case "added":
      return t("servers.fromExample.summary.added", { route, server });
    case "response_added":
      return t("servers.fromExample.summary.responseAdded", { route, name: r.responseName });
    case "replaced":
      return t("servers.fromExample.summary.replaced", { route });
    case "existing":
      return t("servers.fromExample.summary.existing", { route });
    default:
      return t("servers.fromExample.summary.overLimit");
  }
}

function bodyLine(t: Translate, body: { mode: string; type: string; text: string }): string {
  if (body.mode !== "raw") return t("servers.fromExample.noBody");
  if (body.type === "base64") {
    return t("servers.fromExample.binaryBody", { size: formatSize(Math.floor((body.text.replace(/\s/g, "").length * 3) / 4)) });
  }
  return t("servers.fromExample.textBody", { type: body.type.toUpperCase(), size: formatSize(new TextEncoder().encode(body.text).length) });
}
