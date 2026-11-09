import { useCallback, useRef, useSyncExternalStore } from "react";
import type { Store } from "./store";

interface Cache<S, T> {
  state: S;
  selector: (state: S) => T;
  value: T;
}

/**
 * Subscribes a component to one slice of a store. The component re-renders
 * only when the selected value changes (`isEqual`, default `Object.is`),
 * not on every change to the store.
 *
 * A selector that builds a new array or object each call must pass
 * `shallowEqual` (or its own equality), or it re-renders on every change.
 */
export function useSelector<S, T>(
  store: Store<S, unknown>,
  selector: (state: S) => T,
  isEqual: (a: T, b: T) => boolean = Object.is,
): T {
  const cache = useRef<Cache<S, T> | null>(null);

  // useSyncExternalStore needs getSnapshot to return the same value until
  // the store changes. Cache by (state, selector), and keep the previous
  // value when the new one is equal, so its reference stays stable.
  const getSnapshot = useCallback((): T => {
    const state = store.getState();
    const prev = cache.current;
    if (prev !== null && prev.state === state && prev.selector === selector) {
      return prev.value;
    }
    const next = selector(state);
    const value = prev !== null && isEqual(prev.value, next) ? prev.value : next;
    cache.current = { state, selector, value };
    return value;
  }, [store, selector, isEqual]);

  return useSyncExternalStore(store.subscribe, getSnapshot, getSnapshot);
}
