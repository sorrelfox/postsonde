import { useMemo, useSyncExternalStore } from "react";
import { graphqlSubscriptions, type GraphqlSubscription, type SubscriptionState } from "./graphqlSubscriptions";

/**
 * The subscription slot of GraphQL request `requestId` and its state
 * (GQL-34). The slot outlives the editor: it stays while the request's tab
 * is open.
 */
export function useGraphqlSubscription(requestId: string): { subscription: GraphqlSubscription; state: SubscriptionState } {
  const subscription = useMemo(() => graphqlSubscriptions().get(requestId), [requestId]);
  const state = useSyncExternalStore(subscription.subscribe, subscription.getState);
  return { subscription, state };
}
