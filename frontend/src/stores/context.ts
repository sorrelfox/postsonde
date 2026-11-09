import { createContext } from "react";
import { autosaveReducer, initialAutosaveState, type AutosaveAction, type AutosaveState } from "./autosave";
import { collectionReducer, initialCollectionState, type CollectionAction, type CollectionState } from "./collection";
import {
  environmentsReducer,
  initialEnvironmentsState,
  type EnvironmentsAction,
  type EnvironmentsState,
} from "./environments";
import { draftsReducer, initialDraftsState, type DraftsAction, type DraftsState } from "./drafts";
import { initialServersState, serversReducer, type ServersAction, type ServersState } from "./servers";
import { initialSettingsState, settingsReducer, type SettingsAction, type SettingsState } from "./settings";
import { createStore, type Store } from "./store";
import { initialTabsState, tabsReducer, type TabsAction, type TabsState } from "./tabs";
import { initialWorkspaceState, workspaceReducer, type WorkspaceAction, type WorkspaceState } from "./workspace";

export type CollectionStore = Store<CollectionState, CollectionAction>;
export type TabsStore = Store<TabsState, TabsAction>;
export type DraftsStore = Store<DraftsState, DraftsAction>;
export type SettingsStore = Store<SettingsState, SettingsAction>;
export type WorkspaceStore = Store<WorkspaceState, WorkspaceAction>;
export type EnvironmentsStore = Store<EnvironmentsState, EnvironmentsAction>;
export type AutosaveStore = Store<AutosaveState, AutosaveAction>;
export type ServersStore = Store<ServersState, ServersAction>;

/** Every app store. Created once per app (or per test) by StoreProvider. */
export interface Stores {
  readonly collection: CollectionStore;
  readonly tabs: TabsStore;
  readonly drafts: DraftsStore;
  readonly settings: SettingsStore;
  readonly workspace: WorkspaceStore;
  readonly environments: EnvironmentsStore;
  readonly autosave: AutosaveStore;
  readonly servers: ServersStore;
}

export interface InitialStates {
  collection?: CollectionState;
  tabs?: TabsState;
  drafts?: DraftsState;
  settings?: SettingsState;
  workspace?: WorkspaceState;
  environments?: EnvironmentsState;
  autosave?: AutosaveState;
  servers?: ServersState;
}

/** Creates a fresh set of stores; pass initial states to seed tests or previews. */
export function createStores(initial: InitialStates = {}): Stores {
  return {
    collection: createStore(collectionReducer, initial.collection ?? initialCollectionState),
    tabs: createStore(tabsReducer, initial.tabs ?? initialTabsState),
    drafts: createStore(draftsReducer, initial.drafts ?? initialDraftsState),
    settings: createStore(settingsReducer, initial.settings ?? initialSettingsState),
    workspace: createStore(workspaceReducer, initial.workspace ?? initialWorkspaceState),
    environments: createStore(environmentsReducer, initial.environments ?? initialEnvironmentsState),
    autosave: createStore(autosaveReducer, initial.autosave ?? initialAutosaveState),
    servers: createStore(serversReducer, initial.servers ?? initialServersState),
  };
}

// The context value never changes after mount, so the context itself never
// re-renders anything; components subscribe to slices through useSelector.
export const StoresContext = createContext<Stores | null>(null);
