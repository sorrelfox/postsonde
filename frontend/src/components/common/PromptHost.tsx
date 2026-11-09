import { useEffect, useId, useState, useSyncExternalStore } from "react";
import { promptQueue as defaultPromptQueue, type PromptQueue, type PromptRequest } from "../../commands/prompts";
import { Dialog } from "./Dialog";
import { useT } from "../../i18n/react";
import styles from "./PromptHost.module.css";

export interface PromptHostProps {
  /** Default: the app's queue (commands/prompts). */
  queue?: PromptQueue;
}

/**
 * Shows the askPrompts() requests one at a time (DYN-07): one field per
 * {{$prompt}} label, filled with its default, masked when secret. Enter or
 * Send answers; Esc or Cancel cancels the send. The typed values live in
 * this dialog's state only, and go away with it. Mount once, in the app
 * shell.
 */
export function PromptHost({ queue = defaultPromptQueue }: PromptHostProps) {
  const request = useSyncExternalStore(queue.subscribe, queue.current);
  useEffect(() => queue.attach(), [queue]);
  if (request === null) return null;
  return <PromptForm key={request.id} request={request} queue={queue} />;
}

function PromptForm({ request, queue }: { request: PromptRequest; queue: PromptQueue }) {
  const t = useT();
  const baseId = useId();
  const [values, setValues] = useState<Record<string, string>>(() =>
    Object.fromEntries(request.fields.map((f) => [f.label, f.default])),
  );
  const run = request.purpose === "run";
  const one = request.fields.length === 1;
  return (
    <Dialog
      open
      title={run ? t("common.prompt.titleRun") : one ? t("common.prompt.titleOne") : t("common.prompt.titleMany")}
      describedBy={`${baseId}-help`}
      confirmLabel={run ? t("common.prompt.start") : t("common.prompt.send")}
      onCancel={() => queue.settle(request.id, null)}
      onConfirm={() => queue.settle(request.id, { ...values })}
    >
      <p id={`${baseId}-help`} className={styles.help}>
        {run ? t("common.prompt.helpRun") : t("common.prompt.helpSend")}
      </p>
      <div className={styles.fields}>
        {request.fields.map((f, i) => (
          <label key={f.label} className={styles.row}>
            <span className={styles.label}>{f.label}</span>
            <input
              className={styles.input}
              type={f.secret ? "password" : "text"}
              value={values[f.label] ?? ""}
              autoComplete="off"
              spellCheck={false}
              data-autofocus={i === 0 ? "" : undefined}
              onChange={(e) => {
                const v = e.target.value;
                setValues((prev) => ({ ...prev, [f.label]: v }));
              }}
            />
          </label>
        ))}
      </div>
    </Dialog>
  );
}
