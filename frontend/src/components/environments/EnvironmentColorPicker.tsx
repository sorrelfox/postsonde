import { useEffect, useRef, useState, type CSSProperties } from "react";
import { setEnvironmentColor } from "../../commands/environments";
import { ENVIRONMENT_COLOR_PRESETS, findPreset, presetColor } from "../../lib/environmentColors";
import { toAppError } from "../../lib/errors";
import type { MessageKey } from "../../i18n";
import { useT } from "../../i18n/react";
import { isEnvironmentColor, useStores, type Environment } from "../../stores";
import styles from "./EnvironmentsPanel.module.css";

export interface EnvironmentColorPickerProps {
  environment: Environment;
  readOnly: boolean;
}

/** The colour the custom picker starts from when the environment has none: the grey preset. */
const PICKER_FALLBACK = presetColor(ENVIRONMENT_COLOR_PRESETS[ENVIRONMENT_COLOR_PRESETS.length - 1]!);

/** The presets' names, by their English name in lib/environmentColors. */
const PRESET_NAME_KEYS: Readonly<Record<string, MessageKey>> = {
  Red: "environments.color.preset.red",
  Orange: "environments.color.preset.orange",
  Yellow: "environments.color.preset.yellow",
  Green: "environments.color.preset.green",
  Teal: "environments.color.preset.teal",
  Blue: "environments.color.preset.blue",
  Purple: "environments.color.preset.purple",
  Pink: "environments.color.preset.pink",
  Grey: "environments.color.preset.grey",
};

/**
 * The environment's accent colour (ENV-08): none, a preset or a custom
 * colour from the native picker. Saved in the environment file at once
 * (the custom picker when it is closed, not while dragging), so everyone
 * sharing the project sees it. Each choice is a toggle button named after
 * the colour, so screen readers never depend on the colour itself.
 */
export function EnvironmentColorPicker({ environment, readOnly }: EnvironmentColorPickerProps) {
  const t = useT();
  const stores = useStores();
  const [error, setError] = useState<string | null>(null);
  const current = isEnvironmentColor(environment.color) ? environment.color.toLowerCase() : "";
  const isCustom = current !== "" && findPreset(current) === undefined;
  const inputRef = useRef<HTMLInputElement>(null);

  const save = (color: string) => {
    if (readOnly || color === current) return;
    setError(null);
    setEnvironmentColor(stores, environment.id, color).catch((err: unknown) => setError(toAppError(err).message));
  };
  const saveRef = useRef(save);
  saveRef.current = save;

  // The native "change" event fires once the picker is closed; React's
  // onChange would fire on every move while dragging. The input is
  // re-created (key) when the saved colour changes, so this follows it.
  useEffect(() => {
    const input = inputRef.current;
    if (input === null) return;
    const onChange = () => saveRef.current(input.value.toLowerCase());
    input.addEventListener("change", onChange);
    return () => input.removeEventListener("change", onChange);
  }, [current]);

  const customLabel = isCustom
    ? t("environments.color.customValue", { color: current })
    : t("environments.color.custom");

  return (
    <div className={styles.colorRow}>
      <span className={styles.colorLabel} id={`env-color-${environment.id}`}>
        {t("environments.color.label")}
      </span>
      <div className={styles.swatches} role="group" aria-labelledby={`env-color-${environment.id}`}>
        <button
          type="button"
          className={`${styles.swatch} ${styles.swatchNone}`}
          aria-pressed={current === ""}
          aria-label={t("environments.color.none")}
          title={t("environments.color.none")}
          disabled={readOnly}
          onClick={() => save("")}
        />
        {ENVIRONMENT_COLOR_PRESETS.map((preset) => {
          const color = presetColor(preset);
          const nameKey = PRESET_NAME_KEYS[preset.name];
          const name = nameKey === undefined ? preset.name : t(nameKey);
          return (
            <button
              key={preset.hex}
              type="button"
              className={styles.swatch}
              style={{ "--swatch": color } as CSSProperties}
              aria-pressed={current === color}
              aria-label={name}
              title={name}
              disabled={readOnly}
              onClick={() => save(color)}
            />
          );
        })}
        <label
          className={`${styles.swatch} ${styles.swatchCustom}`}
          style={isCustom ? ({ "--swatch": current } as CSSProperties) : undefined}
          data-selected={isCustom ? "" : undefined}
          title={customLabel}
        >
          <input
            ref={inputRef}
            type="color"
            className={styles.colorInput}
            aria-label={customLabel}
            defaultValue={current || PICKER_FALLBACK}
            key={current}
            disabled={readOnly}
          />
        </label>
      </div>
      {error !== null && (
        <p className={styles.error} role="alert">
          {error}
        </p>
      )}
    </div>
  );
}
