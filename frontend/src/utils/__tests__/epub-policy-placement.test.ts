// @vitest-environment jsdom
//
// epubjs parses an archived chapter as XML, runs the spine's content hooks
// on that document, serializes its root element, runs the serialize hooks
// on the string, and renders it through iframe.srcdoc: an HTML parse. A
// Content-Security-Policy <meta> only counts there if it is in <head> and
// only for what the parser meets after it. A chapter without <head>, with
// something before <head>, or whose root is not <html> left the book's CSS
// (@import, url(), inline style url()) and fonts free to load from another
// site. Through epubjs's own Spine and Section, the policy must now be the
// first thing the HTML parser meets in every such chapter.
import { describe, expect, it } from "vitest";
import Spine from "epubjs/src/spine";
import Section from "epubjs/src/section";
import { EPUB_CHAPTER_POLICY, protectChapters } from "@/utils/epub";

const XHTML = "http://www.w3.org/1999/xhtml";
const html = (inner: string) =>
  `<?xml version="1.0" encoding="UTF-8"?>\n<!DOCTYPE html>\n` +
  `<html xmlns="${XHTML}">${inner}</html>`;

const IMPORT = `<style>@import url("https://t.example/a.css"); p { color: red }</style>`;
const INLINE = `<div style="background:url(https://t.example/p.png)">y</div>`;

const chapters: Record<string, string> = {
  normal: html(
    `<head><title>c</title>${IMPORT}</head><body><p>x</p>${INLINE}</body>`
  ),
  noHead: html(`<body>${IMPORT}${INLINE}</body>`),
  styleBeforeHead: html(
    `${IMPORT}<head><title>c</title></head><body>${INLINE}</body>`
  ),
  textBeforeHead: html(`x<head><title>c</title></head><body>${INLINE}</body>`),
  notHtmlRoot:
    `<?xml version="1.0" encoding="UTF-8"?>\n` +
    `<svg xmlns="http://www.w3.org/2000/svg"><style>@import url("https://t.example/a.css");</style>` +
    `<image href="https://t.example/i.png"/></svg>`,
};

// the book's own serialize hook (book.js replacements()): it sets
// section.output from the output it was given, not from section.output
const substitute = (output: string, section: { output: string }) => {
  section.output = output.replace("book.css", "blob:book-css");
};

async function rendered(source: string, substituteFirst: boolean) {
  const spine = new Spine();
  if (substituteFirst) spine.hooks.serialize.register(substitute);
  protectChapters(spine);
  if (!substituteFirst) spine.hooks.serialize.register(substitute);
  const section = new Section(
    { url: "/OEBPS/c1.xhtml", href: "c1.xhtml", canonical: "/OEBPS/c1.xhtml" },
    spine.hooks
  );
  const xml = new DOMParser().parseFromString(source, "application/xhtml+xml");
  expect(xml.getElementsByTagName("parsererror")).toHaveLength(0);
  const output: string = await section.render(() => Promise.resolve(xml));
  // what the iframe gets as srcdoc
  return { output, doc: new DOMParser().parseFromString(output, "text/html") };
}

// the first thing in the document, apart from <html> and <head> themselves
function firstThing(doc: Document): Node | null {
  const walker = doc.createTreeWalker(
    doc,
    NodeFilter.SHOW_ELEMENT | NodeFilter.SHOW_TEXT
  );
  for (let node = walker.nextNode(); node; node = walker.nextNode()) {
    if (node.nodeType === Node.TEXT_NODE && !node.textContent!.trim()) continue;
    if (node === doc.documentElement || node === doc.head) continue;
    return node;
  }
  return null;
}

describe("the EPUB chapter policy", () => {
  for (const [name, source] of Object.entries(chapters)) {
    for (const substituteFirst of [true, false]) {
      it(`comes first in the rendered chapter: ${name}${substituteFirst ? "" : " (book hook after)"}`, async () => {
        const { doc, output } = await rendered(source, substituteFirst);
        const first = firstThing(doc) as Element;
        expect(first?.localName, output).toBe("meta");
        expect(first.parentElement).toBe(doc.head);
        expect(first.getAttribute("http-equiv")).toBe(
          "Content-Security-Policy"
        );
        expect(first.getAttribute("content")).toBe(EPUB_CHAPTER_POLICY);
        // the chapter's own content is still there
        expect(doc.body.textContent + doc.head.textContent).not.toBe("");
      });
    }
  }

  it("keeps a normal chapter's structure: the policy first in its <head>", async () => {
    const { doc } = await rendered(chapters.normal, true);
    expect(doc.title).toBe("c");
    expect(doc.querySelector("body p")?.textContent).toBe("x");
    expect(doc.head.querySelector("style")).not.toBeNull();
  });

  it("strips remote url() and @import from the book's CSS too", async () => {
    for (const name of ["normal", "noHead", "styleBeforeHead"]) {
      const { output, doc } = await rendered(chapters[name], true);
      expect(output, name).not.toContain("t.example");
      // the rest of the CSS stays
      expect(doc.querySelector("style")?.textContent, name).toContain(
        "p { color: red }"
      );
    }
    // @import with a string, and the book's own url() left alone
    const { doc } = await rendered(
      html(
        `<head><style>@import "https://t.example/b.css"; ` +
          `p { background: url(../img/p.png) }</style></head>` +
          `<body><p style="background:url('//t.example/q.png')">x</p></body>`
      ),
      true
    );
    const css = doc.querySelector("style")!.textContent!;
    expect(css).not.toContain("t.example");
    expect(css).toContain("url(../img/p.png)");
    expect(doc.querySelector("p")!.getAttribute("style")).toBe(
      "background:none"
    );
  });
});
