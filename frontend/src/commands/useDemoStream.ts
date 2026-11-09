// The demo stream as a hook, for components/StreamDemo.tsx.
import { DEMO_DOMAIN, startDemoStream, type ItemDTO } from "../lib/demo";
import { useOperation, type OperationControls } from "./useOperation";

/**
 * Starts DemoService.Stream(ratePerSec, durationMs) and follows it.
 * `onItems` gets every batch (the stream demo feeds its message log with it).
 */
export function useDemoStream(
  onItems?: (items: readonly ItemDTO[]) => void,
): OperationControls<ItemDTO, [ratePerSec: number, durationMs: number]> {
  return useOperation<ItemDTO, [number, number]>(DEMO_DOMAIN, startDemoStream, { keep: 1, onItems });
}
