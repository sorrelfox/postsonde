// A reference Socket.IO server for the opt-in interop tests of
// internal/core/realtime/sioclient (interop_test.go). Not run by default:
// see docs/socketio.md#interop-with-the-official-implementations and
// scripts/sio-interop.sh.
//
//   node server.js <install dir> <mode>
//
// <install dir> holds node_modules with the socket.io version of <mode>:
//   v2       socket.io 2.x (Engine.IO 3, Socket.IO protocol 4)
//   v3       socket.io 3.x (Engine.IO 4, Socket.IO protocol 5)
//   v4       socket.io 4.x
//   v4-eio3  socket.io 4.x with allowEIO3 (for a v2 client)
//
// Listens on 127.0.0.1 on a free port and writes one JSON object per line
// to stdout: {"ready":…} first, then what the server saw. Exits when stdin
// closes.
//
// Namespaces: "/" (open) and "/admin" (needs the token "s3cret" in the
// CONNECT auth payload, or in the query for Engine.IO 3 clients). On every
// connection the server emits "welcome" with an object and a Buffer. On
// "echo" it emits "echo" back with the same arguments and acknowledges with
// "ok", what it received (binary as {"binary": hex}), its transport now and
// a Buffer. On "bye" it disconnects the socket.
"use strict";
const fs = require("fs");
const http = require("http");
const path = require("path");
const { createRequire } = require("module");

const [dir, mode] = process.argv.slice(2);
if (!dir || !["v2", "v3", "v4", "v4-eio3"].includes(mode)) {
  process.stderr.write("usage: node server.js <install dir> v2|v3|v4|v4-eio3\n");
  process.exit(2);
}
const req = createRequire(path.join(path.resolve(dir), "package.json"));
const sio = req("socket.io");

// version reads an installed package's version ("" when it is not
// installed at the top level).
function version(name) {
  try {
    return JSON.parse(fs.readFileSync(path.join(dir, "node_modules", name, "package.json"), "utf8")).version;
  } catch {
    return "";
  }
}

const out = (o) => process.stdout.write(JSON.stringify(o) + "\n");

function describe(args) {
  return args.map((a) => (Buffer.isBuffer(a) ? { binary: a.toString("hex") } : a));
}

const opts = { pingInterval: 500, pingTimeout: 2000 };
if (mode === "v4-eio3") opts.allowEIO3 = true;
const httpServer = http.createServer();
const io = mode === "v2" ? sio(httpServer, opts) : new sio.Server(httpServer, opts);

function onConnection(socket) {
  const nsp = socket.nsp.name;
  const transport = () => socket.conn.transport.name;
  out({ ev: "connection", nsp, id: socket.id, transport: transport() });
  socket.emit("welcome", { nsp, id: socket.id }, Buffer.from([1, 2, 3]));
  socket.on("echo", (...args) => {
    const cb = typeof args[args.length - 1] === "function" ? args.pop() : null;
    out({ ev: "echo", nsp, args: describe(args), ack: !!cb, transport: transport() });
    socket.emit("echo", ...args);
    if (cb) cb("ok", describe(args), transport(), Buffer.from([0xde, 0xad, 0xbe, 0xef]));
  });
  socket.on("bye", () => socket.disconnect(true));
  socket.on("disconnect", (reason) => out({ ev: "disconnect", nsp, id: socket.id, reason }));
}

io.on("connection", onConnection);
const admin = io.of("/admin");
admin.use((socket, next) => {
  const h = socket.handshake;
  const token = (h.auth && h.auth.token) || (h.query && h.query.token);
  if (token === "s3cret") return next();
  next(new Error("not authorized"));
});
admin.on("connection", onConnection);

httpServer.listen(0, "127.0.0.1", () => {
  out({
    ready: true,
    port: httpServer.address().port,
    mode,
    versions: { "socket.io": version("socket.io"), "engine.io": version("engine.io"), node: process.versions.node },
  });
});

process.stdin.resume();
process.stdin.on("end", () => process.exit(0));
