import { describe, expect, it } from "vitest";
import { scanTemplate, templateVars, type TemplateSpan } from "./templateSyntax";

const v = (start: number, end: number, name: string): TemplateSpan => ({ kind: "var", start, end, name });
const u = (start: number): TemplateSpan => ({ kind: "unterminated", start, end: start + 2, name: "" });

describe("scanTemplate", () => {
  // The cases of internal/core/template/parse_test.go (TestParse), as spans.
  const cases: [string, string, TemplateSpan[]][] = [
    ["empty", "", []],
    ["plain text", "hello", []],
    ["only a variable", "{{a}}", [v(0, 5, "a")]],
    ["variable in text", "x{{host}}/y", [v(1, 9, "host")]],
    ["whitespace in braces", "{{  id\t}}", [v(0, 9, "id")]],
    ["newline in braces", "{{\nid\n}}", [v(0, 8, "id")]],
    ["empty name", "{{}}", [v(0, 4, "")]],
    ["blank name", "{{   }}", [v(0, 7, "")]],
    ["adjacent variables", "{{a}}{{b}}", [v(0, 5, "a"), v(5, 10, "b")]],
    ["dotted and dynamic names", "{{auth.token}}{{$uuid}}", [v(0, 14, "auth.token"), v(14, 23, "$uuid")]],
    ["inner space kept", "{{a b}}", [v(0, 7, "a b")]],
    ["unterminated at end", "a{{b", [u(1)]],
    ["lone braces", "{{", [u(0)]],
    ["opener before close", "{{a {{b}}", [u(0), v(4, 9, "b")]],
    ["single braces are text", "{a} }}", []],
    ["json object around variable", '{"a":{{{x}}}}', [v(6, 11, "x")]],
    ["four open braces", "{{{{x}}", [v(2, 7, "x")]],
    ["close takes first }}", "{{a}}}", [v(0, 5, "a")]],
    ["escaped braces", "\\{{a}}", []],
    ["escaped then variable", "\\{{a}}{{b}}", [v(6, 11, "b")]],
    ["escaped backslash", "\\\\{{a}}", [v(2, 7, "a")]],
    ["three backslashes", "\\\\\\{{a}}", []],
    ["backslash elsewhere", "C:\\\\dir\\n{", []],
    ["escaped unterminated", "\\{{a", []],
    // UTF-16 offsets here; the Go test has byte offsets.
    ["multibyte", "ё{{имя}}ж", [v(1, 8, "имя")]],
    ["url", "{{baseUrl}}/users?id={{ id }}", [v(0, 11, "baseUrl"), v(21, 29, "id")]],
  ];
  it.each(cases)("%s", (_name, src, want) => {
    expect(scanTemplate(src)).toEqual(want);
  });

  it("lists only terminated variables in templateVars", () => {
    expect(templateVars("{{a {{b}}")).toEqual([v(4, 9, "b")]);
  });
});
