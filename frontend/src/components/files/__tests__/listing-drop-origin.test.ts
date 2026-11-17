// @vitest-environment jsdom
//
// Dropping files from the desktop onto a folder row (to upload them into
// that folder) also moved whatever was selected in the listing into that
// folder: ListingItem's drop handler never checked that the drag started on
// a listing row of this page. Selection is often automatic (the last
// uploaded file, the folder you came back from), so files moved away
// without any prompt. Only a drag started on a row of this tab may move or
// copy the selection; anything else is left to the upload handler.
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  (window as any).FileBrowser = {
    AuthMethod: "json",
    BaseURL: "",
    StaticURL: "/static",
    Name: "",
    EnableThumbs: false,
    EnableExec: false,
    LoginPage: true,
    LogoutPage: "/login",
    TusSettings: { chunkSize: 1, retryCount: 0 },
  };
});

const h = vi.hoisted(() => ({
  move: vi.fn().mockResolvedValue(undefined),
  copy: vi.fn().mockResolvedValue(undefined),
  checkConflict: vi.fn().mockResolvedValue([]),
}));

vi.mock("@/utils/auth", () => ({ renew: vi.fn(), logout: vi.fn() }));
vi.mock("@/api", () => ({
  files: { move: h.move, copy: h.copy, getPreviewURL: () => "" },
}));
vi.mock("@/utils/upload", () => ({ checkConflict: h.checkConflict }));
vi.mock("vue-router", () => ({ useRouter: () => ({ push: vi.fn() }) }));

import { flushPromises, mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import dayjs from "dayjs";
import relativeTime from "dayjs/plugin/relativeTime";
import ListingItem from "@/components/files/ListingItem.vue";
import { useAuthStore } from "@/stores/auth";
import { useFileStore } from "@/stores/file";
import { LISTING_DRAG_TYPE } from "@/utils/dragdrop";
dayjs.extend(relativeTime); // registered globally in src/main.ts

// same global mixin as src/main.ts, which drop() relies on (el.__vue__.url)
const exposeVue = {
  mounted(this: any) {
    this.$el.__vue__ = this;
  },
};

// jsdom has no DataTransfer; this mimics what the handlers can touch.
function fakeDataTransfer(data: Record<string, string> = {}) {
  return {
    files: [] as File[],
    get types() {
      return Object.keys(data);
    },
    setData(type: string, value: string) {
      data[type] = value;
    },
    getData(type: string) {
      return data[type] ?? "";
    },
  };
}

function fire(el: Element, type: string, dataTransfer: any, init = {}) {
  const ev = new Event(type, { bubbles: true, cancelable: true });
  Object.defineProperty(ev, "dataTransfer", { value: dataTransfer });
  Object.assign(ev, init);
  el.dispatchEvent(ev);
  return ev;
}

function setup() {
  const fileStore = useFileStore();
  useAuthStore().user = {
    perm: { rename: true },
    singleClick: false,
  } as any;
  fileStore.req = {
    url: "/files/",
    isDir: true,
    items: [
      {
        index: 0,
        name: "contract.pdf",
        url: "/files/contract.pdf",
        isDir: false,
        size: 1,
        modified: "",
      },
      {
        index: 1,
        name: "Uploads",
        url: "/files/Uploads/",
        isDir: true,
        size: 0,
        modified: "",
      },
    ],
  } as any;
  fileStore.selected = [0]; // contract.pdf is selected (e.g. preselected)

  const global = { mixins: [exposeVue], provide: { $showError: vi.fn() } };
  const props = (i: number) => {
    const item = (fileStore.req as any).items[i];
    return {
      index: i,
      name: item.name,
      isDir: item.isDir,
      url: item.url,
      type: "",
      size: item.size,
      modified: "",
      path: item.url.replace(/^\/files/, ""),
    };
  };
  const source = mount(ListingItem, { props: props(0), global });
  const target = mount(ListingItem, { props: props(1), global });
  return { source, target };
}

describe("dropping onto a folder row", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    vi.clearAllMocks();
  });

  it("an OS file drop does not move the current selection", async () => {
    const { target } = setup();
    const dt = fakeDataTransfer({ Files: "" });
    dt.files = [new File(["x"], "holiday.jpg")];
    const ev = fire(target.element, "drop", dt);
    await flushPromises();

    console.log("api.move calls:", JSON.stringify(h.move.mock.calls));
    expect(h.move).not.toHaveBeenCalled();
    expect(h.copy).not.toHaveBeenCalled();
    // the row still swallows the drop, so a user without create permission
    // (no document-level upload handler) does not navigate to the file
    expect(ev.defaultPrevented).toBe(true);
  });

  it("a drag from a listing row in another tab does not move this tab's selection", async () => {
    const { target } = setup();
    const dt = fakeDataTransfer({ [LISTING_DRAG_TYPE]: "token-of-other-tab" });
    fire(target.element, "drop", dt);
    await flushPromises();
    expect(h.move).not.toHaveBeenCalled();
    expect(h.copy).not.toHaveBeenCalled();
  });

  it("a drag that ended without a drop does not arm a later drop", async () => {
    const { source, target } = setup();
    const dt = fakeDataTransfer();
    fire(source.element, "dragstart", dt);
    fire(source.element, "dragend", dt);
    fire(target.element, "drop", dt);
    await flushPromises();
    expect(h.move).not.toHaveBeenCalled();
  });

  it("control: dragging a row onto a folder row moves the selection", async () => {
    const { source, target } = setup();
    const dt = fakeDataTransfer();
    fire(source.element, "dragstart", dt);
    expect(dt.types).toContain(LISTING_DRAG_TYPE);
    fire(target.element, "drop", dt);
    await flushPromises();
    expect(h.copy).not.toHaveBeenCalled();
    expect(h.move).toHaveBeenCalledTimes(1);
    expect(h.move.mock.calls[0][0][0]).toMatchObject({
      from: "/files/contract.pdf",
      to: "/files/Uploads/contract.pdf",
    });
  });

  it("control: Ctrl+dragging a row onto a folder row copies the selection", async () => {
    const { source, target } = setup();
    const dt = fakeDataTransfer();
    fire(source.element, "dragstart", dt);
    fire(target.element, "drop", dt, { ctrlKey: true });
    await flushPromises();
    expect(h.move).not.toHaveBeenCalled();
    expect(h.copy).toHaveBeenCalledTimes(1);
    expect(h.copy.mock.calls[0][0][0]).toMatchObject({
      from: "/files/contract.pdf",
      to: "/files/Uploads/contract.pdf",
    });
  });
});
