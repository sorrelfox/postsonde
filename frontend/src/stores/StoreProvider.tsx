import { useState, type ReactNode } from "react";
import { createStores, StoresContext, type Stores } from "./context";

interface StoreProviderProps {
  /** Pre-built stores (tests, previews). Defaults to a fresh, empty set. */
  stores?: Stores;
  children: ReactNode;
}

/** Makes the app stores available to useStores and the per-store hooks. */
export function StoreProvider({ stores, children }: StoreProviderProps) {
  const [value] = useState(() => stores ?? createStores());
  return <StoresContext.Provider value={value}>{children}</StoresContext.Provider>;
}
