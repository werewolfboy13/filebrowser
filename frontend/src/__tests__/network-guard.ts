// Test helper (jsdom): records every network request the code under test
// starts, so tests can assert that the app only talks to its own origin.
//
// jsdom never fetches <script>, <link> or <img> resources in these tests, so
// nothing here touches the network. Requests are recorded from:
// - fetch() and XMLHttpRequest.open()
// - new Worker() / new SharedWorker()
// - elements with a src/href/srcset/poster/data attribute that end up in the
//   document (scripts, stylesheets, images, media, iframes, objects)
// - CSS @import / url() inside inline <style> elements
import { vi } from "vitest";

export interface NetworkGuard {
  requests: string[];
  thirdParty(): string[];
  workers: string[];
  stop(): void;
}

const URL_ATTRS = ["src", "href", "srcset", "poster", "data", "background"];

function urlsOf(el: Element): string[] {
  const out: string[] = [];
  for (const attr of URL_ATTRS) {
    const v = el.getAttribute(attr);
    if (!v) continue;
    // <a href> is navigation, not a request
    if (attr === "href" && el.tagName === "A") continue;
    if (attr === "srcset") {
      for (const part of v.split(",")) {
        const u = part.trim().split(/\s+/)[0];
        if (u) out.push(u);
      }
    } else {
      out.push(v);
    }
  }
  if (el.tagName === "STYLE") {
    const css = el.textContent ?? "";
    for (const m of css.matchAll(
      /@import\s+(?:url\()?\s*["']?([^"')\s;]+)|url\(\s*["']?([^"')]+)/gi
    )) {
      out.push(m[1] ?? m[2]);
    }
  }
  return out;
}

export function isSameOrigin(url: string): boolean {
  if (url.startsWith("data:") || url.startsWith("blob:")) return true;
  try {
    return new URL(url, window.location.href).origin === window.location.origin;
  } catch {
    return false;
  }
}

export function startNetworkGuard(
  onElement?: (el: Element) => void
): NetworkGuard {
  const requests: string[] = [];
  const workers: string[] = [];
  const record = (u: unknown) => requests.push(String(u));

  const fetchSpy = vi.fn((input: RequestInfo | URL) => {
    record(input instanceof Request ? input.url : input);
    return Promise.reject(new Error("network disabled in tests"));
  });
  vi.stubGlobal("fetch", fetchSpy);

  const xhrOpen = XMLHttpRequest.prototype.open;
  XMLHttpRequest.prototype.open = function (
    this: XMLHttpRequest,
    _method: string,
    url: string | URL
  ) {
    record(url);
    // never actually opened: send() will throw, which is fine for tests
  } as typeof XMLHttpRequest.prototype.open;

  class FakeWorker {
    onmessage: ((e: MessageEvent) => void) | null = null;
    onerror: ((e: Event) => void) | null = null;
    constructor(url: string | URL) {
      record(url);
      workers.push(String(url));
    }
    postMessage() {}
    terminate() {}
    addEventListener() {}
    removeEventListener() {}
  }
  vi.stubGlobal("Worker", FakeWorker);
  vi.stubGlobal("SharedWorker", FakeWorker);

  const seen = new WeakSet<Element>();
  const scan = (root: ParentNode) => {
    const els = [
      ...(root instanceof Element ? [root] : []),
      ...root.querySelectorAll("*"),
    ];
    for (const el of els) {
      if (seen.has(el)) continue;
      seen.add(el);
      urlsOf(el).forEach(record);
      onElement?.(el);
    }
  };
  scan(document);
  const observer = new MutationObserver((mutations) => {
    for (const m of mutations) {
      if (m.type === "attributes" && m.target instanceof Element) {
        seen.delete(m.target);
        scan(m.target);
      }
      m.addedNodes.forEach((n) => {
        if (n instanceof Element) scan(n);
      });
    }
  });
  observer.observe(document, {
    childList: true,
    subtree: true,
    attributes: true,
    attributeFilter: URL_ATTRS,
  });

  return {
    requests,
    workers,
    thirdParty: () => {
      // MutationObserver does not see into shadow roots (custom elements),
      // so walk them now
      const walk = (root: ParentNode) => {
        for (const el of root.querySelectorAll("*")) {
          if (el.shadowRoot) {
            scan(el.shadowRoot);
            walk(el.shadowRoot);
          }
        }
      };
      walk(document);
      return requests.filter((u) => !isSameOrigin(u));
    },
    stop() {
      observer.disconnect();
      XMLHttpRequest.prototype.open = xhrOpen;
      vi.unstubAllGlobals();
    },
  };
}
