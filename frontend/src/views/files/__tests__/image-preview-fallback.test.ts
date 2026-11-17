// @vitest-environment jsdom
//
// The server refuses to scale an image beyond an absolute size limit: the
// big preview (/api/preview/big/...) and the listing thumbnail answer with
// an error status (413). The preview showed a broken image and the listing
// a blank one. Now the preview falls back to the original file when the
// user may download it (otherwise it says there is no preview), and the
// listing shows the file's icon.
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
    EnableThumbs: true,
    TusSettings: { chunkSize: 1, retryCount: 0 },
  };
});

vi.mock("@/utils/auth", () => ({ renew: vi.fn(), logout: vi.fn() }));
vi.mock("@/api", () => ({
  files: {
    fetch: vi.fn().mockResolvedValue({ items: [], isDir: true }),
    getDownloadURL: () => "/api/raw/pics/huge.png?inline=true",
    getPreviewURL: (_f: unknown, size: string) =>
      `/api/preview/${size}/pics/huge.png`,
    getSubtitlesURL: () => [],
  },
}));
vi.mock("vue-router", () => ({
  useRoute: () => ({
    path: "/files/pics/huge.png",
    fullPath: "/files/pics/huge.png",
    query: {},
  }),
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
}));
vi.mock("vue-reader", () => ({ VueReader: { render: () => null } }));
vi.mock("vue-i18n", async (orig) => ({
  ...(await orig<typeof import("vue-i18n")>()),
  useI18n: () => ({ t: (k: string) => k }),
}));

import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { defineComponent, h } from "vue";
import dayjs from "dayjs";
import relativeTime from "dayjs/plugin/relativeTime";
import Preview from "@/views/files/Preview.vue";
import ListingItem from "@/components/files/ListingItem.vue";
import ExtendedImage from "@/components/files/ExtendedImage.vue";
import { useAuthStore } from "@/stores/auth";
import { useFileStore } from "@/stores/file";
import { lazyloadOptions } from "@/utils/lazyload";
dayjs.extend(relativeTime);

// stands in for ExtendedImage: shows the URL it was given, and can fail
const FakeImage = defineComponent({
  props: { src: String },
  emits: ["error"],
  setup(props) {
    return () => h("img", { class: "fake-image", "data-src": props.src });
  },
});

let wrapper: VueWrapper | null = null;
afterEach(() => {
  wrapper?.unmount();
  wrapper = null;
  document.body.innerHTML = "";
});

async function mountPreview(download: boolean) {
  setActivePinia(createPinia());
  useAuthStore().user = { id: 1, perm: { download } } as any;
  useFileStore().req = {
    name: "huge.png",
    path: "/pics/huge.png",
    url: "/files/pics/huge.png",
    type: "image",
    extension: ".png",
    isDir: false,
    modified: "2024-01-01T00:00:00Z",
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
        ExtendedImage: FakeImage,
        VideoViewer: true,
        CsvViewer: true,
        VueReader: true,
      },
    },
  });
  await flushPromises();
  return wrapper;
}

const shown = (w: VueWrapper) =>
  w.find(".fake-image").exists()
    ? w.find(".fake-image").attributes("data-src")
    : null;

describe("image preview the server cannot scale", () => {
  it("falls back to the original file for a user who may download it", async () => {
    const w = await mountPreview(true);
    expect(shown(w)).toBe("/api/preview/big/pics/huge.png");
    w.findComponent(FakeImage).vm.$emit("error");
    await flushPromises();
    console.log("after the big preview failed:", shown(w));
    expect(shown(w)).toBe("/api/raw/pics/huge.png?inline=true");
  });

  it("says there is no preview when the original fails too", async () => {
    const w = await mountPreview(true);
    w.findComponent(FakeImage).vm.$emit("error");
    await flushPromises();
    w.findComponent(FakeImage).vm.$emit("error");
    await flushPromises();
    expect(shown(w)).toBeNull();
    expect(w.text()).toContain("files.noPreview");
  });

  it("says there is no preview for a user who may not download", async () => {
    const w = await mountPreview(false);
    w.findComponent(FakeImage).vm.$emit("error");
    await flushPromises();
    expect(shown(w)).toBeNull();
    expect(w.text()).toContain("files.noPreview");
  });
});

describe("the image viewer", () => {
  it("reports an image that cannot be loaded", async () => {
    const w = mount(ExtendedImage, {
      props: { src: "/api/preview/big/pics/huge.png" },
    });
    await w.find("img").trigger("error");
    expect(w.emitted("error")).toHaveLength(1);
    w.unmount();
  });
});

describe("listing thumbnail the server cannot make", () => {
  it("shows the file's icon instead", async () => {
    setActivePinia(createPinia());
    useAuthStore().user = { perm: {}, singleClick: false } as any;
    const w = mount(ListingItem, {
      props: {
        name: "huge.png",
        isDir: false,
        url: "/files/pics/huge.png",
        type: "image",
        size: 1,
        modified: "2024-01-01T00:00:00Z",
        index: 0,
        path: "/pics/huge.png",
      },
      global: {
        provide: { $showError: vi.fn() },
        directives: { lazy: {} },
        mocks: { $t: (k: string) => k },
      },
    });
    expect(w.find("img").exists()).toBe(true);
    // what vue-lazyload dispatches on the <img> when loading fails
    await w.find("img").trigger("error");
    expect(w.find("img").exists()).toBe(false);
    expect(w.find("i.material-icons").exists()).toBe(true);
    w.unmount();
  });

  it("vue-lazyload is set up to dispatch that event", () => {
    expect(lazyloadOptions.dispatchEvent).toBe(true);
  });
});
