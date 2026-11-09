import { describe, expect, it } from "vitest";
import { schemaAge } from "./graphqlSchema";

describe("schemaAge", () => {
  const now = Date.parse("2026-09-19T12:00:00Z");
  it("says how long ago a schema was fetched", () => {
    expect(schemaAge("2026-09-19T11:59:30Z", now)).toBe("just now");
    expect(schemaAge("2026-09-19T11:58:00Z", now)).toBe("2 min ago");
    expect(schemaAge("2026-09-19T09:00:00Z", now)).toBe("3 h ago");
    expect(schemaAge("2026-09-18T11:00:00Z", now)).toBe("1 day ago");
    expect(schemaAge("2026-09-14T12:00:00Z", now)).toBe("5 days ago");
    // A clock that went backwards is "just now", an unreadable time nothing.
    expect(schemaAge("2026-09-19T12:05:00Z", now)).toBe("just now");
    expect(schemaAge("", now)).toBe("");
  });
});
