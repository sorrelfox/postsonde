// Captures raw Engine.IO v4 / Socket.IO v4 wire traffic from a local
// socket.io 4.8.1 server, driven by a hand-written raw client (fetch for
// long-polling, ws for WebSocket). Prints the fixture format of
// internal/core/realtime/socketio/testdata.
//
// Regenerate the fixture (needs socket.io 4.x and ws in NODE_PATH; nothing
// is installed by PostSonde):
//
//   NODE_PATH=/path/to/node_modules node capture.js > reference-v4.txt
//
// then put the header comment back. Session and socket ids change on
// every run.
"use strict";
const http = require("http");
const { Server } = require("socket.io");
const WebSocket = require("ws");

const out = [];
function log(dir, kind, data) {
  if (kind === "ws-binary") {
    out.push(`${dir} ${kind} ${Buffer.from(data).toString("hex")}`);
  } else {
    out.push(`${dir} ${kind} ${JSON.stringify(data)}`);
  }
}
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function main() {
  const httpServer = http.createServer();
  const io = new Server(httpServer, { pingInterval: 300, pingTimeout: 2000, maxHttpBufferSize: 1e6 });
  io.on("connection", (socket) => {
    socket.on("hello", (...args) => {
      const cb = typeof args[args.length - 1] === "function" ? args.pop() : null;
      if (cb) cb("ok", ...args);
    });
    socket.on("upload", (meta, buf, cb) => {
      cb({ size: buf.length, meta }, Buffer.from([0xde, 0xad, 0xbe, 0xef]));
    });
    socket.on("burst", () => {
      socket.emit("tick", 1);
      socket.emit("tick", 2, "two", { three: [3] });
      socket.emit("bytes", Buffer.from([1, 2, 3]), { nested: [Buffer.from("hi")] });
      socket.emit("unicode", "héllo ✓ 😀", null, true, 1.5, -2);
    });
    socket.on("ask", () => {
      socket.emit("question", "ready?", () => {});
      socket.timeout(5000).emit("question2", Buffer.from([9]), () => {});
    });
    socket.on("bye", () => socket.disconnect());
  });
  const admin = io.of("/admin");
  admin.use((socket, next) => {
    if (socket.handshake.auth && socket.handshake.auth.token === "123") return next();
    const err = new Error("Not authorized");
    err.data = { reason: "bad token" };
    next(err);
  });
  admin.on("connection", (socket) => {
    socket.on("project:delete", (id, cb) => cb({ deleted: id }));
    socket.emit("welcome", "admin");
  });

  await new Promise((r) => httpServer.listen(0, "127.0.0.1", r));
  const base = `http://127.0.0.1:${httpServer.address().port}/socket.io/?EIO=4`;

  // Session 1: HTTP long-polling only.
  out.push("# session: polling");
  async function get(url) {
    const res = await fetch(url);
    const body = await res.text();
    log("<", "polling", body);
    if (body.split("\x1e").includes("2")) await post(url, "3");
    return body;
  }
  async function post(url, body) {
    log(">", "polling", body);
    const res = await fetch(url, { method: "POST", body, headers: { "content-type": "text/plain;charset=UTF-8" } });
    await res.text();
  }
  const open = await get(`${base}&transport=polling`);
  const sid = JSON.parse(open.slice(1)).sid;
  const purl = `${base}&transport=polling&sid=${sid}`;
  await post(purl, "40");
  await get(purl);
  await post(purl, '40/admin,{"token":"nope"}');
  await get(purl);
  await post(purl, '40/admin,{"token":"123"}');
  await get(purl);
  await post(purl, '42["hello","world",1]\x1e42["burst"]\x1e42/admin,7["project:delete",42]');
  await sleep(50);
  await get(purl);
  await post(purl, '421["hello",{"a":[1,2]}]\x1e451-2["upload",{"name":"f.bin"},{"_placeholder":true,"num":0}]\x1ebAQIDBA==');
  await sleep(50);
  await get(purl);
  await post(purl, '42["ask"]');
  await sleep(50);
  await get(purl);
  await post(purl, '430["fine"]\x1e461-1[{"_placeholder":true,"num":0}]\x1ebCgs=');
  await sleep(400);
  await get(purl); // ping
  await post(purl, "41/admin,\x1e41");
  await post(purl, "1");
  await sleep(50);

  // Session 2: WebSocket only.
  out.push("# session: websocket");
  {
    const ws = new WebSocket(`${base.replace("http", "ws")}&transport=websocket`);
    ws.on("message", (data, isBinary) => log("<", isBinary ? "ws-binary" : "ws-text", isBinary ? data : data.toString()));
    await new Promise((r) => ws.once("open", r));
    const send = (d) => {
      if (typeof d === "string") { log(">", "ws-text", d); ws.send(d); }
      else { log(">", "ws-binary", d); ws.send(d); }
    };
    await sleep(50);
    send("40");
    await sleep(50);
    send('42["hello"]');
    send('4212["hello","again"]');
    send('451-3["upload",{"name":"x"},{"_placeholder":true,"num":0}]');
    send(Buffer.from([0, 1, 2, 255]));
    send('42["burst"]');
    await sleep(100);
    send("40/nope,");
    await sleep(50);
    send('40/admin,{"token":"123"}');
    await sleep(50);
    send('42/admin,8["project:delete","abc"]');
    await sleep(350);
    send("3");
    send('42["bye"]');
    await sleep(100);
    ws.close();
    await sleep(50);
  }

  // Session 3: polling upgraded to WebSocket.
  out.push("# session: upgrade");
  {
    const open = await get(`${base}&transport=polling`);
    const sid = JSON.parse(open.slice(1)).sid;
    const purl = `${base}&transport=polling&sid=${sid}`;
    await post(purl, "40");
    await get(purl);
    const pending = get(purl);
    const ws = new WebSocket(`${base.replace("http", "ws")}&transport=websocket&sid=${sid}`);
    ws.on("message", (data, isBinary) => log("<", isBinary ? "ws-binary" : "ws-text", isBinary ? data : data.toString()));
    await new Promise((r) => ws.once("open", r));
    log(">", "ws-text", "2probe");
    ws.send("2probe");
    await sleep(100);
    await pending;
    log(">", "ws-text", "5");
    ws.send("5");
    await sleep(50);
    log(">", "ws-text", '42["hello"]');
    ws.send('42["hello"]');
    await sleep(50);
    log(">", "ws-text", "1");
    ws.send("1");
    await sleep(50);
    ws.close();
  }

  io.close();
  process.stdout.write(out.join("\n") + "\n");
}
main().catch((e) => { console.error(e); process.exit(1); });
