# Introspection fixtures

Introspection results used by the schema converter tests
(`fixtures_test.go`, `introspection_test.go`). Each must convert without
problems and convert back to the same introspection JSON, compared
semantically (see `compare_test.go`).

| File | Source | Query | What it covers |
|---|---|---|---|
| `countries-full.json` | Real: `https://countries.trevorblades.com/graphql` ([trevorblades/countries](https://github.com/trevorblades/countries)), a current server with graphql-js 16-style output. Fetched 2026-09-16. | `FullIntrospectionQuery` | Every newer field (schema description, `specifiedByURL`, `isRepeatable`, `isOneOf`, input value deprecation), input objects with `{}` defaults, the complete built-in and introspection types and all five spec directives. Also the reference for the built-in definitions (`TestBuiltinsMatchGraphQLJS`). |
| `swapi-conservative.json` | Real: `https://swapi-graphql.netlify.app/graphql` ([graphql/swapi-graphql](https://github.com/graphql/swapi-graphql)), a server on an older graphql-js. Fetched 2026-09-16. | `ConservativeIntrospectionQuery` | Relay-style connections, a `Node` interface with possible types, a query root not named `Query`, `interfaces: null` on an interface, only three directives. |
| `swapi-full-rejected.json` | Real: the same server's answer to `FullIntrospectionQuery`. | `FullIntrospectionQuery` | The errors an older server returns for the newer introspection fields (no `data`). |
| `rickandmorty-conservative.json` | Real: `https://rickandmortyapi.com/graphql` ([afuh/rick-and-morty-api](https://github.com/afuh/rick-and-morty-api)), an Apollo Server 2-era server. Fetched 2026-09-16. | `ConservativeIntrospectionQuery` | Apollo's `Upload` scalar and `@cacheControl` directive, `""` descriptions, input filters. |
| `github-subset.json` | Hand-authored, modelled on the public GitHub GraphQL API (names and shapes only; descriptions reworded). Written as a compact Python description and expanded to introspection JSON once; edit the JSON directly. | Full shape | Mutation and subscription roots, interfaces implementing interfaces, a union, custom scalars with `specifiedByURL`, a `@oneOf` input, deprecated fields, arguments, input fields and enum values (with and without a reason), object and list defaults, a nested list `[[String!]!]`, repeatable and custom directives. It deliberately omits the introspection types and `Float`, which resolve through the built-ins. |

The real results are the servers' unmodified responses. They describe the
public schemas of those open-source services (schema metadata only, no
records from them) and are kept small; refetch with the query named above
if a test needs a newer shape.

## SDL files (`sdl/`)

Type system documents used by the SDL tests (`sdl_test.go`,
`TestSDLFiles`). Each must load with `ParseSDL` without problems.

| File | Source | What it covers |
|---|---|---|
| `starwars.graphql` | The Star Wars schema of graphql.org's "Learn" pages and the graphql-js test suite, written out in SDL. | All three roots in a schema definition, an interface with two implementations, a union, enums with descriptions, input objects, list-of-list types, enum defaults. |
| `github-style.graphql` | Hand-authored, modelled on GitHub's public schema file (names and shapes only; descriptions reworded). | Interfaces implementing interfaces, `@specifiedBy` scalars, a `@oneOf` input, object defaults, deprecations, multi-line argument lists with descriptions, a custom directive, type and enum extensions. |
| `federation-subgraph.graphql` | Hand-authored in the shape `rover subgraph introspect` prints for an Apollo Federation 2 subgraph. | `extend schema @link`, repeatable directives declared in the file, `_Entity`/`_Service`, entity keys, `@requires`/`@provides`/`@external`/`@override`, list defaults of enums, `{}` defaults. |
| `spec-examples.graphql` | The type system examples of the GraphQL spec (chapter 3), joined into one valid schema. | Every definition and extension form: schema, scalar, object, interface (implementing interfaces), union, enum, input, directive definitions and their extensions, block-string descriptions, leading `|`. |

The introspection fixtures above are also printed as SDL and loaded back
(`TestSDLRoundTripsFixtures`), so the real schemas are checked as SDL too.
