// @vitest-environment jsdom
//
// The preview's window keydown handler (Escape closes, Enter and the arrow
// keys switch files) also acted on keys typed into the command shell,
// which stays open on top of the preview: moving the caret or pressing
// Enter to run a command switched or closed the preview.
import { afterEach, describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  (window as any).FileBrowser = {
    AuthMethod: "json",
    BaseURL: "",
    StaticURL: "/static",
    Name: "",
    LoginPage: true,
    LogoutPage: "/login",
    ResizePreview: true,
    TusSettings: { chunkSize: 1, retryCount: 0 },
  };
});

const h = vi.hoisted(() => ({ push: vi.fn(), replace: vi.fn() }));

vi.mock("@/utils/auth", () => ({ renew: vi.fn(), logout: vi.fn() }));
vi.mock("@/api", () => ({
  files: {
    fetch: vi.fn().mockResolvedValue({ items: [], isDir: true }),
    getDownloadURL: () => "/api/raw/a.png",
    getPreviewURL: () => "/api/preview/big/a.png",
    getSubtitlesURL: () => [],
  },
}));
vi.mock("vue-router", () => ({
  useRoute: () => ({
    path: "/files/pics/a.png",
    fullPath: "/files/pics/a.png",
    query: {},
  }),
  useRouter: () => ({ push: h.push, replace: h.replace }),
}));
vi.mock("vue-reader", () => ({ VueReader: { render: () => null } }));
vi.mock("@/components/files/ExtendedImage.vue", () => ({
  default: { render: () => null },
}));
vi.mock("@/components/files/VideoViewer.vue", () => ({
  default: { render: () => null },
}));
vi.mock("@/components/files/CsvViewer.vue", () => ({
  default: { render: () => null },
}));
vi.mock("vue-i18n", async (orig) => ({
  ...(await orig<typeof import("vue-i18n")>()),
  useI18n: () => ({ t: (k: string) => k }),
}));

import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import Preview from "@/views/files/Preview.vue";
import { useAuthStore } from "@/stores/auth";
import { useFileStore } from "@/stores/file";

let wrapper: VueWrapper | null = null;

async function mountPreview(type = "image") {
  setActivePinia(createPinia());
  useAuthStore().user = { id: 1, perm: {} } as any;
  // the folder the preview was opened from: a.png, then b.png
  useFileStore().oldReq = {
    items: [
      { name: "a.png", type, url: "/files/pics/a.png" },
      { name: "b.png", type, url: "/files/pics/b.png" },
    ],
  } as any;
  useFileStore().req = {
    name: "a.png",
    path: "/pics/a.png",
    url: "/files/pics/a.png",
    type,
    extension: ".png",
    isDir: false,
    items: [],
  } as any;
  wrapper = mount(Preview, {
    attachTo: document.body,
    global: {
      mocks: { $t: (k: string) => k },
      provide: { $showError: vi.fn() },
      stubs: {
        HeaderBar: true,
        Action: true,
        ExtendedImage: true,
        VideoViewer: true,
        CsvViewer: true,
        VueReader: true,
      },
    },
  });
  await flushPromises();
  h.push.mockClear();
  h.replace.mockClear();
}

function pressIn(el: Element, key: string, which: number) {
  const ev = new KeyboardEvent("keydown", { key, bubbles: true });
  Object.defineProperty(ev, "which", { value: which });
  el.dispatchEvent(ev);
}

describe("preview keyboard shortcuts", () => {
  afterEach(() => {
    wrapper?.unmount();
    wrapper = null;
    document.body.innerHTML = "";
  });

  it("Escape typed in the command shell does not close the preview", async () => {
    await mountPreview();
    const shell = document.createElement("pre");
    shell.setAttribute("contenteditable", "true"); // as Shell.vue renders it
    document.body.appendChild(shell);
    pressIn(shell, "Escape", 27);
    expect(h.push).not.toHaveBeenCalled();
  });

  it("control: Escape on the page closes the preview", async () => {
    await mountPreview();
    pressIn(document.body, "Escape", 27);
    expect(h.push).toHaveBeenCalledWith({ path: "/files/pics/" });
  });
  it("control: the right arrow on the page goes to the next file", async () => {
    await mountPreview();
    pressIn(document.body, "ArrowRight", 39);
    expect(h.replace).toHaveBeenCalledWith({ path: "/files/pics/b.png" });
  });

  // The video player has focus (VideoViewer focuses it on open) and its
  // shortcuts use the arrow keys to seek.
  it("for a video, the arrow keys do not switch files, and Escape still closes", async () => {
    await mountPreview("video");
    pressIn(document.body, "ArrowRight", 39);
    pressIn(document.body, "ArrowLeft", 37);
    expect(h.replace).not.toHaveBeenCalled();
    pressIn(document.body, "Escape", 27);
    expect(h.push).toHaveBeenCalledWith({ path: "/files/pics/" });
  });
  // A key already handled inside the page must not also act on the
  // preview, e.g. Escape closing the video player's settings menu (its
  // menus call preventDefault() on it) must not close the preview too.
  it("a key the page already handled (defaultPrevented) is ignored", async () => {
    await mountPreview();
    const menu = document.createElement("div");
    document.body.appendChild(menu);
    menu.addEventListener("keydown", (e) => e.preventDefault());
    const ev = new KeyboardEvent("keydown", {
      key: "Escape",
      bubbles: true,
      cancelable: true,
    });
    Object.defineProperty(ev, "which", { value: 27 });
    menu.dispatchEvent(ev);
    expect(ev.defaultPrevented).toBe(true);
    expect(h.push).not.toHaveBeenCalled();
  });
});
