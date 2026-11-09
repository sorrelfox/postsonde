import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { toVariableSet, type VariableDTO } from "../../lib/templateVariables";
import { auditA11y } from "../../test/a11y";
import { TemplateVariablesContext } from "../common/TemplateVariables";
import { RelativeUrlHint } from "./RelativeUrlHint";

const baseUrl = (over: Partial<VariableDTO> = {}): VariableDTO => ({
  name: "baseUrl",
  value: "https://api.stg.test",
  secret: false,
  resolved: true,
  scope: "Environment: Staging",
  source: "environment",
  ...over,
});

function setup(url: string, variables: VariableDTO[] | null) {
  const set = variables === null ? null : toVariableSet({ variables, environment: "Staging" });
  return render(
    <TemplateVariablesContext.Provider value={{ variables: set }}>
      <RelativeUrlHint url={url} />
    </TemplateVariablesContext.Provider>,
  );
}

describe("RelativeUrlHint (COL-08)", () => {
  it("shows where a relative URL is sent and which scope the base comes from", () => {
    setup("/users/{{id}}", [baseUrl()]);
    const note = screen.getByRole("note");
    expect(note.textContent).toBe("→https://api.stg.test/users/{{id}}baseUrl from Environment: Staging");
    expect(auditA11y()).toEqual([]);
  });

  it("says when the base is a local value, and masks a secret one", () => {
    const { unmount } = setup("/users", [baseUrl({ local: true, value: "http://localhost:8080" })]);
    expect(screen.getByRole("note").textContent).toContain("http://localhost:8080/users");
    expect(screen.getByRole("note").textContent).toContain("Environment: Staging · local value");
    unmount();
    setup("/users", [baseUrl({ secret: true, value: "" })]);
    expect(screen.getByRole("note").textContent).toContain("••••••/users");
  });

  it("says that no baseUrl is defined", () => {
    setup("/users", []);
    expect(screen.getByRole("note").textContent).toMatch(/^Relative URL: no variable baseUrl is defined/);
  });

  it("shows nothing for an absolute URL, or before the variables are known", () => {
    const { container, unmount } = setup("https://h.test/users", [baseUrl()]);
    expect(container.textContent).toBe("");
    unmount();
    const again = setup("/users", null);
    expect(again.container.textContent).toBe("");
  });
});
