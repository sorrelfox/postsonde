import type { GrpcCall, GrpcCallState } from "../../commands/grpcCalls";
import { useT } from "../../i18n/react";

export interface GrpcStreamControlsProps {
  call: GrpcCall;
  state: GrpcCallState;
  /** The message to send: the editor's current text. */
  getMessage: () => string;
  /** Class names of the Send message button and of the End stream button. */
  sendClassName: string;
  endClassName: string;
}

/**
 * Send message and End stream for a running client-streaming or bidi call
 * (GRPC-13, GRPC-14, GRPC-22): Send message sends the editor's message on
 * the request stream, End stream half-closes it. Nothing is rendered for
 * other calls or once the call has ended; after End stream only the call's
 * Cancel is left.
 */
export function GrpcStreamControls({ call, state, getMessage, sendClassName, endClassName }: GrpcStreamControlsProps) {
  const t = useT();
  if (!state.clientStreaming || state.status !== "running" || state.halfClosed) return null;
  const busy = !call.canSend;
  return (
    <>
      <button
        type="button"
        className={sendClassName}
        disabled={busy}
        title={t("response.grpcStream.sendTitle")}
        data-stream-send=""
        onClick={() => void call.send(getMessage())}
      >
        {t("response.grpcStream.send")}
      </button>
      <button
        type="button"
        className={endClassName}
        disabled={busy}
        title={t("response.grpcStream.endTitle")}
        data-stream-end=""
        onClick={() => void call.closeSend()}
      >
        {t("response.grpcStream.end")}
      </button>
    </>
  );
}
