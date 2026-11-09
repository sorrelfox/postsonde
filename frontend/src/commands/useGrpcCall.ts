import { useMemo, useSyncExternalStore } from "react";
import { grpcCalls, type GrpcCall, type GrpcCallState } from "./grpcCalls";

/**
 * The call slot of grpc request `requestId` and its state (GRPC-11). The
 * slot outlives the editor: it stays while the request's tab is open.
 */
export function useGrpcCall(requestId: string): { call: GrpcCall; state: GrpcCallState } {
  const call = useMemo(() => grpcCalls().get(requestId), [requestId]);
  const state = useSyncExternalStore(call.subscribe, call.getState);
  return { call, state };
}
