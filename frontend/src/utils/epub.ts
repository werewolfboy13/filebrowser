// An uploaded EPUB must not make the browser load anything from another
// site when someone opens it (an image or stylesheet on a tracker's server
// would tell it who opened the book, and when). Registered as epubjs's
// spine content hook, this runs on each chapter document after it is parsed
// and before it is rendered (epubjs then turns the book's own resources into
// blob: URLs):
// - it removes the attributes that would load something from another
//   origin (links, <a href>, are not loaded and are left alone);
// - it puts a Content-Security-Policy first in the chapter's <head> that
//   lets it load only the book's own resources (blob: and data: URLs, or
//   the app's origin), which also covers url() and @import in the book's
//   CSS. epubjs's sandbox already keeps the chapter's scripts from running.

const XHTML = "http://www.w3.org/1999/xhtml";
const XLINK = "http://www.w3.org/1999/xlink";

export const EPUB_CHAPTER_POLICY =
  "default-src 'none'; img-src 'self' blob: data:; " +
  "style-src 'self' blob: data: 'unsafe-inline'; " +
  "font-src 'self' blob: data:; media-src 'self' blob: data:";

// attributes whose URL the browser loads by itself
const LOADED = new Set(["src", "srcset", "poster", "data", "background"]);
// elements whose href is a link to follow, not something loaded
const LINKS = new Set(["a", "area"]);

function isRemote(value: string): boolean {
  try {
    const url = new URL(value.trim(), window.location.href);
    if (url.protocol === "data:") return false;
    // blob: URLs carry the origin that made them
    return url.origin !== window.location.origin;
  } catch {
    return false; // not a URL: nothing to load
  }
}

// srcset: "url [descriptor], url [descriptor], ..."
const srcsetIsRemote = (value: string) =>
  value
    .split(",")
    .map((candidate) => candidate.trim().split(/\s+/)[0])
    .some((url) => url !== "" && isRemote(url));

export function neutraliseRemoteResources(doc: Document) {
  for (const el of Array.from(doc.getElementsByTagName("*"))) {
    for (const attr of Array.from(el.attributes)) {
      const name = attr.localName.toLowerCase();
      const isHref =
        name === "href" && (!attr.namespaceURI || attr.namespaceURI === XLINK);
      if (!LOADED.has(name) && !isHref) continue;
      if (isHref && LINKS.has(el.localName.toLowerCase())) continue;
      const remote =
        name === "srcset" ? srcsetIsRemote(attr.value) : isRemote(attr.value);
      if (remote) el.removeAttributeNode(attr);
    }
  }

  const head =
    doc.getElementsByTagNameNS(XHTML, "head")[0] ??
    doc.getElementsByTagName("head")[0];
  if (!head) return;
  const meta = doc.createElementNS(XHTML, "meta");
  meta.setAttribute("http-equiv", "Content-Security-Policy");
  meta.setAttribute("content", EPUB_CHAPTER_POLICY);
  head.insertBefore(meta, head.firstChild);
}
