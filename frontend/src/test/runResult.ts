// A RunResultDTO for tests (the folder runner): request `index` of the
// first iteration, passed with a 200, fields overridden by `over`.
import type { RunResultDTO } from "../lib/runner";

export function runResult(index: number, over: Partial<RunResultDTO> = {}): RunResultDTO {
  return {
    iteration: 1,
    index,
    position: index,
    requestId: `req_${index}`,
    name: `Request ${index}`,
    kind: "http",
    method: "GET",
    path: [],
    skip: "",
    outcome: "passed",
    startedAt: 1_700_000_000_000 + index,
    durationMs: 12,
    statusCode: 200,
    status: "200 OK",
    size: 10,
    testsPassed: 0,
    testsFailed: 0,
    tests: [],
    extractions: [],
    events: 0,
    hasResponse: false,
    error: null,
    ...over,
  };
}
