// The official socket.io-client v4 against PostSonde's Socket.IO server,
// for the opt-in interop test of internal/core/servers/sioserver
// (interop_test.go). Not run by default: see
// docs/socketio.md#interop-with-the-official-implementations and
// scripts/sio-interop.sh.
//
//   node client.js <install dir> <server URL> polling|websocket|upgrade
//
// <install dir> holds node_modules with socket.io-client 4.x. The server
// is set up by the Go test: namespaces "/" and "/admin", both needing the
// auth {"token": "s3cret"}, echo on, acks echoing the arguments, a rule
// that answers "hello" with the ack ["hi", <de ad be ef>] and the event
// "greeting" [{"n": 1}, <01 02 03>].
//
// Both namespaces are joined over one Engine.IO connection (the client's
// multiplexing). For each: the rule's ack and event, an echoed emit with
// binary data, and the heartbeat; then it writes {"step": "ask", …} and
// waits for the Go test to emit "ask" (with an ack request, answered with
// "answer", <ca fe> and the arguments) and then "finish". Then a wrong
// token and an unknown namespace are refused. Writes one JSON object per
// line to stdout, and {"step": "done"} or {"step": "fail", "error": …}
// last; exits 0 when everything passed.
"use strict";
const fs = require("fs");
const path = require("path");
const { createRequire } = require("module");

const [dir, url, mode] = process.argv.slice(2);
if (!dir || !url || !["polling", "websocket", "upgrade"].includes(mode)) {
  process.stderr.write("usage: node client.js <install dir> <server URL> polling|websocket|upgrade\n");
  process.exit(2);
}
const req = createRequire(path.join(path.resolve(dir), "package.json"));
const { io } = req("socket.io-client");

const t0 = Date.now();
// out writes one line, with the milliseconds since the start.
const out = (o) => process.stdout.write(JSON.stringify({ ...o, ms: Date.now() - t0 }) + "\n");
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

function version(name) {
  try {
    return JSON.parse(fs.readFileSync(path.join(dir, "node_modules", name, "package.json"), "utf8")).version;
  } catch {
    return "";
  }
}

function describe(args) {
  return args.map((a) => (Buffer.isBuffer(a) ? { binary: a.toString("hex") } : a));
}

function check(cond, what, got) {
  if (!cond) throw new Error(what + ": got " + JSON.stringify(describe(got)));
}

const isHex = (b, hex) => Buffer.isBuffer(b) && b.toString("hex") === hex;

// once resolves with the arguments of the next event, or fails after ms.
function once(socket, event, ms = 5000) {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error("no " + event + " event")), ms);
    socket.once(event, (...args) => {
      clearTimeout(timer);
      resolve(args);
    });
  });
}

// emitAck emits with an ack callback and resolves with every argument of
// the ack (emitWithAck keeps only the first).
function emitAck(socket, event, ...args) {
  return new Promise((resolve, reject) => {
    socket.timeout(5000).emit(event, ...args, (err, ...ack) => (err ? reject(new Error("no ack for " + event)) : resolve(ack)));
  });
}

const transports = { polling: ["polling"], websocket: ["websocket"], upgrade: ["polling", "websocket"] }[mode];
const auth = { token: "s3cret" };

function connect(nsp, opts) {
  const socket = io(url + nsp, { transports, reconnection: false, timeout: 5000, ...opts });
  return new Promise((resolve, reject) => {
    socket.once("connect", () => resolve(socket));
    socket.once("connect_error", (err) => reject(err));
  });
}

async function session(socket, nsp) {
  const engine = socket.io.engine;
  // The rule: an ack and an event, both with binary data.
  const greeting = once(socket, "greeting");
  const ack = await emitAck(socket, "hello", "x", Buffer.from([0, 1, 2, 255]));
  check(ack.length === 2 && ack[0] === "hi" && isHex(ack[1], "deadbeef"), nsp + " hello ack", ack);
  const g = await greeting;
  check(g.length === 2 && g[0].n === 1 && isHex(g[1], "010203"), nsp + " greeting", g);

  // Echo: the event comes back, and the ack echoes the arguments.
  const echoed = once(socket, "echo-me");
  const eack = await emitAck(socket, "echo-me", { a: "€😀" }, Buffer.from([9, 8, 7]));
  check(eack.length === 2 && eack[0].a === "€😀" && isHex(eack[1], "090807"), nsp + " echo ack", eack);
  const e = await echoed;
  check(e.length === 2 && e[0].a === "€😀" && isHex(e[1], "090807"), nsp + " echo event", e);

  // The server's own event with an ack request, from the Go test.
  const asked = new Promise((resolve) => {
    socket.once("ask", (...args) => {
      const cb = args.pop();
      cb("answer", Buffer.from([0xca, 0xfe]), ...args);
      out({ step: "asked", nsp, args: describe(args) });
      resolve(args);
    });
  });
  const finish = once(socket, "finish", 10000);
  out({ step: "ask", nsp, id: socket.id, transport: engine.transport.name });
  const a = await asked;
  check(a.length === 2 && a[0] === "q" && isHex(a[1], "0506"), nsp + " ask", a);
  await finish;
}

async function main() {
  out({ ready: true, mode, versions: { "socket.io-client": version("socket.io-client"), "engine.io-client": version("engine.io-client"), node: process.versions.node } });

  const main = await connect("/", { auth });
  const admin = await connect("/admin", { auth });
  check(main.io === admin.io, "one manager for both namespaces", []);
  const engine = main.io.engine;
  let upgraded = false;
  engine.once("upgrade", () => (upgraded = true));
  out({ step: "connected", ids: { "/": main.id, "/admin": admin.id }, transport: engine.transport.name });

  // The heartbeat (the server pings every 500 ms) keeps the connection up.
  await sleep(1300);
  check(main.connected && admin.connected, "connected after 1.3 s", []);
  if (mode === "upgrade" && engine.transport.name !== "websocket") {
    await new Promise((r) => (upgraded ? r() : engine.once("upgrade", r)));
  }
  const want = mode === "polling" ? "polling" : "websocket";
  check(engine.transport.name === want, "transport " + want, [engine.transport.name]);

  await session(main, "/");
  await session(admin, "/admin");
  admin.disconnect();
  main.disconnect();

  // Refusals, each on a connection of its own.
  for (const [nsp, opts, message] of [
    ["/admin", { auth: { token: "nope" } }, "Not authorized"],
    ["/admin", {}, "Not authorized"],
    ["/nope", { auth }, "Invalid namespace"],
  ]) {
    try {
      const s = await connect(nsp, { forceNew: true, ...opts });
      s.disconnect();
      throw new Error(nsp + " was accepted");
    } catch (err) {
      check(err.message === message, nsp + " refusal", [err.message]);
      out({ step: "refused", nsp, message: err.message, data: err.data });
    }
  }
  // Let the Engine.IO close packets of the disconnects go out before exiting.
  await sleep(200);
  out({ step: "done", transport: want, upgraded });
}

main().then(
  () => process.exit(0),
  (err) => {
    out({ step: "fail", error: String((err && err.stack) || err) });
    process.exit(1);
  },
);
