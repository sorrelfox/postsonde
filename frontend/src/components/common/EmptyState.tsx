import type { ReactNode } from "react";
import styles from "./EmptyState.module.css";

export interface EmptyStateProps {
  icon?: ReactNode;
  title: string;
  children?: ReactNode;
  /** "center" fills and centres in its container; "top" sits near the top (sidebars). */
  placement?: "center" | "top";
}

/** A quiet placeholder for a panel with nothing to show yet. */
export function EmptyState({ icon, title, children, placement = "center" }: EmptyStateProps) {
  const className = placement === "top" ? `${styles.empty} ${styles.top}` : styles.empty;
  return (
    <div className={className}>
      {icon !== undefined && <div className={styles.icon}>{icon}</div>}
      <p className={styles.title}>{title}</p>
      {children !== undefined && <p className={styles.body}>{children}</p>}
    </div>
  );
}
