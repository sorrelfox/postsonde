import { fireEvent, render, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Markdown } from "./Markdown";

// Everything a note could use to load or run something. The renderer must
// produce none of these elements and no request.
const FORBIDDEN = "img, iframe, link, script, style, object, embed, video, audio, source, picture, frame, base, meta, form, svg, math";

const HOSTILE = [
  "# Heading <script>alert(1)</script>",
  "<img src=x onerror=alert(1)>",
  '<img src="https://tracker.example/pixel.png">',
  "![pixel](https://tracker.example/pixel.png)",
  "![local](file:///etc/passwd) ![rel](./logo.png) ![data](data:image/png;base64,iVBORw0KGgo=)",
  '[![badge](https://img.example/b.svg)](https://ci.example "CI")',
  "<iframe src=https://evil.example></iframe>",
  '<link rel="stylesheet" href="https://evil.example/x.css">',
  "<style>body{display:none}</style>",
  "<object data=https://evil.example/x.swf></object><embed src=x>",
  "<svg onload=alert(1)><use href=https://evil.example/s.svg /></svg>",
  "<a href=\"javascript:alert(1)\">click</a>",
  "[click](javascript:alert(1)) [data](data:text/html,<script>alert(1)</script>) [vb](vbscript:x)",
  "<https://auto.example> https://bare.example/path",
  "| a | b |\n|---|---|\n| <img src=x> | ![i](https://t.example/i.png) |",
  "- [x] <script>x</script>\n- [ ] ![t](https://t.example/t.png)",
  "> <iframe src=x>\n> ![q](https://t.example/q.png)",
  "```html\n<img src=https://t.example/c.png>\n<script>alert(1)</script>\n```",
  "&lt;img src=x&gt; &#x3c;script&#x3e;",
].join("\n\n");

let fetchSpy: ReturnType<typeof vi.fn>;
beforeEach(() => {
  fetchSpy = vi.fn(() => Promise.reject(new Error("no network in notes")));
  vi.stubGlobal("fetch", fetchSpy);
});
afterEach(() => {
  vi.unstubAllGlobals();
});

const root = () => document.querySelector("[data-markdown]") as HTMLElement;

describe("Markdown", () => {
  it("renders each construct as its element", () => {
    render(
      <Markdown
        source={[
          "# H1",
          "### H3",
          "Some *em*, **strong**, ~~del~~ and `code`.",
          "> quote",
          "1. one\n2. two\n   - nested",
          "- [x] done\n- [ ] todo",
          "---",
          "```js\nlet a = 1;\n```",
          "| L | R |\n|:--|--:|\n| a | b |",
          "line one  \nline two",
        ].join("\n\n")}
      />,
    );
    const el = root();
    expect(el.querySelector("h1")?.textContent).toBe("H1");
    expect(el.querySelector("h3")?.textContent).toBe("H3");
    expect(el.querySelector("p em")?.textContent).toBe("em");
    expect(el.querySelector("p strong")?.textContent).toBe("strong");
    expect(el.querySelector("p del")?.textContent).toBe("del");
    expect(el.querySelector("p code")?.textContent).toBe("code");
    expect(el.querySelector("blockquote p")?.textContent).toBe("quote");
    expect(el.querySelectorAll("ol > li")).toHaveLength(2);
    expect(el.querySelector("ol > li ul > li")?.textContent).toBe("nested");
    const boxes = el.querySelectorAll<HTMLInputElement>("input[type=checkbox]");
    expect([...boxes].map((b) => [b.checked, b.disabled])).toEqual([
      [true, true],
      [false, true],
    ]);
    expect(el.querySelector("hr")).not.toBeNull();
    expect(el.querySelector("pre")?.getAttribute("data-lang")).toBe("js");
    expect(el.querySelector("pre code")?.textContent).toBe("let a = 1;");
    expect([...el.querySelectorAll("th")].map((th) => [th.textContent, th.style.textAlign])).toEqual([
      ["L", "left"],
      ["R", "right"],
    ]);
    expect(el.querySelector("td")?.textContent).toBe("a");
    expect(el.querySelectorAll("br")).toHaveLength(1);
  });

  it("produces no element that loads or runs anything, and no request, for hostile input", () => {
    const onOpenLink = vi.fn();
    render(<Markdown source={HOSTILE} onOpenLink={onOpenLink} />);
    const el = root();
    expect(el.querySelectorAll(FORBIDDEN)).toHaveLength(0);
    expect(document.querySelectorAll(FORBIDDEN)).toHaveLength(0);
    // No element carries a URL attribute the webview would follow or fetch.
    for (const attr of ["href", "src", "srcset", "action", "formaction", "poster", "data", "background", "style"]) {
      const withAttr = [...el.querySelectorAll(`[${attr}]`)].filter(
        // Table cells get text-align only.
        (e) => !(attr === "style" && /^text-align: \w+;$/.test(e.getAttribute("style") ?? "")),
      );
      expect(withAttr, attr).toHaveLength(0);
    }
    // No event handler attributes.
    for (const e of el.querySelectorAll("*")) {
      expect([...e.attributes].filter((a) => a.name.startsWith("on")), e.outerHTML).toHaveLength(0);
    }
    // Raw HTML shows as text.
    expect(el.textContent).toContain("<script>alert(1)</script>");
    expect(el.textContent).toContain("<img src=x onerror=alert(1)>");
    expect(el.textContent).toContain("<img src=x> <script>");
    expect(fetchSpy).not.toHaveBeenCalled();
    expect(onOpenLink).not.toHaveBeenCalled();
  });

  it("shows images as placeholders with their alt text, never an <img>", () => {
    const onOpenLink = vi.fn();
    render(<Markdown source="![Build status](https://ci.example/badge.svg) ![](./diagram.png)" onOpenLink={onOpenLink} />);
    const el = root();
    expect(el.querySelector("img")).toBeNull();
    const images = el.querySelectorAll("[data-image-url]");
    expect([...images].map((i) => [i.getAttribute("data-image-url"), i.textContent])).toEqual([
      ["https://ci.example/badge.svg", "▣Build status"],
      ["./diagram.png", "▣image"],
    ]);
    // A remote image's URL can be opened like a link (after a confirmation); a local one cannot.
    fireEvent.click(within(images[0] as HTMLElement).getByRole("link"));
    expect(onOpenLink).toHaveBeenCalledWith("https://ci.example/badge.svg");
    expect(within(images[1] as HTMLElement).queryByRole("link")).toBeNull();
    expect(fetchSpy).not.toHaveBeenCalled();
  });

  it("opens http(s) and mailto links through onOpenLink, by click or Enter, and never follows others", () => {
    const onOpenLink = vi.fn();
    render(
      <Markdown
        source="[Docs](https://docs.example/a?b=1 'The docs') <mail@example.com> [bad](javascript:alert(1)) [rel](../users.json)"
        onOpenLink={onOpenLink}
      />,
    );
    const links = within(root()).getAllByRole("link");
    expect(links.map((a) => a.textContent)).toEqual(["Docs", "mail@example.com"]);
    expect(links.every((a) => !a.hasAttribute("href"))).toBe(true);
    expect(links[0]?.getAttribute("title")).toBe("The docs\nhttps://docs.example/a?b=1");

    fireEvent.click(links[0] as HTMLElement);
    fireEvent.keyDown(links[1] as HTMLElement, { key: "Enter" });
    expect(onOpenLink.mock.calls).toEqual([["https://docs.example/a?b=1"], ["mailto:mail@example.com"]]);

    // Inert links are text with the destination in the tooltip.
    const bad = within(root()).getByText("bad");
    expect(bad.getAttribute("title")).toBe("Not opened: javascript:alert(1)");
    fireEvent.click(bad);
    fireEvent.click(within(root()).getByText("rel"));
    expect(onOpenLink).toHaveBeenCalledTimes(2);
  });

  it("renders links inert without onOpenLink", () => {
    render(<Markdown source="[Docs](https://docs.example)" />);
    expect(within(root()).queryByRole("link")).toBeNull();
    expect(within(root()).getByText("Docs").getAttribute("title")).toBe("https://docs.example/");
  });

  it("makes an image link one link, not a link inside a link (UI-11)", () => {
    const onOpenLink = vi.fn();
    render(<Markdown source="[![Build status](https://ci.example/badge.svg)](https://ci.example)" onOpenLink={onOpenLink} />);
    const links = within(root()).getAllByRole("link");
    expect(links).toHaveLength(1);
    expect(links[0]?.textContent).toContain("Build status");
    fireEvent.click(links[0]!);
    expect(onOpenLink).toHaveBeenCalledWith("https://ci.example/");
  });
});
