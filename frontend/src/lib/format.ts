// Calls into the Go FormatService (internal/app/services/format): JSON
// validate, beautify and minify for the body editor (HTTP-12), and
// GraphQL prettify and minify for the query editor (GQL-18), the
// GraphQL variables check (GQL-03, GQL-04), and XML indent for large
// response bodies (HTTP-31), and the SOAP envelope helper (HTTP-18).
// Invalid text is a result with a position, not a rejection. The query editor's syntax and schema check is
// GraphQLService.Diagnostics (lib/graphqlSchema.ts). See
// docs/json-tools.md and docs/graphql-engine.md.
import {
  FormatService,
  type GraphQLCheckDTO,
  type GraphQLErrorDTO,
  type GraphQLFormatDTO,
  type GraphQLOperationDTO,
  type GraphQLUnknownVariableDTO,
  type GraphQLVariableDTO,
  type GraphQLVariablesCheckDTO,
  type JSONCheckDTO,
  type JSONErrorDTO,
  type JSONFormatDTO,
  type SOAPEnvelopeDTO,
  type SOAPHeaderDTO,
  type SOAPInputDTO,
  type XMLErrorDTO,
  type XMLFormatDTO,
} from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/format";
import type { CancellablePromise } from "./calls";

export type {
  GraphQLCheckDTO,
  GraphQLErrorDTO,
  GraphQLFormatDTO,
  GraphQLOperationDTO,
  GraphQLUnknownVariableDTO,
  GraphQLVariableDTO,
  GraphQLVariablesCheckDTO,
  JSONCheckDTO,
  JSONErrorDTO,
  JSONFormatDTO,
  SOAPEnvelopeDTO,
  SOAPHeaderDTO,
  SOAPInputDTO,
  XMLErrorDTO,
  XMLFormatDTO,
};

/** Indent of beautified JSON bodies, in spaces. */
export const JSON_INDENT = 2;

/** Checks that `text` is one JSON value ({{variables}} allowed). */
export function validateJson(text: string): CancellablePromise<JSONCheckDTO> {
  return FormatService.ValidateJSON(text);
}

/** Indents `text` by `indent` spaces per level (0: a tab). */
export function beautifyJson(text: string, indent: number = JSON_INDENT): CancellablePromise<JSONFormatDTO> {
  return FormatService.BeautifyJSON(text, indent);
}

/** Removes whitespace outside strings from `text`. */
export function minifyJson(text: string): CancellablePromise<JSONFormatDTO> {
  return FormatService.MinifyJSON(text);
}

/** Lays out a GraphQL document in Go, keeping its comments. */
export function prettifyGraphql(text: string): CancellablePromise<GraphQLFormatDTO> {
  return FormatService.PrettifyGraphQL(text);
}

/** Prints a GraphQL document on one line, without comments. */
export function minifyGraphql(text: string): CancellablePromise<GraphQLFormatDTO> {
  return FormatService.MinifyGraphQL(text);
}

/**
 * Checks a GraphQL request's variables text in Go: JSON syntax, an object,
 * and the variables the operation that runs declares but the object lacks,
 * or sets but the operation does not declare.
 */
export function checkGraphqlVariables(
  query: string,
  variables: string,
  operationName: string,
): CancellablePromise<GraphQLVariablesCheckDTO> {
  return FormatService.CheckGraphQLVariables(query, variables, operationName);
}

/** Indents an XML document (UTF-8 text; its encoding declaration is not used) by two spaces per level. */
export function indentXml(text: string): CancellablePromise<XMLFormatDTO> {
  return FormatService.IndentXML(text);
}

/**
 * The SOAP helper's envelope and headers (HTTP-18): the current raw body
 * text wrapped in, or switched to, an envelope of the version, and the
 * headers that carry the action. Nothing is saved.
 */
export function soapEnvelope(input: SOAPInputDTO): CancellablePromise<SOAPEnvelopeDTO> {
  return FormatService.SOAPEnvelope(input);
}
