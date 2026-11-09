import { forwardRef, useCallback, useEffect, useMemo, useState } from "react";
import type { EditorView } from "@codemirror/view";
import { formatErrorPath, printGraphqlResponse, type GraphqlResponse, type QueryLocation } from "../../lib/graphqlResponse";
import { useT } from "../../i18n/react";
import { IconWarning } from "../common/icons";
import { CodeEditor, type CodeEditorHandle } from "../editor/CodeEditor";
import { installGraphqlRegions, setGraphqlRegions } from "./graphqlRegions";
import styles from "./ResponseView.module.css";

export interface GraphqlBodyProps {
  graphql: GraphqlResponse;
  hideExtensions: boolean;
  /** Called with a clicked error location; the caller moves the query editor there. */
  onLocation?: (location: QueryLocation) => void;
}

/**
 * The GraphQL view of a response body (GQL-05): the errors list (message,
 * path, locations that jump to the query, extensions summary) above the
 * pretty JSON, whose `data` and `errors` lines are marked
 * (graphqlRegions.ts). With `hideExtensions`, `extensions` is left out of
 * the JSON (the Raw view keeps it).
 */
export const GraphqlBody = forwardRef<CodeEditorHandle, GraphqlBodyProps>(function GraphqlBody({ graphql, hideExtensions, onLocation }, ref) {
  const t = useT();
  const printed = useMemo(() => printGraphqlResponse(graphql, hideExtensions), [graphql, hideExtensions]);
  const [view, setView] = useState<EditorView | null>(null);
  const onReady = useCallback((v: EditorView) => {
    installGraphqlRegions(v);
    setView(v);
  }, []);
  useEffect(() => {
    view?.dispatch({ effects: setGraphqlRegions.of({ data: printed.data, errors: printed.errors }) });
  }, [view, printed]);

  const { errors } = graphql;
  let summary = t("response.graphql.errors", { count: errors.length });
  if (!graphql.hasData) summary = t("response.graphql.noData", { errors: summary });
  else if (!graphql.dataPresent) summary = t("response.graphql.nullData", { errors: summary });
  else if (errors.length > 0) summary = t("response.graphql.partialData", { errors: summary });

  return (
    <div className={styles.bodyPane} data-graphql-view="">
      <div className={styles.gqlSummary} data-has-errors={errors.length > 0 || undefined}>
        {errors.length > 0 && <IconWarning size={12} />}
        <span>{summary}</span>
      </div>
      {errors.length > 0 && (
        <ul className={styles.gqlErrors} aria-label={t("response.graphql.errorsLabel")}>
          {errors.map((error, i) => (
            <li key={i} className={styles.gqlError}>
              <span className={styles.gqlMessage}>{error.message !== "" ? error.message : t("response.graphql.noMessage")}</span>
              <span className={styles.gqlDetails}>
                {error.path !== null && error.path.length > 0 && (
                  <span className={styles.gqlPath} title={t("response.graphql.path")}>
                    {formatErrorPath(error.path)}
                  </span>
                )}
                {error.locations.map((loc) => (
                  <button
                    key={`${loc.line}:${loc.column}`}
                    type="button"
                    className={styles.gqlLocation}
                    title={t("response.graphql.locationTitle")}
                    disabled={onLocation === undefined}
                    onClick={() => onLocation?.(loc)}
                  >
                    {t("response.graphql.location", { line: loc.line, column: loc.column })}
                  </button>
                ))}
                {error.extensions !== "" && (
                  // i18n-ignore: the key of the error's object in the GraphQL response
                  <span className={styles.gqlExtensions} title="extensions">
                    {error.extensions}
                  </span>
                )}
              </span>
            </li>
          ))}
        </ul>
      )}
      <CodeEditor ref={ref} className={styles.bodyEditor} value={printed.text} readOnly
        language="json"
        ariaLabel={t("response.body.responseLabel")}
        onReady={onReady}
      />
    </div>
  );
});
