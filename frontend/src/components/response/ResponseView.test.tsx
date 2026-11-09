import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { ResponseView } from "./ResponseView";
import type { CookieDTO, ResponseDTO } from "../../lib/http";
import { httpResponse } from "../../test/httpResponse";

function responseDTO(over: Partial<ResponseDTO> = {}): ResponseDTO {
  return httpResponse({ headers: [], contentType: "", body: "", bodyPretty: "", size: 0, headerSize: 19, ...over });
}

function tone(): string | null {
  return document.querySelector("[data-response-status] [data-tone]")?.getAttribute("data-tone") ?? null;
}

describe("ResponseView", () => {
  it("colours the status by class (HTTP-30)", () => {
    const { rerender } = render(<ResponseView response={responseDTO({ statusCode: 404, status: "404 Not Found" })} />);
    expect(tone()).toBe("client");
    rerender(<ResponseView response={responseDTO({ statusCode: 503, status: "503 Service Unavailable" })} />);
    expect(tone()).toBe("server");
    rerender(<ResponseView response={responseDTO({ statusCode: 301, status: "301 Moved Permanently" })} />);
    expect(tone()).toBe("redirect");
  });

  it("shows the header sizes, the protocol and no count for no headers (HTTP-30, HTTP-32)", () => {
    render(<ResponseView response={responseDTO({ headerSize: 1536, protocol: "", proto: "HTTP/1.0", redirects: 2 })} />);
    const status = document.querySelector("[data-response-status]") as HTMLElement;
    expect(status.textContent).toContain("1.5 KB headers");
    expect(status.textContent).toContain("HTTP/1.0");
    expect(status.textContent).toContain("No timeout");
    expect(status.textContent).toContain("2 redirects");
    const tab = screen.getByRole("tab", { name: "Headers" });
    expect(tab.textContent).toBe("Headers");
    fireEvent.click(tab);
    expect(screen.getByText("The response has no headers.")).toBeTruthy();
  });

  it("marks a response sent without TLS verification (HTTP-23)", () => {
    const { rerender } = render(<ResponseView response={responseDTO()} />);
    const badge = () => document.querySelector("[data-response-status] [data-tls-off]");
    expect(badge()).toBeNull();
    rerender(<ResponseView response={responseDTO({ insecureTls: true, insecureTlsSource: "request" })} />);
    expect(badge()?.textContent).toBe("TLS unverified");
    expect(badge()?.getAttribute("title")).toContain("request's settings");
    rerender(<ResponseView response={responseDTO({ insecureTls: true, insecureTlsSource: "settings" })} />);
    expect(badge()?.getAttribute("title")).toContain("Settings › Network");
  });

  it("lists every header, repeated names included, as a table", () => {
    render(
      <ResponseView
        response={responseDTO({
          headers: [
            { name: "Set-Cookie", value: "a=1" },
            { name: "Set-Cookie", value: "b=2" },
            { name: "Vary", value: "Accept-Encoding" },
          ],
        })}
      />,
    );
    fireEvent.click(screen.getByRole("tab", { name: /Headers/ }));
    const rows = screen.getByRole("table", { name: "Response headers" }).querySelectorAll("tbody tr");
    expect([...rows].map((r) => r.textContent)).toEqual(["Set-Cookiea=1", "Set-Cookieb=2", "VaryAccept-Encoding"]);
  });

  it("lists the parsed Set-Cookie headers with the jar's outcome, malformed ones included (HTTP-32)", () => {
    const base: CookieDTO = {
      raw: "",
      name: "",
      value: "",
      domain: "",
      path: "",
      expires: "",
      maxAge: 0,
      hasMaxAge: false,
      secure: false,
      httpOnly: false,
      sameSite: "",
      error: "",
      notes: [],
      jar: "",
      jarReason: "",
      jarMessage: "",
    };
    const cookies: CookieDTO[] = [
      { ...base, raw: "session=s1; Path=/; HttpOnly; SameSite=Lax", name: "session", value: "s1", path: "/", httpOnly: true, sameSite: "Lax", jar: "stored" },
      {
        ...base,
        raw: "other=1; Domain=other.example; Expires=Wed, 09 Jun 2021 10:18:14 GMT",
        name: "other",
        value: "1",
        domain: "other.example",
        expires: "2021-06-09T10:18:14Z",
        jar: "rejected",
        jarReason: "domain_mismatch",
        jarMessage: "The Domain attribute does not match the host that sent it.",
      },
      { ...base, raw: "a=1; Max-Age=x", name: "a", value: "1", notes: ["Max-Age=x is not a number and was ignored."] },
      { ...base, raw: "", error: "Not a valid cookie: it is empty or has control characters.", jar: "rejected", jarReason: "invalid", jarMessage: "x" },
    ];
    render(<ResponseView response={responseDTO({ cookies })} />);
    const tab = screen.getByRole("tab", { name: /Cookies/ });
    expect(tab.textContent).toBe("Cookies4");
    fireEvent.click(tab);
    const table = screen.getByRole("table", { name: "Response cookies" });
    const rows = [...table.querySelectorAll("tbody tr")].map((r) => r.textContent);
    expect(rows[0]).toBe("sessions1host only/sessionHttpOnlySameSite=LaxStored");
    expect(rows[1]).toBe("other1other.exampledefault2021-06-09 10:18:14 UTCnoneIgnored");
    expect(rows[2]).toBe("Not stored: The Domain attribute does not match the host that sent it.");
    expect(rows[3]).toBe("a1host onlydefaultsessionnone—");
    expect(rows[4]).toBe("Max-Age=x is not a number and was ignored.");
    expect(rows[6]).toContain("Not a cookie: Not a valid cookie");
  });

  it("says when the response sets no cookies", () => {
    render(<ResponseView response={responseDTO()} />);
    fireEvent.click(screen.getByRole("tab", { name: "Cookies" }));
    expect(screen.getByText("The response sets no cookies.")).toBeTruthy();
  });
});
