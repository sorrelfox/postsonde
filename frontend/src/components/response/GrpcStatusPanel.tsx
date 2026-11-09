import type { StatusDTO, StatusDetailDTO } from "../../lib/grpc";
import { useT } from "../../i18n/react";
import styles from "./GrpcResponseView.module.css";

/** The last part of a dotted name ("google.rpc.ErrorInfo" → "ErrorInfo"). */
function shortType(type: string): string {
  const at = type.lastIndexOf(".");
  return at < 0 ? type : type.slice(at + 1);
}

function Detail({ detail, index }: { detail: StatusDetailDTO; index: number }) {
  const t = useT();
  const title = detail.type !== "" ? shortType(detail.type) : t("response.grpcStatus.detail", { number: index + 1 });
  return (
    <li className={styles.statusDetail} data-detail-type={detail.type}>
      <div className={styles.statusDetailHead}>
        <span className={styles.statusDetailType} title={detail.type}>
          {title}
        </span>
        <span className={styles.statusDetailUrl}>{detail.typeUrl}</span>
      </div>
      {detail.json !== "" && <pre className={styles.statusJson}>{detail.json}</pre>}
      {detail.json === "" && (
        <>
          <p className={styles.statusNote}>
            {detail.error !== ""
              ? detail.error
              : detail.type !== ""
                ? t("response.grpcStatus.unknownType", { type: detail.type })
                : t("response.grpcStatus.unknownNoType")}
          </p>
          <pre className={styles.statusJson}>
            {detail.raw}
            <span className={styles.binaryTag}>Base64</span>
          </pre>
        </>
      )}
    </li>
  );
}

/**
 * The Status response tab (GRPC-20): the call's gRPC status and, when the
 * server sent grpc-status-details-bin, its google.rpc.Status with every
 * detail as JSON (ErrorInfo, BadRequest, RetryInfo, … and types from the
 * request's .proto files), or as Base64 when the type is unknown.
 */
export function GrpcStatusPanel({ status }: { status: StatusDTO | null }) {
  const t = useT();
  if (status === null) return <p className={styles.empty}>{t("response.grpcStatus.none")}</p>;
  const rich = status.rich;
  const details = rich?.details ?? [];
  const differs = rich !== null && rich.error === "" && (rich.code !== status.code || rich.message !== status.message);
  return (
    <div className={styles.statusPanel} role="region" aria-label={t("response.grpcStatus.label")}>
      <p className={styles.statusHead} data-ok={status.code === 0 || undefined}>
        <span className={styles.statusName}>{status.name}</span>
        <span className={styles.statusCode}>{status.code}</span>
        {status.message !== "" && <span className={styles.statusMessage}>{status.message}</span>}
      </p>
      {rich === null && status.code !== 0 && <p className={styles.statusNote}>{t("response.grpcStatus.noDetails")}</p>}
      {rich !== null && rich.error !== "" && (
        <>
          <p className={styles.statusNote}>{rich.error}</p>
          <pre className={styles.statusJson}>
            {status.details}
            <span className={styles.binaryTag}>Base64</span>
          </pre>
        </>
      )}
      {differs && (
        <p className={styles.statusNote}>
          {rich.message !== ""
            ? t("response.grpcStatus.ownStatusMessage", { name: rich.name, message: rich.message })
            : t("response.grpcStatus.ownStatus", { name: rich.name })}
        </p>
      )}
      {details.length > 0 && (
        <ol className={styles.statusDetails} aria-label={t("response.grpcStatus.details")}>
          {details.map((d, i) => (
            <Detail key={i} detail={d} index={i} />
          ))}
        </ol>
      )}
      {rich !== null && rich.error === "" && details.length === 0 && <p className={styles.statusNote}>{t("response.grpcStatus.emptyDetails")}</p>}
    </div>
  );
}
