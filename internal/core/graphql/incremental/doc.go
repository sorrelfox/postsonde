// Package incremental reads GraphQL incremental delivery responses, the
// answers to operations with @defer and @stream (GQL-08): a
// multipart/mixed body whose parts are JSON payloads, or a
// text/event-stream one whose "next" events are. A Collector splits the
// body into payloads as the bytes arrive, notes when each one came and
// merges them with a Result into the response the server would have sent
// without @defer and @stream: deferred fragments merged at their paths,
// streamed items appended to their lists. Both payload formats are read,
// the older one of Apollo's deferSpec=20220824 (path, data or items,
// label, hasNext) and the newer one of graphql-js 17 (pending,
// incremental with id and subPath, completed). See docs/graphql.md.
package incremental
