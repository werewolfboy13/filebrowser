// The view mode is applied once the server stored it (switchView), with
// authStore.updateUser. An answer that arrived after the session ended
// (a logout, forced or inactivity logout meanwhile) recreated a partial
// user {id, viewMode} in the auth store: isLoggedIn turned true on the
// login page. If another user had logged in on the tab meanwhile, the
// answer replaced that user's id with the old user's. updateUser now
// ignores an update when nobody is logged in, or when it carries another
// user's id (which also covers Profile.vue's settings). Real auth store;
// the rest as in file-listing-preference-409.test.ts.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const h = vi.hoisted(() => ({
  update: vi.fn(),
  showError: vi.fn(),
  fileStore: {
    req: {
      items: [] as any[],
      numDirs: 0,
      numFiles: 0,
      sorting: { by: "name", asc: false },
    },
    selected: [] as number[],
    selectedCount: 0,
    multiple: false,
    preselect: "",
    reload: false,
  },
}));

vi.mock("@/stores/clipboard", () => ({ useClipboardStore: () => ({}) }));
vi.mock("@/stores/file", () => ({ useFileStore: () => h.fileStore }));
vi.mock("@/stores/layout", () => ({
  useLayoutStore: () => ({
    currentPrompt: null,
    showHover: vi.fn(),
    closeHovers: vi.fn(),
  }),
}));
vi.mock("@/stores/upload", () => ({ useUploadStore: () => ({}) }));
vi.mock("@/api", () => ({ users: { update: h.update }, files: {} }));
vi.mock("@/api/utils", () => ({ removePrefix: (u: string) => u }));
vi.mock("@/utils/constants", () => ({ enableExec: true }));
vi.mock("@/utils/auth", () => ({}));
vi.mock("@/router", () => ({ default: {} }));
vi.mock("@/i18n", () => ({
  default: {},
  detectLocale: () => "en",
  setLocale: vi.fn(),
}));
vi.mock("@/utils/upload", () => ({}));
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
  useRoute: () => ({ path: "/files/" }),
  onBeforeRouteUpdate: vi.fn(),
}));
vi.mock("vue-i18n", async (importOriginal) => ({
  ...(await importOriginal<typeof import("vue-i18n")>()),
  useI18n: () => ({ t: (key: string) => key }),
}));
vi.mock("pinia", async (importOriginal) => ({
  ...(await importOriginal<typeof import("pinia")>()),
  storeToRefs: () => ({ req: { value: h.fileStore.req } }),
}));
vi.mock("vue", async (importOriginal) => ({
  ...(await importOriginal<typeof import("vue")>()),
  inject: () => h.showError,
  watch: vi.fn(),
  onMounted: vi.fn(),
  onBeforeUnmount: vi.fn(),
  useSSRContext: () => ({ modules: new Set<string>() }),
}));

import { createPinia, setActivePinia } from "pinia";
import { useAuthStore } from "@/stores/auth";
import FileListing from "@/views/files/FileListing.vue";

const setup = () => (FileListing as any).setup({}, { expose: vi.fn() });
const alice = {
  id: 1,
  username: "alice",
  viewMode: "list",
  perm: { create: true },
  sorting: { by: "name", asc: false },
} as any;

describe("the view-mode answer arriving after the session ended", () => {
  beforeEach(() => {
    vi.stubGlobal("window", {
      innerWidth: 1024,
      innerHeight: 768,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    });
    vi.stubGlobal("document", { getElementsByClassName: () => [] });
    setActivePinia(createPinia());
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });

  it("logged out meanwhile: the tab stays logged out", async () => {
    const auth = useAuthStore();
    auth.user = { ...alice };
    let stored!: () => void;
    h.update.mockReturnValueOnce(new Promise<void>((r) => (stored = r)));
    const switching = setup().switchView();
    await vi.waitFor(() => expect(h.update).toHaveBeenCalled());
    auth.clearUser(); // what logout() does
    expect(auth.isLoggedIn).toBe(false);
    stored();
    await switching;
    expect(auth.user).toBe(null);
    expect(auth.isLoggedIn).toBe(false);
  });

  it("another user logged in meanwhile: that user is left as it is", async () => {
    const auth = useAuthStore();
    auth.user = { ...alice };
    let stored!: () => void;
    h.update.mockReturnValueOnce(new Promise<void>((r) => (stored = r)));
    const switching = setup().switchView();
    await vi.waitFor(() => expect(h.update).toHaveBeenCalled());
    auth.clearUser();
    const bob = { ...alice, id: 2, username: "bob", viewMode: "list" };
    auth.user = { ...bob };
    stored();
    await switching;
    expect(auth.user).toEqual(bob);
  });

  it("the same session: the stored view mode is applied", async () => {
    const auth = useAuthStore();
    auth.user = { ...alice };
    h.update.mockResolvedValueOnce(undefined);
    await setup().switchView();
    expect(h.update).toHaveBeenCalledWith({ id: 1, viewMode: "mosaic" }, [
      "viewMode",
    ]);
    expect(auth.user).toEqual({ ...alice, viewMode: "mosaic" });
  });
});
