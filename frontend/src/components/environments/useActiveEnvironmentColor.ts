import { isEnvironmentColor, selectActiveEnvironment, useEnvironments } from "../../stores";

/** The active environment's accent colour (ENV-08), or null for none or no colour. */
export function useActiveEnvironmentColor(): string | null {
  return useEnvironments((s) => {
    const color = selectActiveEnvironment(s)?.color ?? "";
    return isEnvironmentColor(color) ? color : null;
  });
}
