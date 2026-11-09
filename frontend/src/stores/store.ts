// The store primitive: a value, a pure reducer and a listener set.
// No React here, so reducers and stores can be tested in plain Node.
// React reads a store through useSelector (./useSelector.ts).

/** A pure function: same state and action in, same state out. No I/O. */
export type Reducer<S, A> = (state: S, action: A) => S;

export interface Store<S, A> {
  getState(): S;
  dispatch(action: A): void;
  /** Calls `listener` after every change. Returns the unsubscribe function. */
  subscribe(listener: () => void): () => void;
}

export function createStore<S, A>(reducer: Reducer<S, A>, initialState: S): Store<S, A> {
  let state = initialState;
  let dispatching = false;
  const listeners = new Set<() => void>();

  return {
    getState: () => state,
    dispatch(action) {
      if (dispatching) {
        throw new Error("store: dispatch called from inside a reducer");
      }
      let next: S;
      dispatching = true;
      try {
        next = reducer(state, action);
      } finally {
        dispatching = false;
      }
      // Reducers return the same object when nothing changed: no notify.
      if (Object.is(next, state)) return;
      state = next;
      for (const listener of [...listeners]) listener();
    },
    subscribe(listener) {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
  };
}

/**
 * Equality for selectors that build a new array or object on every call
 * (e.g. a list of ids): equal when the same keys hold `Object.is` values.
 */
export function shallowEqual(a: unknown, b: unknown): boolean {
  if (Object.is(a, b)) return true;
  if (typeof a !== "object" || typeof b !== "object" || a === null || b === null) {
    return false;
  }
  if (Array.isArray(a) !== Array.isArray(b)) return false;
  const keysA = Object.keys(a);
  if (keysA.length !== Object.keys(b).length) return false;
  const recA = a as Record<string, unknown>;
  const recB = b as Record<string, unknown>;
  return keysA.every((k) => Object.prototype.hasOwnProperty.call(recB, k) && Object.is(recA[k], recB[k]));
}
