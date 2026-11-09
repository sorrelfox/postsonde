import { useCallback } from "react";
import {
  chooseGraphqlFile,
  PREFLIGHT_HEADER,
  sendsPreflight,
  setGraphqlFileContentType,
  setGraphqlFiles,
  setSendPreflight,
} from "../../commands/graphqlFiles";
import type { PartDTO, RequestDTO } from "../../lib/collection";
import { Trans, useT } from "../../i18n/react";
import { fileMediaType } from "../../lib/contentType";
import { useStores, type RequestId } from "../../stores";
import { IconWarning } from "../common/icons";
import { KeyValueTable, type KeyValueCellProps } from "../common/KeyValueTable";
import { Failure, FileChip, usePick } from "./BodyFiles";
import fileStyles from "./BodyFiles.module.css";
import gqlStyles from "./GraphqlEditor.module.css";
import { usePartRows } from "./partRows";
import settingStyles from "./RequestSettings.module.css";

export interface GraphqlFilesProps {
  id: RequestId;
  request: RequestDTO;
  readOnly: boolean;
}

/**
 * The Files tab of a GraphQL request (GQL-07): a table of files, each
 * bound to a variable path ("variables.file", "variables.input.files.0";
 * the "variables." prefix may be left out), sent by Go with the GraphQL
 * multipart request spec. A row's value is its file chip with Choose… (the
 * native dialog), then an optional Content-Type whose placeholder is the
 * type guessed from the file name. The checkbox under the table turns the
 * automatic Apollo-Require-Preflight header off (settings.omitHeaders).
 */
export function GraphqlFiles({ id, request, readOnly }: GraphqlFilesProps) {
  const t = useT();
  const stores = useStores();
  const files = request.files ?? [];
  const onFiles = useCallback((next: PartDTO[]) => setGraphqlFiles(stores, id, next), [stores, id]);
  const [rows, setRows] = usePartRows(request.files ?? null, onFiles);
  const [failure, busy, start] = usePick();
  const choose = (index: number) => start(() => chooseGraphqlFile(stores, id, index));

  const valueCell = (_row: unknown, index: number, cell: KeyValueCellProps) => {
    const file = files[index];
    if (file === undefined) return null;
    return (
      <FileChip
        compact
        path={file.file}
        readOnly={readOnly}
        busy={busy}
        cell={cell}
        chooseLabel={t("editorProtocols.files.chooseRow", { row: index + 1 })}
        onChoose={() => choose(index)}
      />
    );
  };

  const valueActions = (_row: unknown, index: number) => {
    const file = files[index];
    if (file === undefined) return null;
    return (
      <input
        type="text"
        className={fileStyles.partType}
        value={file.contentType}
        placeholder={file.file !== "" ? fileMediaType(file.file, "") : "Content-Type"}
        aria-label={t("editorProtocols.files.contentTypeRow", { row: index + 1 })}
        title={t("editorProtocols.files.contentTypeTitle")}
        readOnly={readOnly}
        spellCheck={false}
        autoComplete="off"
        onChange={(e) => setGraphqlFileContentType(stores, id, index, e.target.value)}
      />
    );
  };

  const get = request.method === "GET";
  return (
    <div className={fileStyles.parts}>
      <p className={gqlStyles.hint}>
        <Trans
          k="editorProtocols.files.hint"
          values={{
            type: <code>multipart/form-data</code>,
            path: <code>variables.file</code>,
            listPath: <code>variables.input.files.0</code>,
            value: <code>null</code>,
          }}
        />
      </p>
      {get && (
        <p className={fileStyles.failure} role="status">
          <IconWarning size={12} />
          <span>{t("editorProtocols.files.postOnly")}</span>
        </p>
      )}
      <KeyValueTable
        label={t("editorProtocols.files.label")}
        rows={rows}
        onChange={readOnly ? undefined : setRows}
        readOnly={readOnly}
        placeholders={{ key: "variables.file" }}
        valueCell={valueCell}
        valueActions={valueActions}
      />
      <Failure message={failure} />
      <label className={settingStyles.check}>
        <input
          type="checkbox"
          checked={sendsPreflight(request.settings)}
          disabled={readOnly}
          onChange={(e) => setSendPreflight(stores, id, e.currentTarget.checked)}
        />
        <Trans k="editorProtocols.files.preflight" values={{ header: <code>{PREFLIGHT_HEADER}: true</code> }} />
        <span className={settingStyles.checkHelp}>{t("editorProtocols.files.preflightHelp")}</span>
      </label>
    </div>
  );
}
