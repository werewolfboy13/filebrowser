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
// epubjs then serializes the chapter's root element and renders the string
// through iframe.srcdoc, an HTML parse, where the policy counts only in
// <head> and only for what follows it. Content before <head>, or a root
// that is not <html>, would come first there, so the serialize hook also
// puts the policy at the very start of the string (protectChapters).

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

// url(...) and @import "..." in CSS. Defence in depth only (the policy is
// what covers CSS: escapes or a URL split across nodes get past this).
const CSS_URL = /url\(\s*(["']?)([^"')]*)\1\s*\)|@import\s+(["'])([^"']*)\3/gi;
const stripRemoteCss = (css: string) =>
  css.replace(CSS_URL, (match, _q, url?: string, _q2?, imported?: string) =>
    !isRemote(url ?? imported ?? "")
      ? match
      : url !== undefined
        ? "none"
        : "@import none"
  );

export function neutraliseRemoteResources(doc: Document) {
  for (const el of Array.from(doc.getElementsByTagName("*"))) {
    if (el.localName.toLowerCase() === "style") {
      for (const text of Array.from(el.childNodes)) {
        if (text instanceof CharacterData)
          text.data = stripRemoteCss(text.data);
      }
    }
    for (const attr of Array.from(el.attributes)) {
      const name = attr.localName.toLowerCase();
      if (name === "style") attr.value = stripRemoteCss(attr.value);
      const isHref =
        name === "href" && (!attr.namespaceURI || attr.namespaceURI === XLINK);
      if (!LOADED.has(name) && !isHref) continue;
      if (isHref && LINKS.has(el.localName.toLowerCase())) continue;
      const remote =
        name === "srcset" ? srcsetIsRemote(attr.value) : isRemote(attr.value);
      if (remote) el.removeAttributeNode(attr);
    }
  }

  // <head> (made if missing) first in <html>, the policy first in <head>.
  // Any other root is left as it is: the serialize hook covers it.
  const root = doc.documentElement;
  if (root?.localName.toLowerCase() !== "html") return;
  const head =
    doc.getElementsByTagNameNS(XHTML, "head")[0] ??
    doc.getElementsByTagName("head")[0] ??
    doc.createElementNS(XHTML, "head");
  root.insertBefore(head, root.firstChild);
  const meta = doc.createElementNS(XHTML, "meta");
  meta.setAttribute("http-equiv", "Content-Security-Policy");
  meta.setAttribute("content", EPUB_CHAPTER_POLICY);
  head.insertBefore(meta, head.firstChild);
}

const POLICY_META =
  `<meta http-equiv="Content-Security-Policy" ` +
  `content="${EPUB_CHAPTER_POLICY}"/>`;

// epubjs's serialize hook: the policy as the first thing in the chapter's
// string, so the srcdoc HTML parse meets it before anything else whatever
// the chapter's shape (a srcdoc document without a doctype is not in
// quirks mode, and epubjs serializes the root element only, so there is no
// doctype to put it after). The book's own serialize hook sets
// section.output from the string it was given; prepending after the other
// hooks of this round have run keeps the two from undoing each other in
// either order.
function prependPolicy(_output: string, section: { output: string }) {
  return Promise.resolve().then(() => {
    section.output = POLICY_META + section.output;
  });
}

type Hook = { register(fn: (...args: never[]) => unknown): void };

// Registers both hooks on a book's spine, before its first chapter is shown.
export function protectChapters(spine: {
  hooks: { content: Hook; serialize: Hook };
}) {
  spine.hooks.content.register(neutraliseRemoteResources);
  spine.hooks.serialize.register(prependPolicy);
}
