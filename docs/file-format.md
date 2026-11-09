# Project file format

A PostSonde project is a folder of plain JSON files (STO-01, STO-02): one
file per request, one `_folder.json` per folder, one file per environment,
and a workspace file at the root. The format is versioned and documented
here so the files stay readable, diffable and editable by hand for years
(STO-07). There is no database and no hidden state inside the project.

| Piece | Where |
|---|---|
| Types, reading, writing, migrations | `internal/core/storage/projectfile` (`Decode`, `Marshal`, `ReadFile`, `WriteFile`) |
| JSON encoding (formatting, key order, unknown keys) | `internal/core/storage/stablejson` |
| Atomic writes | `internal/core/storage/atomicfile` |
| The examples on this page | `internal/core/storage/projectfile/testdata/examples/*.golden` (a test checks they match this page) |

## Layout

```
acme-api/                       the project folder (any name)
  postsonde.json                workspace file: marks the folder as a PostSonde project
  health.json                   a request at the top level
  import-catalogue.json         a request with a binary file body
  users/                        a folder
    _folder.json                the folder's own settings
    user-by-id.json             a GraphQL request
    user-events.json            a GraphQL subscription
    create-user.json            an HTTP request
    upload-avatar.json          an HTTP request
    admins/                     a nested folder
      _folder.json
  environments/
    production.json             an environment
    staging.json
  fixtures/                     not a folder (no _folder.json): ignored by the tree
    avatar.png
    catalogue.csv
```

- **The project root** is the folder that holds `postsonde.json`. It is the
  top of the request tree; its folders are the collections. The root has
  no `_folder.json`: the workspace file holds its settings.
- **A folder** is a directory that contains `_folder.json`. Directories
  without one (fixtures, scripts, anything else you keep in the project)
  are not part of the tree and PostSonde leaves them alone.
- **A request** is a `.json` file in the root or a folder whose `kind` is a
  request kind (`http`, `graphql`, `tcp`, `udp`, `websocket`, `grpc`, `sse`, `socketio`). The file name is chosen by the app from
  the request's name; the name shown in the app is the `name` member, so it
  may contain characters a file name cannot. How the tree is read and file
  names are chosen is in [collection.md](collection.md).
- **Environments** are the `.json` files directly in `environments/` at the
  root.
- **Reserved names:** `postsonde.json` and `environments` at the root, and
  in every directory `_folder.json`, anything starting with `_` and anything
  starting with `.`. Dot files include `.<name>.tmp-*` leftovers of an
  interrupted save, which are safe to delete.
- **Other files** are left alone: a `.json` file without a `kind` is not a
  project file, and a file whose `kind` this version does not know (for
  example an MQTT request saved by a newer PostSonde) is kept exactly as it
  is.

### What is not in the project

The project holds only what can be shared. Everything local to one
machine or person is kept in PostSonde's own data directory, keyed by the
IDs below, and never written into the project (STO-04):

- secret values of variables, global, environment and folder ones (SEC-01,
  SEC-04), and local values of environment and global variables (SEC-02,
  [environments.md](environments.md#local-values)): the file keeps the
  shared value, a local value replaces it on one computer only;
- passwords, tokens and API keys typed in a request's or folder's auth
  ([Auth](#auth));
- which environment is active, open tabs, expanded folders;
- history, cookies, OAuth tokens, cached GraphQL schemas.

So copying, zipping or committing the project folder never shares secrets;
"Export as zip…" on a folder does exactly that for one folder
([import.md](import.md#folder-zips)).
PostSonde moves a secret typed into a project file by hand to its local
storage the next time it writes that file (PRV-09,
[auth.md](auth.md#how-it-flows)); until then the file is used as it is.

## Common rules

These hold for every file.

- **Encoding.** UTF-8 JSON, one object per file, two-space indentation,
  exactly one trailing newline. `<`, `>` and `&` are written as they are.
- **Head.** Every file starts with `schemaVersion` (a whole number, see
  [Versions](#versions)) and `kind` (`workspace`, `folder`, `http`,
  `graphql`, `tcp`, `websocket`, `grpc`, `sse`, `socketio` or `environment`).
- **IDs.** Every file has an `id`, unique within the project and stable
  across renames and moves. Local data refers to items by ID. PostSonde
  makes IDs as a prefix (`ws_`, `fld_`, `req_`, `env_`) and 16 random hex
  digits; any other non-empty string works. If you copy a file by hand,
  give the copy a new `id`; otherwise PostSonde gives all but one of the
  files a new ID, as [collection.md](collection.md#ids) describes.
- **Member order.** PostSonde writes members in the order the tables below
  list them, so saves diff cleanly. Arrays keep their order.
- **Optional members** are left out when they are empty (`""`, `false`,
  `[]`) and are marked *optional* below. A missing optional member has its
  empty value. Required members are always written.
- **Unknown members are kept.** Members PostSonde does not know, at any
  level (in a header row, a body, the file itself), are kept and written
  back after the known members, sorted by key. This covers files from a
  newer PostSonde and your own notes.
- **Notes.** The `description` of the workspace, a folder or a request is
  its notes, in Markdown (COL-07), written as typed. A folder's and a
  request's are edited and rendered in their Notes tab without loading
  anything ([collection.md](collection.md#notes)); the workspace's is kept
  but not shown yet. The `description` of a row
  (header, parameter, variable, part) is a plain-text note.
- **Values are stored as typed.** `{{variables}}` are never resolved in the
  files, and editor text (bodies, GraphQL queries and variables) is kept
  character for character, even when it is not valid yet.
- **File paths** (file bodies, multipart files, `.proto` files and import
  paths, GraphQL schema files) point at files on disk;
  the files' contents are never copied into project files. A file inside
  the project is stored relative to the project root with `/`
  (`fixtures/avatar.png`), so the project can be moved or shared with its
  fixtures; a file elsewhere is stored as an absolute path, which only
  works on machines that have it at that path. The file dialog chooses
  between the two; a hand-edited relative path is always read from the
  project root. A file that is missing when the request is sent fails the
  send with an error naming the part or the body and the path.

### Reading and writing

A file that PostSonde reads and saves again without changes is
byte-identical. A file you edited by hand is normalized on the first save:
members are put in order, empty optional members are dropped, whitespace is
reformatted. After that it is stable again.

A file that is not valid JSON, has no `kind`, or has a member of the wrong
type (`"headers": "Accept: */*"`) is not loaded and not overwritten; fix it
by hand. The error says which file and, for a syntax error, the line and
column.

## Workspace: `postsonde.json`

| Member | Type | Meaning |
|---|---|---|
| `schemaVersion` | whole number | Format version. |
| `kind` | `"workspace"` | |
| `id` | string | Workspace ID. Local data about the project (side files, recent-workspace list) is keyed by it. |
| `name` | string | Display name. |
| `description` | string, optional | Markdown notes (COL-07). |
| `order` | array of strings, optional | Manual order of the top-level items; see [Order](#order). |
| `variables` | array of [variables](#variables), optional | Global variables, available to every request (ENV-02) and the base environment every environment inherits and overrides (ENV-05, [environments.md](environments.md#base-environment)): the lowest scope of the precedence `global < environment < collection < folder < runtime` (ENV-06, [templates.md](templates.md#scopes-and-precedence)). Edited in the Environments view ("Global variables"); PostSonde writes this file through the collection tree, so the other members are kept. |

```json
{
  "schemaVersion": 1,
  "kind": "workspace",
  "id": "ws_7c1e4b9a2f3d5e60",
  "name": "Acme API",
  "description": "Requests for the Acme public API.",
  "order": [
    "users",
    "health.json"
  ],
  "variables": [
    {
      "name": "apiVersion",
      "value": "v2"
    }
  ]
}
```

## Folder: `_folder.json`

The folder's own settings (COL-03). Its requests and subfolders are the
other entries in its directory.

| Member | Type | Meaning |
|---|---|---|
| `schemaVersion`, `kind` | | `kind` is `"folder"`. |
| `id` | string | Folder ID. |
| `name` | string | Display name. |
| `description` | string, optional | Markdown notes (COL-07), edited in the folder's Notes tab. |
| `order` | array of strings, optional | Manual order of the folder's items; see [Order](#order). |
| `variables` | array of [variables](#variables), optional | Folder variables, inherited by everything inside. On a top-level folder they are the **collection variables** (ENV-03, scope `Collection: <name>`), below it folder variables (ENV-04, `Folder: <name>`); they win over the environment and the global variables, and a subfolder's variable of the same name wins over its parent's. |
| `headers` | array of [rows](#rows), optional | Headers added to every HTTP and GraphQL request inside, subfolders included. A request's own enabled header of the same name (compared ignoring case) wins, then the nearer folder's; disabled rows override nothing. |
| `auth` | [auth](#auth) | Auth for the requests inside that inherit it; `inherit` passes on the auth of the folder above. A typed secret is kept in the folder's local side file, as a request's. |
| `grpc` | object, optional | gRPC defaults for the requests inside: `proto`, a [proto setup](#proto-setup) (its `inherit` is not used) that gRPC requests with `proto.inherit` use when no nearer folder has one. Left out when empty. |
| `server` | object, optional | The folder's local server, a [saved server config](#saved-server-config) (SRV-04). Left out when the folder has none. |

```json
{
  "schemaVersion": 1,
  "kind": "folder",
  "id": "fld_0b8d2e61c4a9f375",
  "name": "Users",
  "order": [
    "user-by-id.json",
    "create-user.json",
    "upload-avatar.json"
  ],
  "variables": [
    {
      "name": "userId",
      "value": "42",
      "description": "A user that exists on staging and production"
    }
  ],
  "headers": [
    {
      "name": "Accept",
      "value": "application/json"
    }
  ],
  "auth": {
    "type": "inherit"
  }
}
```

The app edits `variables`, `headers` and `auth` in the folder's settings
view and writes only those members, keeping the others as they are
([collection.md](collection.md#folder-settings)); the Server tab writes
only `server`.

A folder that shares a proto setup with its gRPC requests:

```json
{
  "schemaVersion": 1,
  "kind": "folder",
  "id": "fld_6e2a9c0d41b7f538",
  "name": "Shop",
  "auth": {
    "type": "inherit"
  },
  "grpc": {
    "proto": {
      "files": [
        "proto/shop/v1/orders.proto"
      ],
      "importPaths": [
        "proto",
        "/usr/local/include"
      ]
    }
  }
}
```

### Saved server config

A folder can define one local server ([servers.md](servers.md#saved-server-configs),
SRV-04), edited in the folder's **Server** tab. The file holds only the
definition: opening the project never starts the server, the user does.
Every member but `type` is optional and left out at its default.

| Member | Type | Meaning |
|---|---|---|
| `type` | string | The kind of server: `mock_http`, `echo_http`, `websocket`, `tcp`, `webhook`, `sse`, `udp`, `grpc_mock`, `socketio` or `graphql_mock`. A kind this version does not know is kept, and the server cannot be started. |
| `name` | string | A label; left out, the kind's label ("Mock HTTP"). |
| `host` | string | The bind address: left out or `localhost` for loopback (`127.0.0.1` and `::1`), or an IP address. |
| `port` | number | 1–65535; left out (0) for any free port, picked at every start. |
| `allowLan` | boolean | Lets a `host` other than loopback listen (SRV-02); without it such a server fails to start. |
| `tls` | object | Serves TLS (SRV-05: HTTPS, `wss`, TLS over TCP, gRPC over h2; not for `udp`); left out for plain text. `certificate` is `generated` (a certificate for localhost made at every start by PostSonde's local CA, [servers.md](servers.md#tls)) or `files` with `certFile` and `keyFile` (PEM; `keyFile` left out when the certificate file holds the key) or `pfxFile` (PKCS#12). Paths are absolute. A passphrase is never stored here. |
| `mock` | object | `mock_http`: `routes` and the matching options `strictTrailingSlash` and `caseInsensitive`. A route has `id`, `method` (a method or `ANY`), `path` (a pattern, [mock-routes.md](mock-routes.md)), `status`, `headers` ([rows](#rows)), `body` (a [body](#body) in mode `none` or `raw`; a mock body's `type` can also be `base64`, a binary body written in Base64, [mock-routes.md](mock-routes.md#responses)), `delay` (`mode` `fixed` with `ms`, or `random` with `minMs` and `maxMs`; left out for none), `responses`, `templated` and `disabled`. `templated: true` (MOCK-06; left out when false, so older files keep their bodies literal) renders the body and header values with the request's values and dynamic values ([mock-routes.md](mock-routes.md#templates)). `responses` (MOCK-05, MOCK-07; left out for none) are the route's other responses, tried in order before its own: each has `name` (for the `X-PostSonde-Response` request header), `status`, `headers`, `body`, `rules` and `templated`, each rule `source` (`query`, `header`, `body` or `json`), `name` (the parameter, header or JSONPath), `op` and `value` ([mock-routes.md](mock-routes.md#responses)). Left out, the example route `GET /hello`. |
| `webSocket` | object | `websocket`: `path` (left out: `/`; `*` for any path), `subprotocols`, `echo` (send a message back when no rule answers it), `maxMessageSize` in bytes (left out: 16 MiB) and `rules`, the auto-reply rules in order ([servers.md](servers.md#auto-reply-rules)): `match` (`exact`, `contains` or `regex`), `pattern`, `reply` (with `regex`, `$1`, `${1}`, `$name`, `${name}` are capture groups and `$$` is a `$`), `delayMs` (left out: none) and `disabled`. Left out when there are none; files without it load as before. |
| `webhook` | object | `webhook`: the response to every request, `status`, `headers` ([rows](#rows)) and `body`. Left out, `200` with no body. |
| `sse` | object | `sse` (SSRV-04): `path` (left out: `/events`; `*` for any path), `allowOrigin` (the CORS origin allowed to subscribe, `*` for any; left out for none), `keepAliveSec` (the keep-alive comment interval; left out for none), `retryMs` (the `retry` field sent to new subscribers; left out for none), `autoId` (number the events sent without an ID) and `timers`, each `event` (left out: `message`), `data`, `id` (all three may hold dynamic values such as `{{$timestamp}}`, [templates.md](templates.md)), `intervalMs` (100 to 86,400,000) and `disabled` ([servers.md](servers.md#sse-server)). Left out, a new server's settings: `/events`, a keep-alive every 15 s. |
| `udp` | object | `udp` (SSRV-06): `echo` (send every datagram back), `fixedReply` with `reply` (sent to the sender of every datagram, after the echo) and `replyFormat` (`text`, left out, or `hex`). Left out, echo on ([servers.md](servers.md#udp-server)). |
| `grpcMock` | object | `grpc_mock` (SSRV-07): `files` and `importPaths` (the `.proto` files served and their import roots, stored like a gRPC request's: relative to the project root with `/` when inside it, else absolute), `noReflection` (turns server reflection off; left out, it is served) and `methods`, the canned responses, each `service` (fully qualified), `method`, `messages` (JSON text of the output type, kept as typed; a unary or client-streaming method sends the first), `status` (0–16; left out for OK), `statusMessage`, `headers` and `trailers` ([rows](#rows); lower-case keys, values of `-bin` keys in Base64), `delayMs`, `intervalMs` (between streamed messages) and `bidi` (`each`, left out, `stream` or `echo`) ([servers.md](servers.md#grpc-mock-server)). Left out, no files and reflection on. |
| `socketIo` | object | `socketio` (SSRV-03): `path` (the Engine.IO path; left out: `/socket.io/`), `namespaces` (accepted besides `/`; left out for any), `auth` (a JSON object as text whose members a CONNECT's auth payload must have; left out for none), `transports` (`websocket` or `polling` to accept one only; left out for both), `allowOrigin` (the CORS origin of long-polling responses, `*` for any; left out for none), `pingIntervalMs` and `pingTimeoutMs` (left out: 25000 and 20000), `maxPayload` in bytes (left out: 1000000), `echo` (emit events no rule answers back), `ackMode` (`empty` or `none`; left out, an ack echoes the event's arguments) and `rules`, the auto-reply rules in order: `match` (`exact`, `contains` or `regex`, on the event name), `pattern`, `ack` (the acknowledgement's arguments as JSON text), `emit` and `emitArgs` (an event sent back), `delayMs` and `disabled`; arguments may hold dynamic values such as `{{$timestamp}}` ([servers.md](servers.md#socketio-server)). Left out, a new server's settings. |
| `graphqlMock` | object | `graphql_mock` (SSRV-08): `path` (the endpoint; left out: `/graphql`), `schemaFile` (an SDL file, or a saved introspection result ending in `.json`, stored like a GraphQL request's schema file: relative to the project root with `/` when inside it, else absolute) or `schemaText` (pasted SDL or introspection JSON, used when there is no file; with neither, the server serves no schema), `noIntrospection` (turns `__schema` and `__type` off), `listLength` (items in a generated list, 0–100; left out: 2), `nullPercent` (how often a nullable field is generated as null, 0–100), `seed` (varies the generated data), `allowOrigin` (the CORS origin allowed, `*` for any; left out for none) and `overrides`, the static responses in order, first match wins: `target` (`Type.field`, or a scalar or enum type for all its values), `args` (a JSON object as text the field's arguments must match; left out for any), `mode` (`error`, or left out for a value), `value` (JSON text; members an object leaves out are generated), `error` and `code` (the field error's message and `extensions.code`) and `disabled` ([servers.md](servers.md#graphql-mock-server)). Left out, a new server's settings with the example schema. |

The members of the other kinds are kept but not used (a `webSocket`
member on a mock server). Server configs hold no secrets: `tls` names
certificate files but never holds a key or a passphrase (the passphrase of
a PKCS#12 file or an encrypted key stays in the app's local secret
storage, [privacy.md](privacy.md)); a header value typed into a mock route
or a webhook response is written as it is.

Unlike the other file paths of a project, the certificate paths of `tls`
are absolute, as the file dialog gives them: a project opened on another
machine needs the files at the same place, or the generated certificate
(which needs no file and works everywhere). A `tls` with a `certificate`
this version does not know is reported as a problem of the folder and the
server is not defined.

```json
"server": {
  "type": "websocket",
  "port": 8443,
  "tls": {
    "certificate": "files",
    "certFile": "/Users/me/certs/dev.pem",
    "keyFile": "/Users/me/certs/dev-key.pem"
  }
}
```

A mock server:

```json
{
  "schemaVersion": 1,
  "kind": "folder",
  "id": "fld_93d0b7a2e5c14f68",
  "name": "Mocks",
  "auth": {
    "type": "inherit"
  },
  "server": {
    "type": "mock_http",
    "name": "Users API mock",
    "port": 8080,
    "mock": {
      "routes": [
        {
          "id": "rt_4f1a9c2e7b30",
          "method": "GET",
          "path": "/users/:id",
          "status": 200,
          "headers": [
            {
              "name": "X-Mock",
              "value": "true"
            }
          ],
          "body": {
            "mode": "raw",
            "type": "json",
            "text": "{\"id\": 42, \"name\": \"Zoë\"}"
          },
          "responses": [
            {
              "name": "admin",
              "status": 200,
              "body": {
                "mode": "raw",
                "type": "json",
                "text": "{\"id\": 1, \"role\": \"admin\"}"
              },
              "rules": [
                {
                  "source": "query",
                  "name": "role",
                  "op": "equals",
                  "value": "admin"
                },
                {
                  "source": "header",
                  "name": "X-Debug",
                  "op": "exists"
                }
              ]
            },
            {
              "name": "not-found",
              "status": 404,
              "body": {
                "mode": "none"
              }
            }
          ]
        },
        {
          "id": "rt_b62d08e1f5a9",
          "method": "POST",
          "path": "/users",
          "status": 503,
          "body": {
            "mode": "none"
          },
          "delay": {
            "mode": "random",
            "minMs": 100,
            "maxMs": 900
          }
        }
      ]
    }
  }
}
```

A WebSocket server on port 8081 that answers `ping` with `pong` and
`get 42` with a JSON reply after 200 ms, and echoes every other message
(the third rule is disabled):

```json
{
  "schemaVersion": 1,
  "kind": "folder",
  "id": "fld_2c7e5a91d0b3f846",
  "name": "Chat",
  "auth": {
    "type": "inherit"
  },
  "server": {
    "type": "websocket",
    "host": "localhost",
    "port": 8081,
    "webSocket": {
      "path": "/ws",
      "subprotocols": [
        "chat"
      ],
      "echo": true,
      "rules": [
        {
          "match": "exact",
          "pattern": "ping",
          "reply": "pong"
        },
        {
          "match": "regex",
          "pattern": "^get (?P<id>\\d+)$",
          "reply": "{\"id\": ${id}, \"name\": \"Zoë\"}",
          "delayMs": 200
        },
        {
          "match": "contains",
          "pattern": "debug",
          "reply": "debug is off",
          "disabled": true
        }
      ]
    }
  }
}
```

An SSE server on port 8082 that any origin may subscribe to (a page served elsewhere can open an `EventSource` on it), numbers its events, and sends a `price` event every second with the time rendered at each tick; the heartbeat timer is off:

```json
{
  "schemaVersion": 1,
  "kind": "folder",
  "id": "fld_5b8e1d3a7c204f96",
  "name": "Prices",
  "auth": {
    "type": "inherit"
  },
  "server": {
    "type": "sse",
    "port": 8082,
    "sse": {
      "path": "/events",
      "allowOrigin": "*",
      "keepAliveSec": 15,
      "retryMs": 3000,
      "autoId": true,
      "timers": [
        {
          "event": "price",
          "data": "{\"symbol\": \"ACME\", \"at\": {{$timestamp}}}",
          "intervalMs": 1000
        },
        {
          "event": "heartbeat",
          "data": "{{$isoTimestamp}}",
          "intervalMs": 30000,
          "disabled": true
        }
      ]
    }
  }
}
```

A UDP server on port 9125 that echoes every datagram, then sends the byte `0x06`:

```json
{
  "schemaVersion": 1,
  "kind": "folder",
  "id": "fld_e7a2c9046d1b3f85",
  "name": "Telemetry",
  "auth": {
    "type": "inherit"
  },
  "server": {
    "type": "udp",
    "port": 9125,
    "udp": {
      "echo": true,
      "fixedReply": true,
      "reply": "06",
      "replyFormat": "hex"
    }
  }
}
```

A gRPC mock server on port 50051 serving `protos/shop/v1/orders.proto`:
`GetOrder` answers one message after 250 ms with a header, `ListOrders`
streams two messages half a second apart, `CancelOrder` fails with
`NOT_FOUND` and a binary trailer, `Chat` echoes what the client sends.
Methods not listed answer `UNIMPLEMENTED`:

```json
{
  "schemaVersion": 1,
  "kind": "folder",
  "id": "fld_3c9a1e7b5d20f846",
  "name": "Orders mock",
  "auth": {
    "type": "inherit"
  },
  "server": {
    "type": "grpc_mock",
    "port": 50051,
    "grpcMock": {
      "files": [
        "protos/shop/v1/orders.proto"
      ],
      "importPaths": [
        "protos"
      ],
      "methods": [
        {
          "service": "shop.v1.Orders",
          "method": "GetOrder",
          "messages": [
            "{\n  \"id\": \"o-1\",\n  \"total\": {\"currency\": \"EUR\", \"units\": \"42\"}\n}"
          ],
          "headers": [
            {
              "name": "x-mock",
              "value": "postsonde"
            }
          ],
          "delayMs": 250
        },
        {
          "service": "shop.v1.Orders",
          "method": "ListOrders",
          "messages": [
            "{\"id\": \"o-1\"}",
            "{\"id\": \"o-2\"}"
          ],
          "intervalMs": 500
        },
        {
          "service": "shop.v1.Orders",
          "method": "CancelOrder",
          "status": 5,
          "statusMessage": "no such order",
          "trailers": [
            {
              "name": "x-trace-bin",
              "value": "AAEC"
            }
          ]
        },
        {
          "service": "shop.v1.Orders",
          "method": "Chat",
          "bidi": "echo"
        }
      ]
    }
  }
}
```

A Socket.IO server with two namespaces, a required token and auto-reply rules:

```json
{
  "schemaVersion": 1,
  "kind": "folder",
  "id": "fld_8d2f6a0c4e1b9537",
  "name": "Live",
  "auth": {
    "type": "inherit"
  },
  "server": {
    "type": "socketio",
    "port": 3000,
    "socketIo": {
      "namespaces": [
        "/admin",
        "/chat"
      ],
      "auth": "{\"token\":\"dev-token\"}",
      "allowOrigin": "http://localhost:5173",
      "echo": true,
      "ackMode": "none",
      "rules": [
        {
          "match": "exact",
          "pattern": "ping",
          "ack": "[\"pong\", {\"at\": {{$timestamp}}}]"
        },
        {
          "match": "regex",
          "pattern": "^order:",
          "emit": "order:updated",
          "emitArgs": "{\"status\": \"shipped\"}",
          "delayMs": 500
        },
        {
          "match": "contains",
          "pattern": "debug",
          "ack": "\"off\"",
          "disabled": true
        }
      ]
    }
  }
}
```

A GraphQL mock server serving an SDL file of the project, with a static
product, a not-found error and a disabled override of a custom scalar:

```json
{
  "schemaVersion": 1,
  "kind": "folder",
  "id": "fld_3a9c5e1f7b2d4068",
  "name": "Catalog mock",
  "auth": {
    "type": "inherit"
  },
  "server": {
    "type": "graphql_mock",
    "port": 4000,
    "graphqlMock": {
      "schemaFile": "schema/catalog.graphql",
      "listLength": 5,
      "nullPercent": 10,
      "seed": 42,
      "allowOrigin": "*",
      "overrides": [
        {
          "target": "Query.product",
          "args": "{\"id\": \"1\"}",
          "value": "{\n  \"id\": \"1\",\n  \"name\": \"Teapot\"\n}"
        },
        {
          "target": "Query.product",
          "args": "{\"id\": \"404\"}",
          "mode": "error",
          "error": "Product not found.",
          "code": "NOT_FOUND"
        },
        {
          "target": "DateTime",
          "value": "\"2026-01-01T00:00:00Z\"",
          "disabled": true
        }
      ]
    }
  }
}
```

### Order

`order` lists entry names: a request's file name (`create-user.json`) or a
folder's directory name (`admins`). Listed entries come first, in that
order. Entries not listed follow, sorted by name ignoring case. Names that
match no entry are ignored and dropped on the next save. Unknown and broken
files are entries too. [collection.md](collection.md#order) has the exact
matching rules.

## HTTP request: kind `http`

| Member | Type | Meaning |
|---|---|---|
| `schemaVersion`, `kind` | | `kind` is `"http"`. |
| `id` | string | Request ID. |
| `name` | string | Display name. |
| `description` | string, optional | Markdown notes. |
| `method` | string | Method token as typed: `GET`, `POST`, `QUERY` or a custom one (HTTP-01…03). Never empty. |
| `url` | string | The URL as shown in the editor, with the enabled query parameters (HTTP-04). One that starts with a single `/` is relative: it is sent to the `{{baseUrl}}` variable followed by that path (COL-08, [environments.md](environments.md#base-urls)). |
| `params` | array of [rows](#rows), optional | The query-parameter table, disabled rows included (HTTP-06). The editor keeps it in step with `url`. |
| `pathParams` | array of [rows](#rows), optional | Values for the `:name` and `{name}` placeholders in `url` (HTTP-05). |
| `headers` | array of [rows](#rows), optional | Headers (HTTP-07). |
| `body` | [body](#body) | The body (HTTP-10…15); with body examples, the active example's. |
| `bodyExamples` | array of [body examples](#body-examples), optional | Named bodies, one of them active (HTTP-17). Missing: `body` is the only body. |
| `auth` | [auth](#auth) | |
| `settings` | [settings](#settings), optional | Execution settings that differ from the app's. |
| `assertions` | array of [assertions](#assertions), optional | Checks run against every response (TEST-01). |
| `extractions` | array of [extraction rules](#extractions), optional | Values taken from every response into variables (TEST-05). Missing: none. |
| `responseExamples` | array of [response examples](#response-examples), optional | Responses saved with the request (HTTP-38). Missing: none. |

A request with no body, inheriting auth:

```json
{
  "schemaVersion": 1,
  "kind": "http",
  "id": "req_5a0f93c7e2b14d68",
  "name": "Health check",
  "method": "GET",
  "url": "{{baseUrl}}/health",
  "body": {
    "mode": "none"
  },
  "auth": {
    "type": "inherit"
  }
}
```

A request with parameters, headers, a JSON body, its own settings and
assertions:

```json
{
  "schemaVersion": 1,
  "kind": "http",
  "id": "req_c93e1a7b40f2d856",
  "name": "Create user",
  "description": "Creates a user in an organisation. Needs the `users:write` scope.",
  "method": "POST",
  "url": "{{baseUrl}}/orgs/:org/users?notify=true",
  "params": [
    {
      "name": "notify",
      "value": "true"
    },
    {
      "name": "dryRun",
      "value": "true",
      "description": "Validate only",
      "disabled": true
    }
  ],
  "pathParams": [
    {
      "name": "org",
      "value": "acme"
    }
  ],
  "headers": [
    {
      "name": "Content-Type",
      "value": "application/json"
    },
    {
      "name": "X-Request-Id",
      "value": "{{$uuid}}",
      "disabled": true
    }
  ],
  "body": {
    "mode": "raw",
    "type": "json",
    "text": "{\n  \"name\": \"Zoë <admin>\",\n  \"email\": \"zoe@example.com\"\n}"
  },
  "auth": {
    "type": "inherit"
  },
  "settings": {
    "timeoutMs": 30000,
    "followRedirects": false
  },
  "assertions": [
    {
      "target": "status",
      "op": "equals",
      "expected": "201"
    },
    {
      "target": "jsonpath",
      "expr": "$.id",
      "op": "is_type",
      "expected": "string"
    },
    {
      "target": "header",
      "expr": "Location",
      "op": "contains",
      "expected": "/users/"
    },
    {
      "target": "time",
      "op": "lt",
      "expected": "{{maxLatency}}",
      "disabled": true
    }
  ]
}
```

A multipart upload:

```json
{
  "schemaVersion": 1,
  "kind": "http",
  "id": "req_18d4b6f0a3c95e27",
  "name": "Upload avatar",
  "method": "PUT",
  "url": "{{baseUrl}}/users/{{userId}}/avatar",
  "body": {
    "mode": "multipart",
    "parts": [
      {
        "name": "caption",
        "value": "Profile photo"
      },
      {
        "name": "image",
        "value": "",
        "file": "fixtures/avatar.png",
        "contentType": "image/png"
      }
    ]
  },
  "auth": {
    "type": "none"
  }
}
```

A binary file body, sent with its own Content-Type:

```json
{
  "schemaVersion": 1,
  "kind": "http",
  "id": "req_7b2e9c41d0a8f365",
  "name": "Import catalogue",
  "method": "POST",
  "url": "{{baseUrl}}/catalogue/import",
  "body": {
    "mode": "file",
    "path": "fixtures/catalogue.csv",
    "contentType": "text/csv; charset=utf-8"
  },
  "auth": {
    "type": "inherit"
  }
}
```

A SOAP 1.1 call (HTTP-18, [below](#soap-envelopes)) with two named body
examples; `ACME` is active, so its body is `body`:

```json
{
  "schemaVersion": 1,
  "kind": "http",
  "id": "req_2e6c9a0d4b7f1358",
  "name": "Get quote",
  "method": "POST",
  "url": "{{baseUrl}}/StockQuote",
  "headers": [
    {
      "name": "Content-Type",
      "value": "text/xml; charset=utf-8"
    },
    {
      "name": "SOAPAction",
      "value": "\"urn:example:GetQuote\""
    }
  ],
  "body": {
    "mode": "raw",
    "type": "xml",
    "text": "<soap:Envelope xmlns:soap=\"http://schemas.xmlsoap.org/soap/envelope/\">\n  <soap:Body>\n    <m:GetQuote xmlns:m=\"urn:example\"><m:Symbol>ACME</m:Symbol></m:GetQuote>\n  </soap:Body>\n</soap:Envelope>"
  },
  "bodyExamples": [
    {
      "name": "Empty",
      "body": {
        "mode": "none"
      }
    },
    {
      "name": "ACME",
      "active": true
    }
  ],
  "auth": {
    "type": "none"
  }
}
```

### Body examples

`bodyExamples` (HTTP-17) lists a request's named bodies in the order the
editor shows them. One item is `active`: its body is the request's `body`,
the one that is sent, and it has no `body` member of its own. Switching the
active example moves the bodies between `body` and the list; nothing else
in the file changes, so a PostSonde without examples still sends the
active body and keeps the list as an unknown member.

| Member | Type | Meaning |
|---|---|---|
| `name` | string | The example's name as shown in the editor. |
| `active` | `true`, optional | This example is `body`. |
| `body` | [body](#body), optional | The example's body; left out for the active one. |

A request without `bodyExamples` has one unnamed body, as before. A list
without an `active` item is read as if the request's `body` were an
unnamed first example; when several items are `active`, the first one is.

### Response examples

`responseExamples` (HTTP-38) are responses saved with an `http` or
`graphql` request, in the order the request's Examples tab lists them.
Like a Postman example, each one pairs a response with the request that
got it: the request **as it was sent** (variables resolved, every header
field written), not the editor's request with its `{{variables}}`. They
are for reading later, comparing and mocking; nothing in them is sent or
checked. A file without the member has no examples, as before, and an
older PostSonde keeps the list as an unknown member.

| Member | Type | Meaning |
|---|---|---|
| `name` | string | The example's name. Saving one names it by its status (`200 OK`); it can be renamed. |
| `savedAt` | string, optional | When it was saved, RFC 3339 in UTC. |
| `request` | object, optional | The request as sent: `method`, `url`, `headers` ([rows](#rows), of which only `name` and `value` are used; HTTP/2 pseudo-header fields are left out) and `body` (a [body](#body): saved from a send, in mode `raw` with its `type` from the Content-Type, left out when none was sent or it was not text; imported from Postman, in the mode it had there). Left out when not known (an imported example without one). |
| `statusCode` | number | The response's status code. |
| `statusText` | string, optional | Its reason phrase (`OK`, `Not Found`). |
| `headers` | array of [rows](#rows), optional | The response's header fields in the order received; only `name` and `value` are used. |
| `body` | string, optional | The response body as text, or its bytes in standard Base64 when `bodyEncoding` is `"base64"`. |
| `bodyEncoding` | `"base64"`, optional | The body is not UTF-8 text and is kept as Base64. |
| `bodyClipped` | `true`, optional | `body` holds only the start of the body. |
| `bodySize` | number, optional | With `bodyClipped`, the size of the whole body in bytes. |

- **Size.** Bodies are kept inline, never as side files, and at most
  **64 KiB** of each (`projectfile.ResponseExampleMaxBody`, the request
  history's limit, [history.md](history.md#what-is-recorded)): a longer
  body is saved clipped, with `bodyClipped` and `bodySize`, and the
  example view says so. Keeping them in the request file keeps examples
  with the request through renames, moves, copies and commits; the limit
  keeps the file small enough to diff and edit. A longer body written by
  hand is read as it is.
- **Secrets.** An example is made from the request history, where the
  secrets of the send are already masked as `••••••`
  ([history.md](history.md#secrets)), and masked again with every secret
  PostSonde keeps when it is saved (secret variables, typed auth
  secrets, certificate passphrases; [history.md](history.md#response-examples)).
  So a project file never holds a secret through an example. Examples
  imported from Postman have the credentials of their `Authorization`
  headers masked.
- **Postman.** The import maps a Postman item's `response` list onto
  `responseExamples` and the export writes them back
  ([import.md](import.md#postman-files)): `name`, `originalRequest`
  (method, header, body, url) is `request`, `code` is `statusCode`,
  `status` is `statusText`, `header` is `headers`, `body` is `body`.
  A Base64 body is not exported (Postman keeps text), and `savedAt` has
  no Postman member.

A request with two examples, one saved from a send and one written by
hand:

```json
{
  "schemaVersion": 1,
  "kind": "http",
  "id": "req_4d1b7e9a0c6f2835",
  "name": "Get user",
  "method": "GET",
  "url": "{{baseUrl}}/users/:id",
  "pathParams": [
    {
      "name": "id",
      "value": "{{userId}}"
    }
  ],
  "body": {
    "mode": "none"
  },
  "auth": {
    "type": "inherit"
  },
  "responseExamples": [
    {
      "name": "200 OK",
      "savedAt": "2026-09-23T10:15:02Z",
      "request": {
        "method": "GET",
        "url": "https://api.acme.example/users/42",
        "headers": [
          {
            "name": "Host",
            "value": "api.acme.example"
          },
          {
            "name": "Accept",
            "value": "application/json"
          },
          {
            "name": "Authorization",
            "value": "Bearer ••••••"
          }
        ]
      },
      "statusCode": 200,
      "statusText": "OK",
      "headers": [
        {
          "name": "Content-Type",
          "value": "application/json"
        },
        {
          "name": "Etag",
          "value": "\"u42-7\""
        }
      ],
      "body": "{\"id\":\"42\",\"name\":\"Zoë\"}"
    },
    {
      "name": "Not found",
      "savedAt": "2026-09-23T10:16:40Z",
      "statusCode": 404,
      "statusText": "Not Found",
      "headers": [
        {
          "name": "Content-Type",
          "value": "application/problem+json"
        }
      ],
      "body": "{\"title\":\"No such user\"}"
    }
  ]
}
```

### SOAP envelopes

A SOAP call (HTTP-18) is an ordinary request: a raw XML `body` holding the
envelope and the headers SOAP needs, which the editor's SOAP helper writes
([http-client.md](http-client.md#soap-helper)). Nothing SOAP-specific is
stored.

## GraphQL request: kind `graphql`

| Member | Type | Meaning |
|---|---|---|
| `schemaVersion`, `kind` | | `kind` is `"graphql"`. |
| `id` | string | Request ID. |
| `name` | string | Display name. |
| `description` | string, optional | Markdown notes. |
| `method` | string, optional | Left out for the default, POST with a JSON body; `"GET"` for GET mode (GQL-02). How each is sent: [graphql.md](graphql.md#sending-gql-02). |
| `url` | string | The endpoint; a relative one (`/graphql`) is sent to `{{baseUrl}}` as for `http`. |
| `query` | string | The GraphQL document as typed (GQL-01). |
| `variables` | string, optional | The variables editor's JSON text as typed (GQL-03). It is text, not a JSON object, so half-typed JSON is saved as it is. |
| `operationName` | string, optional | The operation to run when `query` has several (GQL-04). |
| `connectionParams` | string, optional | The connection params editor's JSON text as typed (GQL-33): the `connection_init` payload when an operation runs over WebSocket, e.g. an auth token. Text like `variables`; left out when empty. Files without it load as before. How it is sent: [graphql-subscriptions.md](graphql-subscriptions.md#running-subscriptions-gql-33-gql-34). |
| `schemaFile` | string, optional | A local GraphQL SDL file (`.graphql`, `.graphqls`) the editor takes the schema from instead of introspection (GQL-11), for offline use or when introspection is disabled. A [file path](#common-rules): relative to the project root with `/` when inside the project, absolute otherwise. Left out: the schema comes from introspection. The file is read, never copied into the project file; how it is used: [graphql.md](graphql.md#schema-from-a-file-gql-11). |
| `extensions` | string, optional | The extensions editor's JSON text as typed (GQL-06): the request's `extensions` object, e.g. a persisted query's hash. Text like `variables`; left out when empty. Files without it load as before. How it is sent: [graphql.md](graphql.md#extensions-gql-06). |
| `files` | array of [multipart parts](#body) (a part as in a `multipart` body), optional | Files uploaded with the GraphQL multipart request spec (GQL-07). Each part's `name` is the variable path the file goes to (`"variables.input.files.0"`), `file` the file (a [file path](#common-rules), as for a multipart body part), `contentType`, `description` and `disabled` as for a part; `value` is unused. Left out when empty; files without it load as before. How they are sent: [graphql.md](graphql.md#file-uploads-gql-07). |
| `headers` | array of [rows](#rows), optional | Headers. |
| `auth` | [auth](#auth) | |
| `settings` | [settings](#settings), optional | Execution settings that differ from the app's. `omitHeaders` may name `"Apollo-Require-Preflight"`, the header sent with files. `graphqlTransport` (below) picks the transport of each operation type, `graphqlSchemaRefresh` (below) the opt-in schema auto-refresh. |
| `assertions` | array of [assertions](#assertions), optional | Checks run against every response of a query or mutation (TEST-01); not against subscription events. |
| `extractions` | array of [extraction rules](#extractions), optional | Values taken from every response of a query or mutation into variables (TEST-05); not from subscription events. |
| `responseExamples` | array of [response examples](#response-examples), optional | Responses saved with the request (HTTP-38), as for `http`. |

```json
{
  "schemaVersion": 1,
  "kind": "graphql",
  "id": "req_e4a7c2019b5d83f6",
  "name": "User by ID",
  "url": "{{baseUrl}}/graphql",
  "query": "query UserById($id: ID!) {\n  user(id: $id) {\n    id\n    name\n  }\n}",
  "variables": "{\n  \"id\": \"{{userId}}\"\n}",
  "operationName": "UserById",
  "auth": {
    "type": "inherit"
  }
}
```

A subscription with connection params (`users/user-events.json`):

```json
{
  "schemaVersion": 1,
  "kind": "graphql",
  "id": "req_9c0d4e2b7a15f863",
  "name": "User events",
  "url": "{{baseUrl}}/graphql",
  "query": "subscription UserEvents($id: ID!) {\n  userEvents(id: $id) {\n    type\n    at\n  }\n}",
  "variables": "{\n  \"id\": \"{{userId}}\"\n}",
  "connectionParams": "{\n  \"authToken\": \"{{token}}\"\n}",
  "auth": {
    "type": "inherit"
  }
}
```

`settings.graphqlTransport` (GQL-35) picks the transport of each
operation type. Every member is optional and left out for its default;
`graphqlTransport` itself is left out when all are defaults, so files
written before it load and run as before. A value this version does not
know is kept but fails the run with `request.graphql_invalid_transport`.
How each runs: [graphql-subscriptions.md](graphql-subscriptions.md#transport-per-operation-gql-35).

| Member | Type | Meaning |
|---|---|---|
| `query`, `mutation` | `"http"`, `"ws"`, `"ws-legacy"` or `"sse"`, optional | `http`: GraphQL over HTTP (GQL-02); `ws`: WebSocket with graphql-transport-ws (GQL-30); `ws-legacy`: WebSocket with the legacy subscriptions-transport-ws, subprotocol `graphql-ws` (GQL-31); `sse`: GraphQL over Server-Sent Events (GQL-32). Missing: `http`. |
| `subscription` | `"ws"`, `"ws-legacy"` or `"sse"`, optional | As above; `http` is refused. Missing: `ws`. |

Queries over WebSocket and subscriptions over SSE (`ticker.json`):

```json
{
  "schemaVersion": 1,
  "kind": "graphql",
  "id": "req_8a3f1c6e0b92d547",
  "name": "Ticker",
  "url": "{{baseUrl}}/graphql",
  "query": "subscription Ticker {\n  ticker {\n    symbol\n    price\n  }\n}",
  "auth": {
    "type": "inherit"
  },
  "settings": {
    "graphqlTransport": {
      "query": "ws",
      "subscription": "sse"
    }
  }
}
```

`settings.graphqlSchemaRefresh` (GQL-19) is the opt-in schema
auto-refresh. Both members are off when left out, and the member itself
is left out when both are off, so files without it (all files written
before it) never refresh by themselves: the schema is fetched only from
the Fetch schema / Refresh button. Unknown members inside are kept. How
it runs: [graphql.md](graphql.md#schema-refresh-gql-19).

| Member | Type | Meaning |
|---|---|---|
| `onSend` | `true` or `false`, optional | Fetch the schema again after a send whose response says the server's schema changed: the server refused the document in validation although the cached schema accepts it. Missing: `false`. |
| `intervalMinutes` | whole number, optional | Fetch the schema every so many minutes while the request's editor is open; the editor offers 5, 15, 30 and 60. Missing or `0`: never. |

A request that refreshes its schema after rejected sends and every half
hour (`catalogue/catalogue.json`):

```json
{
  "schemaVersion": 1,
  "kind": "graphql",
  "id": "req_3d7f2a9c5e1b8046",
  "name": "Catalogue",
  "url": "{{baseUrl}}/graphql",
  "query": "{\n  products {\n    id\n  }\n}",
  "auth": {
    "type": "inherit"
  },
  "settings": {
    "graphqlSchemaRefresh": {
      "onSend": true,
      "intervalMinutes": 30
    }
  }
}
```

A request whose schema comes from an SDL file in the project
(`catalogue/products-offline.json`):

```json
{
  "schemaVersion": 1,
  "kind": "graphql",
  "id": "req_5b8e1d3c9f07a246",
  "name": "Products offline",
  "url": "{{baseUrl}}/graphql",
  "query": "{\n  products {\n    id\n  }\n}",
  "schemaFile": "schemas/catalogue.graphql",
  "auth": {
    "type": "inherit"
  }
}
```

An upload with a persisted-query extension (`users/upload-avatar.json`):

```json
{
  "schemaVersion": 1,
  "kind": "graphql",
  "id": "req_2f6a9c1e4b7d0835",
  "name": "Upload avatar",
  "url": "{{baseUrl}}/graphql",
  "query": "mutation Upload($file: Upload!, $docs: [Upload!]!) {\n  upload(file: $file, docs: $docs)\n}",
  "variables": "{\n  \"docs\": [null, null]\n}",
  "extensions": "{\"persistedQuery\": {\"version\": 1, \"sha256Hash\": \"{{hash}}\"}}",
  "files": [
    {
      "name": "variables.file",
      "value": "",
      "file": "fixtures/avatar.png"
    },
    {
      "name": "variables.docs.0",
      "value": "",
      "file": "fixtures/a.pdf"
    },
    {
      "name": "variables.docs.1",
      "value": "",
      "file": "/Users/me/Documents/b.pdf",
      "contentType": "application/pdf"
    }
  ],
  "auth": {
    "type": "inherit"
  }
}
```

## TCP request: kind `tcp`

A raw TCP connection (TCP-01), optionally over TLS (TCP-02) and with a
message framing (TCP-03): where to connect and the message being composed.
How the connection works is in [tcp.md](tcp.md).

| Member | Type | Meaning |
|---|---|---|
| `schemaVersion`, `kind` | | `kind` is `"tcp"`. |
| `id` | string | Request ID. |
| `name` | string | Display name. |
| `description` | string, optional | Markdown notes. |
| `address` | string | `host:port` as typed (`localhost:7000`, `[::1]:7000`, `{{echoHost}}:7000`). |
| `sendMode` | string, optional | How `message` becomes bytes: left out for `"text"` (UTF-8), or `"hex"`. A mode this version does not know is kept. |
| `lineEnding` | string, optional | Appended to text messages when sent without a framing: left out for `"none"`, `"lf"` or `"crlf"`. Hex messages are sent exactly as written. |
| `message` | string, optional | The composer's text as typed, also when it is not valid hex yet. |
| `framing` | object, optional | How received bytes are split into messages and sent messages framed ([tcp.md](tcp.md#framing)); left out for no framing. `mode`: left out for `"none"`, `"newline"` or `"length"`. `delimiter` (newline): left out for `"lf"`, or `"crlf"`, appended to sent messages. `prefixBytes` (length): `1`, `2`, `4` (missing) or `8`. `byteOrder` (length): left out for `"big"`, or `"little"`. `maxMessageBytes`: the largest message received, whole number from 1 to 33554432; missing is 1 MiB. The other members are kept when the mode changes. Values this version does not know are kept, and connecting fails with `framing.invalid_options`. |
| `settings` | object, optional | `connectTimeoutMs`: whole number, optional, the timeout of connect and the TLS handshake in milliseconds (`0`: no limit); missing uses the app's (10 s). `tls`: `true` connects with TLS (TCP-02); left out for plain TCP. `verifyTls`: `false` turns off certificate verification; left out verifies. |

The defaults are written as missing members, so a file that says
`"sendMode": "text"` loses that member on its first save. Files written
before TLS and framing existed have neither member and load unchanged:
plain TCP without framing.

```json
{
  "schemaVersion": 1,
  "kind": "tcp",
  "id": "req_7d2b5e90c41a6f38",
  "name": "Echo server",
  "address": "{{echoHost}}:7000",
  "sendMode": "hex",
  "lineEnding": "crlf",
  "message": "DE AD be ef 0x0a",
  "framing": {
    "mode": "length",
    "prefixBytes": 2,
    "byteOrder": "little",
    "maxMessageBytes": 65536
  },
  "settings": {
    "connectTimeoutMs": 5000,
    "tls": true,
    "verifyTls": false
  }
}
```

## UDP request: kind `udp`

A UDP request (UDP-01): where datagrams go, how they are sent and the
datagram being composed. Datagrams go through the proxy in effect like
every connection, a SOCKS5 proxy relaying them with UDP ASSOCIATE
(SOCKS-07). How it works is in [udp.md](udp.md). Added in this version
without a schema change: older versions show such a file as one of an
unknown kind and leave it as it is.

| Member | Type | Meaning |
|---|---|---|
| `schemaVersion`, `kind` | | `kind` is `"udp"`. |
| `id` | string | Request ID. |
| `name` | string | Display name. |
| `description` | string, optional | Markdown notes. |
| `address` | string | `host:port` as typed (`localhost:5353`, `[::1]:53`, `{{dnsHost}}:53`). |
| `sendMode` | string, optional | How `message` becomes a datagram: left out for `"text"` (UTF-8, `{{variables}}` rendered), `"hex"` or `"base64"` (the bytes as written). A mode this version does not know is kept. |
| `message` | string, optional | The composer's text as typed, also when it is not valid hex or Base64 yet. |
| `settings` | object, optional | `connected`: `true` receives only from the target and reports ICMP "port unreachable"; left out receives from any sender. `ipVersion`: left out for either, `"ipv4"` or `"ipv6"`. `maxDatagramBytes`: whole number from 1 to 65535, larger datagrams are not sent and longer received ones are cut; missing is 65535. `openTimeoutMs`: whole number, DNS and the proxy's UDP association in milliseconds (`0`: no limit); missing is 10 s. Values this version does not know are kept, and opening fails. |

The defaults are written as missing members.

```json
{
  "schemaVersion": 1,
  "kind": "udp",
  "id": "req_5c8e1a7f2b904d63",
  "name": "DNS probe",
  "address": "{{dnsHost}}:53",
  "sendMode": "hex",
  "message": "12 34 01 00 00 01 00 00 00 00 00 00 07 65 78 61 6d 70 6c 65 03 63 6f 6d 00 00 01 00 01",
  "settings": {
    "connected": true,
    "ipVersion": "ipv4",
    "maxDatagramBytes": 1500,
    "openTimeoutMs": 5000
  }
}
```

## WebSocket request: kind `websocket`

A WebSocket connection (WS-01, WS-02, WS-03, WS-07, WS-09, WS-10): the
handshake, the message being composed, the saved messages, and the keepalive
and reconnect settings. How the connection works is in
[websocket.md](websocket.md#request-kind-websocket).

| Member | Type | Meaning |
|---|---|---|
| `schemaVersion`, `kind` | | `kind` is `"websocket"`. |
| `id` | string | Request ID. |
| `name` | string | Display name. |
| `description` | string, optional | Markdown notes. |
| `url` | string | The `ws://` or `wss://` URL as typed, with `{{variables}}` unresolved and the enabled query parameters in its query string (kept in step with `params` as for `http`). |
| `params` | array, optional | Query parameter rows (`name`, `value`, `description`, `disabled`), including disabled ones. |
| `headers` | array, optional | Handshake header rows, same shape. Rows for the headers the handshake sets (`Upgrade`, `Sec-WebSocket-*`, …) are refused when connecting. |
| `subprotocols` | array of strings, optional | Offered in `Sec-WebSocket-Protocol`, in order. Left out when empty. |
| `message` | string, optional | The composer's text as typed, also when it is not valid JSON yet. |
| `messageFormat` | string, optional | Left out for `"text"`, or `"json"` (checked and beautified in the editor, sent as a text message), `"hex"` or `"base64"` (`message` spells the bytes of a binary message, WS-04: hex pairs as for TCP, or Base64 in either alphabet). A format this version does not know is kept; older versions keep `"hex"` and `"base64"` the same way and edit them as text. |
| `auth` | [auth](#auth), optional | The request's [auth](#auth) (AUTH-31), sent with the upgrade request ([auth.md](auth.md#auth-on-every-protocol-auth-31)). Missing (as in files written before PostSonde applied auth to handshakes) reads as `"none"`, so such a file connects as before, and `"none"` is written as a missing member; requests created in the app start as `"inherit"`. Digest, OAuth 1.0, AWS Signature and Hawk are kept but refused when connecting. |
| `savedMessages` | array, optional | Saved messages (WS-07), in the order the editor lists them; left out when there are none. Each is an object: `name` (the label, need not be unique), `text` (the message as typed; `{{variables}}` resolved only when a text or JSON message is sent, never in hex or Base64) and `format` (left out for `"text"`, or `"json"`, `"hex"` or `"base64"`, as `messageFormat`). Unknown members of each object are kept. Files without the member (written before saved messages existed) read as having none. |
| `settings` | object, optional | `connectTimeoutMs`: whole number, the limit for connect, TLS and the upgrade in milliseconds (`0`: none; missing: 30 s). `verifyTls`: `false` accepts any certificate for `wss`. `sendCookies`: `false` leaves the project's cookie jar out of the handshake (missing: cookies are sent and stored). Keepalive (WS-09): `pingIntervalMs` sends a ping this often while connected (missing or `0`: none; at least 100); `pongTimeoutMs` closes the connection when a keepalive ping gets no pong in time (missing: 10 s; `0`: never). Reconnect (WS-10): `autoReconnect` `true` connects again after a drop (missing: off); `reconnectMaxAttempts` (missing: 5; `0`: no limit), `reconnectDelayMs` (the first wait, doubled for each further reconnect; missing: 1000) and `reconnectMaxDelayMs` (the cap; missing: 30000). Files written before these settings existed have none of them and connect as before: no keepalive, no reconnect. |
| `assertions` | array of [assertions](#assertions), optional | Checks on the messages received (TEST-08), run when the connection ends and on demand; left out when there are none. |

The defaults are written as missing members.

```json
{
  "schemaVersion": 1,
  "kind": "websocket",
  "id": "req_4c9e1a7b20d85f36",
  "name": "Live feed",
  "url": "wss://{{host}}/feed?room=lobby",
  "params": [
    {
      "name": "room",
      "value": "lobby"
    },
    {
      "name": "debug",
      "value": "1",
      "disabled": true
    }
  ],
  "headers": [
    {
      "name": "Origin",
      "value": "https://app.example.com"
    }
  ],
  "subprotocols": [
    "graphql-transport-ws",
    "v2.feed"
  ],
  "message": "{\"type\": \"subscribe\", \"channel\": \"prices\"}",
  "messageFormat": "json",
  "savedMessages": [
    {
      "name": "Subscribe",
      "text": "{\"type\": \"subscribe\", \"token\": \"{{token}}\"}",
      "format": "json"
    },
    {
      "name": "Ping",
      "text": "ping"
    },
    {
      "name": "Hello bytes",
      "text": "de ad be ef",
      "format": "hex"
    },
    {
      "name": "Handshake blob",
      "text": "AAECAw==",
      "format": "base64"
    }
  ],
  "settings": {
    "connectTimeoutMs": 10000,
    "sendCookies": false,
    "pingIntervalMs": 30000,
    "pongTimeoutMs": 5000,
    "autoReconnect": true,
    "reconnectMaxAttempts": 10,
    "reconnectDelayMs": 500,
    "reconnectMaxDelayMs": 60000
  }
}
```

## gRPC request: kind `grpc`

A gRPC call (GRPC-01, GRPC-02, GRPC-10): the server, the `.proto` files
that describe it, the chosen service and method, and the message being
composed. How the setup is loaded is in [grpc.md](grpc.md).

| Member | Type | Meaning |
|---|---|---|
| `schemaVersion`, `kind` | | `kind` is `"grpc"`. |
| `id` | string | Request ID. |
| `name` | string | Display name. |
| `description` | string, optional | Markdown notes. |
| `address` | string | `host:port` as typed, with `{{variables}}` unresolved. |
| `proto` | [proto setup](#proto-setup), optional | The request's own `.proto` files and import paths, or `inherit`. Left out when empty. |
| `service` | string, optional | Fully-qualified service name (`shop.v1.Orders`). |
| `method` | string, optional | The method's simple name (`GetOrder`). |
| `message` | string, optional | The request message's JSON text as typed, also when it is not valid JSON yet. |
| `metadata` | array of [rows](#rows), optional | Request metadata, including disabled rows. |
| `auth` | [auth](#auth), optional | The request's [auth](#auth) (AUTH-31), sent in the call's metadata ([auth.md](auth.md#auth-on-every-protocol-auth-31)). Missing (as in files written before PostSonde applied auth to handshakes) reads as `"none"`, so such a file calls as before, and `"none"` is written as a missing member; requests created in the app start as `"inherit"`. Digest, OAuth 1.0, AWS Signature and Hawk are kept but refused when calling, as is `in` `"query"` (gRPC has no query string). |
| `settings` | object, optional | `tls`: `true` connects with TLS (missing: plaintext HTTP/2). `verifyTls`: `false` accepts any certificate. `connectTimeoutMs`: whole number, the limit for connect and TLS in milliseconds (`0`: none). `deadlineMs`: the call's deadline in milliseconds (`0` or missing: none). `transport`: `"grpc-web"` or `"grpc-web-text"` calls a gRPC-Web endpoint with binary or Base64 framing (missing or `"grpc"`: gRPC over HTTP/2). `compression`: `"gzip"` compresses request messages (missing or `"identity"`: none). |
| `assertions` | array of [assertions](#assertions), optional | Checks on the response messages, the status and the metadata (TEST-08), run when the call ends and on demand; left out when there are none. |

### Proto setup

| Member | Type | Meaning |
|---|---|---|
| `files` | array of strings, optional | The `.proto` files whose services can be called, in order. |
| `importPaths` | array of strings, optional | Import roots, searched in order (protoc's `--proto_path`); the built-in well-known types come after them. |
| `inherit` | boolean, optional | On a request: use the setup of the nearest folder above that has one, instead of `files` and `importPaths` (which are kept, so turning it off brings them back). Missing: `false`. |

Paths follow the rule for [file paths](#common-rules): relative to the
project root with `/` when inside the project, absolute otherwise.

The defaults are written as missing members.

```json
{
  "schemaVersion": 1,
  "kind": "grpc",
  "id": "req_7b3f0e2c9a1d5846",
  "name": "Get order",
  "address": "{{grpcHost}}:50051",
  "proto": {
    "inherit": true
  },
  "service": "shop.v1.Orders",
  "method": "GetOrder",
  "message": "{\"id\": \"{{orderId}}\"}",
  "metadata": [
    {
      "name": "x-request-id",
      "value": "{{$uuid}}"
    },
    {
      "name": "x-debug",
      "value": "1",
      "disabled": true
    }
  ],
  "settings": {
    "tls": true,
    "deadlineMs": 5000
  }
}
```

## SSE request: kind `sse`

A Server-Sent Events stream (SSE-01, SSE-02): an HTTP request whose
response is read as a `text/event-stream` and shown event by event. Its
members are those of an [HTTP request](#http-request-kind-http), with the
same meaning, except that `method` and `body` are optional. How the stream
is read is in [sse.md](sse.md#client).

| Member | Type | Meaning |
|---|---|---|
| `schemaVersion`, `kind` | | `kind` is `"sse"`. |
| `id`, `name`, `description` | | As for `http`. |
| `method` | string, optional | Left out for `GET`, the default; another method token (`POST` for a streaming API that takes a body) is written. |
| `url`, `params`, `pathParams`, `headers` | | As for `http`. |
| `body` | [body](#body), optional | As for `http`; left out when its mode is `none` with no other members. |
| `bodyExamples` | array of [body examples](#body-examples), optional | As for `http`. |
| `auth` | [auth](#auth) | As for `http`. |
| `settings` | [settings](#settings), optional | As for `http`. `timeoutMs` bounds connecting and waiting for the response headers only: once they are in, the stream runs until it ends or is stopped. `settings.reconnect` (below) is the reconnection policy. |
| `assertions` | array of [assertions](#assertions), optional | Checks on the events received (TEST-08), run when the stream ends and on demand; kept as they are when the request is switched between `http` and `sse`. |
| `extractions` | array of [extraction rules](#extractions), optional | Kept as they are, like `assertions`; nothing is extracted from an event stream. |
| `responseExamples` | array of [response examples](#response-examples), optional | Kept as they are, like `assertions`; an event stream has no history to save one from. |

`settings.reconnect` (SSE-03) says whether a stream that ends or whose
connection drops is opened again with `Last-Event-ID`
([sse.md](sse.md#reconnecting)). It is an object; every member is
optional, and `reconnect` itself is left out when it has none, which does
not reconnect: files written before it connect exactly as before.

| Member | Type | Meaning |
|---|---|---|
| `enabled` | `true` or `false`, optional | Reconnect. Missing: `false`. |
| `maxAttempts` | whole number, optional | How many reconnection attempts in a row may fail (end without delivering a byte of the stream) before the stream gives up with `sse.reconnect_failed`; `0` means no limit. Missing (or negative): 5. |
| `delayMs` | whole number, optional | The reconnection time in milliseconds until the server sets one with a `retry` field. Missing (or negative): 3000. |

Other kinds ignore `settings.reconnect` but keep it, so a request switched
between `http` and `sse` keeps its policy.

The defaults are written as missing members.

```json
{
  "schemaVersion": 1,
  "kind": "sse",
  "id": "req_2d8f5a1c7e03b964",
  "name": "Price stream",
  "url": "{{baseUrl}}/prices/stream?symbols=ACME",
  "params": [
    {
      "name": "symbols",
      "value": "ACME"
    }
  ],
  "headers": [
    {
      "name": "Accept",
      "value": "text/event-stream"
    }
  ],
  "auth": {
    "type": "inherit"
  },
  "settings": {
    "timeoutMs": 10000,
    "reconnect": {
      "enabled": true,
      "maxAttempts": 10
    }
  }
}
```

## Socket.IO request: kind `socketio`

A Socket.IO v4 connection (SIO-01, SIO-03…SIO-07): the server, the
handshake (path, namespace, auth payload, transport), the emit being
composed, the saved emits and the events the log shows. How the
connection works is in [socketio.md](socketio.md#request-kind-socketio).

| Member | Type | Meaning |
|---|---|---|
| `schemaVersion`, `kind` | | `kind` is `"socketio"`. |
| `id` | string | Request ID. |
| `name` | string | Display name. |
| `description` | string, optional | Markdown notes. |
| `url` | string | The server's `http://`, `https://`, `ws://` or `wss://` URL as typed, with `{{variables}}` unresolved and the enabled query parameters in its query string (kept in step with `params` as for `http`). The scheme only says whether TLS is used. Its path is the namespace when `namespace` is missing, as with `io("https://host/admin")`. |
| `params` | array, optional | Query parameter rows, including disabled ones; sent with every Engine.IO request. |
| `headers` | array, optional | Header rows sent with every long-polling request and the WebSocket handshake. |
| `path` | string, optional | The Engine.IO handshake path (SIO-06). Left out for `/socket.io/`, the default; a trailing `/` is added when connecting. |
| `namespace` | string, optional | The namespace to connect to (SIO-06), like `/admin`. Missing: the URL's path, or the main namespace `/`. |
| `authPayload` | string, optional | The JSON object sent as the `auth` of the CONNECT packet (SIO-07), as typed, `{{variables}}` unresolved. Missing: no auth. |
| `transport` | string, optional | Left out for `"auto"` (WebSocket, falling back to long-polling), or `"websocket"`, `"polling"` or `"upgrade"` (long-polling, then upgraded to WebSocket). A transport this version does not know is kept. |
| `version` | string, optional | The server's Socket.IO version (SIO-02): `"v2"` for socket.io v2 (Engine.IO 3; no auth payload then); left out for v3 and v4. A value this version does not know is kept. |
| `event` | string, optional | The composer's event name. |
| `args` | string, optional | The composer's arguments as JSON text, as typed: an array holds one argument per element, another value is one argument. An object `{"$base64": "…"}` or `{"$hex": "…"}` anywhere is a binary argument (an attachment). |
| `ack` | boolean, optional | `true` asks for an acknowledgement (SIO-05). |
| `savedEmits` | array, optional | Saved emits, in the order the editor lists them. Each is an object: `name` (the label), `event`, `args` (as `args`, optional) and `ack` (optional). Unknown members of each object are kept. |
| `listen` | array of strings, optional | The event names the log shows (SIO-04); missing: every event. |
| `auth` | [auth](#auth), optional | The request's [auth](#auth) (AUTH-31), sent with the handshake ([auth.md](auth.md#auth-on-every-protocol-auth-31)). Missing (as in files written before PostSonde applied auth to handshakes) reads as `"none"`, so such a file connects as before, and `"none"` is written as a missing member; requests created in the app start as `"inherit"`. Digest, OAuth 1.0, AWS Signature and Hawk are kept but refused when connecting. |
| `authIn` | string, optional | Where `auth` goes: left out for `"handshake"` (the headers and query string of every transport request), or `"payload"` (members of the CONNECT packet's auth payload: `token`, `username` and `password`, or the API key's name, after the members typed in `authPayload`, which win; with `version` `"v2"`, which has no auth payload, the same members as query parameters of the URL, after those typed there, which win). A value this version does not know is kept and refused when connecting. |
| `settings` | object, optional | `connectTimeoutMs`: the limit for the handshake, the upgrade and the namespace CONNECT in milliseconds (`0`: none; missing: 20 s). `ackTimeoutMs`: how long an emit waits for its acknowledgement (missing: 10 s). `verifyTls`: `false` accepts any certificate. `sendCookies`: `false` leaves the project's cookie jar out. |
| `assertions` | array of [assertions](#assertions), optional | Checks on the events received (TEST-08), run when the connection ends and on demand; left out when there are none. |

The defaults are written as missing members.

```json
{
  "schemaVersion": 1,
  "kind": "socketio",
  "id": "req_6b1d0e4f9a2c8d37",
  "name": "Chat",
  "url": "{{baseUrl}}/chat?room=lobby",
  "params": [
    {
      "name": "room",
      "value": "lobby"
    }
  ],
  "headers": [
    {
      "name": "X-Client",
      "value": "postsonde"
    }
  ],
  "path": "/realtime/",
  "authPayload": "{\"token\": \"{{chatToken}}\"}",
  "transport": "upgrade",
  "event": "message",
  "args": "[\"hello\", {\"to\": \"all\"}]",
  "ack": true,
  "savedEmits": [
    {
      "name": "Join lobby",
      "event": "join",
      "args": "[\"lobby\"]",
      "ack": true
    },
    {
      "name": "Avatar",
      "event": "upload",
      "args": "[{\"$base64\": \"iVBORw0KGgo=\"}]"
    }
  ],
  "listen": [
    "message",
    "typing"
  ],
  "settings": {
    "ackTimeoutMs": 5000
  }
}
```

## Environment: `environments/*.json`

A named set of variables (ENV-01). Which environment is active is local
state, not part of the project.

| Member | Type | Meaning |
|---|---|---|
| `schemaVersion`, `kind` | | `kind` is `"environment"`. |
| `id` | string | Environment ID. |
| `name` | string | Display name. |
| `color` | string, optional | Accent colour as `#rrggbb` (ENV-08), shared with the project; set in the Environments view, which writes it in lower case ([environments.md](environments.md#colour)). |
| `variables` | array of [variables](#variables), optional | The environment's variables. A name it does not define is inherited from the global variables of `postsonde.json`, the base environment (ENV-05). Saving variables that did not change leaves the file byte for byte as it is, whatever its layout (golden files: `internal/core/environments/testdata/roundtrip/`). |

```json
{
  "schemaVersion": 1,
  "kind": "environment",
  "id": "env_92f0d5b8a61c3e47",
  "name": "Production",
  "color": "#d0342c",
  "variables": [
    {
      "name": "baseUrl",
      "value": "https://api.acme.example"
    },
    {
      "name": "token",
      "value": "",
      "secret": true,
      "description": "Personal access token"
    }
  ]
}
```

## Shared objects

### Rows

A row of a key/value table: a header, a query or path parameter, a form
field.

| Member | Type | Meaning |
|---|---|---|
| `name` | string | Name; may be empty while you are typing. |
| `value` | string | Value, with `{{variables}}` unresolved. |
| `description` | string, optional | Note shown next to the row. |
| `disabled` | `true`, optional | The row is kept but not sent. Left out when the row is enabled. |

### Variables

| Member | Type | Meaning |
|---|---|---|
| `name` | string | Variable name, used as `{{name}}`. |
| `value` | string | Value. Always `""` for a secret variable. |
| `secret` | `true`, optional | The value is secret (SEC-01). It is kept in PostSonde's local storage, never in the project: an environment's in `secrets/environments/<environment id>.json`, a folder's and the global variables in the item's side file `secrets/requests/<folder or workspace id>.json` as `var.<name>` ([auth.md](auth.md#secrets)). `Marshal` refuses a secret variable with a value; a value typed into the file by hand is used, and moves to local storage when PostSonde next writes the file. |
| `description` | string, optional | Note. |
| `disabled` | `true`, optional | The variable is ignored. |

### Body

`mode` says which of the other members apply; the others are left out.

| `mode` | Members | Sends |
|---|---|---|
| `"none"` | | No body (HTTP-10). |
| `"raw"` | `type`: `"json"`, `"xml"`, `"html"`, `"text"` or `"yaml"`; `text`: the body as typed | The text (HTTP-11). |
| `"urlencoded"` | `fields`: array of [rows](#rows) | `application/x-www-form-urlencoded` (HTTP-13). |
| `"multipart"` | `parts`: array of parts, below | `multipart/form-data` (HTTP-14). |
| `"file"` | `path`: the file to send; `contentType`, optional: its Content-Type, when it should not be guessed from the file name | The file's bytes (HTTP-15). |

A multipart part:

| Member | Type | Meaning |
|---|---|---|
| `name` | string | Field name. |
| `value` | string | Text value; `""` for a file part. |
| `file` | string, optional | Path of the file to send; makes this a file part. |
| `contentType` | string, optional | The part's Content-Type. Without it a file part's type is guessed from the file name and a text part has none. |
| `description` | string, optional | Note. |
| `disabled` | `true`, optional | The part is not sent. |

A mode this version does not know is kept with all its members.

### Auth

`type` says which of the other members apply; the members of the other
types are left out. What each type sends is in [auth.md](auth.md). Every string member may hold `{{variables}}`, kept as typed and rendered at send time with the sending request's scopes, also when the auth is a folder's that the request inherits ([auth.md](auth.md#variables-in-auth-fields-auth-32)).

| `type` | Members | Sends |
|---|---|---|
| `"inherit"` | | The auth of the closest parent folder whose `type` is not `"inherit"`; none when no folder sets one (AUTH-01). |
| `"none"` | | No credentials (AUTH-02). |
| `"basic"` | `username`, `password` (secret), `disabled`, `localSecrets` | `Authorization: Basic …` (AUTH-03). |
| `"bearer"` | `prefix`, `token` (secret), `disabled`, `localSecrets` | `Authorization: <prefix> <token>` (AUTH-04). |
| `"digest"` | `username`, `password` (secret), `disabled`, `localSecrets` | Answers the server's 401 Digest challenge with `Authorization: Digest …` (AUTH-06, RFC 7616). |
| `"apikey"` | `key`, `value` (secret), `in`, `disabled`, `localSecrets` | `<key>: <value>` as a header, or `key=value` in the query string (AUTH-05). |
| `"jwt"` | `algorithm`, `secret` (secret), `keyFile`, `secretBase64`, `jwtHeader`, `payload`, `issuedAt`, `notBefore`, `jwtId`, `expiresIn`, `prefix`, `key`, `in`, `disabled`, `localSecrets` | A JWT signed at send time, as `Authorization: <prefix> <jwt>`, another header, or a query parameter (AUTH-16, [auth.md](auth.md#jwt-bearer-auth-16)). |
| `"oauth1"` | `consumerKey`, `consumerSecret` (secret), `accessToken`, `tokenSecret` (secret), `signatureMethod`, `callback`, `verifier`, `realm`, `omitVersion`, `nonce`, `timestamp`, `bodyHash`, `in`, `disabled`, `localSecrets` | The request signed at send time with OAuth 1.0, the parameters in `Authorization: OAuth …`, the query string or the form body (AUTH-15, [auth.md](auth.md#oauth-10-auth-15)). |
| `"awsv4"` | `accessKey`, `secretKey` (secret), `sessionToken` (secret), `region`, `service`, `unsignedPayload`, `expires`, `in`, `disabled`, `localSecrets` | The request signed at send time with AWS Signature Version 4, in `Authorization: AWS4-HMAC-SHA256 …` or as a presigned URL (AUTH-17, [auth.md](auth.md#aws-signature-v4-auth-17)). |
| `"hawk"` | `hawkId`, `hawkKey` (secret), `hawkAlgorithm`, `ext`, `app`, `dlg`, `payloadHash`, `disabled`, `localSecrets` | The request signed at send time with Hawk, in `Authorization: Hawk …` (AUTH-18, [auth.md](auth.md#hawk-auth-18)). |
| `"oauth2"` | `grantType`, `tokenUrl`, `authUrl`, `redirectUrl`, `clientId`, `clientSecret` (secret), `clientAuth`, `scope`, `audience`, `resource`, `state`, `tokenParams`, `username`, `password` (secret), `issuer`, `idToken`, `prefix`, `key`, `in`, `disabled`, `localSecrets` | An OAuth 2.0 access token (or with `idToken` the OpenID Connect ID token) got when the request is sent, from the token endpoint or the implicit grant's redirect (kept in the local side files and refreshed, never in the file), as `Authorization: <prefix> <token>`, another header, or a query parameter (AUTH-07…12, AUTH-14, [auth.md](auth.md#oauth-20-auth-08-auth-12-auth-33)). |
| `"netrc"` | `netrcFile`, `disabled` | `Authorization: Basic …` with the login and password of the URL host's entry in a `.netrc` file, read at send time; nothing of the file is stored (AUTH-19, [auth.md](auth.md#netrc-auth-19)). |

| Member | Type | Meaning |
|---|---|---|
| `username` | string, optional | The Basic or Digest user-id; the OAuth 2.0 password grant's username. |
| `password` | string, optional | The Basic or Digest password; the OAuth 2.0 password grant's password. Secret, see below. |
| `prefix` | string, optional | What goes before the bearer token (or a JWT sent in a header), followed by a space. Left out for the default, `"Bearer"`; `""` sends the token alone. |
| `token` | string, optional | The bearer token. Secret. |
| `key` | string, optional | The API key's header or query parameter name. For `jwt` and `oauth2`, the header (left out: `Authorization`) or query parameter (left out: `access_token`) the token goes in. |
| `value` | string, optional | The API key. Secret. |
| `in` | string, optional | Where the API key, JWT, OAuth 2.0 token, OAuth 1.0 parameters or AWS signature go: `"query"` (for `awsv4` a presigned URL), for `oauth1` also `"body"` (a URL-encoded form body), or left out for a header. |
| `algorithm` | string, optional | The JWT's `alg`: `HS256`, `HS384`, `HS512`, `RS256`, `RS384`, `RS512`, `PS256`, `PS384`, `PS512`, `ES256`, `ES384`, `ES512` or `EdDSA`. Left out: `HS256`. |
| `secret` | string, optional | The JWT signing key as text: the HMAC secret, or a PEM private key for the other algorithms. Secret. |
| `keyFile` | string, optional | A file to read the JWT key from instead, when the request is sent: relative to the project root (`/` separators) or absolute. Not a secret: only the path is stored. |
| `secretBase64` | `true`, optional | The HMAC secret (typed or from `keyFile`) is base64 (standard or URL alphabet, padding optional) and is decoded before signing. |
| `jwtHeader` | string, optional | JSON object text of extra JWT header parameters (`kid`…). `alg` is always `algorithm`; `typ` is `"JWT"` unless set here. |
| `payload` | string, optional | The JWT claims set, JSON object text. Left out: `{}`. |
| `issuedAt`, `notBefore`, `jwtId` | `true`, optional | Add `iat`, `nbf` (both the send time, Unix seconds) and `jti` (a random UUID) to the payload at send time, unless it has them. |
| `expiresIn` | integer, optional | Seconds: adds `exp` that long after the send time, unless the payload has it. Left out or 0: none. |
| `consumerKey`, `accessToken` | string, optional | The OAuth 1.0 client identifier and token (`oauth_consumer_key`, `oauth_token`). Left out: nothing is sent without a consumer key; no `oauth_token` without a token. |
| `consumerSecret`, `tokenSecret` | string, optional | The OAuth 1.0 shared secrets. For `RSA-SHA1`, `consumerSecret` is the client's PEM private key and `tokenSecret` is not used. Secret, each on its own. |
| `signatureMethod` | string, optional | `HMAC-SHA1`, `HMAC-SHA256`, `RSA-SHA1` or `PLAINTEXT`. Left out: `HMAC-SHA1`. |
| `callback`, `verifier` | string, optional | Sent as `oauth_callback` and `oauth_verifier` when set. |
| `realm` | string, optional | The `realm` of the `Authorization` header (not signed). |
| `omitVersion` | `true`, optional | Leaves `oauth_version=1.0` out. |
| `nonce`, `timestamp` | string, optional | Fixed `oauth_nonce` and `oauth_timestamp`; left out, they are generated on every send. |
| `bodyHash` | `true`, optional | Adds `oauth_body_hash` to a request whose body is not a URL-encoded form. |
| `accessKey` | string, optional | The AWS access key ID. Left out: nothing is sent. |
| `secretKey`, `sessionToken` | string, optional | The AWS secret access key, and a temporary credential's session token (sent as `X-Amz-Security-Token`). Secret, each on its own. |
| `region`, `service` | string, optional | The credential scope, e.g. `"us-east-1"` and `"execute-api"`; required when there is an access key. `"s3"` signs the path encoded once and adds `X-Amz-Content-Sha256`. |
| `unsignedPayload` | `true`, optional | Signs `UNSIGNED-PAYLOAD` instead of the body's SHA-256. |
| `expires` | integer, optional | Seconds a presigned URL (`in` `"query"`) is valid, 1 to 604800. Left out: 3600. |
| `hawkId`, `hawkKey` | string, optional | The Hawk credentials: the key identifier (left out: nothing is sent) and the key. `hawkKey` is secret. |
| `hawkAlgorithm` | string, optional | `sha256` or `sha1`. Left out: `sha256`. |
| `ext`, `app`, `dlg` | string, optional | Hawk's application data and Oz application and delegated-by IDs, sent and covered by the MAC when set (`dlg` only with `app`). |
| `payloadHash` | `true`, optional | Hawk: covers the body and its content type with the MAC (`hash`). |
| `grantType` | string, optional | The OAuth 2.0 grant: `"client_credentials"` (written when left out), `"authorization_code"` (with PKCE, through the system browser), `"password"` (the resource owner's `username` and `password`) or `"implicit"` (deprecated: through the system browser, the token in the redirect). Another is kept, and sending fails. |
| `authUrl` | string, optional | The authorization endpoint of the authorization code and implicit grants, an absolute http or https URL. For the implicit grant, left out: nothing is sent. |
| `redirectUrl` | string, optional | The authorization code and implicit grants' redirect URL, a loopback `http://` URL (`127.0.0.1`, `[::1]` or `localhost`, optional port and path). Left out: `http://127.0.0.1:<random port>/callback`. |
| `tokenUrl` | string, optional | The token endpoint, an absolute http or https URL. Left out: nothing is sent. Not used by the implicit grant. |
| `clientId`, `clientSecret` | string, optional | The OAuth 2.0 client's credentials. `clientSecret` is secret; left out, the client sends only its ID. |
| `clientAuth` | string, optional | How the client authenticates to the token endpoint: left out for the Authorization header (`client_secret_basic`), `"body"` for `client_id` and `client_secret` in the form (`client_secret_post`). |
| `scope` | string, optional | The space-separated scopes asked for. |
| `audience`, `resource` | string, optional | Sent in the token request when set (`audience` as Auth0 and Okta take it; `resource`, RFC 8707). |
| `state` | string, optional | Sent with the redirect grants' authorization request and checked on the redirect; left out, a random one per sign-in. Not sent by client credentials. |
| `tokenParams` | array of key-value rows, optional | Extra parameters of the token request's body, in order; a row with `disabled` is not sent. |
| `issuer` | string, optional | OpenID Connect: the `iss` an ID token must name exactly. Left out: any non-empty `iss`. |
| `idToken` | `true`, optional | OpenID Connect (AUTH-14): sends the ID token the server sent with the access token instead of the access token (the scope must ask for `openid`). |
| `netrcFile` | string, optional | The netrc file to read: absolute, `~/…` in the home directory, or relative to the project root (`/` separators). Left out: the file the `NETRC` environment variable names, else `~/.netrc` (`~/_netrc`, then `~/.netrc`, on Windows). Not a secret: only the path is stored. |
| `disabled` | `true`, optional | The auth is kept but nothing is sent, and no folder's auth is used instead (AUTH-04). |
| `localSecrets` | array of strings, optional | Names the secret members (`"password"`, `"token"`, `"value"`, `"secret"`, `"consumerSecret"`, `"tokenSecret"`, `"secretKey"`, `"sessionToken"`, `"hawkKey"`, `"clientSecret"`; an `oauth2` auth has `"clientSecret"` and `"password"`) whose value is kept in PostSonde's local storage; each member named is then left out. |

**Secret members** (`password`, `token`, `value`, `secret`, `consumerSecret`, `tokenSecret`, `secretKey`, `sessionToken`, `hawkKey`, `clientSecret`) are written to the file
only when they are made of `{{variable}}` references, like
`"{{token}}"`: the recommended way, with the value in a secret environment
variable. A value typed as it is (`"hunter2"`, `"sk_{{suffix}}"`) is kept in
local storage on the machine where it was typed, keyed by the file's `id`,
and `localSecrets` says so. On another machine, or in a copy of the project,
that value is missing until it is typed again, and sending the request fails
with `request.auth_secret_missing`. [auth.md](auth.md#secrets) has the
details.

An auth `type` this version does not know is kept with all its members.

A bearer token from a variable, with a custom prefix:

```json
{
  "schemaVersion": 1,
  "kind": "http",
  "id": "req_3b9e0c5d71a4f286",
  "name": "List orders",
  "method": "GET",
  "url": "{{baseUrl}}/orders",
  "body": {
    "mode": "none"
  },
  "auth": {
    "type": "bearer",
    "prefix": "Token",
    "token": "{{token}}"
  }
}
```

An API key in the query string, typed and kept in local storage:

```json
{
  "schemaVersion": 1,
  "kind": "http",
  "id": "req_7d2a64e9b0c1f358",
  "name": "Weather",
  "method": "GET",
  "url": "https://weather.example/v1/today",
  "body": {
    "mode": "none"
  },
  "auth": {
    "type": "apikey",
    "key": "api_key",
    "in": "query",
    "localSecrets": [
      "value"
    ]
  }
}
```

A JWT signed with an EC key read from the project, with claims added at
send time (AUTH-16):

```json
{
  "schemaVersion": 1,
  "kind": "http",
  "id": "req_5e1c93a7d04b62f8",
  "name": "Me",
  "method": "GET",
  "url": "{{baseUrl}}/me",
  "body": {
    "mode": "none"
  },
  "auth": {
    "type": "jwt",
    "algorithm": "ES256",
    "keyFile": "keys/service.pem",
    "jwtHeader": "{\"kid\": \"service-1\"}",
    "payload": "{\n  \"sub\": \"{{userId}}\",\n  \"aud\": \"api\"\n}",
    "issuedAt": true,
    "jwtId": true,
    "expiresIn": 300
  }
}
```

An OAuth 1.0 signature in the Authorization header, the consumer secret
typed and kept in local storage, the token secret from a variable
(AUTH-15):

```json
{
  "schemaVersion": 1,
  "kind": "http",
  "id": "req_8c4f20b6e91d7a53",
  "name": "Photos",
  "method": "GET",
  "url": "https://photos.example.net/photos?size=original",
  "body": {
    "mode": "none"
  },
  "auth": {
    "type": "oauth1",
    "consumerKey": "dpf43f3p2l4k3l03",
    "accessToken": "{{accessToken}}",
    "tokenSecret": "{{tokenSecret}}",
    "realm": "Photos",
    "localSecrets": [
      "consumerSecret"
    ]
  }
}
```

### Settings

Per-request execution settings. Every member is optional: a missing member
uses the app setting, and `settings` itself is left out when it is empty.

| Member | Type | Meaning |
|---|---|---|
| `timeoutMs` | whole number, optional | Timeout for the whole request in milliseconds; `0` means none (HTTP-21). Missing uses the app's default timeout, `network.defaultTimeoutMs` ([settings.md](settings.md)). |
| `followRedirects` | `true` or `false`, optional | Follow redirects (HTTP-22). Missing: `true`. |
| `maxRedirects` | whole number, optional | Most redirects to follow (HTTP-22); `0` follows none, so the 3xx response is shown. Missing: 10. |
| `followOriginalMethod` | `true` or `false`, optional | Keep the method and body on a 301, 302 or 303 redirect instead of switching to GET (HTTP-22). Missing: `false`. |
| `forwardAuthorization` | `true` or `false`, optional | Send `Authorization` to another origin on a redirect (HTTP-22). Missing: `false`, it is sent only while the redirects stay on the request's origin. |
| `verifyTls` | `true` or `false`, optional | Verify TLS certificates (HTTP-23). Missing: the app setting `network.verifyTls` ([settings.md](settings.md)), `true` by default. |
| `urlEncoding` | `"auto"`, `"rfc3986"` or `"none"`, optional | How the path and query are percent-encoded before sending (HTTP-24). `auto` encodes as browsers do (the WHATWG URL Standard), `rfc3986` encodes every character RFC 3986 does not allow, `none` sends them as typed except what a request line cannot carry. Missing: `auto`. [http-client.md](http-client.md#url-encoding-http-24) has the character sets. |
| `httpVersion` | `"auto"`, `"http1"` or `"http2"`, optional | The HTTP version offered (HTTP-25): `auto` offers HTTP/2 and HTTP/1.1 through ALPN on https (HTTP/1.1 on plain http), `http1` only HTTP/1.1, `http2` only HTTP/2 (h2c with prior knowledge on plain http). Missing: `auto`. |
| `sendCookies` | `true` or `false`, optional | Send the project cookie jar's cookies (HTTP-26); `Cookie` header rows are sent either way. Missing: `true`. |
| `storeCookies` | `true` or `false`, optional | Store the response's `Set-Cookie` values in the project cookie jar (HTTP-26). Missing: `true`. |
| `omitHeaders` | array of strings, optional | Automatic headers not to send (HTTP-09), compared ignoring case: `"Content-Type"` for the type the body implies, or the name of a header the auth adds (`"Authorization"`, an API key's header). http requests only. Missing or empty: all are sent. |
| `reconnect` | object, optional | sse requests only: the reconnection policy (SSE-03), [above](#sse-request-kind-sse). Missing: no reconnecting. |
| `graphqlTransport` | object, optional | graphql requests only: the transport of each operation type (GQL-35), [above](#graphql-request-kind-graphql). Missing: the defaults. |
| `graphqlSchemaRefresh` | object, optional | graphql requests only: the opt-in schema auto-refresh (GQL-19), `onSend` and `intervalMinutes`, [above](#graphql-request-kind-graphql). Missing: the schema is fetched only when the user asks. |

An unknown `urlEncoding` or `httpVersion` value is kept in the file but
fails the send with `request.invalid_setting` (details: `field`), rather
than being sent some other way. `sendCookies` and `storeCookies` apply to
http, graphql and sse requests; a GraphQL subscription's handshake leaves
the jar out when `sendCookies` is `false`, as a websocket request does.

Before HTTP-24, a missing `urlEncoding` sent the path and query as typed,
which is now `"none"`: `auto` also encodes `"`, `<`, `>` and, in the path,
`` ` ``, `^`, `{` and `}` (and `'` in the query), as browsers do. These characters are not valid in a URL, and servers decode them the
same, so there is no migration and no version bump. Write `"none"` to keep
them as typed.

`maxRedirects: 0` meant "the default, 10" before HTTP-22 was built. No
PostSonde version wrote the member, so there is no migration and no version
bump: a hand-written `0` now follows no redirect, as the member's name
says. Write `10`, or leave it out, for the default.

### Assertions

A check on the response of an `http` or `graphql` request (TEST-01): rows
of *target · operator · expected*, checked in order after every send by
`internal/core/assert` ([assertions.md](assertions.md)). The results show
in the response's Tests tab; a failed assertion does not fail the send.
On a `websocket`, `socketio`, `grpc` or `sse` request it checks the
messages of the stream, when the stream ends and on demand (TEST-08,
[assertions.md](assertions.md#streams)).

| Member | Type | Meaning |
|---|---|---|
| `target` | string | `status`, `header`, `cookie`, `jsonpath`, `xpath`, `regex`, `body`, `time` or `size` (TEST-02). |
| `expr` | string, optional | The header or cookie name, the JSONPath, the XPath or the regular expression; left out for the other targets. |
| `op` | string | `equals`, `not_equals`, `contains`, `not_contains`, `exists`, `not_exists`, `is_empty`, `gt`, `lt`, `gte`, `lte`, `matches`, `is_type` (TEST-03) or `matches_schema` (TEST-07). |
| `expected` | string, optional | The value to compare with, as typed. `{{variables}}` in it and in `expr` are rendered with the request's variables when the response is checked; an undefined one makes that assertion invalid, not the send. Ignored by `exists` and `not_exists`. For `matches_schema`, the JSON Schema itself (text starting with `{`, or `true`/`false`) or the path of a schema file: relative to the project root with `/` separators when the file is inside the project, else absolute, as for body files ([jsonschema.md](jsonschema.md)). |
| `messages` | string, optional | Stream requests only (`websocket`, `socketio`, `grpc`, `sse`; TEST-08): which received messages the target is read from, `any`, `every`, `none`, `first`, `last` or `nth`; left out to check the stream as a whole. A scope this version does not know is kept (and reported as `assert.unknown_scope`). |
| `index` | whole number, optional | The message `nth` checks, counted from 1; left out for 0. |
| `disabled` | `true`, optional | The assertion is kept but not checked. |

A stream request's assertions also take the target `message_count`, the
number of messages received ([assertions.md](assertions.md#streams)).

A target or operator this version does not know is kept in the file; the
assertion is reported as invalid (`assert.unknown_target`,
`assert.unknown_operator`). Files without `assertions` load as before.

```json
"assertions": [
  { "target": "body", "op": "matches_schema", "expected": "schemas/user.json" },
  { "target": "jsonpath", "expr": "$.items[0]", "op": "matches_schema",
    "expected": "{\"type\":\"object\",\"required\":[\"id\"]}" }
]
```

`matches_schema` is a new operator value, not a new member: no version
change. An older PostSonde keeps it and reports the assertion as
`assert.unknown_operator`.

The stream members (`messages`, `index`, and `assertions` on `websocket`,
`socketio` and `grpc` files) are new optional members, not a version
change: an older PostSonde keeps them as unknown members (on an `sse`
file, the assertions it already kept) and checks no stream.

```json
"assertions": [
  { "target": "message_count", "op": "gte", "expected": "3" },
  { "messages": "last", "target": "jsonpath", "expr": "$.done", "op": "equals", "expected": "true" },
  { "messages": "nth", "index": 2, "target": "body", "op": "contains", "expected": "tick" }
]
```

### Extractions

A rule that takes a value from every response of an `http` or `graphql`
request into a variable (TEST-05), applied in order to every response by
`internal/core/extract` ([extraction.md](extraction.md)). The response's
Variables tab shows what each rule set. **Only the rule is stored**: the
values live in the runtime variables (a persisted one in the project's
local data, never in the project) or in the run.

| Member | Type | Meaning |
|---|---|---|
| `source` | string | `jsonpath`, `xpath`, `regex` (on the body), `header`, `cookie` or `status`. |
| `expr` | string, optional | The JSONPath, the XPath, the regular expression (its first capture group is the value, or the whole match without one), or the header or cookie name; left out for `status`. `{{variables}}` in it are rendered with the request's variables. |
| `variable` | string | The name of the variable set: a name a `{{reference}}` can spell. |
| `scope` | string, optional | Left out: the project's **runtime** variables (and, in a folder run, the run's variable of the name too). `"run"`: the folder run's own variables only; skipped outside a run. |
| `secret` | `true`, optional | The value is masked wherever it is shown or recorded, like a secret variable's. |
| `persist` | `true`, optional | The runtime variable is kept after the project is closed ([environments.md](environments.md#runtime-variables)). Ignored with `"scope": "run"`. |
| `disabled` | `true`, optional | The rule is kept but not applied. |

A source or scope this version does not know is kept in the file; the
rule is reported as invalid (`extract.unknown_source`,
`extract.unknown_scope`). Files without `extractions` load as before; an
older PostSonde keeps the member as an unknown one.

A login request whose token and session the next requests use:

```json
{
  "schemaVersion": 1,
  "kind": "http",
  "id": "req_9b3e6a1d07c4f528",
  "name": "Log in",
  "method": "POST",
  "url": "{{baseUrl}}/login",
  "headers": [
    {
      "name": "Content-Type",
      "value": "application/json"
    }
  ],
  "body": {
    "mode": "raw",
    "type": "json",
    "text": "{\"user\": \"{{user}}\", \"password\": \"{{password}}\"}"
  },
  "auth": {
    "type": "none"
  },
  "assertions": [
    {
      "target": "status",
      "op": "equals",
      "expected": "200"
    }
  ],
  "extractions": [
    {
      "source": "jsonpath",
      "expr": "$.access_token",
      "variable": "token",
      "secret": true
    },
    {
      "source": "jsonpath",
      "expr": "$.user.id",
      "variable": "userId",
      "persist": true
    },
    {
      "source": "cookie",
      "expr": "session",
      "variable": "session",
      "secret": true
    },
    {
      "source": "header",
      "expr": "X-Request-Id",
      "variable": "loginRequestId",
      "scope": "run"
    },
    {
      "source": "regex",
      "expr": "\"expires_in\":(\\d+)",
      "variable": "expiresIn",
      "disabled": true
    }
  ]
}
```

## Versions

`schemaVersion` is the version of this format, not of the app. All kinds of
file share one version number; this PostSonde writes version **1**.

- **Adding a member does not change the version.** Older PostSonde versions
  keep a member they don't know (see the common rules), so new optional
  members are added freely. Every new member needs a default that means
  "as before" when it is missing.
- **The version goes up** only when old files must be converted: a member
  is renamed, moved or changes meaning, or a kind is renamed.
- **Older files are migrated.** Each version bump comes with a migration
  step from version *n* to *n+1*. Reading a file runs the steps in order,
  in memory; the file is rewritten in the new version the next time it is
  saved. Steps are kept forever, so a file from any earlier version still
  opens.
- **Newer files are read, not written.** A file from a newer PostSonde
  loads what this version understands, and PostSonde does not save changes
  to it, so nothing the newer version wrote is lost.
- A file without `schemaVersion` counts as version 1.

## Problems

Errors use the shared model ([errors.md](errors.md)). Messages never contain
secret values.

| Code | Kind | When | Details |
|---|---|---|---|
| `projectfile.parse_failed` | `parse` | Not a JSON object, no `kind`, a bad `schemaVersion` or a member of the wrong type. | `line` and `column` for a syntax error; `field` for `kind` or `schemaVersion`; `path` |
| `projectfile.wrong_kind` | `parse` | A file of another kind where a specific one belongs (a request in `environments/`). | `kind`, `expected`, `path` |
| `projectfile.unknown_kind` | `unsupported` | A `kind` this version does not know. The file is left as it is. | `kind`, `path` |
| `projectfile.migration_failed` | `parse` | An older file could not be upgraded. | `version`, `path` |
| `projectfile.newer_version` | `unsupported` | Saving a file that a newer PostSonde wrote. | `version`, `supported`, `path` |
| `projectfile.invalid_value` | `invalid_argument` | Saving a file without an `id`, an HTTP request without a `method`, or a secret variable with a value. | `field`, `path` |
| `projectfile.read_failed` | from the cause | The file could not be read. | `path` |
| `projectfile.save_failed` | from the cause | The file could not be written. | `path` |

## For developers

`internal/core/storage/projectfile` (standard library only):

- Types: `Workspace`, `Folder`, `HTTPRequest`, `GraphQLRequest`,
  `TCPRequest`, `WebSocketRequest`, `GRPCRequest`, `Environment`, each
  embedding `Head{SchemaVersion, Kind}`,
  and the shared `KeyValue`, `Variable`, `Body`, `Part`, `Auth`,
  `RequestSettings`, `TCPSettings`, `WebSocketSettings`, `WebSocketMessage`, `GRPCProto`,
  `GRPCSettings`, `FolderGRPC`, `SSEReconnect`, `Assertion`, `Extraction`. Each
  object type has an `Unknown` map with the members this version does not
  know. They all implement `File`.
- `Decode(data) (File, error)`; `DecodeWorkspace`, `DecodeFolder`,
  `DecodeEnvironment` also check the kind. `ReadFile(path)` adds the path
  to errors.
- `Marshal(File) ([]byte, error)` validates and encodes; `WriteFile(path,
  File)` writes atomically, `0644` for a new file.
- `NewID(kind)`, `CurrentVersion`, `WorkspaceFileName`, `FolderFileName`,
  `EnvironmentsDir`, `Ext`, and the `Code…` constants.

### Changing the format

1. **A new optional member:** add the field with `omitempty` (or `omitzero`
   for a struct), a comment giving its default, and a row in the tables
   above. No version bump. If it holds a new example, add it to
   `examples()` in `projectfile_test.go`, run
   `go test ./internal/core/storage/projectfile -run TestExamples -update`
   and copy the golden file into this page.
2. **A breaking change** (rename, move, new meaning): bump `CurrentVersion`,
   add `migrations[n]` in `decode.go` taking the members of version *n* to
   *n+1* for every kind it affects, add a test with a version-*n* file, and
   describe the change in [Versions](#versions).
3. **A new kind** (an MQTT request): add the type, its `Kind`
   constant, `FileKind`, the cases in `decode` and `Marshal`, a section on
   this page and an example. Older versions keep such files untouched
   (`projectfile.unknown_kind`), so this needs no version bump.
4. **Never** add a member that holds a secret or local-only state. A
   secret member of `auth` holds only `{{variable}}` references; the
   collection tree and the import writer move typed values into local
   storage with `requestsecrets.Store.MoveOut` before any file is written
   (`Marshal` itself does not check auth members, so hand-written files
   still load). A new secret member must be added to
   `requestsecrets.SplitFile` and `MergeFile`, and to the project scan of
   `TestProjectFilesNeverHoldSecrets`.
