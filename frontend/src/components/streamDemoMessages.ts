// Turns the demo stream's items into WebSocket-like messages for the
// message log demo: a deterministic mix of directions, JSON, text,
// binary and control frames, with frame metadata. Demo only.
import { t } from "../i18n";
import type { LogMessageInput } from "../lib/messageSource";

const SYMBOLS = ["BTC-USD", "ETH-USD", "SOL-USD", "ADA-USD", "DOT-USD"];

/** Deterministic pseudo-random bytes for item `index`. */
function demoBytes(index: number, length: number): Uint8Array {
  const out = new Uint8Array(length);
  let x = (index * 2654435761) >>> 0 || 1;
  for (let i = 0; i < length; i++) {
    x ^= x << 13;
    x ^= x >>> 17;
    x ^= x << 5;
    out[i] = x & 0xff;
  }
  return out;
}

function frame(opcode: string, masked: boolean) {
  return { opcode, fin: true, masked };
}

/** The demo message for stream item `index`, stamped `time` (epoch ms). */
export function demoMessage(index: number, time?: number): LogMessageInput {
  const symbol = SYMBOLS[index % SYMBOLS.length];
  switch (index % 10) {
    case 0:
    case 1:
    case 2:
    case 3:
      return {
        time,
        direction: "received",
        kind: "text",
        text: JSON.stringify({
          type: "tick",
          seq: index,
          symbol,
          price: Math.round((1000 + ((index * 7919) % 5000) / 3) * 100) / 100,
          volume: (index * 31) % 977,
        }),
        meta: frame("text", false),
      };
    case 4:
      return {
        time,
        direction: "sent",
        kind: "text",
        text: JSON.stringify({ op: "subscribe", id: index, channels: ["ticker", "trades"], symbols: [symbol] }),
        meta: frame("text", true),
      };
    case 5:
      // i18n-ignore: a simulated payload on the wire, not UI text
      return { time, direction: "sent", kind: "text", text: `hello from client #${index}`, meta: frame("text", true) };
    case 6: {
      const data = demoBytes(index, 16 + (index % 96));
      return { time, direction: "received", kind: "binary", data, meta: frame("binary", false) };
    }
    case 7:
      return {
        time,
        direction: "received",
        kind: "text",
        text: JSON.stringify({
          type: "snapshot",
          seq: index,
          book: {
            bids: [
              [99.5, 3],
              [99.25, 12],
            ],
            asks: [
              [100.25, 5],
              [100.5, 9],
            ],
          },
          // i18n-ignore: a simulated payload on the wire, not UI text
          note: "a longer payload whose preview is cut at the end of the row",
        }),
        meta: frame("text", false),
      };
    case 8:
      return index % 30 === 8
        ? { time, direction: "sent", kind: "ping", text: "", size: 0, meta: frame("ping", true) }
        : { time, direction: "received", kind: "text", text: `heartbeat ${index}\nstatus: ok`, meta: frame("text", false) };
    default:
      return index % 30 === 9
        ? { time, direction: "received", kind: "pong", text: "", size: 0, meta: frame("pong", false) }
        : index % 1000 === 999
          ? { time, direction: "system", kind: "error", text: t("app.streamDemo.message.simulatedError", { index }) }
          : // i18n-ignore: a simulated payload on the wire, not UI text
            { time, direction: "received", kind: "text", text: `plain text message ${index}`, meta: frame("text", false) };
  }
}
