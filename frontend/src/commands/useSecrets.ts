import { useSyncExternalStore } from "react";
import { getSecretsState, subscribeSecrets, type SecretsState } from "./secrets";

/** The encrypted secrets' state (commands/secrets), re-rendering on every change. */
export function useSecrets(): SecretsState {
  return useSyncExternalStore(subscribeSecrets, getSecretsState);
}
