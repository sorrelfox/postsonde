// Small immutable helpers shared by the reducers.

/** Returns a copy of `items` with `item` inserted at `index` (clamped). */
export function insertAt<T>(items: readonly T[], item: T, index: number = items.length): T[] {
  const i = Math.max(0, Math.min(index, items.length));
  return [...items.slice(0, i), item, ...items.slice(i)];
}

/** Returns a copy of `record` without `keys`, or `record` itself if none were present. */
export function omitKeys<V>(
  record: Readonly<Record<string, V>>,
  keys: Iterable<string>,
): Readonly<Record<string, V>> {
  let copy: Record<string, V> | null = null;
  for (const key of keys) {
    if (!Object.prototype.hasOwnProperty.call(record, key)) continue;
    copy ??= { ...record };
    delete copy[key];
  }
  return copy ?? record;
}
