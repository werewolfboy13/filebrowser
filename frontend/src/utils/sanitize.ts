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
// <details>, KaTeX's MathML) and drops the rest.

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

purifier.addHook("afterSanitizeAttributes", (node) => {
  // GFM task lists render <input type="checkbox" disabled>; any other input
  // becomes an inert checkbox too.
  if (node.nodeName === "INPUT") {
    node.setAttribute("type", "checkbox");
    node.setAttribute("disabled", "");
  }
});

export function sanitizeMarkdownHtml(html: string): string {
  return purifier.sanitize(html, {
    USE_PROFILES: { html: true, mathMl: true }, // no SVG
    FORBID_TAGS: [
      "style",
      "form",
      "button",
      "select",
      "option",
      "optgroup",
      "textarea",
      "fieldset",
      "legend",
      "label",
      "datalist",
      "output",
      "dialog",
      "video",
      "audio",
      "source",
      "track",
      "picture",
    ],
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
