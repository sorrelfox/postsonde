// English messages of src/commands/ (namespace "commands"): t("commands.<id>").
// See docs/frontend.md#localization.
import type { Messages } from "../messages";

export default {
  // palette.ts: why an action of the command palette cannot run now.
  "palette.comingSoon": "Coming soon",
  "palette.notAvailable": "Not available here",
  // {place}: "in the collection tree", "on an entry of a log or timeline".
  "palette.onlyIn": "Only {place}",
  "palette.onlyInRequest": "Only {place}, outside its code editors",


  // wsSessions.ts: the WebSocket log and status line.
  "ws.attempt": "attempt {attempt}",
  "ws.attemptOf": "attempt {attempt} of {max}",
  // {delay}: "1.5 s"; {attempt}: "attempt 2 of 5".
  "ws.reconnecting": "Reconnecting in {delay} ({attempt})",
  "ws.reconnectCancelled": "Reconnect cancelled",
  "ws.gaveUp": { one: "Gave up reconnecting after {count} attempt", other: "Gave up reconnecting after {count} attempts" },
  "ws.notReconnectingCloseCode": "Not reconnecting: the server's close code asks not to",
  "ws.notReconnectingError": "Not reconnecting after this error",
  // A close: "1000 Normal closure “bye”".
  "ws.closeCodeNamed": "{code} {name}",
  "ws.closeReason": "{close} “{reason}”",
  "ws.close.1000": "Normal closure",
  "ws.close.1001": "Going away",
  "ws.close.1002": "Protocol error",
  "ws.close.1003": "Unsupported data",
  "ws.close.1005": "No status",
  "ws.close.1006": "Abnormal closure",
  "ws.close.1007": "Invalid payload",
  "ws.close.1008": "Policy violation",
  "ws.close.1009": "Message too big",
  "ws.close.1010": "Missing extension",
  "ws.close.1011": "Internal error",
  "ws.close.1012": "Service restart",
  "ws.close.1013": "Try again later",
  "ws.close.1014": "Bad gateway",
  "ws.connecting": "Connecting to {url}",
  "ws.reconnectingTo": "Reconnecting to {url} (attempt {attempt})",
  "ws.connected": "Connected to {url}",
  "ws.connectedSubprotocol": "Connected to {url} (subprotocol {subprotocol})",
  "ws.closing": "Closing",
  "ws.closed": "Closed",
  "ws.closedByServer": "Closed by the server: {close}",
  "ws.disconnected": "Disconnected: {close}",
  "ws.messages": { one: "{count} message", other: "{count} messages" },
  "ws.closeTotals": "{text} · {sent} sent, {received} received",


  // sioSessions.ts: the Socket.IO log and status.
  "sio.transport.websocket": "WebSocket",
  "sio.transport.polling": "long-polling",
  "sio.reason.clientDisconnect": "Disconnected",
  "sio.reason.serverDisconnect": "The server disconnected the socket",
  "sio.reason.transportClose": "The server closed the connection",
  "sio.reason.transportError": "The connection failed",
  "sio.reason.pingTimeout": "The server stopped answering (ping timeout)",
  "sio.reason.parseError": "The server sent a packet that could not be read",
  "sio.reason.connectFailed": "Connecting failed",
  "sio.closed": "Closed",
  // {transport}: "WebSocket" or "long-polling".
  "sio.handshake": "Engine.IO handshake with {url} over {transport}",
  "sio.fallback": "WebSocket failed, trying long-polling:",
  "sio.fallbackError": "WebSocket failed, trying long-polling: {error}",
  "sio.upgraded": "Upgraded to WebSocket",
  "sio.upgradeFailed": "The upgrade to WebSocket failed; staying on long-polling.",
  "sio.upgradeFailedError": "The upgrade to WebSocket failed; staying on long-polling. {error}",
  "sio.connected": "Connected to {namespace} over {transport}",
  "sio.ackTimeout": "ack timeout",
  "sio.ackTimeoutText": "No acknowledgement for “{event}” (#{id}) within {ms} ms",


  // exportHar.ts: exporting requests or the history as a HAR file.
  "har.nothing": "Nothing to export: the folder has no HTTP, GraphQL or SSE request.",
  "har.exported": { one: "Exported {count} request as HAR", other: "Exported {count} requests as HAR" },
  "har.exportedMasked": { one: "Exported {count} request as HAR, with secrets masked", other: "Exported {count} requests as HAR, with secrets masked" },
  "har.sentence": "{text}.",
  // {text}: the export above; {reason}: why the first was left out.
  "har.skipped": { one: "{text}. {count} request was left out: {reason}.", other: "{text}. {count} requests were left out: {reason}." },
  "har.withSecrets": "Export with secrets",
  "har.failed": "Could not export as HAR: {error}",
  // The suggested name of a request's history file.
  "har.historyName": "{name} history",
  "har.historyExported": { one: "Exported {count} history entry as HAR.", other: "Exported {count} history entries as HAR." },
  "har.historyExportedMasked": {
    one: "Exported {count} history entry as HAR, with secrets masked.",
    other: "Exported {count} history entries as HAR, with secrets masked.",
  },
  "har.historyFailed": "Could not export the history: {error}",


  // mockRoutes.ts: where a mock response rule looks, and its operators.
  "mock.source.query": "Query param",
  "mock.source.queryPlaceholder": "name",
  "mock.source.header": "Header",
  "mock.source.json": "JSON body",
  "mock.source.body": "Body text",
  "mock.op.equals": "equals",
  "mock.op.notEquals": "does not equal",
  "mock.op.contains": "contains",
  "mock.op.notContains": "does not contain",
  "mock.op.matches": "matches regex",
  "mock.op.exists": "exists",
  "mock.op.notExists": "does not exist",


  // httpSends.ts: sending (connecting, subscribing) with {{variables}} that
  // are not defined. {list}: "{{token}}, {{baseUrl}}".
  "unresolved.send.title": { one: "Send with an undefined variable?", other: "Send with {count} undefined variables?" },
  "unresolved.send.message": {
    one: "{list} is not defined in the active environment or any other scope. It is sent as written if you send anyway.",
    other: "{list} are not defined in the active environment or any other scope. They are sent as written if you send anyway.",
  },
  "unresolved.send.confirm": "Send anyway",
  "unresolved.connect.title": { one: "Connect with an undefined variable?", other: "Connect with {count} undefined variables?" },
  "unresolved.connect.message": {
    one: "{list} is not defined in the active environment or any other scope. It is used as written if you connect anyway.",
    other: "{list} are not defined in the active environment or any other scope. They are used as written if you connect anyway.",
  },
  "unresolved.connect.confirm": "Connect anyway",
  "unresolved.subscribe.title": { one: "Subscribe with an undefined variable?", other: "Subscribe with {count} undefined variables?" },
  "unresolved.subscribe.message": {
    one: "{list} is not defined in the active environment or any other scope. It is used as written if you subscribe anyway.",
    other: "{list} are not defined in the active environment or any other scope. They are used as written if you subscribe anyway.",
  },
  "unresolved.subscribe.confirm": "Subscribe anyway",


  // exportPostman.ts: "Exported 12 requests in 2 folders as a Postman
  // collection; 1 item left out, 2 warnings."
  "postman.requests": { one: "{count} request", other: "{count} requests" },
  "postman.folders": { one: "{count} folder", other: "{count} folders" },
  "postman.inFolders": "{requests} in {folders}",
  "postman.exported": "Exported {what} as a Postman collection",
  "postman.leftOut": { one: "{count} item left out", other: "{count} items left out" },
  "postman.warnings": { one: "{count} warning", other: "{count} warnings" },
  "postman.list": "{list}, {item}",
  "postman.sentence": "{text}.",
  "postman.withExtra": "{text}; {extra}.",
  "postman.reportTitle": "Postman export report",
  "postman.reportMessage": "Secrets are not exported. These items were left out, or exported with a change:",
  "postman.showReport": "Show report",
  "postman.failed": "Could not export as a Postman collection: {error}",

  // exportZip.ts: "Exported 7 requests in 2 folders as a zip, without
  // secrets; 2 secrets typed into files left out, 1 item left out." The
  // folder count includes the exported folder.
  "zip.requests": { one: "{count} request", other: "{count} requests" },
  "zip.folders": { one: "{count} folder", other: "{count} folders" },
  "zip.inFolders": "{requests} in {folders}",
  "zip.exported": "Exported {what} as a zip, without secrets",
  "zip.secretsRemoved": { one: "{count} secret typed into a file left out", other: "{count} secrets typed into files left out" },
  "zip.leftOut": { one: "{count} item left out", other: "{count} items left out" },
  "zip.warnings": { one: "{count} warning", other: "{count} warnings" },
  "zip.list": "{list}, {item}",
  "zip.sentence": "{text}.",
  "zip.withExtra": "{text}; {extra}.",
  "zip.reportTitle": "Zip export report",
  "zip.reportMessage": "Secrets are not exported. These items were left out, or exported without a value:",
  "zip.showReport": "Show report",
  "zip.failed": "Could not export as a zip: {error}",


  // quickAssert.ts: an assertion added from the response (TEST-04).
  // {assertion}: "JSONPath $.id equals 42".
  "quickAssert.added": "Assertion added: {assertion}",
  // sseStreams.ts: the SSE stream's log.
  "sse.attemptOf": "{attempt} of {max}",
  "sse.end.closed": "The server ended the stream",
  "sse.end.stopped": "Stopped",
  "sse.end.error": "The stream failed",
  "sse.end.ended": "Ended",
  // {end}: one of the above.
  "sse.endedAfter": { one: "{end} after {count} event.", other: "{end} after {count} events." },
  "sse.discarded": "{text} An incomplete event at the end was dropped.",
  "sse.truncated": { one: "the first {shown} of {count} byte", other: "the first {shown} of {count} bytes" },
  // {reason}: why the stream ended, from Go; {attempt}: "2 of 5".
  "sse.reconnecting": "{reason} Reconnecting in {ms} ms (attempt {attempt}).",
  "sse.reconnectingResume": "{reason} Reconnecting in {ms} ms with Last-Event-ID {id} (attempt {attempt}).",


  // tcpSessions.ts: the TCP connection's log.
  "tcp.connecting": "Connecting to {address}",
  "tcp.connected": "Connected to {address}",
  "tcp.connectedTls": "Connected to {address} with {tls}",
  "tcp.connectedFrom": "Connected to {address} from {local}",
  "tcp.connectedFromTls": "Connected to {address} from {local} with {tls}",
  "tcp.disconnecting": "Disconnecting",
  "tcp.closedByServer": "Closed by the server",
  "tcp.disconnected": "Disconnected",
  "tcp.bytes": { one: "{count} byte", other: "{count} bytes" },
  "tcp.closeTotals": "{text} ({sent} sent, {received} received)",

  // udpSessions.ts: the UDP socket's log. {mode}: who may answer.
  "udp.opening": "Opening a UDP socket to {address}",
  "udp.connectedMode": "replies from the target only",
  "udp.unconnectedMode": "replies from any sender",
  "udp.open": "Ready to send to {address} from {local}; {mode}",
  "udp.openVia": "Ready to send to {address} through {via} (UDP ASSOCIATE, relay {relay}); {mode}",
  "udp.closing": "Closing",
  "udp.closed": "Closed",
  "udp.closedTotals": "Closed ({sent} sent, {received} received)",
  "udp.datagrams": { one: "{count} datagram", other: "{count} datagrams" },
  "udp.dropped": "A datagram was dropped",
  // The badge of a received datagram that was cut to the maximum size.
  "udp.peerTruncated": "{peer}, cut",
  "udp.truncated": "cut",


  // copyCurl.ts: copying a request as a curl command.
  "curl.copied": "Copied as cURL",
  "curl.copiedMasked": "Copied as cURL, with secrets masked",
  "curl.sentence": "{text}.",
  // {list}: "{{token}}, {{baseUrl}}".
  "curl.unresolved": {
    one: "{text}. {list} is not defined and left as written.",
    other: "{text}. {list} are not defined and left as written.",
  },
  "curl.withSecrets": "Copy with secrets",
  "curl.failed": "Could not copy as cURL: {error}",
  // snippets.ts: copying the code of the snippet panel.
  "snippet.copied": "Copied.",
  "snippet.copiedMasked": "Copied, with secrets masked.",
  "snippet.copiedRevealed": "Copied, with the real values of the secrets.",
  "snippet.copyFailed": "Could not copy the code: {error}",
  // fileDrop.ts: files dropped on the window to import them.
  "fileDrop.refusedOne": "{file}: {reason}",
  "fileDrop.cannotImport": "PostSonde cannot import this file.",
  "fileDrop.unknownFormats": {
    one: "PostSonde does not recognise the format of {count} file: {names}.",
    other: "PostSonde does not recognise the format of {count} files: {names}.",
  },
  "fileDrop.refused": { one: "{count} file cannot be imported: {names}.", other: "{count} files cannot be imported: {names}." },
  "fileDrop.noProject": "Open a project to import files into it.",
  "fileDrop.readOnly": "This project is read-only; files cannot be imported into it.",
  "fileDrop.notHere": "Files cannot be imported here.",
  // links.ts: following a link of a note.
  "links.title": "Open this link in your browser?",
  "links.mailTitle": "Write an email?",
  "links.open": "Open link",
  "links.openMail": "Open mail app",
  "links.failed": "Could not open the link: {error}",


  // logExport.ts: copying and saving log entries. {what}: "entry", "3
  // records" (the callers' text); {log}: the log's title in a sentence.
  "log.entry": "entry",
  "log.copied": "Copied {what}.",
  "log.copiedMasked": "Copied {what}, secrets masked.",
  "log.copiedJson": "Copied {what} as JSON.",
  "log.copiedJsonMasked": "Copied {what} as JSON, secrets masked.",
  "log.copyFailed": "Could not copy the {what}: {error}",
  "log.saved": "Saved {log} to {path}.",
  "log.savedMasked": "Saved {log} to {path}, secrets masked.",
  "log.saveFailed": "Could not save the {log}: {error}",


  // dataDir.ts: the data directory notice at startup.
  "dataDir.problem": "Data directory: {error}",
  "dataDir.problemMore": "Data directory: {error} (and {count} more)",
  "dataDir.moved": "Your data was moved to {path}. The old folder still has a copy until you delete it.",
  "dataDir.settings": "Settings",


  // grpcCalls.ts: the gRPC call's log.
  "grpc.streamOpened": "Stream opened: {method}",
  "grpc.requestStreamEnded": { one: "Request stream ended after {count} message", other: "Request stream ended after {count} messages" },
  "grpc.ended": "Ended",
  // responseHistory.ts: response examples (HTTP-38).
  "responseExamples.saved": "Saved the response as the example “{name}”.",
  "responseExamples.show": "Show examples",
  "responseExamples.saveFailed": "Could not save the response as an example: {error}",
  "responseExamples.noResponse": "This send got no response.",
  // bodyExamples.ts: names of new body examples (saved in the request file).
  "bodyExamples.first": "Default",
  "bodyExamples.numbered": "Example {n}",

  // mockFromExample.ts: a mock route made from a response (MOCK-03).
  // {route}: "GET /users/:id"; {server}: the mock server's name.
  "mockFromExample.added": "Added {route} to {server}",
  "mockFromExample.responseAdded": "Added the response “{name}” to {route} on {server}",
  "mockFromExample.replaced": "Replaced the response of {route} on {server}",
  // {message}: one of the three above; {error}: why the new server did not start.
  "mockFromExample.notStarted": "{message}. The server did not start: {error}",
  "mockFromExample.show": "Show server",

  // responseCompare.ts: comparing two responses (HTTP-40).
  "compare.noExample": "This example is not there any more.",
} as const satisfies Messages;
