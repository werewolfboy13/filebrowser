// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://fb.test/files/notes/README.md" }
//
// sanitizeMarkdownHtml(): what the markdown preview may and may not render.
import { describe, expect, it } from "vitest";
import { Marked } from "marked";
import markedKatex from "marked-katex-extension";
import { sanitizeMarkdownHtml } from "@/utils/sanitize";

const render = async (md: string) => {
  const marked = new Marked();
  marked.use(markedKatex({ output: "mathml", throwOnError: false }));
  return sanitizeMarkdownHtml(await marked.parse(md, { async: true }));
};
const clean = (html: string) => sanitizeMarkdownHtml(html);

describe("markdown preview sanitiser keeps ordinary markdown", () => {
  it("headings, emphasis, links, local and data: images, tables, code, task lists and KaTeX", async () => {
    const out = await render(
      [
        "# Title",
        "",
        "**bold** _it_ [docs](https://example.org) [rel](../other.md)",
        "![logo](./logo.png) ![abs](/api/raw/notes/a.png) ![d](data:image/png;base64,AAAA)",
        "",
        "| a | b |",
        "|:-:|---|",
        "| 1 | 2 |",
        "",
        "- [x] done",
        "- [ ] todo",
        "",
        "```js",
        "let x = 1 < 2;",
        "```",
        "",
        "$E=mc^2$ and $\\notacommand$",
        "",
        "<details><summary>More</summary>hidden</details>",
      ].join("\n")
    );
    console.log(out);
    expect(out).toContain("<h1>Title</h1>");
    expect(out).toContain("<strong>bold</strong>");
    expect(out).toContain('href="https://example.org"');
    expect(out).toContain('href="../other.md"');
    expect(out).toContain('src="./logo.png"');
    expect(out).toContain('src="/api/raw/notes/a.png"');
    expect(out).toContain('src="data:image/png;base64,AAAA"');
    expect(out).toContain("<table>");
    expect(out).toContain('<td align="center">1</td>');
    expect(out).toMatch(/<input[^>]*type="checkbox"/);
    expect(out).toContain("let x = 1 &lt; 2;");
    expect(out).toContain("<math");
    expect(out).toContain("<msup>");
    expect(out).toContain('mathcolor="#cc0000"'); // KaTeX error colour
    // the TeX source (KaTeX's <annotation>) is not shown next to the formula
    expect(out).not.toContain("E=mc^2");
    expect(out).not.toMatch(/<\/mrow>\\notacommand/);
    expect(out).toContain("<details>");
  });
});

describe("markdown preview sanitiser removes", () => {
  const cases: Array<[string, string, RegExp]> = [
    // not leading: the HTML parser moves a leading <style> out of the body
    // by itself, which plain DOMPurify would pass too
    [
      "<style>",
      "<p>x</p><style>header{display:none}</style><p>y</p>",
      /<style|display:none/i,
    ],
    ["style attributes", '<p style="position:fixed">x</p>', /style=/],
    ["forms", '<form action="https://a.example/"><p>x</p></form>', /<form/],
    ["buttons", "<button>x</button>", /<button/],
    ["textareas", "<textarea>x</textarea>", /<textarea/],
    ["selects", "<select><option>x</option></select>", /<select|<option/],
    ["ids", '<a id="focus-prompt" href="/x">x</a>', /id=/],
    ["names", '<a name="x" href="/x">x</a>', /name=/],
    ["classes", '<div class="card floating">x</div>', /class=/],
    ["accesskeys", '<a accesskey="s" href="/x">x</a>', /accesskey/],
    ["ping", '<a ping="https://a.example/" href="/x">x</a>', /ping/],
    ["popovers", "<div popover>x</div>", /popover/],
    ["autofocus", '<a autofocus href="/x">x</a>', /autofocus/],
    ["tabindex", '<a tabindex="1" href="/x">x</a>', /tabindex/],
    ["targets", '<a target="_top" href="/x">x</a>', /target=/],
    ["dialogs", "<dialog open>x</dialog>", /<dialog/],
    [
      "meta refresh",
      '<meta http-equiv="refresh" content="0;url=https://a.example/">',
      /<meta/,
    ],
    ["base", '<base href="https://a.example/">', /<base/],
    ["iframes", '<iframe src="/x"></iframe>', /<iframe/],
    [
      "svg",
      '<svg><image href="https://a.example/p.png"/></svg>',
      /<svg|<image/,
    ],
    [
      "media",
      '<video src="/x.mp4" poster="https://a.example/p.png"></video>',
      /<video|poster/,
    ],
    ["remote img", '<img src="https://a.example/p.gif">', /a\.example/],
    ["protocol-relative img", '<img src="//a.example/p.gif">', /a\.example/],
    [
      "img with leading space",
      '<img src=" https://a.example/p.gif">',
      /a\.example/,
    ],
    ["img with backslashes", '<img src="\\\\a.example/p.gif">', /a\.example/],
    [
      "img with tab in scheme",
      '<img src="ht&#9;tps://a.example/p.gif">',
      /a\.example/,
    ],
    [
      "remote srcset",
      '<img src="/ok.png" srcset="/ok.png 1x, https://a.example/p.gif 2x">',
      /a\.example/,
    ],
    [
      "table background",
      '<table background="https://a.example/p.gif"><tr><td>x</td></tr></table>',
      /a\.example/,
    ],
    ["data-* attributes", '<p data-dir="true">x</p>', /data-dir/],
    ["javascript: links", '<a href="javascript:alert(1)">x</a>', /javascript:/],
    // the app's CSS pins bare <header> and <nav> to the viewport
    ["<header>", "<header>Session expired</header>", /<header/],
    ["<nav>", "<nav>x</nav>", /<nav/],
    ["<main>", "<main>x</main>", /<main/],
    ["<aside>", "<aside>x</aside>", /<aside/],
    ["<footer>", "<footer>x</footer>", /<footer/],
    ["<section>", "<section>x</section>", /<section/],
    ["<article>", "<article>x</article>", /<article/],
    ["<body>", "<body>x</body>", /<body/],
    ["<object>", '<object data="/x"></object>', /<object/],
    ["<embed>", '<embed src="/x">', /<embed/],
    [
      "<link>",
      '<link rel="stylesheet" href="https://a.example/x.css">',
      /<link/,
    ],
    ["<marquee>", "<marquee>x</marquee>", /<marquee/],
  ];
  for (const [what, html, bad] of cases) {
    it(what, () => {
      const out = clean(html);
      expect(out).not.toMatch(bad);
    });
  }

  it("non-checkbox inputs, and makes checkboxes inert", () => {
    const out = clean(
      '<input type="password" name="p"><input type="checkbox" checked>'
    );
    expect(out).not.toMatch(/password|name=/);
    expect(out).toMatch(/<input[^>]*type="checkbox"/);
    for (const m of out.matchAll(/<input[^>]*>/g)) {
      expect(m[0]).toContain("disabled");
    }
  });
});
