import { useContext } from "react";
import type { AutosaveState } from "./autosave";
import type { CollectionState } from "./collection";
import { StoresContext, type Stores } from "./context";
import type { DraftsState } from "./drafts";
import type { EnvironmentsState } from "./environments";
import type { ServersState } from "./servers";
import type { SettingsState } from "./settings";
import type { TabsState } from "./tabs";
import type { WorkspaceState } from "./workspace";
import { useSelector } from "./useSelector";

type Equality<T> = (a: T, b: T) => boolean;

/**
 * All stores, for dispatching (`useStores().tabs.dispatch(...)`) and for
 * passing to commands. Reading through it does not subscribe; use the
 * per-store hooks below to read state.
 */
export function useStores(): Stores {
  const stores = useContext(StoresContext);
  if (stores === null) throw new Error("useStores: no <StoreProvider> above this component");
  return stores;
}

export function useCollection<T>(selector: (state: CollectionState) => T, isEqual?: Equality<T>): T {
  return useSelector(useStores().collection, selector, isEqual);
}

export function useTabs<T>(selector: (state: TabsState) => T, isEqual?: Equality<T>): T {
  return useSelector(useStores().tabs, selector, isEqual);
}

export function useDrafts<T>(selector: (state: DraftsState) => T, isEqual?: Equality<T>): T {
  return useSelector(useStores().drafts, selector, isEqual);
}

export function useSettings<T>(selector: (state: SettingsState) => T, isEqual?: Equality<T>): T {
  return useSelector(useStores().settings, selector, isEqual);
}

export function useWorkspace<T>(selector: (state: WorkspaceState) => T, isEqual?: Equality<T>): T {
  return useSelector(useStores().workspace, selector, isEqual);
}

export function useEnvironments<T>(selector: (state: EnvironmentsState) => T, isEqual?: Equality<T>): T {
  return useSelector(useStores().environments, selector, isEqual);
}

export function useAutosave<T>(selector: (state: AutosaveState) => T, isEqual?: Equality<T>): T {
  return useSelector(useStores().autosave, selector, isEqual);
}

export function useServers<T>(selector: (state: ServersState) => T, isEqual?: Equality<T>): T {
  return useSelector(useStores().servers, selector, isEqual);
}
