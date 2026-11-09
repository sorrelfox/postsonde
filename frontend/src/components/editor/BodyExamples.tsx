import type { ChangeEvent } from "react";
import {
  activeExampleIndex,
  addBodyExample,
  deleteBodyExample,
  renameBodyExample,
  selectBodyExample,
} from "../../commands/bodyExamples";
import type { BodyExampleDTO } from "../../lib/collection";
import { useT } from "../../i18n/react";
import { useStores, type RequestId } from "../../stores";
import styles from "./BodyEditor.module.css";

export interface BodyExamplesProps {
  id: RequestId;
  examples: readonly BodyExampleDTO[];
  readOnly: boolean;
}

/**
 * The body examples bar of the Body section (HTTP-17): a picker of the
 * request's named bodies (the one picked is sent), the active example's
 * name, Add (a copy of the current body, made active) and Delete. Without
 * examples only "Add example" shows. Commands in commands/bodyExamples.ts.
 */
export function BodyExamples({ id, examples, readOnly }: BodyExamplesProps) {
  const t = useT();
  const stores = useStores();
  const active = activeExampleIndex(examples);
  const current = examples[active];

  const onAdd = () => addBodyExample(stores, id);
  if (current === undefined) {
    return (
      <div className={styles.examples}>
        <button
          type="button"
          className={styles.tool}
          disabled={readOnly}
          title={t("editor.examples.addExample.title")}
          onClick={onAdd}
        >
          {t("editor.examples.addExample")}
        </button>
      </div>
    );
  }

  const onSelect = (e: ChangeEvent<HTMLSelectElement>) => selectBodyExample(stores, id, Number(e.target.value));
  const onRename = (e: ChangeEvent<HTMLInputElement>) => renameBodyExample(stores, id, active, e.target.value);
  const label = (e: BodyExampleDTO, i: number) => (e.name.trim() === "" ? t("editor.examples.unnamed", { n: i + 1 }) : e.name);

  return (
    <div className={styles.examples} role="group" aria-label={t("editor.examples.label")}>
      <select
        className={styles.type}
        aria-label={t("editor.examples.select")}
        value={String(active)}
        disabled={readOnly}
        onChange={onSelect}
      >
        {examples.map((e, i) => (
          <option key={i} value={String(i)}>
            {label(e, i)}
          </option>
        ))}
      </select>
      <input
        className={styles.exampleName}
        aria-label={t("editor.examples.name")}
        placeholder={t("editor.examples.unnamed", { n: active + 1 })}
        value={current.name}
        readOnly={readOnly}
        onChange={readOnly ? undefined : onRename}
      />
      <button type="button" className={styles.tool} disabled={readOnly} title={t("editor.examples.add.title")} onClick={onAdd}>
        {t("editor.examples.add")}
      </button>
      <button
        type="button"
        className={styles.tool}
        disabled={readOnly}
        title={examples.length === 1 ? t("editor.examples.deleteLast.title") : t("editor.examples.delete.title")}
        onClick={() => deleteBodyExample(stores, id, active)}
      >
        {t("editor.examples.delete")}
      </button>
    </div>
  );
}
