import { useCallback, useEffect, useId, useRef, useState } from "react";
import {
  graphQLMockUrl,
  isJsonText,
  newGraphQLMockOverride,
  toGraphQLMockConfigDTO,
  updateGraphQLMockConfig,
} from "../../commands/graphqlMock";
import { formatNumber } from "../../i18n";
import { Trans, useT } from "../../i18n/react";
import { isCancelled, toAppError } from "../../lib/errors";
import { describeGraphQLMock, pickGraphQLSchemaFile, type GraphQLMockDescriptionDTO } from "../../lib/servers";
import { useStores, type GraphQLMockConfig, type GraphQLMockMode, type GraphQLMockOverride, type LocalServer } from "../../stores";
import { FileAccessNotice, useFileAccessCheck } from "../common/FileAccessNotice";
import styles from "./GraphQLMock.module.css";
import grpcStyles from "./GrpcMock.module.css";
import wsStyles from "./WsServer.module.css";

/** How long edits must pause before they are sent (and the schema described), in ms. */
export const GRAPHQL_MOCK_CONFIG_DELAY_MS = 400;

const DEFAULT_CONFIG: GraphQLMockConfig = {
  path: "/graphql",
  schemaFile: "",
  schemaText: "",
  introspection: true,
  listLength: 2,
  nullPercent: 0,
  seed: 0,
  allowOrigin: "",
  overrides: [],
};

/**
 * The drafts not yet confirmed by Go, by server: the Schema and Responses
 * tabs are separate panes, and one that opens while the other's last edit
 * is on its way starts from that edit, not from the older server config.
 */
const unconfirmed = new Map<string, GraphQLMockConfig>();

function errorText(err: unknown): string {
  const e = toAppError(err);
  return e.hint ? `${e.message} ${e.hint}` : e.message;
}

/** A whole number from min to max typed in a field, or null. */
function wholeNumber(text: string, min: number, max: number): number | null {
  const n = Number(text.trim() === "" ? "0" : text);
  return Number.isInteger(n) && n >= min && n <= max ? n : null;
}

/** Which part of the editor a pane shows. */
export type GraphQLMockView = "schema" | "responses" | "all";

/**
 * The editor of a GraphQL mock server (SSRV-08). The schema part: the
 * endpoint path, where the schema comes from (a file chosen with the
 * native dialog, stored like a GraphQL request's schema file, or SDL or
 * introspection JSON pasted in), what Go loaded from it (types, root
 * fields, problems), whether introspection is answered and the CORS
 * origin. The responses part: how data is generated (list length, share
 * of nulls, seed) and the overrides, first match wins: a field
 * ("Type.field", with the arguments to match) or a scalar or enum type,
 * answered with JSON or with a field error. Edits reach Go once typing
 * pauses; a running server loads the schema again and answers with them at
 * once. One draft serves both parts, so the folder tab shows them together.
 */
export function GraphQLMockPanel({ server, view = "all", readOnly = false }: { server: LocalServer; view?: GraphQLMockView; readOnly?: boolean }) {
  const t = useT();
  const stores = useStores();
  const baseId = useId();
  const [draft, setDraft] = useState<GraphQLMockConfig>(() => unconfirmed.get(server.id) ?? server.graphqlMock ?? DEFAULT_CONFIG);
  const [problem, setProblem] = useState<string | null>(null);
  const [status, setStatus] = useState<string | null>(null);
  const [described, setDescribed] = useState<GraphQLMockDescriptionDTO | null>(null);
  const [describeError, setDescribeError] = useState<string | null>(null);
  const [reloads, setReloads] = useState(0);
  const [numbers, setNumbers] = useState(() => ({
    listLength: String(draft.listLength),
    nullPercent: String(draft.nullPercent),
    seed: String(draft.seed),
  }));
  // A schema file outside the allowed directories is refused (PRV-10).
  const access = useFileAccessCheck();

  const pending = useRef<GraphQLMockConfig | null>(null);
  const seq = useRef(0);
  const flush = useCallback(() => {
    const config = pending.current;
    if (config === null) return;
    pending.current = null;
    const mine = ++seq.current;
    updateGraphQLMockConfig(stores, server.id, config).then(
      () => {
        if (unconfirmed.get(server.id) === config) unconfirmed.delete(server.id);
        if (mine === seq.current) setProblem(null);
      },
      (err: unknown) => {
        if (mine === seq.current) setProblem(errorText(err));
      },
    );
  }, [stores, server.id]);

  const edit = useCallback(
    (next: GraphQLMockConfig) => {
      setDraft(next);
      pending.current = next;
      unconfirmed.set(server.id, next);
    },
    [server.id],
  );

  useEffect(() => {
    if (pending.current === null) return;
    const timer = setTimeout(flush, GRAPHQL_MOCK_CONFIG_DELAY_MS);
    return () => clearTimeout(timer);
  }, [draft, flush]);
  // Edits still waiting are sent when the pane closes.
  useEffect(() => flush, [flush]);

  // What Go loads from the schema, and which overrides do not fit it.
  const describeKey = JSON.stringify([draft.schemaFile, draft.schemaText, draft.overrides, reloads]);
  useEffect(() => {
    let call: ReturnType<typeof describeGraphQLMock> | null = null;
    const timer = setTimeout(() => {
      call = describeGraphQLMock(toGraphQLMockConfigDTO(draft));
      call.then(
        (result) => {
          setDescribed(result);
          setDescribeError(null);
        },
        (err: unknown) => {
          if (isCancelled(err)) return;
          setDescribed(null);
          setDescribeError(errorText(err));
        },
      );
    }, GRAPHQL_MOCK_CONFIG_DELAY_MS);
    return () => {
      clearTimeout(timer);
      void call?.cancel?.();
    };
    // describeKey stands for the parts of the draft that change the description.
  }, [describeKey]);

  const chooseFile = async () => {
    setStatus(null);
    try {
      const picked = await pickGraphQLSchemaFile();
      if (picked === "") return;
      edit({ ...draft, schemaFile: picked });
      access.check(picked);
    } catch (err) {
      if (!isCancelled(err)) setStatus(errorText(err));
    }
  };

  const setNumber = (key: "listLength" | "nullPercent" | "seed", text: string) => {
    setNumbers((n) => ({ ...n, [key]: text }));
    const n =
      key === "listLength" ? wholeNumber(text, 0, 100) : key === "nullPercent" ? wholeNumber(text, 0, 100) : wholeNumber(text, -(2 ** 31), 2 ** 31 - 1);
    if (n !== null) edit({ ...draft, [key]: n });
  };
  const numberInvalid = (key: "listLength" | "nullPercent" | "seed") =>
    (key === "seed" ? wholeNumber(numbers.seed, -(2 ** 31), 2 ** 31 - 1) : wholeNumber(numbers[key], 0, 100)) === null;

  const setOverride = (i: number, patch: Partial<GraphQLMockOverride>) =>
    edit({ ...draft, overrides: draft.overrides.map((o, j) => (j === i ? { ...o, ...patch } : o)) });
  const moveOverride = (i: number, by: -1 | 1) => {
    const to = i + by;
    if (to < 0 || to >= draft.overrides.length) return;
    const overrides = [...draft.overrides];
    [overrides[i], overrides[to]] = [overrides[to], overrides[i]];
    edit({ ...draft, overrides });
  };

  const url = graphQLMockUrl(server);
  const usesFile = draft.schemaFile !== "";
  const schemaProblems = (described?.problems ?? []).filter((p) => p.overrideIndex < 0);
  const overrideProblems = new Map<number, string>();
  for (const p of described?.problems ?? []) {
    if (p.overrideIndex >= 0 && !overrideProblems.has(p.overrideIndex)) overrideProblems.set(p.overrideIndex, p.message);
  }
  const targets = described?.targets ?? [];
  const showSchema = view !== "responses";
  const showResponses = view !== "schema";

  return (
    <div className={wsStyles.settings}>
      <p className={wsStyles.connectNote}>
        {url === null ? (
          t("servers.graphql.startToQuery")
        ) : draft.introspection ? (
          <Trans k="servers.graphql.queryOrIntrospect" values={{ url: <code className={wsStyles.url}>{url}</code> }} />
        ) : (
          <Trans k="servers.graphql.query" values={{ url: <code className={wsStyles.url}>{url}</code> }} />
        )}
      </p>

      {showSchema && (
        <>
          <h3 className={wsStyles.sectionTitle}>{t("servers.graphql.schema")}</h3>
          <div className={wsStyles.settingRow} role="radiogroup" aria-label={t("servers.graphql.source")}>
            <label className={wsStyles.check}>
              <input
                type="radio"
                name={`${baseId}-source`}
                checked={usesFile}
                disabled={readOnly}
                onChange={() => void chooseFile()}
              />
              {t("servers.graphql.source.file")}
            </label>
            <label className={wsStyles.check}>
              <input
                type="radio"
                name={`${baseId}-source`}
                checked={!usesFile}
                disabled={readOnly}
                onChange={() => edit({ ...draft, schemaFile: "" })}
              />
              {t("servers.graphql.source.text")}
            </label>
          </div>
          {usesFile ? (
            <div className={grpcStyles.pathRow}>
              <span className={grpcStyles.path} title={draft.schemaFile}>
                {draft.schemaFile}
              </span>
              <button type="button" className={wsStyles.button} disabled={readOnly} onClick={() => void chooseFile()}>
                {t("servers.graphql.chooseFile")}
              </button>
              <button
                type="button"
                className={wsStyles.button}
                title={t("servers.graphql.reloadTitle")}
                onClick={() => {
                  setReloads((n) => n + 1);
                  // A running server reads the file again when it is reconfigured.
                  edit({ ...draft });
                }}
              >
                {t("servers.graphql.reload")}
              </button>
            </div>
          ) : (
            <textarea
              className={styles.schemaText}
              aria-label={t("servers.graphql.schemaText")}
              value={draft.schemaText}
              placeholder={t("servers.graphql.schemaTextPlaceholder")}
              spellCheck={false}
              readOnly={readOnly}
              rows={12}
              onChange={(e) => edit({ ...draft, schemaText: e.target.value })}
            />
          )}
          <FileAccessNotice access={access} />
          {status !== null && (
            <p className={wsStyles.error} role="alert">
              {status}
            </p>
          )}
          {describeError !== null ? (
            <p className={wsStyles.error} role="alert">
              {describeError}
            </p>
          ) : described !== null && described.source !== "" ? (
            <p className={wsStyles.help} aria-live="polite">
              {t(described.format === "introspection" ? "servers.graphql.loadedIntrospection" : "servers.graphql.loadedSdl", {
                types: formatNumber(described.types),
                query: formatNumber(described.queryFields),
                mutation: formatNumber(described.mutationFields),
              })}
            </p>
          ) : described !== null ? (
            <p className={grpcStyles.empty}>{t("servers.graphql.noSchema")}</p>
          ) : null}
          {schemaProblems.length > 0 && (
            <ul className={grpcStyles.diagnostics} aria-label={t("servers.graphql.schemaProblems")}>
              {schemaProblems.slice(0, 10).map((p, i) => (
                <li key={i}>{p.message}</li>
              ))}
            </ul>
          )}
          <div className={wsStyles.settingRow}>
            <label className={wsStyles.settingLabel} htmlFor={`${baseId}-path`}>
              {t("servers.graphql.path")}
            </label>
            <input
              id={`${baseId}-path`}
              className={`${wsStyles.input} ${wsStyles.pathInput}`}
              value={draft.path}
              placeholder="/graphql"
              spellCheck={false}
              autoComplete="off"
              readOnly={readOnly}
              onChange={(e) => edit({ ...draft, path: e.target.value })}
            />
          </div>
          <label className={wsStyles.check}>
            <input
              type="checkbox"
              checked={draft.introspection}
              disabled={readOnly}
              onChange={(e) => edit({ ...draft, introspection: e.target.checked })}
            />
            {t("servers.graphql.introspection")}
          </label>
          <div className={wsStyles.settingRow}>
            <label className={wsStyles.settingLabel} htmlFor={`${baseId}-origin`}>
              {t("servers.graphql.allowOrigin")}
            </label>
            <input
              id={`${baseId}-origin`}
              className={`${wsStyles.input} ${wsStyles.pathInput}`}
              value={draft.allowOrigin}
              placeholder={t("servers.graphql.allowOriginPlaceholder")}
              spellCheck={false}
              autoComplete="off"
              readOnly={readOnly}
              onChange={(e) => edit({ ...draft, allowOrigin: e.target.value })}
            />
          </div>
        </>
      )}

      {showResponses && (
        <>
          <h3 className={wsStyles.sectionTitle}>{t("servers.graphql.generated")}</h3>
          <div className={wsStyles.settingRow}>
            <label className={wsStyles.settingLabel} htmlFor={`${baseId}-list`}>
              {t("servers.graphql.listLength")}
            </label>
            <input
              id={`${baseId}-list`}
              className={`${wsStyles.input} ${wsStyles.sizeInput}`}
              inputMode="numeric"
              value={numbers.listLength}
              aria-invalid={numberInvalid("listLength") || undefined}
              readOnly={readOnly}
              onChange={(e) => setNumber("listLength", e.target.value)}
            />
            <label className={wsStyles.settingLabel} htmlFor={`${baseId}-null`}>
              {t("servers.graphql.nullPercent")}
            </label>
            <input
              id={`${baseId}-null`}
              className={`${wsStyles.input} ${wsStyles.sizeInput}`}
              inputMode="numeric"
              value={numbers.nullPercent}
              aria-invalid={numberInvalid("nullPercent") || undefined}
              readOnly={readOnly}
              onChange={(e) => setNumber("nullPercent", e.target.value)}
            />
            <label className={wsStyles.settingLabel} htmlFor={`${baseId}-seed`}>
              {t("servers.graphql.seed")}
            </label>
            <input
              id={`${baseId}-seed`}
              className={`${wsStyles.input} ${wsStyles.sizeInput}`}
              inputMode="numeric"
              value={numbers.seed}
              aria-invalid={numberInvalid("seed") || undefined}
              readOnly={readOnly}
              onChange={(e) => setNumber("seed", e.target.value)}
            />
          </div>
          <p className={wsStyles.help}>{t("servers.graphql.generatedHelp")}</p>

          <h3 className={wsStyles.sectionTitle}>{t("servers.graphql.overrides")}</h3>
          <datalist id={`${baseId}-targets`}>
            {targets.map((tg) => (
              <option key={tg.target} value={tg.target}>
                {tg.type}
              </option>
            ))}
          </datalist>
          {draft.overrides.length === 0 ? (
            <p className={grpcStyles.empty}>{t("servers.graphql.noOverrides")}</p>
          ) : (
            <ol className={wsStyles.rules} aria-label={t("servers.graphql.overrides")}>
              {draft.overrides.map((o, i) => (
                <OverrideRow
                  key={i}
                  index={i}
                  override={o}
                  count={draft.overrides.length}
                  targetsId={`${baseId}-targets`}
                  targetType={targets.find((tg) => tg.target === o.target)?.type ?? null}
                  problem={overrideProblems.get(i) ?? null}
                  readOnly={readOnly}
                  onChange={(patch) => setOverride(i, patch)}
                  onMove={(by) => moveOverride(i, by)}
                  onRemove={() => edit({ ...draft, overrides: draft.overrides.filter((_, j) => j !== i) })}
                />
              ))}
            </ol>
          )}
          <div className={wsStyles.settingRow}>
            <button
              type="button"
              className={wsStyles.button}
              disabled={readOnly}
              onClick={() => edit({ ...draft, overrides: [...draft.overrides, newGraphQLMockOverride(targets.find((tg) => tg.root)?.target ?? "")] })}
            >
              {t("servers.graphql.addOverride")}
            </button>
          </div>
          <p className={wsStyles.help}>{t("servers.graphql.overridesHelp")}</p>
        </>
      )}

      {problem !== null && (
        <p className={wsStyles.error} role="alert">
          {problem}
        </p>
      )}
    </div>
  );
}

/** One override: its target, arguments to match, and value or error. */
function OverrideRow({
  index,
  override: o,
  count,
  targetsId,
  targetType,
  problem,
  readOnly,
  onChange,
  onMove,
  onRemove,
}: {
  index: number;
  override: GraphQLMockOverride;
  count: number;
  targetsId: string;
  targetType: string | null;
  problem: string | null;
  readOnly: boolean;
  onChange: (patch: Partial<GraphQLMockOverride>) => void;
  onMove: (by: -1 | 1) => void;
  onRemove: () => void;
}) {
  const t = useT();
  const id = useId();
  const n = index + 1;
  const isField = o.target.includes(".");
  const valueInvalid = o.mode === "value" && !isJsonText(o.value);
  const argsInvalid = o.args.trim() !== "" && !isJsonText(o.args);
  return (
    <li className={styles.override} data-disabled={!o.enabled || undefined}>
      <div className={wsStyles.ruleLine}>
        <span className={wsStyles.ruleNumber}>{formatNumber(n)}</span>
        <input
          type="checkbox"
          checked={o.enabled}
          disabled={readOnly}
          aria-label={t("servers.graphql.overrideEnabled", { n })}
          onChange={(e) => onChange({ enabled: e.target.checked })}
        />
        <input
          className={`${wsStyles.input} ${styles.target}`}
          list={targetsId}
          value={o.target}
          placeholder={t("servers.graphql.targetPlaceholder")}
          aria-label={t("servers.graphql.overrideTarget", { n })}
          spellCheck={false}
          autoComplete="off"
          readOnly={readOnly}
          onChange={(e) => onChange({ target: e.target.value })}
        />
        {targetType !== null && <span className={grpcStyles.kind}>{targetType}</span>}
        <select
          className={wsStyles.input}
          value={o.mode}
          disabled={readOnly || !isField}
          aria-label={t("servers.graphql.overrideMode", { n })}
          onChange={(e) => onChange({ mode: e.target.value as GraphQLMockMode })}
        >
          <option value="value">{t("servers.graphql.mode.value")}</option>
          <option value="error">{t("servers.graphql.mode.error")}</option>
        </select>
        <button
          type="button"
          className={wsStyles.iconButton}
          disabled={readOnly || index === 0}
          aria-label={t("servers.graphql.moveUp", { n })}
          onClick={() => onMove(-1)}
        >
          ↑
        </button>
        <button
          type="button"
          className={wsStyles.iconButton}
          disabled={readOnly || index === count - 1}
          aria-label={t("servers.graphql.moveDown", { n })}
          onClick={() => onMove(1)}
        >
          ↓
        </button>
        <button
          type="button"
          className={`${wsStyles.button} ${wsStyles.danger}`}
          disabled={readOnly}
          aria-label={t("servers.graphql.removeOverride", { n })}
          onClick={onRemove}
        >
          {t("servers.graphql.remove")}
        </button>
      </div>
      {isField && (
        <div className={wsStyles.settingRow}>
          <label className={wsStyles.settingLabel} htmlFor={`${id}-args`}>
            {t("servers.graphql.args")}
          </label>
          <input
            id={`${id}-args`}
            className={`${wsStyles.input} ${wsStyles.pathInput}`}
            value={o.args}
            placeholder={t("servers.graphql.argsPlaceholder")}
            aria-invalid={argsInvalid || undefined}
            spellCheck={false}
            autoComplete="off"
            readOnly={readOnly}
            onChange={(e) => onChange({ args: e.target.value })}
          />
        </div>
      )}
      {o.mode === "value" ? (
        <textarea
          className={styles.value}
          aria-label={t("servers.graphql.overrideValue", { n })}
          value={o.value}
          placeholder={t("servers.graphql.valuePlaceholder")}
          aria-invalid={valueInvalid || undefined}
          spellCheck={false}
          readOnly={readOnly}
          rows={Math.min(10, Math.max(2, o.value.split("\n").length))}
          onChange={(e) => onChange({ value: e.target.value })}
        />
      ) : (
        <div className={wsStyles.settingRow}>
          <input
            className={`${wsStyles.input} ${wsStyles.pathInput}`}
            value={o.error}
            placeholder={t("servers.graphql.errorPlaceholder")}
            aria-label={t("servers.graphql.overrideError", { n })}
            readOnly={readOnly}
            onChange={(e) => onChange({ error: e.target.value })}
          />
          <input
            className={`${wsStyles.input} ${styles.code}`}
            value={o.code}
            placeholder={t("servers.graphql.codePlaceholder")}
            aria-label={t("servers.graphql.overrideCode", { n })}
            spellCheck={false}
            readOnly={readOnly}
            onChange={(e) => onChange({ code: e.target.value })}
          />
        </div>
      )}
      {valueInvalid && <p className={wsStyles.error}>{t("servers.graphql.invalidJson")}</p>}
      {problem !== null && <p className={wsStyles.error}>{problem}</p>}
    </li>
  );
}
