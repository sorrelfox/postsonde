# Environments

An environment is a named set of variables, such as Local, Staging and
Production (ENV-01). A project can have any number of them; at most one is
active at a time. Switching the active environment changes which values
`{{variables}}` get, without touching the requests.

| Piece | Where |
|---|---|
| Files, active environment, secret values, template scope | `internal/core/environments` (`Store`) |
| Local values of environment and global variables (SEC-02) | `internal/core/localvalues` (`Store`) |
| Runtime variables (ENV-15) | `internal/core/runtimevars` (`Store`, `Registry`), bound as `internal/app/services/runtimevars` (`RuntimeVarsService`), `frontend/src/lib/runtimeVariables.ts`, `frontend/src/components/environments/RuntimeVariables.tsx` |
| Environment file format | `internal/core/storage/projectfile`, [file-format.md](file-format.md#environment-environmentsjson) |
| Bound service | `internal/app/services/environments` (`EnvironmentsService`) |
| Binding wrappers | `frontend/src/lib/environments.ts` |
| Store and commands | `frontend/src/stores/environments.ts`, `frontend/src/commands/environments.ts` |
| Sidebar panel, title-bar switcher | `frontend/src/components/environments/` |
| Global variables (read, write) | `internal/app/services/collection/globals.go`, bound through `internal/app/services/environments/globals.go` |
| Scope chain and precedence | `internal/app/services/http/variables.go` (`ScopeChain`), `internal/app/services/http/runtime.go` (`RuntimeScope`) |

## Where things are kept

```
<project>/environments/<slug>.json                        the environment: id, name, colour, variables
<local>/environments.json                                 the active environment's id
<local>/secrets/environments/<environment id>.json        values of its secret variables
<local>/local-values/environments/<environment id>.json   local values of its variables (see Local values)
<local>/local-values/global/<workspace id>.json           local values of the global variables
<local>/cookies/<environment id>.json                     its cookie jar, with separate cookies per environment on (docs/cookies.md)
<local>/runtime-variables.json                            the persisted runtime variables (see Runtime variables)
<local>/secrets/runtime/variables.json                    values of the persisted secret runtime variables
```

`<local>` is the workspace's local side-file directory in the app data
directory (`<data>/workspaces/<id>/`, see [workspace.md](workspace.md)). It
is never inside the project, so:

- **Which environment is active is not part of the project.** Two people
  sharing a project each have their own choice, and switching never changes
  a project file. The choice survives a restart: the workspace reopens with
  the same local directory and `environments.json` in it.
- **Secret values never reach the project.** A variable marked secret is
  written to the project file with `"value": ""` and `"secret": true`; its
  value goes to the secrets file, which only the user can read (`0600`, in a
  `0700` directory). Values are stored by variable name, so two secret
  variables with the same name share one value.
- **Local values never reach the project either.** A variable's local
  value (SEC-02) replaces its shared value on this computer only; the
  project file keeps the shared value ([Local values](#local-values)).

> **Encryption.** The secrets file is plain JSON, readable by anyone who
> can read your user account's files, until you set a master password
> (SEC-03); it is then encrypted, and while the secrets are locked an
> environment with secret variables does not load and cannot be sent with
> (`secretstore.locked`; [privacy.md](privacy.md#encrypted-secrets)). A copy of a project (see
> [workspace.md](workspace.md#moved-and-copied-projects)) has its own local
> directory, so it starts without secret values and without an active
> environment.

The files `environments.json` and the secrets files have a `version` member
(`1`). `environments.json` that cannot be read counts as no active
environment. A secrets file that cannot be parsed is an error
(`environments.local_data_failed`): the environment is not loaded, so a save
cannot overwrite values that could not be read.

## Files in `environments/`

- Every `.json` file directly in `environments/` is an environment, except
  names starting with `.` (including interrupted saves) or `_`. Other files
  and folders are left alone.
- A new environment's file name comes from its name: lower-case ASCII
  letters and digits, other characters as `-`, at most 60 characters
  (`Local dev (8080)` → `local-dev-8080.json`). A name without such
  characters gives `environment.json`; Windows device names get `-env`
  (`con-env.json`). When the name is taken, ignoring case, `-2`, `-3`, … is
  added.
- **Renaming** writes the new name, then renames the file to match. If only
  the file rename fails, the environment keeps its new name in the old file.
  A change of case only (`Staging` → `STAGING`) keeps the file.
- Names are required, at most 100 characters, and unique ignoring case
  (`environments.name_taken`).
- **Deleting** removes the file and its secrets file. If it was the active
  environment, none is active afterwards.
- Saving variables rewrites the file only when its content changed. Members
  PostSonde does not know are kept, on the file and on each variable (matched
  by name). A secret value typed into the file by hand is shown and moves to
  the secrets file on the next save, or on a rename (unless the secrets file
  already has a value of that name, which is the one shown and kept).
- Files that cannot be loaded are listed under the environments in the
  panel and logged; the others still load. Reasons: not valid JSON or not an
  environment (`projectfile.*` codes), no `id` (`environments.missing_id`),
  or the `id` of another environment file (`environments.duplicate_id`; the
  file whose name sorts first wins).
- A file from a newer PostSonde is shown read-only
  (`environments.read_only`). In a workspace from a newer PostSonde nothing
  can be created, changed or deleted, but the active environment can still
  be chosen: it is local state.

Environment files are read again on every call. Changes made outside
PostSonde are noticed by the project watcher (STO-05): the environments
list reloads and `environments:changed` is sent
([collection.md](collection.md#external-changes)).

## Using an environment

`Environment.Scope()` returns the enabled variables as a `template.Scope`
named `Environment: <name>` (later definitions of a name win; secret
variables keep their flag), for the scope chain in
[templates.md](templates.md): `global < environment < collection < folder <
runtime`. `Store.Active()` returns the active environment. The HTTP sender
(HTTP and GraphQL requests) reads it on every send and renders the URL,
path parameters, auth, headers and body, and a GraphQL request's query and
variables, with the project's global variables, the active environment,
the collection's variables, then the folders' variables from the top down
([http-client.md](http-client.md#sending-from-the-app)), so switching the
environment changes what the next send sends (ENV-10). The editors draw
each reference as a chip, red when it does not resolve, and a send with
undefined names asks first (ENV-11,
[frontend.md](frontend.md#template-variables)). The WebSocket service uses
the same chain (`http.ScopeChain`) for a connection's URL, headers and
subprotocols on connect and for each message when it is sent (WS-11,
[websocket.md](websocket.md#variables)). The other senders are not wired to
it yet.

## Precedence

A name defined in several places resolves in one fixed order, lowest
first (ENV-06, details in [templates.md](templates.md#scopes-and-precedence)):

```
global < environment < collection < folder < runtime
```

- **Global** (ENV-02): `postsonde.json`'s `variables`, for every request of
  the project, whatever environment is active (see below). It is the base
  environment every environment inherits and overrides (ENV-05,
  [Base environment](#base-environment)).
- **Environment** (ENV-01): the active environment.
- **Collection** (ENV-03): the variables of a top-level folder. A
  collection is a top-level folder: the project root holds the global
  variables, so a project can have many collections.
- **Folder** (ENV-04): the variables of the folders inside a collection,
  from the top down; the folder nearest to the request wins.
- **Runtime** (ENV-15): the open project's runtime variables, set in
  the Environments view or by extraction rules from responses
  ([extraction.md](extraction.md)); see [Runtime variables](#runtime-variables).

A folder run's own variables ([runner.md](runner.md#run-variables))
come after all of these, for the requests of that run only:
`global < environment < collection < folder < runtime < run`.

**Local values** (SEC-02, [below](#local-values)) do not add a level: a
variable's local value replaces its shared value *within its level*. So
the order is, in full: levels first, then, inside the level that defines
the name, the local value over the shared one. An environment variable's
local value wins over the global variable of the same name (a lower
level), and loses to a collection or folder variable of that name (a
higher level), exactly as its shared value would.

The Environments view lists this order at its bottom (with a note on local
values); a chip's hover names the scope a value comes from, says when the
value is a local one ("Environment: dev · local value") and lists the
scopes it overrides, and links to the request's **Variables** sub-tab,
which lists every variable the request uses with each scope's value and
the one that is sent ([templates.md](templates.md#resolution-view)); the folder view's Variables tab says whether its
variables are collection or folder variables and lists those of the
folders above.

## Base environment

The **global variables are the base environment** (ENV-05): one
shared set that every environment inherits and overrides, like Insomnia's
base environment. There is no second shared level: a base environment
would be a set of variables below the environment, for every request of
the project, with secret and local values, which is exactly what the
global level already is. So existing projects need no change, the file
format has nothing new, and the precedence stays `global < environment <
…`.

- **Inherited.** A name the active environment does not define resolves
  to the global variable of that name, whichever environment is active.
  Unchanged values are written once, in `postsonde.json`.
- **Overridden.** An enabled variable of that name in the active
  environment wins over the base. Removing or disabling it inherits the
  base again. A secret environment variable without a value on this
  computer is left out of its scope like a global or folder one, so the
  name falls back to the base (earlier versions resolved it to `""`).
- **Secret and local values at both levels.** Each level keeps its own:
  a global secret in the workspace's side file, an environment secret in
  the environment's secrets file; a global local value in
  `local-values/global/`, an environment one in
  `local-values/environments/`. A local value replaces its shared value
  within its own level only, so an environment's shared value still wins
  over the base's local value, and an environment's local value over
  both.
- **UI.** Under an environment's table, **Inherited from the base
  environment** lists the global variables it inherits (enabled ones, a
  secret only with a value; the local value where there is one, a secret
  masked), each marked **Overridden here** when a row of the environment
  defines the name, or with **Override**, which adds a row of that name
  starting from the base's shared value (empty and secret for a secret,
  whose value is not copied). The list is reloaded after every
  `environments:changed` and when `postsonde.json` changes outside
  PostSonde (`useBaseVariables` in `EnvironmentsPanel.tsx`,
  `inheritedVariables` in `variableRows.ts`).
- **Tests.** `TestBaseEnvironmentInheritance` (internal/app/services/http)
  sends one request per case to a test server and checks the value sent,
  the scope and the secret and local flags the hover gets, for two
  environments (one overriding, one not), with secret and local values at
  both levels, a secret without a value and a disabled override; it also
  checks that no secret or local value reached a project file.

## Base URLs

Requests can use relative URLs, so one switch of the environment sends
them to another server (COL-08). **The base URL is the variable
`baseUrl`**, not a member of its own: a convention, shown in the UI.

- **What is relative.** A request URL that, with its `{{variables}}` and
  path parameters rendered and spaces trimmed, starts with exactly one
  `/`: `/users`, `/users/{{id}}?page=2`, `{{path}}/x` when `path` renders
  to `/api`. `//cdn.example/x` (a network path), `users` and absolute URLs
  are not. Such a URL never worked before (httpclient refused it: "The
  URL has no scheme"), so no existing request changes.
- **How it is joined.** The URL sent is the rendered `{{baseUrl}}`, its
  trailing slashes dropped, followed by the path as it is: `baseUrl =
  https://api.example.com/v1/` and `/users?page=2` give
  `https://api.example.com/v1/users?page=2`. A join, not RFC 3986
  resolution: a path in the base is kept. `request.ExpandURL`
  (`internal/core/request/baseurl.go`) does it, for every sender that
  builds an HTTP request: HTTP, GraphQL (POST, GET, SSE and
  graphql-transport-ws subscriptions, whose `ws(s)://` URL comes from the
  joined one), SSE requests, the GraphQL endpoint of schema fetches, and
  the netrc check. WebSocket, Socket.IO, TCP and gRPC URLs must stay
  absolute.
- **Why a variable.** It is resolved like every variable, through
  `global < environment < collection < folder < runtime < run`: each
  environment sets its server; the global variables (the base
  environment) give the default that environments without one inherit;
  a collection or folder can send its requests elsewhere; a local value
  points only this computer at `localhost`; a secret one is masked. The
  resolution view, the hover and completion show it like any variable,
  and a project imported from Postman, which calls it `baseUrl` already,
  works as it is. "One or more base URLs": other servers are other
  variables (`{{authUrl}}/token`).
- **Undefined.** A relative URL with no `baseUrl` in any scope is an
  undefined variable: the send lists `baseUrl` with the other undefined
  names ("Send anyway" sends `{{baseUrl}}/users`, which fails with "The
  URL has no scheme"). A `baseUrl` that is not an `http://` or `https://`
  URL with a host and without a query or fragment is refused with
  `request.invalid_base_url` (the message never repeats the value).
- **Snippets and exports.** Copy as cURL, HAR export and the resolved and
  masked snippets are built as a send is, so they hold the absolute URL
  (a secret base masked: `••••••/users`). The templated snippet keeps
  `{{baseUrl}}/users`. The Postman export writes a relative URL as
  `{{baseUrl}}/users`, since Postman has no such rule.
- **In the UI.** Under the URL of an HTTP, SSE or GraphQL request,
  `RelativeUrlHint` shows where a relative URL goes: `→
  https://staging.example/users/{{id}} · baseUrl from Environment:
  Staging` (masked when secret, "· local value" when it is one), or that
  no `baseUrl` is defined. It works from the request's resolved variables
  (`lib/baseUrl.ts`), so it recognises a URL typed with a leading `/`
  only; one that becomes relative once rendered is sent to the base all
  the same. Under an environment's variables, `BaseUrlNote` says which
  base URL it uses (its own, or the one inherited from the global
  variables) or how to set one.
- **Tests.** `TestExpandURL` and `TestBuildHTTPRelativeURL`
  (internal/core/request); `TestRelativeURLFollowsTheEnvironment`
  (internal/app/services/http) sends one relative request to two test
  servers while switching environments (own base, inherited base, a base
  with a path, a secret one, a local value, none active), and checks the
  cURL command, HAR entry, snippets, GraphQL endpoint, the invalid and the
  undefined base; `TestExportRelativeURL` (Postman).

## Raw JSON editor

A **Table / JSON** switch above every variables table (an environment's
and the global variables; ENV-07) shows the same variables as JSON text
in a CodeMirror JSON editor (`lib/rawVariables.ts`): an array with one
variable per line.

```json
[
  { "name": "baseUrl", "value": "https://staging.api.example" },
  { "name": "token", "value": "••••••", "secret": true },
  { "name": "user", "value": "team", "localValue": "me" },
  { "name": "old", "value": "x", "enabled": false, "description": "v1 only" }
]
```

- **Members:** `name` (required), `value` (default `""`), `localValue`
  (this computer's local value, never written to the project), `secret`,
  `enabled` (default `true`) and `description`; any other member is
  refused, so nothing is dropped silently. A plain object
  (`{"host": "localhost", "port": 8080}`) is read too, as plain variables
  (a value that is not a string becomes its JSON text).
- **Secrets stay out of the text.** A secret variable with a value shows
  `"••••••"`; one without a value on this computer shows `""`. Reading the
  text back, the mask keeps the value this computer holds for that name
  (so reordering or editing other members keeps it); typing another value
  replaces it. The mask is never taken as a value: on a variable that is
  not secret, or a secret name that has no value to keep (renamed, new),
  the text is refused with a message saying so. A secret variable has no
  local value.
- **Saving.** Each edit of the text that parses replaces the rows and is
  saved like a table edit (400 ms later, `SaveVariables` or
  `SaveGlobalVariables`). Text that does not parse is not saved; the
  reason is shown under the editor and the switch back to the table is
  disabled until it is fixed.
- **Round trip.** Table to text to table gives the same rows, and the
  text of those rows is the same text (`rawVariables.test.ts`,
  `VariablesEditor.test.tsx`). Saving variables that did not change never
  rewrites the file, whatever its layout: `SaveVariables` compares the
  content, not only the bytes, so a file written by hand stays byte for
  byte as it is (`TestEnvironmentFilesRoundTrip`, with the files in
  `internal/core/environments/testdata/roundtrip/`, which also pins the
  canonical layout PostSonde writes). The one exception is a secret value
  typed into the file by hand: the save moves it out, as before.

## Global variables

The Environments view has a **Global variables** row above the
environments. Selecting it shows postsonde.json's `variables` in the same
key/value table as an environment's (lock for secret, 400 ms auto-save).
They are read and written through the collection tree, which holds
`postsonde.json` (`collection.LoadGlobalVariables` /
`collection.SaveGlobalVariables`, bound through
`EnvironmentsService.GlobalVariables` / `SaveGlobalVariables`), so a
reorder of the top level later keeps them, and the next send uses them
without reopening the project. A save writes the file at once (only when
its content changed; members PostSonde does not know are kept, matched by
variable name), puts secret values in the workspace's side file (below)
and sends `environments:changed`, so open editors reload their chips. A
project from a newer PostSonde shows them read-only.

## Folder and collection variables

Folder variables (ENV-03, ENV-04) are the `variables` of each folder's
`_folder.json`, edited in the Variables tab of the folder's `DIR` tab
(COL-03, [collection.md](collection.md#folder-settings)), where the
variables of the folders above are listed read-only (struck through when
a nearer folder sets the same name). A top-level folder's tab calls them
**collection variables**. A folder's variables apply to every
request inside it and its subfolders; the nearest folder wins, and every
folder wins over the environment and the global variables. Edits count at
once, before they are written: the scopes read a folder's pending edit.
`HTTPService.Variables` (the chips) also takes a folder ID: its own
variables then come last, as for the requests inside, so the folder
view's `{{references}}` resolve the way they will at send time; the view
reloads them shortly after its variables change. Secret folder and global
variables are described next.

## Folder and global secret variables

A folder variable (the lock button in the folder's Variables tab) and a
global variable (the lock in the Global variables table) can be secret
too (SEC-01, SEC-04, PRV-09). Their
values are kept like a typed auth secret, in the item's side file
([auth.md](auth.md#secrets)), keyed `var.<name>`:

```
<local>/secrets/requests/<folder id>.json      {"values": {"var.apiKey": "…"}}
<local>/secrets/requests/<workspace id>.json   {"values": {"var.apiKey": "…"}}
```

The project file keeps the variable with `"value": ""` and `"secret":
true`. At send time the local value is used and marked secret: the chips
show it masked and the timeline shows `••••••` (DBG-01). A secret variable
that has no value on this computer (typed on another one, or in a copy of
the project) is left out of its scope, so a reference to it is reported
undefined (or resolves to a scope further out that defines the name).

**Typed into the file by hand.** A value found in a project file for a
secret variable (environment, folder or global) is used as it is when
there is no local value; a local value wins. The next time PostSonde
writes that file for any reason (an edit, a rename, a reorder), the value
moves to local storage (unless a local value is already there) and the
file gets `""`. Loading never writes, and when the local storage cannot be
written the project file is not written either, so no value is lost.

Dynamic values (DYN-01 to DYN-03: `{{$uuid}}`, `{{$timestamp +1d}}`,
`{{$isoTimestamp}}`, `{{$datetime YYYY-MM-DD}}`, `{{$randomInt 1 100}}`,
`{{$randomString 8}}`, `{{$randomHex}}`, `{{$randomBoolean}}`) need no
environment: the template resolver generates them for every reference at
send time, unless a scope defines the same name. So does fake data from
built-in word lists (DYN-05: `{{$randomFullName}}`, `{{$randomEmail}}`
at an `example.*` domain, `{{$randomCity}}`, `{{$randomLoremSentence}}`
and more, [templates.md](templates.md#fake-data)). See
[templates.md](templates.md#dynamic-values); typing `{{` in a request field
lists them with what each generates.

**Values from another request's response** (DYN-06):
`{{$response REQUEST jsonpath $.access_token}}`, `header NAME`, `status`
or `body` read the last response of another request (picked by name after
`{{$response `; the reference keeps its ID), and `send=missing`,
`send=10m` or `send=always` send that request first when its response is
missing, older than the age, or every time. See
[templates.md](templates.md#response-references).

**Encoding and hashing** (DYN-04). A reference can pass a variable
through filters: `{{ password | base64 }}`, `{{ "user:pass" | base64 }}`,
`{{ query | urlEncode }}`, `{{ body | sha256 }}`,
`{{ body | hmacSha256 signingKey base64 }}` (Base64 and back, URL and hex
encoding, MD5, SHA-1, SHA-256, SHA-512 and their HMACs). The result of a
secret input or key is secret too, so it is masked in the editor and the
timeline like the secret itself. See
[templates.md](templates.md#filters-encoding-and-hashing).

**Structured values** (ENV-13). A variable whose value is JSON can be read
field by field: with `auth = {"token": "t0k", "users": [{"id": 7}]}`,
`{{auth.token}}` sends `t0k` and `{{auth.users.0.id}}` sends `7`. Values
may use other variables (`base = {{scheme}}://{{host}}`) to any depth up to
10 levels; loops are reported and refused. A variable literally named
`auth.token` still wins over the lookup, so flat dotted names (as the
Insomnia import creates) keep working. To send `{{` as it is, write
`\{{` (ENV-14). Details in
[templates.md](templates.md#dot-notation).

## Local values

A variable of an environment, or a global variable, can have a **local
value** (SEC-02): a value that stays on this computer and, here,
replaces the variable's shared value. A personal token, a local base URL,
your own user name: the project file keeps the shared value, which
teammates see and send; this computer sends its own.

- **Where it is kept:** `<local>/local-values/environments/<environment
  id>.json` for an environment's variables and
  `<local>/local-values/global/<workspace id>.json` for the global ones,
  in the workspace's local directory next to the secret values, never in
  the project: `{"version": 1, "owner": <id>, "values": {<name>: <local
  value>}}`, `0600` in `0700` directories, written atomically
  (`internal/core/localvalues`, the only reader and writer). Values are
  kept by variable name; an empty local value is a value (it sends `""`).
  A file that cannot be parsed is an error (`localvalues.local_data_failed`)
  and the environment, or the global variables, are not loaded, so no
  save overwrites it.
- **Never in the project:** the project file is written with the shared
  value only, and setting or changing only a local value does not rewrite
  it. Nothing that writes project files (the tree, the importers, the
  environments store, `SaveGlobalVariables` of the collection service)
  ever receives a local value: the environments service splits it off
  before. Exports read the project files (the Postman export, HAR, cURL
  use the value that is sent, see below), so a Postman export carries the
  shared value. `TestProjectFilesNeverHoldSecrets`,
  `TestLocalValuesStayOutOfTheProject`, `TestLocalValues` and
  `TestExportPostmanLeavesOutSecretAndLocalValues` check it.
- **Not a secret.** A local value is shown in the table, in the chips and
  their hover, in the timeline, the history and a copied cURL command like
  any value: it is what this computer sends. For a password or token, mark
  the variable secret (the lock). A secret variable has no local value: its
  value is local already. The two do not combine: the lock is disabled
  while a row has a local value, and a secret row has no local value
  button; Go ignores a local value sent with a secret variable, and a
  local value kept for a variable that is now secret.
- **Precedence:** within the variable's own level, before the shared value
  ([Precedence](#precedence)). A disabled variable is left out with its
  local value. A local value for a name the file no longer has (a
  teammate renamed or removed the variable) is ignored and dropped at the
  next save of the variables.
- **Lifetime:** kept per workspace, so a copy of the project (see
  [workspace.md](workspace.md#moved-and-copied-projects)) starts without
  them; deleting an environment removes its file.
- **Editing:** the monitor button of a row in the variables table (not
  shown for a secret row) adds a local value, starting from the shared
  value, or removes it. Rows with a local value are listed under the
  table in **Local values · this computer only**: the name, the local
  value in a text field (focused when just added) with the shared value
  below it, and a remove button. In the table the shared value stays
  editable and is drawn struck through. Edits are saved with the others,
  400 ms after the last change.
- **In `VariableDTO`** (`EnvironmentsService`): `localValue`, a string, or
  `null` for none (always `null` for a secret variable). In the chips'
  `HTTPService.Variables`: `local: true` on a value that is a local one.

Folder and collection variables have no local values yet.

## Runtime variables

Runtime variables (ENV-15) are values set while working: typed in
the Environments view, or taken from responses by a request's extraction
rules ([extraction.md](extraction.md)). They are the **Runtime** scope, above every scope a project file
defines, so they win over a global, environment, collection or folder
variable of the same name; only a folder run's own variables
([runner.md](runner.md#run-variables)) win over them, within that run.

- **Scoped to the project.** Each open project has its own; switching
  projects switches them. `runtimevars.Registry` (one for the app) hands
  out the `Store` of the open project.
- **Session or persisted, per variable.** A *session* variable lives in
  memory only: it is gone when the project is closed or opened again
  (every `workspace.Manager.Open` returns a new `Workspace`, and the
  registry starts a new session for it) and when PostSonde quits. A
  *persisted* one ("Kept", the pin) is also written to the workspace's
  local directory and is back the next time the project opens:
  ```
  <local>/runtime-variables.json            {"version": 1, "variables": [{"name", "value", "secret"}]}, sorted by name
  <local>/secrets/runtime/variables.json    {"version": 1, "values": {<name>: <value>}}: the persisted secret ones
  ```
  Both `0600` in `0700` directories, written atomically, never in the
  project: nothing that writes project files sees them, and an export
  reads the project files (`TestRuntimeVariablePrecedence`,
  `TestFlow` in `internal/app/services/runtimevars` and
  `TestRegistrySessions` check that the project folder is untouched).
  Session variables are never written anywhere.
- **Secret runtime variables.** A runtime variable can be secret (the
  lock): a token taken from a login response is the case in point. It is
  masked like every secret variable: the chips show no value, and the
  timeline, the history, a copied cURL command and a HAR export show
  `••••••` (the scope marks it `Secret`, so the send's masker has its
  value; `TestSecretRuntimeVariableMasked`). A copied or saved log is
  masked with it too, session ones included (`knownSecrets`). A persisted
  secret keeps its value in the secrets directory, next to the other
  secret values, and the list file has `""` for it, so the encryption of
  the secrets (SEC-03) covers it. A secret listed without a value there is
  left out (undefined, not `""`). The Runtime section shows a secret's
  value only when its eye is clicked.
- **Names** are those a `{{reference}}` can spell: not empty, at most 256
  bytes, no whitespace around them, no `{{` or `}}`
  (`runtimevars.invalid_name`). Setting a name that exists replaces it.
- **A file that cannot be read** (`runtimevars.local_data_failed`) fails
  every send of the project, as a broken file of local values does, so no
  change overwrites it; the Runtime section shows the error with **Clear
  all**, which removes the files whatever they hold.
- **Where they apply.** Every sender that renders with the project's
  scopes: HTTP, GraphQL and SSE sends, previews (cURL, HAR, the header
  preview, JWT and netrc), GraphQL schema fetches and subscriptions,
  folder runs, WebSocket and Socket.IO connections (`http.RuntimeScope`
  after `http.ScopeChain`). gRPC and TCP do not render variables yet.
- **Hover and completion** say `Runtime` (the scope name,
  `runtimevars.ScopeName`; `source: "runtime"` in `HTTPService.Variables`)
  and list the scopes the value overrides, like any other scope.

**Setting one from code.** Extraction after a response
([extraction.md](extraction.md)) sets a value with
`Store.Set(runtimevars.Variable{Name, Value, Secret, Persist})`. The store of the request's project is
`HTTPService.runtimeVariables(src.Workspace)` in the HTTP service (the
registry's `For`), for a single send (`Send`, `Repeat`) as for a request
of a run (`RunSession.Send` goes through the same service): the next
render of any request of the project sees it. Within a run, a name the
run's own `Vars` define still wins; set both when a run must see the new
value whatever its `Vars` hold (a runtime extraction rule does). Every change, from anywhere, calls the
registry's change hook, which sends `runtimevars:changed` (`ChangedDTO:
count`): the Runtime section reloads its list and the editors reload
their chips.

**Service.** `RuntimeVarsService` (TypeScript: `RuntimeVarsService.*`,
wrapped by `lib/runtimeVariables.ts`); every method works on the open
workspace (`workspace.none_open` without one) and answers with the whole
list, `RuntimeVariablesDTO{variables}`, sorted by name, each
`RuntimeVariableDTO{name, value, secret, persist}` (a secret's value
included: the UI masks it):

| Method | Does |
|---|---|
| `List()` | the list |
| `Set({previousName, name, value, secret, persist})` | sets one; with `previousName` another name, replaces that one (a rename) in one step |
| `Delete(name)` | removes one (`runtimevars.not_found`) |
| `Clear()` | removes them all, session and persisted, also an unreadable file |

**UI.** The **Runtime variables** row of the Environments view shows the
**Runtime** section: the variables (name, value masked for a secret with
an eye to show it, **Kept** or **Session**, and per row the lock for
secret, the pin for kept, and delete), a form to set one (name, value,
"Secret", "Keep after closing the project"; clicking a name loads it into
the form to edit or rename it), and **Clear all** (asks first). It loads
when shown and again on `runtimevars:changed`.

## Colour

An environment can have an accent colour (ENV-08), so the one in use is
recognisable at a glance ("production is red").

- **Where it is kept:** the `color` member of the environment file
  (`"#rrggbb"`, [file-format.md](file-format.md#environment-environmentsjson)),
  not in local data. The colour is shared intent, like the name: everyone
  who opens the project sees production in red. Clearing it removes the
  member. Choosing it rewrites the file only when it changed; other members
  are kept as for every save.
- **Setting it:** the selected environment's **Colour** row in the
  Environments view: No colour, nine presets (Red, Orange, Yellow, Green,
  Teal, Blue, Purple, Pink, Grey; `lib/environmentColors.ts`) or a custom
  colour from the native colour picker (saved when the picker closes).
  Go accepts `""` or six hex digits and stores them in lower case; anything
  else is `environments.invalid_color`. A colour typed into the file by
  hand in another form is ignored by the UI (no colour is shown).
- **Where it shows:** the title bar's environment switcher gets a 3px bar
  in the colour on its left, its dot in the colour and a light tint of the
  pill; a 2px stripe runs along the top of the title bar; the Environments
  view's dots use it. With no active environment, or one without a colour,
  none of that is shown.
- **Accessibility:** no text is ever drawn in the environment colour or on
  a solid fill of it. The name stays in `--text` on a background mixed from
  `--bg1` with at most 14% of the colour (22% on hover): with the built-in
  themes the contrast stays above 7:1 (dark) and 10:1 (light) for any
  colour, well over WCAG AA (4.5:1). The colour is extra information: the
  name is always shown, and the picker's buttons are named after their
  colours.

## Export

EXP-05: **Export…** and **Export for Postman…** on the selected
environment (also in a read-only project) write it to a file picked in
the native save dialog (`EnvironmentsService.Export`,
`internal/app/services/environments/export.go`):

| Format | File | Read back by |
|---|---|---|
| `postsonde` | a PostSonde environment file, `<slug>.json`: the format of the project's `environments/` files (`kind` `environment`, `id`, `name`, `color`, `variables`), only the members PostSonde knows | the PostSonde environment importer ([import.md](import.md#postsonde-environments)), which makes a new environment with a free name and a new ID; or copied as it is into another project's `environments/` folder (the same ID: in a project that has it already, one of the two is skipped as `environments.duplicate_id`) |
| `postman` | a Postman environment export, `<slug>.postman_environment.json`: `values` with `key`, `value`, `type` (`default` or `secret`), `enabled` and `description`; `_postman_variable_scope` `environment`; an `id` derived from the environment's ID (a UUID in the version 5 layout, so a second export has the same one) | Postman, and the Postman importer (IMP-03) |

**No secret and no local value.** The export is made from the
environment's file in the project only (`environments.Store.Shared`); the
local side files are never read. A secret variable is written as a secret
without a value, also when a value was typed into the file by hand (it
is counted and logged, never written); `projectfile.Marshal` refuses a
secret with a value on top of that. Local values (SEC-02) live only in the
local side files, so the shared value is exported. The toast says how
many secret variables were written without their value. Global variables
(`postsonde.json`) are part of the project and are not exported here.

Errors: `environments.not_found`, `environments.invalid_format`,
`environments.export_failed` (the file could not be written; details
`path`), `environments.export_unavailable`. A cancelled dialog writes
nothing (`saved: false`). Tests: `TestExportLeavesOutSecretAndLocalValues`
(the service, both formats, each read back by its importer),
`TestSharedLeavesOutSecretAndLocalValues` (core),
`TestExportEnvironmentRoundTrip` (Postman), `EnvironmentsPanel.test.tsx`.

## Service

`EnvironmentsService` (TypeScript: `EnvironmentsService.*`, wrapped by
`lib/environments.ts`). Every method works on the open workspace and fails
with `workspace.none_open` without one.

| Method | Returns |
|---|---|
| `List()` | `EnvironmentsDTO`: `environments` (sorted by name), `activeId` (`""` for none), `problems` (`fileName`, `message`, `code`), `readOnly` |
| `Create(name)` | `EnvironmentDTO` |
| `Rename({id, name})` | `EnvironmentDTO` |
| `Delete(id)` | nothing |
| `SaveVariables({id, variables})` | `EnvironmentDTO`; `variables` replace the old ones, in order |
| `SetActive(id)` | nothing; `""` for none |
| `SetColor({id, color})` | `EnvironmentDTO`; `color` is `"#rrggbb"` (stored in lower case) or `""` for none, written to the environment file (ENV-08) |
| `Export({id, format})` | `ExportResultDTO` (`saved`, `path`, `variables`, `secrets`): the environment as a file picked in the save dialog, `format` `"postsonde"` or `"postman"`, without secret or local values ([Export](#export)) |
| `GlobalVariables()` | `GlobalVariablesDTO`: `variables` (file order; secret ones with the local value, others with their `localValue`), `readOnly` |
| `SaveGlobalVariables({variables})` | `GlobalVariablesDTO`; `variables` replace the global ones, in order, and `postsonde.json` is written at once; their local values replace those in `<local>/local-values/global/`, written first |

`EnvironmentDTO` has `id`, `name`, `color`, `fileName`, `readOnly` and
`variables`; a `VariableDTO` has `name`, `value` (the shared value; for a
secret, the value kept on this computer), `description`, `secret`,
`enabled` and `localValue` (the local value, [Local values](#local-values);
`null` for none). `SaveVariables` and `SaveGlobalVariables` replace the
local values with those given. Calls are serialised. The UI loads the list
when a project opens and applies each call's result.

**Event.** After every successful `Create`, `Rename`, `Delete`,
`SaveVariables`, `SetActive`, `SetColor` and `SaveGlobalVariables`, the service sends `environments:changed`
(`ChangedDTO`: `activeId`, `""` for none). It is not needed by the
environments store, which applies the call's result; it tells the request
editors that what `{{variables}}` resolve to may have changed, so they
reload their resolved set (ENV-11, [frontend.md](frontend.md#template-variables)).
`List` and failed calls send nothing.

Error codes: `environments.not_found`, `environments.invalid_name`,
`environments.invalid_color`,
`environments.name_taken`, `environments.read_only`,
`environments.list_failed`, `environments.save_failed`,
`environments.local_data_failed`, `environments.invalid_format`,
`environments.export_failed`, `environments.export_unavailable` (Export
without a save dialog), `localvalues.local_data_failed` (a file
of local values that cannot be read or written), plus the `projectfile.*` codes for a file
that cannot be written; the global variables calls fail with the
collection service's codes (`collection.read_only`,
`requestsecrets.local_data_failed`) and `environments.globals_unavailable`
when the app did not wire them (`WithGlobals`). Messages never contain variable values; the service
logs ids and file names only.

## UI

- **Title bar:** the switcher shows the active environment with its colour
  (the `color` member: a bar, a tint and the dot, plus a stripe along the
  top of the title bar, see [Colour](#colour); without one, the dot in the
  accent colour) in a native select;
  "No environment" deactivates. It is disabled while the project has no
  environments. The switch shows at once and is undone if it cannot be
  saved.
- **Sidebar, Environments view** (the rail button): the environments, with
  the active one's dot ringed and marked "Active". `+` creates one (inline
  name field: Enter creates, Escape cancels). Clicking a row selects it,
  double-clicking activates it.
- **Selected environment:** Activate (or Active, which deactivates), the
  Colour row ([Colour](#colour)), Rename
  (inline), Export… and Export for Postman… ([Export](#export)) and Delete (asks first in a danger dialog, `confirm()`; after
  the delete a toast says so, without undo: the file is gone), then its variables in the key/value
  table without the description column. Edits are saved 400 ms after
  the last change, and at once when another environment or sidebar view is
  selected. The lock button marks a variable secret: its value is masked
  and the eye button shows it. The monitor button gives a variable a local
  value, edited in the list under the table ([Local values](#local-values)).
  The Table / JSON switch shows the variables as JSON text
  ([Raw JSON editor](#raw-json-editor)); under the table, the global
  variables the environment inherits and overrides
  ([Base environment](#base-environment)).
- **Global variables:** the row above the environments (globe icon) shows
  the global variables, the base environment, in the same table (and the
  same Table / JSON switch) instead of the selected environment
  (`GlobalDetails`, loaded from Go each time it is shown). Clicking an
  environment goes back to it.
- **Runtime variables:** the row below it (play icon) shows the runtime
  variables instead of the selected environment (`RuntimeVariables`,
  [Runtime variables](#runtime-variables)). Clicking an environment goes
  back to it.
- **Precedence:** the bottom of the view lists `Global < Environment <
  Collection < Folder < Runtime` (`VARIABLE_PRECEDENCE` in
  `lib/environments.ts`), each with a title saying where that level is
  defined (Runtime's adds that a folder run's own variables win over it),
  and notes that within a level a local value wins over the shared value.
- The store (`environments`) is loaded by `useEnvironmentsSync`, called by
  the shell of the open project; it reloads when another project opens and
  clears when the project closes. Results that arrive after the project
  changed are dropped.

## Not handled yet

- Local values of folder and collection variables (SEC-02 covers
  environment and global variables).
- Environments inheriting from another environment (only the global
  variables are a base, see [Base environment](#base-environment)), and
  YAML in the raw editor (JSON only, like the project files).
- Relative URLs for WebSocket, Socket.IO, TCP and gRPC requests
  ([Base URLs](#base-urls) covers HTTP, GraphQL and SSE).
- References to another request's last response; setting
  variables from responses is done by extraction rules
  ([extraction.md](extraction.md)).
- Edits typed less than 400 ms before the project is closed are not saved.
