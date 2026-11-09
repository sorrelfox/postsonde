# PostSonde architecture

PostSonde is a local-only desktop API testing tool. The engine is written in
Go using only the standard library, the desktop shell is Wails v3, and the UI
is React + TypeScript. Storage is plain JSON files.

## Layout

```
cmd/postsonde/          entry point: resolves appdirs, takes the single-instance lock
                        (app.New, before anything is written; a second start opens a
                        window in the running process), opens the rotating log file
                        (and the console's logging.Ring, app.Config.Console), embeds
                        the UI, runs internal/app with the project folder of the
                        command line, sets the version (docs/windows.md)
internal/
  app/                  Wails integration: app and window setup (app.Options, app.Config);
                        the windows (windows.go: windowHost, InstanceID, titles); file
                        drops forwarded to the window they were dropped on as
                        collection:files-dropped (filedrop.go, docs/import.md#dropped-files)
    windowctx/          the window a bound call came from (application.WindowKey), 0 for none
                        (docs/windows.md)
    services/<domain>/  one bound Wails service per domain, with its DTOs (docs/bindings.md)
                        e.g. services/settings (Get/Update/Problems, settings:changed),
                        services/proxyscopes (Scopes/SetScope, proxyscopes:changed: the
                        project and environment proxies; docs/network.md#proxy-scopes),
                        services/workspace (Open/Create/Close/Recent/PickFolder,
                        workspace:changed; docs/workspace.md),
                        services/collection (Tree/Reload, tree operations and trash,
                        collection:changed; Request/UpdateRequest/Flush/FlushAll
                        auto-save, collection:saved and collection:save-failed;
                        ExportPostman, a folder as a Postman collection;
                        ExportFolderZip, a folder as a zip of project files;
                        docs/collection.md),
                        services/environments (List/Create/Rename/Delete/
                        SaveVariables/SetActive/SetColor; Export, an environment
                        without secret or local values; docs/environments.md),
                        services/runtimevars (List/Set/Delete/Clear of the runtime
                        variables, runtimevars:changed; docs/environments.md),
                        services/servers (Types/List/Create/UpdateConfig/Start/Stop/
                        Remove/Traffic/ClearTraffic and the per-type operations,
                        servers:changed, the per-type list events and batched
                        servers:traffic; stops every server at shutdown;
                        docs/servers.md),
                        services/tcp (Connect/Send/Close/CloseAll, TLS with the app's trust
                        and client certificates, framing; tcp:state and batched tcp:data;
                        closed on shutdown and workspace change; docs/tcp.md),
                        services/udp (Open/Send/Close/CloseAll, {{variables}}, through
                        SOCKS5 UDP ASSOCIATE; udp:state and batched udp:data; closed on
                        shutdown and workspace change; docs/udp.md),
                        services/websocket (Connect/Send/Close/CloseAll, ws:state with the
                        handshake and close info, batched ws:message; the project's cookie
                        jar; closed with 1001 on shutdown and workspace change;
                        docs/websocket.md),
                        services/grpc (Describe: links a request's .proto setup and lists
                        its services, methods and diagnostics, cached while the files are
                        unchanged; PickProtoFiles/PickImportPath; Invoke; ExampleMessage/
                        ValidateMessage for the message editor; docs/grpc.md),
                        services/sse (Connect: an sse request built like an HTTP send,
                        read with SendStream and the sse parser as an sse operation whose
                        items are the stream's events; docs/sse.md),
                        services/socketio (Connect: a socketio request's sioclient
                        connection as a sio operation whose items are the handshake,
                        events, acks and heartbeat; Emit on it; docs/socketio.md),
                        services/console (List/Clear over the logging.Ring of recent
                        redacted log records, batched console:entries; DBG-06,
                        docs/frontend.md#console),
                        services/network (List/Clear over the netlog.Log connection
                        log, throttled network:activity; PRV-06,
                        docs/network.md#connection-log),
                        services/logexport (Mask/Save: copied and saved timeline and
                        log entries, masked with every secret the app keeps; DBG-07,
                        docs/privacy.md#copied-and-saved-logs),
                        services/themes (List/OpenFolder over the custom theme
                        files; UI-05, docs/themes.md)
                        services/localdata (Usage/Clear: the history, run history,
                        cookies, OAuth 2.0 tokens, cached schemas and runtime
                        variables of the open project or of every project; PRV-13,
                        docs/privacy.md#clearing-local-data)
                        services/secrets (Status/Enable/Unlock/Lock/ChangePassword/
                        Disable/Reset/SetIdleLock over the secretstore vault, the idle
                        lock timer, secrets:changed; SEC-03,
                        docs/privacy.md#encrypted-secrets)
                        services/history (List/Get/Delete/Clear over the open project's
                        request history, history:changed; Recorder hands the HTTP service
                        the recorder of every send; Example, an entry as a response
                        example masked again, HTTP-38; NAV-02, docs/history.md)
                        services/runner (Plan/Start/Response/Release: the folder runner, a
                        folder's requests (the chosen ones, in the chosen order) for N
                        iterations with a delay or once per row of a data file chosen in
                        the open dialog (PickDataFile/PreviewDataFile), stopping at a
                        failure if asked, sent through the HTTP service's RunSession,
                        runner:op:* events; the results' responses kept in memory,
                        bounded app-wide; Runs/Run/DeleteRun/ClearRuns over the run
                        history, ExportRun as JUnit XML, JSON or HTML with the save
                        dialog; RUN-01 to RUN-09, docs/runner.md)
                        services/window (Current/NewWindow/OpenInNewWindow; Launch for a
                        second start of PostSonde, window:open-project; APP-06,
                        docs/windows.md)
    apperr/             turns any error into the ErrorDTO the UI gets in the rejected
                        promise's `cause`; every service is registered with
                        serviceOptions (docs/errors.md)
    operation/          runs long-running operations and streams <domain>:op:* events,
                        cancelled by ID via services/ops (docs/events.md)
  core/                 engine; standard library only (see below)
    httpclient/         HTTP/REST client on net/http over a netx.Dialer: requests sent
                        as configured (no default headers, proxy or cookie jar), raw
                        response capture with a body cap, cancellation at every stage
                        (docs/http-client.md). Every Response carries per-hop timing,
                        connection and TLS info and a raw event timeline from
                        httptrace; netx trace events feed the timeline only; an optional
                        cookie jar per client (WithCookieJar)
    tlsconf/            user TLS settings (verification, server name, custom CA PEM files, client
                        certificate and key, min version) to *tls.Config with coded validation
                        errors and warnings; shared by gRPC, the app-wide CA bundles (Trust) and
                        the per-host client certificates (ClientCerts: host patterns, PEM or
                        PKCS#12, cached per entry); tlstest/ generates CAs and certificates in
                        tests and serves the PKCS#12 fixtures (docs/http-client.md#tls-options,
                        docs/network.md#client-certificates)
    pkcs12/             PKCS#12 (.p12/.pfx) decoder: encoding/asn1 with BER normalization, MAC
                        (HMAC with the PKCS#12 KDF, PBMAC1), PBES2 (PBKDF2, AES/3DES/RC2) and
                        SHA1 PBEs (3DES, RC2, RC4); RC2 by hand (RFC 2268); key + ordered chain
                        as a tls.Certificate; DecryptPKCS8 for encrypted PKCS#8 keys (docs/pkcs12.md)
    respdiff/           two responses compared (HTTP-40): header names ignoring case,
                        JSON bodies by path (arrays by index) as pretty-printed rows,
                        other text by a Myers line diff, binary equal or not; limits
                        with notices (docs/response-diff.md)
    history/            request history (NAV-02): append-only JSON Lines segments with
                        rotation, count and byte retention, an in-memory index, torn-line
                        repair at open, masked body and header snapshots; a per-workspace
                        Registry; Example, an entry as a response example (HTTP-38)
                        (docs/history.md)
    cookies/            RFC 6265 cookie jar (CK-01): lenient Set-Cookie parsing, domain/
                        path/expiry/Secure rules, public suffix heuristic, limits and
                        eviction, listing; saved to <local>/cookies.json (and, with
                        separate cookies per environment, <local>/cookies/<env>.json;
                        CK-03) with a debounced Store and a per-workspace Registry
                        (docs/cookies.md)
    graphql/            GraphQL engine (docs/graphql-engine.md)
      lexer/            tokens with byte offsets, line/column and error tokens that
                        never abort; string/block string values; UTF-16 offset index
      ast/              executable-document syntax tree: byte ranges on every node,
                        delimiter offsets, placeholders for missing pieces,
                        NodePathAt(offset) for completion, Dump for tests
      parser/           executable documents to ast with error recovery: partial
                        trees for half-typed queries, errors with code and byte
                        range, depth and error caps; SDL skipped as not executable
      schema/           schema model built from introspection results: types,
                        directives, descriptions, deprecations, type refs,
                        lookups incl. built-ins and meta fields; full and
                        conservative introspection queries; broken references
                        reported as Problems
      validate/         validation against the schema (GQL-12): diagnostics with
                        code, byte range and suggestions; Rule/Visitor/Context
                        framework; exported TypeInfo walker (reused by
                        autocomplete); never reports parser placeholders
      generate/         documents from the schema (GQL-15..17): operations and fragments
                        to a depth with recursion cut, variables for required
                        arguments, fields to fill in at a cursor, the docs target
                        of a name
      complete/         completion at a cursor: context from the partial tree
                        (selections, arguments, values, variables, types,
                        directives, fragments); ranked items with type,
                        description, deprecation and replace range
      transportws/      graphql-transport-ws client over wsclient (GQL-30): init/ack
                        with timeout (4408), subscribe with ids, next/error/complete,
                        ping/pong, invalid messages closed with 4400, close codes,
                        protocol event log; the legacy subscriptions-transport-ws
                        protocol (graphql-ws: start/data/stop, ka, connection_error,
                        connection_terminate) as Options.Protocol (GQL-31)
                        (docs/graphql-subscriptions.md)
      transportsse/     GraphQL over SSE, graphql-sse distinct connections (GQL-32):
                        reads a response's next/complete events and keep-alive
                        comments with realtime/sse, or a non-stream answer as one
                        result; the caller sends the request (docs/graphql-subscriptions.md)
      incremental/      @defer/@stream responses (GQL-08): multipart/mixed and
                        text/event-stream bodies split into payloads as they
                        arrive, timed, and merged (older and newer payload
                        formats) keeping member order and literals (docs/graphql.md)
    grpc/               gRPC client                    (in progress)
      proto/            .proto files without protoc (docs/proto.md): lexer/, ast/ and
                        parser/ for proto2, proto3 and editions with error recovery,
                        line:column errors, protoc-style comments and single-file
                        Check diagnostics; linker/ loads imports through import
                        paths (overlays, embedded wellknown/ types) and resolves
                        names, extensions, options and features into desc/, the
                        linked descriptor model; descproto/ converts
                        FileDescriptorProto bytes to and from that model
                        (docs/grpc-reflection.md)
      protowire/        protobuf wire primitives (varints, tags, fixed, length-delimited,
                        groups) with strict bounds (docs/protobuf-codec.md)
      dynamic/          messages typed by desc descriptors at run time: typed field access,
                        binary decode/encode with protobuf-go's rules (packed/unpacked,
                        merging, oneofs, maps, closed enums, UTF-8 by syntax/features),
                        unknown fields kept, extensions via a resolver, offset+path errors
      protojson/        canonical proto3 JSON mapping for dynamic messages, both ways, with
                        protobuf-go's rules and all well-known type forms (Any, Timestamp,
                        Duration, Struct/Value, wrappers, FieldMask); offset errors
                        (docs/protobuf-json.md)
      transport/        gRPC over net/http HTTP/2 without grpc-go (docs/grpc-transport.md): h2c
                        prior knowledge or TLS ALPN h2 over netx.Dialer, Stream for all four
                        call kinds on raw bytes, length-prefixed framing with receive limit and
                        gzip, metadata incl. -bin, grpc-timeout, status from trailers/trailers-
                        only/HTTP mapping; h2c, TLS with custom CAs and mTLS (tlsconf) with coded
                        certificate errors; grpctest/ in-process test server (h2c, TLS, mTLS)
      rpcstatus/        google.rpc.Status from grpc-status-details-bin with its Any details
                        decoded by type URL (built-in error_details.proto types, then the
                        caller's), unknown types kept as bytes (docs/grpc.md)
      reflection/       server reflection v1/v1alpha client (reflectwire/ messages), linking
                        via descproto, per-project JSON cache for offline use
                        (docs/grpc-reflection.md)
    realtime/           WebSocket, SSE, Socket.IO, raw sockets
      wsframe/          RFC 6455 framing shared by the WebSocket client, server and
                        GraphQL subscriptions: frame encode/decode with every wire
                        detail, message layer (Conn) per role with fragmentation,
                        limits, incremental UTF-8, auto pong, close handshake; never
                        dials (docs/websocket.md)
      wsclient/         WebSocket client: upgrade handshake by hand over netx.Dialer (TLS,
                        subprotocols, cookie jar, raw request/response, timing), states
                        idle/connecting/open/closing/closed, close codes both ways, 1006
                        synthesized (docs/websocket.md)
      tcpclient/        raw TCP client (TCP-01) over netx.Dialer: connect timeout, TLS with
                        trust and client certificates (TCP-02), states connecting/open/
                        closing/closed with CloseInfo (who, error, bytes), chunks as read or
                        framed messages, half-close (docs/tcp.md)
      udpclient/        UDP client (UDP-01) over netx.Dialer: connected or unconnected,
                        udp4/udp6, datagrams with their sender, notes for dropped datagrams
                        and ICMP errors, SOCKS5 UDP ASSOCIATE (SOCKS-07; docs/udp.md)
      framing/          TCP message framing (TCP-03): newline-delimited and 1/2/4/8-byte
                        length-prefixed, split across reads, maximum message size (docs/tcp.md)
      sse/              Server-Sent Events parser (WHATWG "Interpreting an event stream"):
                        incremental Feed/Close and an io.Reader iterator, UTF-8 with
                        replacement, BOM, CR/LF/CRLF split across chunks, comments and
                        fields with offsets for the timeline, size limits (docs/sse.md)
      engineio/         Engine.IO v4 packet codec: open/close/ping/pong/message/upgrade/noop,
                        text and binary, WebSocket framing, long-polling payloads (\x1e,
                        "b"+Base64), handshake JSON; Engine.IO 3 (v3.go: type-byte binary
                        messages, "<length>:" and XHR2 binary payloads, "b4"+Base64);
                        strict coded errors (docs/socketio.md)
      socketio/         Socket.IO v5 (servers v3/v4) packet codec: all 7 types, namespaces,
                        ack IDs, binary attachments and placeholders, Decoder per direction,
                        arguments typed as JSON with $base64/$hex markers (ParseArgs),
                        exact round trip on fixtures captured from socket.io 4.8.1
                        (docs/socketio.md)
      sioclient/        Socket.IO v4 client: Engine.IO handshake over WebSocket or
                        long-polling, fallback and upgrade, heartbeat, one namespace with
                        auth, emits with acks and binary arguments, traces; Socket.IO v2
                        (Engine.IO 3, Options.Version, SIO-02); siotest/ is an in-process
                        server for tests, in v4, v3 or v2 mode (docs/socketio.md)
      hexbytes/         bytes typed as hex ("DE AD", "0xDE,0xAD", "deadbeef", "\xde") with
                        the offset of an invalid character; mirrored in lib/hexBytes.ts
    servers/            local servers (docs/servers.md): Manager with start/stop, state
                        machine (starting, running, stopping, stopped, failed), graceful
                        stop then force close, StopAll; listens only through
                        netx/listen; ServerType plug-in interface; traffic events with
                        capped content and a recent buffer per server
      httpbase/         what the HTTP types share: net/http on the manager's listener,
                        connection IDs and events, graceful stop, request events
      echo/             Echo HTTP server type: JSON echo reply
      mockhttp/         Mock HTTP server type: hand-made routes (status, headers, body,
                        fixed or random delay), conditional and header-selected
                        responses, templated responses, 404/405, live config swap
      sseserver/        SSE server type: text/event-stream subscribers, events pushed
                        from the app or on timers (dynamic values rendered), CORS,
                        keep-alives, Last-Event-ID shown
      udpserver/        UDP server type (a PacketType on listen.ListenPackets): datagram
                        log, echo and fixed replies, send to a recent peer
      grpcmock/         gRPC mock server type (SSRV-07): the methods of .proto files
                        (the repo's linker) with canned JSON responses (protojson and
                        dynamic), statuses, trailers, delays and streams, over h2c, h2
                        over TLS and gRPC-Web on one port, server reflection v1 and
                        v1alpha (reflectwire, descproto.Marshal), a log entry per call
                        and message
      sioserver/        Socket.IO server type (SSRV-03): Engine.IO 4 over long-polling and
                        WebSocket with the upgrade and heartbeat, Socket.IO 5 packets on the
                        client's codecs: namespaces with required auth, events and acks both
                        ways with attachments, sockets, rooms, emits, auto-reply rules
      graphqlmock/      GraphQL mock server type (SSRV-08): a schema from SDL or an
                        introspection result, operations parsed and validated by the
                        graphql engine and executed per the spec (variables, fragments,
                        null propagation), static overrides and deterministic generated
                        data, introspection, GraphQL over HTTP (POST, GET, both media types)
      mockexample/      a mock route from a response example (MOCK-03): method and path
                        from the request's URL as typed, status, headers (hop-by-hop,
                        Content-Length, Date and a decoded Content-Encoding left out)
                        and body (binary as base64), masked again; merged as a route,
                        a named response or in place of a route's response
                        (docs/mock-routes.md#routes-from-a-response)
      mockroute/        mock route matcher: method + path patterns (:id, {id},
                        {id:regex}, *, **, *name), specificity order, HEAD→GET,
                        405 with Allow, diagnostics with positions (docs/mock-routes.md)
    storage/
      stablejson/       the only JSON encoder for project files: fixed format, sorted
                        keys, unknown keys kept (SplitUnknown / MarshalWithExtra)
      atomicfile/       the only way to write them: temp file, fsync, rename
                        (directory loaders must ignore `.*.tmp-*` leftovers)
      projectfile/      project file format: workspace, folder, request and environment
                        files, schemaVersion and migrations (docs/file-format.md)
    runner/             the folder runner's engine (RUN-01 to RUN-04): the plan of a run
                        and its skip reasons, the selection and order of the requests
                        sent (Plan.Select), stop on failure, the steps over iterations, the delay
                        between requests (Pacer, injectable sleep), the run's variables
                        (Vars, the runtime scope, with each iteration's data row), data
                        files: CSV and JSON rows with limits (RUN-05), limits
                        (docs/runner.md)
      report/           a run's record (summary and results, no responses), masked by a
                        function, as JUnit XML, JSON or a self-contained HTML page
                        (RUN-08)
      runstore/         the run history: records in <local>/runs/, summary files for
                        the list, retention by count and size, torn files cleaned up
                        (RUN-09)
    stream/             batcher/throttler for event streams (docs/events.md)
    opid/               operation IDs (op_ + 16 hex digits)
    netx/               the one way to open connections: Dialer interface, Direct
                        dialer, Route/Middleware/Guard/Trace hooks for proxies,
                        privacy policy and timeline; no net.Dial or
                        http.DefaultTransport elsewhere (TestNoDirectDials,
                        docs/network.md); Classify/ConnError map DNS, refused,
                        reset and TLS failures to shared netx.* codes and hints
                        for every protocol client (DBG-09); proxy URLs
                        (ParseProxyURL, socks5/socks5h/socks4/4a/http/https),
                        ALL_PROXY (ProxyFromEnv) and ProxyRouter, the app's
                        route: loopback direct, else the proxy in effect, fail
                        closed (docs/network.md#proxy-settings); SOCKS5, SOCKS4
                        (SOCKS-06: SOCKS4a, user ID, IPv4 only) and
                        HTTPProxy (NET-02: CONNECT, Basic auth, https://
                        proxies; ForwardProxier for the HTTP client's
                        absolute-form requests, docs/network.md#http-proxy);
                        ProxyHop, the hop through a proxy in DialDoneInfo
                        (SOCKS-12), ProxyRouter.Test, the proxy connection
                        test (SOCKS-11, docs/network.md#testing-the-proxy),
                        and ProxyRouter.OnChange/Refresh, which close the
                        idle pools when the proxy changes (SOCKS-05);
                        ProxyConfig and Bypass, the bypass list (SOCKS-09:
                        hosts, *.domain, .domain, <local>, IPs, CIDRs, ports;
                        NO_PROXY, docs/network.md#bypass-list)
      listen/           the one way to open listening sockets: loopback by default,
                        LAN/0.0.0.0 only with AllowLAN and a structured warning, no
                        host names, dual-stack localhost; no net.Listen elsewhere
                        (TestNoDirectListens, docs/network.md#listening)
      netxtest/         test support: resolvers that fail or record lookups in
                        process, in-process SOCKS5 and SOCKS4/4a proxies that
                        record names versus IPs (SOCKS5Server, SOCKS4Server), an HTTP proxy with CONNECT,
                        absolute form, Basic auth and TLS (HTTPProxyServer)
    netlog/             the connection log (PRV-06): a netx.Middleware on the shared
                        dialer that records every dial (cause, host:port, addresses,
                        timing, bytes, outcome, refusals too), bounded, in memory
                        (docs/network.md#connection-log)
    logexport/          masking of copied and saved timeline and log entries (DBG-07):
                        mask by value (httpclient.SecretMasker) plus Authorization
                        lines, JSON masked string by string so it stays valid;
                        ReadSecrets reads the local secret side files
                        (docs/privacy.md#copied-and-saved-logs)
    appdirs/            per-OS config, data, cache and log directories
                        (POSTSONDE_HOME override)
    datadir/            where the directories are for this run: POSTSONDE_HOME >
                        portable marker (APP-04) > chosen data directory (SET-03) >
                        appdirs defaults; the verified move at start
                        (docs/settings.md#data-directory-set-03)
    logging/            slog logger, rotating log file, secret redaction.
                        All logging goes through logging.New with a shared
                        Redactor; register secrets with Redactor.AddSecret.
                        Ring: the last records, redacted, teed next to the file
                        (Options.Ring) for the in-app console (DBG-06)
    workspace/          open and create projects (postsonde.json), the open workspace, the
                        local side-file directory per workspace in the data dir
                        (<data>/workspaces/<id>/), copies and moves, recent list
                        (docs/workspace.md)
    collection/         the request tree on disk: folders as directories with _folder.json,
                        one file per request; tolerant Load with problems, IDs with
                        duplicate resolution, order, write-only-if-changed saves,
                        Slug/UniqueName file names, create/rename/duplicate/move/
                        delete with a local trash and restore (docs/collection.md)
    autosave/           keyed debounced saver: latest value in memory, one write once
                        edits pause, Flush/FlushAll, failed writes kept for retry,
                        injectable clock (docs/collection.md#auto-save)
    environments/       environments/*.json in the project; the active environment and
                        secret values in the workspace's local directory; template
                        scope of an environment (docs/environments.md)
    requestsecrets/     typed auth passwords, tokens and API keys split off request files
                        into <local>/secrets/requests/ and merged back (docs/auth.md)
    sysproxy/           the OS proxy settings (NET-01): scutil --proxy (macOS), the
                        Internet Settings registry key via syscall (Windows), gsettings
                        or http_proxy & co (Linux); per-scheme proxies, bypass list,
                        PAC refused; a cache (docs/network.md#system-proxy)
    proxyscope/         the proxies of a project and of its environments (SOCKS-08) in
                        <local>/proxy-scopes.json, their passwords in requestsecrets'
                        side file; Resolve picks the most specific level (environment >
                        project > app) with its bypass list (docs/network.md#proxy-scopes)
    localvalues/        local values of environment and global variables (SEC-02): this
                        computer's value, which replaces the shared one, in
                        <local>/local-values/<kind>/<id>.json; not secrets, kept apart from
                        them (docs/environments.md#local-values)
    localdata/          the kinds of local data of a project's local directory (history,
                        runs, cookies, tokens, schemas, runtime variables): their paths,
                        sizes and removal (PRV-13, docs/privacy.md#clearing-local-data)
    runtimevars/        runtime variables (ENV-15): the "Runtime" scope above the folders,
                        per project, each kept for the session or persisted in
                        <local>/runtime-variables.json (secret values in
                        <local>/secrets/runtime/); Registry: one session per opening
                        of a project (docs/environments.md#runtime-variables)
    oauth1/             OAuth 1.0 signing (RFC 5849): parameter encoding and sorting, base
                        string, HMAC-SHA1/SHA256, RSA-SHA1, PLAINTEXT, body hash,
                        Authorization header (docs/auth.md#oauth-10-auth-15)
    awssigv4/           AWS Signature Version 4: canonical request (path double-encoded but
                        for S3, sorted query, trimmed headers, payload hash), signing key,
                        Authorization header, presigned parameters
                        (docs/auth.md#aws-signature-v4-auth-17)
    hawk/               Hawk: header normalized string, HMAC-SHA256/SHA1 MAC, payload hash,
                        Authorization header (docs/auth.md#hawk-auth-18)
    oauth2/             OAuth 2.0 client side (AUTH-07, AUTH-08, AUTH-12): grants (client
                        credentials and authorization code with PKCE run: Authorize opens
                        the browser and takes the code on a listen.Listen loopback
                        redirect, state checked; password and implicit modelled),
                        token request with client_secret_basic or client_secret_post, token
                        response and RFC 6749 section 5.2 errors, expiry, refresh
                        (RefreshRequest), Store and MemoryStore; sends through a
                        Doer, the app's HTTP client
                        (docs/auth.md#oauth-20-auth-08-auth-12-auth-33)
      tokenstore/       the OAuth 2.0 token store (AUTH-13): oauth2.Store over
                        <local>/secrets/oauth2/tokens.json, one token per auth
                        configuration, per workspace, Clear/List/Values
                        (docs/auth.md#token-store)
    netrc/              .netrc files as curl reads them (AUTH-19): parser (machine, default,
                        login, password, account, macdef, quoted tokens, comments), host
                        lookup, file location (request path, NETRC, ~/.netrc or ~/_netrc)
                        behind an injectable Env (docs/auth.md#netrc-auth-19)
    appsecrets/         app-wide secrets kept out of settings.json (client certificate
                        passphrases, the manual proxy's password) in
                        <data>/secrets/app.json, 0600 (docs/privacy.md)
    secretstore/        encryption at rest of every secret file (SEC-03): the vault of the
                        data directory (<data>/secrets/vault.json: PBKDF2-HMAC-SHA256 key
                        wrapping a random AES-256-GCM data key), ReadFile/WriteFile that
                        the secret stores use instead of os.ReadFile/atomicfile, lock,
                        unlock, idle lock, password change, crash-safe migrations both
                        ways, reset, and the KeyStore seam for the OS keychain
                        (docs/privacy.md#encrypted-secrets)
      keychain/         the KeyStore backends: macOS /usr/bin/security, the
                        Secret Service through secret-tool, Windows DPAPI through
                        syscall; the key on stdin, never on a command line
                        (docs/privacy.md#keychain)
    snippet/            code generators over one request model built from a request as
                        sent (FromBuild), with secrets masked by value (Masked): Curl, a
                        POSIX-shell curl command (GEN-01, docs/curl.md#copy-as-curl); the
                        languages of the snippet panel (Languages: curl for cmd and
                        PowerShell, HTTPie, wget, raw HTTP, fetch, Python requests,
                        Go, Java, C#) and its templated
                        view (TemplateAuth, FromTemplateBuild, MaskedTemplate; GEN-12,
                        GEN-13, docs/snippets.md); grpcurl over a gRPC call model
                        (GRPCLanguages, GEN-10) and websocat and wscat over a WebSocket
                        handshake model (WebSocketLanguages, GEN-11)
    settings/           settings.json in the config dir: versioned, unknown keys kept,
                        validated; Store reloads hand edits (docs/settings.md); the admin
                        policy file (SET-02, LoadPolicy/SystemPolicyPath) locks any
                        setting and can require TLS verification for every protocol
                        (docs/settings.md#settings-locked-by-your-administrator-set-02)
    themes/             custom theme files in <data>/themes (UI-05): known tokens only,
                        colours parsed by a strict grammar and normalised to hex,
                        unknown members as warnings (docs/themes.md)
    errs/               shared error kinds, codes, hints and cause chains; Redact masks the
                        text of an error and its causes (docs/errors.md,
                        docs/privacy.md#secrets-in-errors)
    template/           {{var}} parser (segments with byte offsets, \{{ escape) and resolver
                        over a scope chain: nested values, source scope, unresolved and
                        cycle reports, $uuid/timestamp/random dynamic values,
                        {{$prompt}} answers typed at send time, {{$file PATH}} contents
                        through an injected FileReader (docs/templates.md)
    fileaccess/         the file-access allowlist (PRV-10) of every file named in project
                        data: ~ and project-relative paths, links and .. followed on disk
                        before the check, opens through os.Root, size limit
                        (docs/privacy.md#local-file-access-prv-10)
    jsonpath/           RFC 9535 JSONPath: parser with offsets and type checks, evaluator
                        with exact number comparison, functions and I-Regexp, ordered
                        decoder, normalized paths, work limits (docs/jsonpath.md)
    xmldom/             XML tree from encoding/xml tokens: namespaces as written and resolved,
                        CDATA flag, byte offsets and line:col, document order, canonical
                        paths; strict or HTML-lenient; UTF-8/16, Latin-1, Windows-1252;
                        no entity expansion (XXE-safe); size/depth/node limits (docs/xpath.md)
    xpath/              XPath 1.0 subset on xmldom: all axes but namespace, core functions
                        but id(), XPath 1.0 comparisons, lenient or strict name matching,
                        offsets in syntax errors, work limits (docs/xpath.md)
    soap/               SOAP-over-HTTP helper (HTTP-18): 1.1 and 1.2 envelope template
                        around the body text (or a version switch of an envelope), and
                        the Content-Type/SOAPAction headers of the action; no WSDL
                        (docs/http-client.md#soap-helper)
    assert/             declarative assertions on a response: status, header, cookie,
                        JSONPath, XPath, regex, body, time and size targets; 14
                        operators (with matches_schema); pass/fail messages with
                        expected and actual (docs/assertions.md); Read gives a
                        target's value without an operator, for extract
    extract/            extraction rules (TEST-05): a response's JSONPath, XPath, regex,
                        header, cookie or status into a runtime or run variable; the
                        values as text, why a rule failed (docs/extraction.md)
    jsonschema/         JSON Schema validator: draft 2020-12 subset plus draft-07
                        spellings, local $ref/$anchor/$dynamicRef, unevaluated*,
                        formats, RE2 patterns with ECMA-262 escapes, errors with
                        JSONPath and schema locations, work limits (docs/jsonschema.md)
    yaml/               YAML subset reader for importers: block and flow collections, all
                        scalar styles, anchors/aliases and merge keys, core schema; tree
                        with line:col, ordered JSON output, coded errors for unsupported
                        constructs, size/depth/alias-expansion limits (docs/yaml.md)
    openapi/            OpenAPI 3.0/3.1 reader (JSON or YAML): typed model with ordered maps,
                        local $ref resolution (pointers with ~0/~1, anchors, chains) to
                        shared objects, recursive schemas as cycles, 3.0/3.1 schema
                        normalisation, diagnostics with line:col, limits (docs/openapi.md)
    curl/               curl command lines (cURL paste): POSIX/Bash, cmd and PowerShell
                        quoting with byte ranges, curl options into a Command, coded
                        diagnostics; nothing is run, read or expanded (docs/curl.md)
    importer/           the import framework: Importer interface and Registry (detect,
                        parse to a model of folders, requests, environments with
                        per-item diagnostics and source locations), Preview, Write into
                        the project (new folder, free names, local secrets, never
                        overwriting) and the import report (docs/import.md)
      curlimport/       cURL command files: one request per command (docs/import.md)
      postman/          Postman collections, environments and globals; also the export of
                        a folder as a Postman Collection v2.1 with a report (EXP-03,
                        docs/import.md#postman-export)
      altair/           Altair GraphQL Client window, collection and environment exports
                        (docs/import.md)
      projectzip/       a folder as a zip of native project files without secrets, and
                        its import with zip-slip, link and size checks (EXP-02,
                        docs/import.md#folder-zips)
  testutil/
    golden/             golden-file test helper
    fakeclock/          manual clock for timer tests (autosave.Clock)
    testenv/            the time bounds of the speed tests, lifted under the race detector
    nodeharness/        runs the Node scripts of the opt-in Socket.IO interop tests
                        (scripts/sio-interop, docs/socketio.md#interop-with-the-official-implementations)
frontend/               React + TypeScript UI (Vite, npm)
  assets.go             Go package `frontend`: embeds dist/ as frontend.Assets
  src/                  UI source
  dist/                 Vite build output, embedded (gitignored except .gitkeep)
  bindings/             generated TypeScript bindings (committed; `wails3 task bindings`)
  package.json          npm manifest; package-lock.json is committed
build/                  Wails build config
  config.yml            app metadata (name, identifier, version) and dev mode
  Taskfile.yml          shared tasks: frontend build, bindings, icon sync
  darwin/ linux/ windows/  per-platform Taskfile and packaging metadata
  appicon.png           copies of assets/icons (task sync:icons)
Taskfile.yml            entry point for `wails3 build`, `wails3 package`, `wails3 dev`
assets/icons/           app icon sources (source of truth for build/ icons)
scripts/
  check-core-deps.sh    enforces the core dependency rule
docs/
```

Packages marked "planned" are created when they are implemented.

## Windows

PostSonde runs as one process per set of app directories, with any number
of windows (APP-06, [windows.md](windows.md)). Every window loads the same
page and shows the one open project; the bound services are one set for
the process:

- **Per window** is what the page holds: tabs, drafts and their undo
  history, layout, responses, the operations it started. The page learns
  its window ID from `WindowService.Current`.
- **Shared** is what Go holds: the open project and its tree, pending
  auto-saves, settings, cookie jars, history, environments, connections,
  servers. Services are safe for concurrent calls from several windows
  (tested with `-race`).
- **Events** go to every window. Those that belong to one window name it
  (`window` in `collection:saved`, `collection:save-failed`,
  `collection:edited`, `window:open-project`) and the other windows drop
  them; `collection:files-dropped` is sent to its window only. Services
  that need the calling window read it from the call's context
  (`internal/app/windowctx`).
- **Edits** of one request from two windows are versioned in Go
  (`collection/windows.go`): an edit made from content another window has
  changed since is refused and shown as a conflict, never written over the
  other window's edit; `collection:edited` keeps the other windows' drafts
  in step.
- **The project** cannot be switched or closed while other windows are
  open (`workspace.shared_by_windows`). A different project per window is
  the next step; [windows.md](windows.md#not-handled-yet) lists what it
  needs.

## Dependency rule

Dependencies point inward, toward `internal/core`:

| Layer               | May import                                              |
|---------------------|---------------------------------------------------------|
| `internal/core/...` | Go standard library and other `internal/core` packages only |
| `internal/app`      | `internal/core`, Wails, standard library                |
| `cmd/postsonde`     | `internal/app`, `internal/core`, `frontend`, Wails, standard library |
| `frontend` (Go)     | standard library only (it just embeds `dist/`)          |
| `frontend/` (TS)    | the generated Wails bindings and `@wailsio/runtime` only |

- Wails may be imported only from `internal/app` and `cmd/`.
- `internal/core` never imports `internal/app`, Wails or any third-party
  module, including in its tests. Test helpers it uses (`internal/testutil/...`)
  must be standard library only too.
- The frontend talks to Go only through the bindings; it does no networking
  of its own. Everything it needs is bundled into `dist/` and embedded in
  the binary: no fonts, images or scripts are loaded from the network. A
  strict Content Security Policy and a build check enforce this; see
  [`docs/privacy.md`](privacy.md).

The rule for `internal/core` is checked two ways, both running
`go list -e -deps -test ./internal/core/...` and rejecting any non-standard
package outside the module:

- `internal/core/deps_test.go` runs as part of `go test ./...`.
- `scripts/check-core-deps.sh` is the same check for CI.

## Toolchain

Pinned versions. Bump them together, in one commit, and update this table.

| Tool                         | Version          | Pinned in |
|------------------------------|------------------|-----------|
| Go                           | 1.27.0           | `go.mod` (`go` directive) |
| Wails v3 module              | v3.0.0           | `go.mod` |
| `wails3` CLI                 | v3.0.0           | must match the module; install below |
| `@wailsio/runtime` (npm)     | 3.0.0            | `frontend/package.json` (exact) |
| Node.js                      | v26.7.0          | developer machine |
| npm                          | 11.19.0          | developer machine |
| React / TypeScript / Vite    | 18.3.1 / 5.9.3 / 8.3.0 | `frontend/package-lock.json` |
| Task                         | bundled in `wails3` | run as `wails3 task ...` |
| Go for the release builds    | 1.27.3           | `scripts/pins.env` (the go.dev tarball and its SHA-256 for the Linux binaries and the Flatpak; `actions/setup-go` for macOS) |
| Go for the Windows executables | the golang image's | `scripts/pins.env`, `POSTSONDE_PIN_GOLANG` (`make windows`) |
| Go for the RPM               | Fedora 45: its own `golang`; Fedora 44: the go.dev toolchain above | Fedora 45: the `fedora:45` image's repositories (not pinned); Fedora 44's `golang` is older than `go.mod`'s Go |
| Node.js for the macOS release build | 26.12.1     | `scripts/pins.env` (the node image's version) |
| NSIS (`makensis`)            | 3.11-1 (Debian trixie) | `scripts/package-windows-installer.sh` |
| CI and `make linux` images   | by digest        | `scripts/pins.env` ([reproducible builds](reproducible-builds.md)) |
| Flatpak runtime              | GNOME 51, by commit | `scripts/pins.env` |

Install the CLI at exactly the module version:

```sh
go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0
wails3 doctor
```

Platform prerequisites:

- **macOS**: Xcode Command Line Tools (cgo). If the full Xcode is installed
  but its license has not been accepted, cgo fails; either run
  `sudo xcodebuild -license` once or point the build at the Command Line
  Tools with `DEVELOPER_DIR=/Library/Developer/CommandLineTools`.
- **Linux**: gcc and the GTK4 + WebKitGTK 6.0 development packages (the
  default). The legacy GTK3/WebKit2GTK 4.1 stack builds with
  `wails3 build EXTRA_TAGS=gtk3`.
- **Windows**: no cgo needed; WebView2 runtime at run time.

## Building and running the app

From the repo root. `make` wraps these (`make build`, `make check`,
`make linux`); [`docs/building.md`](building.md) is the entry point for local
builds without CI.


```sh
wails3 dev                          # Vite dev server + live-rebuilt Go app
wails3 build                        # production binary: bin/postsonde
wails3 package                      # macOS: bin/postsonde.app (ad-hoc signed)
wails3 build VERSION=v0.1.0         # stamp main.version (default "dev")
wails3 task sync:icons              # re-copy assets/icons into build/
wails3 task common:update:build-assets  # regenerate build/ metadata from build/config.yml
```

`wails3 build` runs `npm ci` (from `package-lock.json`), regenerates the
bindings, builds the frontend into `frontend/dist`, syncs the icons, then
builds `./cmd/postsonde` with `-tags production`. Output goes to `bin/`.

### Embedding and fresh checkouts

`frontend/assets.go` embeds `frontend/dist` with `//go:embed all:dist`.
`dist/` is gitignored except for an empty, committed `dist/.gitkeep`, so the
embed pattern always matches and `go build ./...` / `go test ./...` work on
a fresh checkout without running npm. Vite empties `dist/` on every build;
a small plugin in `vite.config.ts` recreates `.gitkeep` afterwards so builds
leave git clean. A binary built with plain `go build` (no frontend build)
starts but has no page to show; use `wails3 build` or `wails3 dev`.

`go.mod` has `ignore ./frontend/node_modules` so `./...` patterns never
descend into npm packages that ship `.go` files.

### Bindings

`frontend/bindings/` holds the TypeScript bindings generated by
`wails3 generate bindings`. They are committed, so `tsc` and `npm run build`
work on a fresh checkout without the Wails CLI. `wails3 build` and
`wails3 dev` regenerate them; regenerate by hand with `wails3 task bindings`,
and check they are current with `wails3 task bindings:check` (CI runs it).
The service, DTO and cancellation conventions are in
[`docs/bindings.md`](bindings.md). Only `frontend/src/lib/` imports the
bindings.

### Frontend checks

```sh
cd frontend
npm ci
npx tsc --noEmit                    # strict mode, no opt-outs
```

## Test conventions

- **Table-driven tests.** One `tests := []struct{...}` table per behaviour,
  each case run with `t.Run(tt.name, ...)`. Use `t.Parallel()` where the
  cases are independent.
- **Race detector.** Tests are run with `go test -race ./...`, locally and in
  CI.
- **Speed tests.** A test that bounds wall-clock time, to catch quadratic
  work or a search index no longer used, takes its bound from
  `testenv.Budget`, which lifts it under the race detector: there the
  slowdown is too large and too uneven for any bound to mean something,
  so the bounds hold in plain builds (`go test ./...` without `-race`, and
  CI's Windows job). A tight bound (milliseconds) is checked against the
  best of a few runs, or a percentile, rather than the slowest run, which
  a loaded machine or a GC pause can push past any bound.
- **Golden files.** For larger outputs (rendered requests, parsed documents,
  generated code), compare against `testdata/<name>.golden` with
  `golden.Assert(t, name, got)` from `internal/testutil/golden`. After an
  intended change, regenerate the files and review the diff before
  committing:

  ```sh
  go test ./internal/core/foo -run TestRender -update
  git diff internal/core/foo/testdata
  ```

  See `internal/testutil/golden/golden_test.go` for an example.

## Running the checks

```sh
gofmt -l $(git ls-files '*.go')  # must print nothing
go vet ./...
go build ./...
go test -race ./...
scripts/check-core-deps.sh
scripts/check-css-tokens.sh      # no colours outside frontend/src/styles/tokens.css
wails3 task bindings:check       # committed bindings match the Go services
(cd frontend && npm run typecheck && npm test)
(cd frontend && npm run build && npm run check:dist)  # no remote URLs, CSP meta (docs/privacy.md)
```

CI runs the same checks on every push; see [`docs/ci.md`](ci.md).

Build just the Go binary with a version (no frontend build, see above):

```sh
go build -ldflags "-X main.version=v0.1.0" -o bin/postsonde ./cmd/postsonde
```
