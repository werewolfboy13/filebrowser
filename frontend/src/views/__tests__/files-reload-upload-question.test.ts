// @vitest-environment jsdom
//
// The listing reloads after a paste, a move, a delete, a rename and other
// dialogs' requests, without a route change. Reloading closed the open
// prompt: an upload's Replace/Skip question was closed without an answer,
// which skips every file still to be asked about. A reload now leaves that
// question open; opening the listing and a route change still close it.
//
// Real Files.vue, layout, file and upload stores; its child views, the API
// and the router are mocked. No network.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  (window as any).FileBrowser = {
    AuthMethod: "json",
    BaseURL: "",
    StaticURL: "/static",
    Name: "",
    TusSettings: { chunkSize: 1024, retryCount: 0 },
  };
});
const h = vi.hoisted(() => ({
  fetch: vi.fn(),
  showError: vi.fn(),
  // a child view, rendered as nothing
  empty: (name: string) => ({ default: { name, render: () => null } }),
}));
vi.mock("@/api", () => ({
  files: {
    fetch: h.fetch,
    // an upload: the server reports a conflict
    post: () =>
      Promise.reject(
        Object.assign(new Error("409 Conflict\n"), {
          status: 409,
          method: "POST",
        })
      ),
    abortResourceUploads: vi.fn(),
  },
  share: {},
  users: {},
  pub: {},
}));
vi.mock("@/api/tus", () => ({ abortAllUploads: vi.fn() }));
vi.mock("@/utils/upload", () => ({
  checkConflict: vi.fn(),
  handleFiles: vi.fn(),
}));
vi.mock("@/utils/buttons", () => ({
  default: { loading: vi.fn(), success: vi.fn(), done: vi.fn() },
}));
vi.mock("@/utils/auth", () => ({ renew: vi.fn(), logout: vi.fn() }));
vi.mock("vue-router", async () => {
  const { reactive } = await import("vue");
  const route = reactive({ path: "/files/docs/", query: {} });
  return { useRoute: () => route, useRouter: () => ({}), route };
});
vi.mock("vue-i18n", async (orig) => ({
  ...(await orig<typeof import("vue-i18n")>()),
  useI18n: () => ({ t: (k: string) => k }),
}));
vi.mock("@/components/header/HeaderBar.vue", () => h.empty("HeaderBar"));
vi.mock("@/components/Breadcrumbs.vue", () => h.empty("Breadcrumbs"));
vi.mock("@/views/Errors.vue", () => h.empty("Errors"));
vi.mock("@/views/files/FileListing.vue", () => h.empty("FileListing"));

import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { createPinia, setActivePinia, type Pinia } from "pinia";
import { nextTick } from "vue";
import * as router from "vue-router";
import Files from "@/views/Files.vue";
import { useLayoutStore } from "@/stores/layout";
import { useFileStore } from "@/stores/file";
import { useUploadStore } from "@/stores/upload";

const route = (router as any).route as { path: string };
const settle = () => new Promise((r) => setTimeout(r, 0));

let pinia: Pinia;
let wrapper: VueWrapper | null = null;

beforeEach(async () => {
  route.path = "/files/docs/";
  pinia = createPinia();
  setActivePinia(pinia);
  h.fetch.mockReset();
  h.showError.mockReset();
  h.fetch.mockResolvedValue({
    url: "/files/docs/",
    path: "/docs/",
    name: "docs",
    isDir: true,
    type: "",
    items: [],
  });
  wrapper = mount(Files, {
    global: {
      plugins: [pinia],
      provide: { $showError: h.showError },
    },
  });
  await flushPromises();
  // an upload batch asks about a conflict
  useUploadStore().upload(
    "/files/docs/a.pdf",
    "a.pdf",
    new File(["data"], "a.pdf"),
    false,
    "blob"
  );
  await settle();
  expect(useLayoutStore().currentPromptName).toBe("replace");
});
afterEach(() => {
  wrapper?.unmount();
  wrapper = null;
});

describe("the upload's Replace/Skip question", () => {
  it("stays open when the listing reloads", async () => {
    const fetches = h.fetch.mock.calls.length;
    useFileStore().reload = true;
    await nextTick();
    await flushPromises();
    const layout = useLayoutStore();
    console.log(
      "after a reload: prompt:",
      layout.currentPromptName ?? null,
      "| toasts:",
      h.showError.mock.calls.length
    );
    expect(h.fetch.mock.calls.length).toBe(fetches + 1); // it did reload
    expect(layout.currentPromptName).toBe("replace");
    expect(h.showError).not.toHaveBeenCalled();
  });

  it("control: is closed on a route change", async () => {
    route.path = "/files/other/";
    await nextTick();
    await flushPromises();
    expect(useLayoutStore().prompts).toEqual([]);
  });

  it("control: another prompt is still closed by a reload", async () => {
    const layout = useLayoutStore();
    layout.prompts = [];
    layout.showHover("info");
    useFileStore().reload = true;
    await nextTick();
    await flushPromises();
    expect(layout.prompts).toEqual([]);
  });
});
