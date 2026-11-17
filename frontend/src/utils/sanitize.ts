import DOMPurify from "dompurify";

// Sanitiser for the markdown preview, which renders a file's content
// (written by anyone who can write to a folder the viewer can read) into
// the app's own document.
//
// DOMPurify's default profile stops scripts, but still lets through markup
// that can redress the File Browser UI on its real origin: <style> applies
// to the whole app, style attributes make full-screen overlays, forms post
// typed passwords anywhere, ids (e.g. "focus-prompt") and classes hook into
// the app's own scripts and CSS, and remote images track who opened the
// file. This profile keeps what markdown produces (headings, emphasis,
// lists and task lists, tables, code, links, same-origin and data: images,
// <details>, KaTeX's MathML) and drops the rest: no SVG, styles, forms,
// dialogs, media or sectioning elements, and no attributes that hook into
// the app.

const purifier = DOMPurify();

// Only same-origin and data: URLs may be fetched by the preview itself.
// (Links are navigation and stay allowed; DOMPurify still filters their
// schemes.) Parse like the browser does, so whitespace, "//host" or
// "\\host" tricks resolve to their real origin.
function isLocalURL(value: string): boolean {
  try {
    const url = new URL(value, document.baseURI);
    return url.protocol === "data:" || url.origin === window.location.origin;
  } catch {
    return false;
  }
}

const FETCHING_ATTRS = new Set(["src", "poster", "background", "xlink:href"]);

purifier.addHook("uponSanitizeAttribute", (_node, data) => {
  if (FETCHING_ATTRS.has(data.attrName)) {
    if (!isLocalURL(data.attrValue)) data.keepAttr = false;
  } else if (data.attrName === "srcset") {
    const urls = data.attrValue
      .split(",")
      .map((candidate) => candidate.trim().split(/\s+/)[0])
      .filter(Boolean);
    if (!urls.every(isLocalURL)) data.keepAttr = false;
  }
});

// What a task-list checkbox may keep.
const CHECKBOX_ATTRS = new Set(["type", "checked", "disabled"]);

purifier.addHook("afterSanitizeAttributes", (node) => {
  // GFM task lists render <input type="checkbox" disabled>. Keep those,
  // inert and with nothing else; drop every other input.
  if (node.nodeName === "INPUT") {
    if ((node.getAttribute("type") ?? "").toLowerCase() !== "checkbox") {
      node.remove();
      return;
    }
    for (const { name } of [...node.attributes]) {
      if (!CHECKBOX_ATTRS.has(name)) node.removeAttribute(name);
    }
    node.setAttribute("type", "checkbox");
    node.setAttribute("disabled", "");
  }
});

// An allow-list of tags rather than a deny-list: the app's CSS styles some
// bare tags (header and nav are position: fixed), so sectioning elements
// like <header>, <nav>, <main> or <aside> must not get through either.
const HTML_TAGS = [
  "a",
  "abbr",
  "b",
  "bdi",
  "bdo",
  "blockquote",
  "br",
  "caption",
  "cite",
  "code",
  "col",
  "colgroup",
  "dd",
  "del",
  "details",
  "dfn",
  "div",
  "dl",
  "dt",
  "em",
  "figcaption",
  "figure",
  "h1",
  "h2",
  "h3",
  "h4",
  "h5",
  "h6",
  "hr",
  "i",
  "img",
  "input", // GFM task-list checkboxes, made inert above; others dropped
  "ins",
  "kbd",
  "li",
  "mark",
  "ol",
  "p",
  "pre",
  "q",
  "rp",
  "rt",
  "ruby",
  "s",
  "samp",
  "small",
  "span",
  "strike",
  "strong",
  "sub",
  "summary",
  "sup",
  "table",
  "tbody",
  "td",
  "tfoot",
  "th",
  "thead",
  "time",
  "tr",
  "u",
  "ul",
  "var",
  "wbr",
];

// The MathML elements DOMPurify allows by default (what KaTeX renders).
const MATHML_TAGS = [
  "math",
  "menclose",
  "merror",
  "mfenced",
  "mfrac",
  "mglyph",
  "mi",
  "mlabeledtr",
  "mmultiscripts",
  "mn",
  "mo",
  "mover",
  "mpadded",
  "mphantom",
  "mprescripts",
  "mroot",
  "mrow",
  "ms",
  "mspace",
  "msqrt",
  "mstyle",
  "msub",
  "msubsup",
  "msup",
  "mtable",
  "mtd",
  "mtext",
  "mtr",
  "munder",
  "munderover",
];

export function sanitizeMarkdownHtml(html: string): string {
  return purifier.sanitize(html, {
    ALLOWED_TAGS: [...HTML_TAGS, ...MATHML_TAGS],
    // KaTeX wraps each formula in <semantics> with an <annotation> holding
    // the TeX source. DOMPurify does not allow those tags; drop the
    // annotation with its text, or the source would show after the formula.
    ADD_FORBID_CONTENTS: ["annotation", "annotation-xml"],
    FORBID_ATTR: [
      "style",
      "id",
      "name",
      "class",
      "accesskey",
      "autofocus",
      "tabindex",
      "target",
      "ping",
      "popover",
      "popovertarget",
      "popovertargetaction",
      "form",
      "formaction",
      "action",
      "contenteditable",
      "draggable",
    ],
    ALLOW_DATA_ATTR: false,
  });
}
