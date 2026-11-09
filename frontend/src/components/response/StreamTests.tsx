import { useCallback } from "react";
import { jumpToAssertion, TESTS_SUB_TAB } from "../../commands/assertionJump";
import { checkStreamTests, useStreamTests } from "../../commands/streamTests";
import { summarizeTests, type TestSummary } from "../../lib/assertions";
import { useT } from "../../i18n/react";
import { useStores, type RequestId } from "../../stores";
import { IconWarning } from "../common/icons";
import { ResponseTests } from "./ResponseTests";
import styles from "./ResponseTests.module.css";

/**
 * The results of a stream request's assertions (TEST-08): those Go sent
 * when the latest gRPC call, WebSocket or Socket.IO connection or SSE
 * stream ended, or those of "Check now", which checks the assertions as
 * they are now against the messages so far (while the stream runs, or
 * after it ended). Results jump to their row in the Tests sub-tab, as an
 * HTTP response's do.
 */
export function StreamTests({ id }: { id: RequestId }) {
  const t = useT();
  const stores = useStores();
  const state = useStreamTests(id);
  const onJump = useCallback(
    (index: number | null) => {
      if (index === null) stores.drafts.dispatch({ type: "drafts/setSubTab", id, subTab: TESTS_SUB_TAB });
      else jumpToAssertion(stores, id, index);
    },
    [stores, id],
  );
  let note = t("response.streamTests.waiting");
  if (state.ref === null && state.tests === null) note = t("response.streamTests.notRun");
  else if (state.tests !== null) note = state.final ? t("response.streamTests.final") : t("response.streamTests.onDemand");
  return (
    <div className={styles.stream} data-stream-tests={id}>
      <div className={styles.streamBar}>
        <span className={styles.streamNote} role="status">
          {note}
        </span>
        <button
          type="button"
          className={styles.checkNow}
          disabled={state.ref === null || state.checking}
          title={t("response.streamTests.checkTitle")}
          onClick={() => void checkStreamTests(id)}
        >
          {state.checking ? t("response.streamTests.checking") : t("response.streamTests.check")}
        </button>
      </div>
      {state.error !== null && (
        <p className={styles.streamError} role="alert">
          <IconWarning size={13} />
          {state.error.message}
        </p>
      )}
      {state.tests !== null && <ResponseTests tests={state.tests} onJump={onJump} />}
    </div>
  );
}

/** The counts of the request's latest stream results, for a Tests tab's badge; null before any. */
export function useStreamTestSummary(id: RequestId): TestSummary | null {
  const state = useStreamTests(id);
  return state.tests === null ? null : summarizeTests(state.tests);
}
