// @vitest-environment jsdom
//
// An EPUB preview rendered the book's chapters as they are: an <img> or a
// stylesheet with a URL on another site inside an uploaded book was loaded
// from that site when someone opened it, telling it who opened the book
// and when (the markdown preview blocks this). Now each chapter document
// is cleaned before epubjs renders it (its spine content hook): attributes
// that would load something from another origin are removed, and a
// Content-Security-Policy meta tag lets the chapter load only the book's
// own resources (blob: and data: URLs, which epubjs makes of them, or the
// app's origin). Links (<a href>) are left alone: they are not loaded.
// Whether a browser enforces the meta policy (which also covers url() and
// @import in the book's CSS) can only be seen in a real browser.
import { afterEach, describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  (window as any).FileBrowser = {
    AuthMethod: "json",
    BaseURL: "",
    StaticURL: "/static",
    Name: "",
    LoginPage: true,
    LogoutPage: "/login",
    TusSettings: { chunkSize: 1, retryCount: 0 },
  };
});

const h = vi.hoisted(() => ({
  register: vi.fn(),
  serialize: vi.fn(),
  order: [] as string[],
}));

vi.mock("@/utils/auth", () => ({ renew: vi.fn(), logout: vi.fn() }));
vi.mock("@/api", () => ({
  files: {
    fetch: vi.fn().mockResolvedValue({ items: [], isDir: true }),
    getDownloadURL: () => "/api/raw/books/b.epub?inline=true",
    getPreviewURL: () => "",
    getSubtitlesURL: () => [],
  },
}));
vi.mock("vue-router", () => ({
  useRoute: () => ({
    path: "/files/books/b.epub",
    fullPath: "/files/books/b.epub",
    query: {},
  }),
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
}));
// vue-reader as it works: getRendition is called with the new rendition
// before the first chapter is displayed
vi.mock("vue-reader", async () => {
  const { defineComponent, onMounted } = await import("vue");
  return {
    VueReader: defineComponent({
      props: { getRendition: Function },
      setup(props) {
        onMounted(() => {
          const rendition = {
            book: {
              spine: {
                hooks: {
                  content: {
                    register: (fn: unknown) => {
                      h.order.push("register");
                      h.register(fn);
                    },
                  },
                  serialize: { register: h.serialize },
                },
              },
            },
            themes: {
              override: vi.fn(),
              registerRules: vi.fn(),
              fontSize: vi.fn(),
              select: vi.fn(),
            },
          };
          props.getRendition?.(rendition);
          h.order.push("display");
        });
        return () => null;
      },
    }),
  };
});
vi.mock("vue-i18n", async (orig) => ({
  ...(await orig<typeof import("vue-i18n")>()),
  useI18n: () => ({ t: (k: string) => k }),
}));

import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import Preview from "@/views/files/Preview.vue";
import { useAuthStore } from "@/stores/auth";
import { useFileStore } from "@/stores/file";

const CHAPTER = `<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:xlink="http://www.w3.org/1999/xlink">
<head>
  <title>Chapter</title>
  <link rel="stylesheet" type="text/css" href="https://tracker.example/s.css"/>
  <link rel="stylesheet" type="text/css" href="../css/book.css"/>
</head>
<body>
  <img id="remote" src="https://tracker.example/pixel.gif" alt=""/>
  <img id="protocol-relative" src="//tracker.example/p.gif" alt=""/>
  <img id="local" src="../images/cover.jpg" alt=""/>
  <img id="inline" src="data:image/gif;base64,R0lGODlhAQABAAAAACw=" alt=""/>
  <img id="same-origin" src="${location.origin}/static/img/logo.svg" alt=""/>
  <img id="srcset" src="../images/a.jpg" srcset="../images/a.jpg 1x, https://tracker.example/a2.jpg 2x" alt=""/>
  <video id="video" poster="https://tracker.example/poster.jpg" src="http://tracker.example/v.mp4"/>
  <audio id="audio" src="https://tracker.example/a.mp3"/>
  <object id="object" data="https://tracker.example/o.svg"/>
  <svg xmlns="http://www.w3.org/2000/svg"><image id="svg-image" xlink:href="https://tracker.example/i.png"/></svg>
  <a id="link" href="https://example.org/more">more</a>
</body>
</html>`;

let wrapper: VueWrapper | null = null;

async function cleanerFromPreview() {
  setActivePinia(createPinia());
  useAuthStore().user = { id: 1, perm: {} } as any;
  useFileStore().req = {
    name: "b.epub",
    path: "/books/b.epub",
    url: "/files/books/b.epub",
    type: "blob",
    extension: ".epub",
    isDir: false,
    items: [],
  } as any;
  wrapper = mount(Preview, {
    attachTo: document.body,
    global: {
      mocks: { $t: (k: string) => k },
      provide: { $showError: vi.fn() },
      stubs: { HeaderBar: true, Action: true },
    },
  });
  await flushPromises();
  expect(h.register).toHaveBeenCalledTimes(1);
  return h.register.mock.calls[0][0] as (doc: Document) => void;
}

describe("EPUB preview and remote resources", () => {
  afterEach(() => {
    wrapper?.unmount();
    wrapper = null;
    h.register.mockClear();
    h.serialize.mockClear();
    h.order.length = 0;
  });

  it("cleans each chapter before the first one is displayed", async () => {
    await cleanerFromPreview();
    expect(h.order).toEqual(["register", "display"]);
    // and the policy is put first in each serialized chapter (utils/epub)
    expect(h.serialize).toHaveBeenCalledTimes(1);
  });

  it("removes what would load from another origin, and only that", async () => {
    const clean = await cleanerFromPreview();
    const doc = new DOMParser().parseFromString(
      CHAPTER,
      "application/xhtml+xml"
    );
    clean(doc);
    const attr = (id: string, name: string) =>
      doc.getElementById(id)?.getAttribute(name) ?? null;

    expect(
      [...doc.querySelectorAll("link")].map((l) => l.getAttribute("href"))
    ).toEqual([null, "../css/book.css"]);
    expect(attr("remote", "src")).toBeNull();
    expect(attr("protocol-relative", "src")).toBeNull();
    expect(attr("srcset", "srcset")).toBeNull();
    expect(attr("video", "poster")).toBeNull();
    expect(attr("video", "src")).toBeNull();
    expect(attr("audio", "src")).toBeNull();
    expect(attr("object", "data")).toBeNull();
    expect(
      doc
        .getElementById("svg-image")
        ?.getAttributeNS("http://www.w3.org/1999/xlink", "href") ?? null
    ).toBeNull();

    // the book's own resources, inline ones, the app's own, and links stay
    expect(attr("local", "src")).toBe("../images/cover.jpg");
    expect(attr("srcset", "src")).toBe("../images/a.jpg");
    expect(attr("inline", "src")).toMatch(/^data:image\/gif/);
    expect(attr("same-origin", "src")).toBe(
      `${location.origin}/static/img/logo.svg`
    );
    expect(attr("link", "href")).toBe("https://example.org/more");
  });

  it("adds a policy that lets the chapter load only the book's own resources", async () => {
    const clean = await cleanerFromPreview();
    const doc = new DOMParser().parseFromString(
      CHAPTER,
      "application/xhtml+xml"
    );
    clean(doc);
    const first = doc.head.firstElementChild!;
    expect(first.localName).toBe("meta");
    expect(first.namespaceURI).toBe("http://www.w3.org/1999/xhtml");
    expect(first.getAttribute("http-equiv")).toBe("Content-Security-Policy");
    const policy = first.getAttribute("content")!;
    console.log("policy:", policy);
    expect(policy).toMatch(/default-src 'none'/);
    for (const directive of ["img-src", "style-src", "font-src", "media-src"]) {
      const sources = policy
        .split(";")
        .map((d) => d.trim().split(/\s+/))
        .find(([name]) => name === directive)!
        .slice(1);
      expect(sources).toEqual(
        expect.arrayContaining(["'self'", "blob:", "data:"])
      );
      expect(sources.some((s) => /^https?:|^\*$/.test(s))).toBe(false);
    }
  });
});
