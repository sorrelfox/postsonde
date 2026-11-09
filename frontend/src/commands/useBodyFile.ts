// What the editor shows about a body or multipart file (HTTP-14, HTTP-15):
// its name, size and whether it exists, asked from Go
// (WorkspaceService.BodyFile). The file itself is never read by the UI.

import { useEffect, useState } from "react";
import { isCancelled } from "../lib/calls";
import { getBodyFile, type BodyFileDTO } from "../lib/workspace";

/** The last answer per stored path, so a chip shows at once when it mounts again. */
const known = new Map<string, BodyFileDTO>();

/** Remembers `file` (e.g. just picked), so its chip needs no round trip. */
export function rememberBodyFile(file: BodyFileDTO): void {
  if (file.path !== "") known.set(file.path, file);
}

/** Forgets every remembered file (tests). */
export function forgetBodyFiles(): void {
  known.clear();
}

/**
 * The description of the file stored as `path`, or null while it is
 * loading (or when `path` is ""). It is asked again whenever the path
 * changes or the component mounts, so a file restored on disk shows up;
 * a failed call leaves the last known state.
 */
export function useBodyFile(path: string): BodyFileDTO | null {
  const [file, setFile] = useState<BodyFileDTO | null>(() => (path === "" ? null : (known.get(path) ?? null)));

  useEffect(() => {
    setFile(path === "" ? null : (known.get(path) ?? null));
    if (path === "") return;
    let live = true;
    const call = getBodyFile(path);
    call.then(
      (dto) => {
        known.set(path, dto);
        if (live) setFile(dto);
      },
      (err: unknown) => {
        if (!isCancelled(err) && live) setFile(known.get(path) ?? null);
      },
    );
    return () => {
      live = false;
      call.cancel();
    };
  }, [path]);

  return file;
}
