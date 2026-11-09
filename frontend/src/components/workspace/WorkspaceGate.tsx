import { useCallback, useState, type ReactNode } from "react";
import { showToast } from "../../commands/toasts";
import { formatError, type AppError } from "../../lib/errors";
import { useStores, useWorkspace } from "../../stores";
import { IconWarning } from "../common/icons";
import { useWorkspaceSync } from "./useWorkspaceSync";
import { WelcomeScreen } from "./WelcomeScreen";
import styles from "./WelcomeScreen.module.css";

export interface WorkspaceGateProps {
  /** The app shell, rendered while a workspace is open. */
  children: ReactNode;
}

/**
 * Shows the app shell when a project is open and the welcome screen when
 * none is. Until Go has answered it renders an empty frame, so the
 * welcome screen does not flash before the last project reopens.
 *
 * A project this window was asked to open (the command line, a second
 * start of PostSonde; APP-06) that does not open is shown on the welcome
 * screen, or as a toast in the app shell.
 */
export function WorkspaceGate({ children }: WorkspaceGateProps) {
  const stores = useStores();
  const [notice, setNotice] = useState<AppError | null>(null);
  const onOpenFailed = useCallback(
    (err: AppError) => {
      if (stores.workspace.getState().current === null) setNotice(err);
      else showToast({ message: formatError(err), icon: <IconWarning size={14} strokeWidth={1.9} /> });
    },
    [stores],
  );
  useWorkspaceSync(onOpenFailed);
  const status = useWorkspace((s) => s.status);
  const open = useWorkspace((s) => s.current !== null);

  if (status === "loading") return <div className={styles.loading} aria-busy="true" />;
  return open ? <>{children}</> : <WelcomeScreen notice={notice} />;
}
