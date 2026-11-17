// Node-style tests of FileListing.vue's event handlers (same harness
// pattern as upload-conflict-resolution.test.ts: the component's setup runs
// directly, with its stores and helpers mocked; no DOM, no network).
import { beforeEach, describe, expect, it, vi } from "vitest";
import FileListing from "@/views/files/FileListing.vue";
import { LISTING_DRAG_TYPE } from "@/utils/dragdrop";

const harness = vi.hoisted(() => ({
  routePath: "/files/docs/",
  copy: vi.fn().mockResolvedValue(undefined),
  move: vi.fn().mockResolvedValue(undefined),
  fetch: vi.fn().mockResolvedValue({ items: [] }),
  checkConflict: vi.fn().mockResolvedValue([]),
  scanFiles: vi.fn(),
  handleFiles: vi.fn(),
  showHover: vi.fn(),
  closeHovers: vi.fn(),
  clipboard: {
    key: "x",
    path: "/files/inbox/",
    items: [] as any[],
    $patch: vi.fn(),
    resetClipboard: vi.fn(),
  },
  fileStore: {
    req: { items: [] as any[], numDirs: 0, numFiles: 2 },
    selected: [] as number[],
    selectedCount: 0,
    multiple: false,
    preselect: "",
    reload: false,
  },
}));

vi.mock("@/stores/auth", () => ({
  useAuthStore: () => ({
    user: { perm: { delete: true, rename: true, create: true } },
  }),
}));
vi.mock("@/stores/clipboard", () => ({
  useClipboardStore: () => harness.clipboard,
}));
vi.mock("@/stores/file", () => ({ useFileStore: () => harness.fileStore }));
vi.mock("@/stores/layout", () => ({
  useLayoutStore: () => ({
    currentPrompt: null,
    showHover: harness.showHover,
    closeHovers: harness.closeHovers,
  }),
}));
vi.mock("@/stores/upload", () => ({
  useUploadStore: () => ({ upload: vi.fn() }),
}));
vi.mock("@/api", () => ({
  users: {},
  files: { copy: harness.copy, move: harness.move, fetch: harness.fetch },
}));
vi.mock("@/api/utils", () => ({
  // the real removePrefix, without importing the real module graph
  removePrefix(url: string) {
    url = url.split("/").splice(2).join("/");
    if (url === "") url = "/";
    if (url[0] !== "/") url = "/" + url;
    return url;
  },
}));
vi.mock("@/utils/constants", () => ({ enableExec: true }));
vi.mock("@/utils/auth", () => ({}));
vi.mock("@/router", () => ({ default: {} }));
vi.mock("@/i18n", () => ({ default: {} }));
vi.mock("@/utils/upload", () => ({
  checkConflict: harness.checkConflict,
  scanFiles: harness.scanFiles,
  handleFiles: harness.handleFiles,
}));
vi.mock("@/utils/css", () => ({ default: vi.fn() }));
vi.mock("@/utils/buttons", () => ({
  default: { loading: vi.fn(), done: vi.fn(), success: vi.fn() },
}));
vi.mock("@/components/header/HeaderBar.vue", () => ({ default: {} }));
vi.mock("@/components/header/Action.vue", () => ({ default: {} }));
vi.mock("@/components/Search.vue", () => ({ default: {} }));
vi.mock("@/components/files/ListingItem.vue", () => ({ default: {} }));
vi.mock("@/components/ContextMenu.vue", () => ({ default: {} }));
vi.mock("vue-router", () => ({
  useRoute: () => ({
    get path() {
      return harness.routePath;
    },
  }),
  onBeforeRouteUpdate: vi.fn(),
}));
vi.mock("vue-i18n", async (importOriginal) => ({
  ...(await importOriginal<typeof import("vue-i18n")>()),
  useI18n: () => ({ t: (key: string) => key }),
}));
vi.mock("pinia", async (importOriginal) => ({
  ...(await importOriginal<typeof import("pinia")>()),
  storeToRefs: () => ({ req: { value: harness.fileStore.req } }),
}));
vi.mock("vue", async (importOriginal) => ({
  ...(await importOriginal<typeof import("vue")>()),
  inject: () => vi.fn(),
  watch: vi.fn(),
  onMounted: vi.fn(),
  onBeforeUnmount: vi.fn(),
  useSSRContext: () => ({ modules: new Set<string>() }),
}));

function setup() {
  return (FileListing as any).setup({}, { expose: vi.fn() });
}

function stubBrowser() {
  vi.stubGlobal("window", {
    innerWidth: 1024,
    innerHeight: 768,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  });
  vi.stubGlobal("document", { getElementsByClassName: () => [] });
}

// the folder row a drop lands on
const folderRow = (url: string) => ({
  classList: { contains: (c: string) => c === "item" },
  dataset: { dir: "true" },
  parentElement: null,
  __vue__: { url },
});

describe("listing drop handler (uploads)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    stubBrowser();
    harness.routePath = "/files/docs/";
    harness.fileStore.req.items = [];
    harness.scanFiles.mockImplementation(async (dt: any) =>
      [...dt.files].map((file: File) => ({
        file,
        name: file.name,
        size: file.size,
        isDir: false,
      }))
    );
  });

  const dropEvent = (types: Record<string, string>, target: unknown) => ({
    preventDefault: vi.fn(),
    target,
    dataTransfer: {
      files: [new File(["x"], "holiday.jpg")],
      types: Object.keys(types),
      getData: (t: string) => types[t] ?? "",
    },
  });

  it("files dropped from the desktop onto a folder row are uploaded into that folder", async () => {
    await setup().drop(
      dropEvent({ Files: "" }, folderRow("/files/docs/Photos/"))
    );
    expect(harness.handleFiles).toHaveBeenCalledTimes(1);
    const [files, path] = harness.handleFiles.mock.calls[0];
    expect(files.map((f: any) => f.name)).toEqual(["holiday.jpg"]);
    expect(path).toBe("/files/docs/Photos/");
  });

  it("a listing row dragged with an attached file is not uploaded", async () => {
    await setup().drop(
      dropEvent(
        { [LISTING_DRAG_TYPE]: "token", Files: "" },
        folderRow("/files/docs/Photos/")
      )
    );
    expect(harness.handleFiles).not.toHaveBeenCalled();
  });
});

// A folder URL without the trailing slash is common: Sidebar "My files"
// pushes /files, the catch-all redirect turns /docs into /files/docs, and
// bookmarks or typed URLs. Paste built the destination as route.path +
// name, so at /files/docs a cut of /inbox/report.pdf was moved to
// /docsreport.pdf at the root.
describe("paste destination does not depend on a trailing slash", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    stubBrowser();
    harness.clipboard.key = "x"; // Ctrl+X earlier
    harness.clipboard.path = "/files/inbox/";
    harness.clipboard.items = [
      {
        from: "/files/inbox/report.pdf",
        name: "report.pdf",
        size: 1,
        isDir: false,
        modified: "",
      },
    ];
  });

  for (const path of ["/files/docs/", "/files/docs"]) {
    it(`pasting at ${path} moves the file into /docs/`, async () => {
      harness.routePath = path;
      await setup().paste({ target: { tagName: "BODY" } });
      const [items, overwrite, rename] = harness.move.mock.calls[0];
      console.log(`route ${path} -> item.to=${items[0].to}`);
      expect(items[0].to).toBe("/files/docs/report.pdf");
      expect(items[0].rename).toBe(false);
      expect([overwrite, rename]).toEqual([false, false]);
      expect(harness.checkConflict).toHaveBeenCalledWith(
        expect.anything(),
        "/files/docs/",
        true
      );
      expect(harness.fileStore.preselect).toBe("/docs/report.pdf");
    });
  }

  it("copying and pasting in the same folder renames, with or without the slash", async () => {
    harness.clipboard.key = "c";
    harness.clipboard.path = "/files/docs/";
    harness.clipboard.items[0].from = "/files/docs/report.pdf";
    harness.routePath = "/files/docs";
    await setup().paste({ target: { tagName: "BODY" } });
    expect(harness.copy.mock.calls[0][0][0]).toMatchObject({
      to: "/files/docs/report.pdf",
      rename: true,
    });
  });

  it("Ctrl+C at a folder URL without the slash records the folder with one", () => {
    harness.routePath = "/files/docs";
    harness.fileStore.req.items = [
      { name: "a.txt", url: "/files/docs/a.txt", isDir: false },
    ];
    harness.fileStore.selected = [0];
    setup().copyCut({ key: "c", target: { tagName: "BODY" } });
    expect(harness.clipboard.$patch).toHaveBeenCalledWith(
      expect.objectContaining({ path: "/files/docs/" })
    );
  });
});
