import type { ReactNode } from "react";
import styles from "./TabBarSlot.module.css";

export interface TabBarSlotProps {
  /** The tab bar (components/tabs/). The row is empty until then. */
  children?: ReactNode;
}

/** The 40px row above the panes that holds the tab bar. */
export function TabBarSlot({ children }: TabBarSlotProps) {
  return (
    <div className={styles.tabBar} data-slot="tab-bar">
      {children}
    </div>
  );
}
