import { useCallback, type ReactNode } from "react";
import { commitSplitRatio } from "../../commands/settings";
import { MAX_SPLIT_RATIO, MIN_SPLIT_RATIO, useSettings, useStores } from "../../stores";
import { EmptyState } from "../common/EmptyState";
import { IconSignal } from "../common/icons";
import { SplitPane } from "../common/SplitPane";
import { TokenPreview } from "./TokenPreview";
import { useT } from "../../i18n/react";
import styles from "./WorkArea.module.css";

/** Neither pane gets narrower than this, beside each other, while there is room for both. */
export const MIN_PANE_WIDTH = 240;
/** Neither pane gets lower than this, one below the other, while there is room for both. */
export const MIN_PANE_HEIGHT = 140;

export interface WorkAreaProps {
  /** Request pane content. Default: the design-token preview. */
  request?: ReactNode;
  /** Response pane content. Default: an empty state. */
  response?: ReactNode;
}

/**
 * The request and response panes with a draggable splitter: side by
 * side, or the response below the request, as settings.responsePosition
 * says (UI-02). Each pane is a flex column with overflow hidden: slot
 * content should fill it (flex: 1; min-height: 0) and scroll itself. Each
 * layout has its own split, settings.splitRatio beside and
 * settings.splitRatioBelow below, saved when a drag or key press commits it.
 * Switching the layout keeps the panes mounted, so editors keep their state.
 */
export function WorkArea({ request, response }: WorkAreaProps) {
  const t = useT();
  const stores = useStores();
  const position = useSettings((s) => s.responsePosition);
  const below = position === "below";
  const ratio = useSettings((s) => (below ? s.splitRatioBelow : s.splitRatio));
  const commit = useCallback((next: number) => commitSplitRatio(stores, next, position), [stores, position]);
  const minSize = below ? MIN_PANE_HEIGHT : MIN_PANE_WIDTH;

  return (
    <SplitPane
      className={styles.workArea}
      orientation={below ? "vertical" : "horizontal"}
      ratio={ratio}
      minRatio={MIN_SPLIT_RATIO}
      maxRatio={MAX_SPLIT_RATIO}
      minStart={minSize}
      minEnd={minSize}
      onRatioCommit={commit}
      label={t("shell.workArea.resize")}
    >
      <section className={styles.pane} aria-label={t("shell.workArea.request")} data-slot="request">
        {request ?? <TokenPreview />}
      </section>
      <section className={styles.pane} aria-label={t("shell.workArea.response")} data-slot="response">
        {response ?? (
          <EmptyState icon={<IconSignal size={40} strokeWidth={1.4} />} title={t("shell.workArea.noResponse")}>
            {t("shell.workArea.noResponseBody")}
          </EmptyState>
        )}
      </section>
    </SplitPane>
  );
}
