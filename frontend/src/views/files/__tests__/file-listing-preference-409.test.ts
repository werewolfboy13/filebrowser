// The listing's own-account preference changes (view mode, sorting)
// answered 409 by PUT /api/users/{id} (the account was revoked between
// authentication and the handler's read) showed the raw "409 Conflict"
// with a "Report issue" link, and the view mode was switched in the page
// although it was not stored. They now say the account was changed at the
// same time, and the view mode is switched only once it is stored.
// FileListing's setup runs directly with its stores and helpers mocked
// (harness from file-listing-handlers.test.ts).
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const h = vi.hoisted(() => {
  class Status409 extends Error {
    status = 409;
  }
  return {
    error: new Status409("409 Conflict"),
    update: vi.fn(),
    showError: vi.fn(),
    updateUser: vi.fn(),
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
  };
});

vi.mock("@/stores/auth", () => ({
  useAuthStore: () => ({
    user: {
      id: 1,
      viewMode: "list",
      perm: {},
      sorting: { by: "name", asc: false },
    },
    updateUser: h.updateUser,
  }),
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
vi.mock("@/i18n", () => ({ default: {} }));
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

import FileListing from "@/views/files/FileListing.vue";

const setup = () => (FileListing as any).setup({}, { expose: vi.fn() });

describe("listing preferences answered 409", () => {
  beforeEach(() => {
    vi.stubGlobal("window", {
      innerWidth: 1024,
      innerHeight: 768,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    });
    vi.stubGlobal("document", { getElementsByClassName: () => [] });
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.clearAllMocks();
    h.fileStore.reload = false;
  });

  it("view mode: says the account changed, and the page keeps its mode", async () => {
    h.update.mockRejectedValueOnce(h.error);
    await setup().switchView();
    expect(h.showError).toHaveBeenCalledWith(
      "errors.accountChangedConcurrently"
    );
    expect(h.updateUser).not.toHaveBeenCalled();
  });

  it("view mode: switched in the page only once it is stored", async () => {
    let stored!: () => void;
    h.update.mockReturnValueOnce(new Promise<void>((r) => (stored = r)));
    const switching = setup().switchView();
    await vi.waitFor(() => expect(h.update).toHaveBeenCalled());
    expect(h.updateUser).not.toHaveBeenCalled();
    stored();
    await switching;
    expect(h.updateUser).toHaveBeenCalledWith({ id: 1, viewMode: "mosaic" });
    expect(h.showError).not.toHaveBeenCalled();
  });

  it("sorting: says the account changed, then the listing reloads", async () => {
    h.update.mockRejectedValueOnce(h.error);
    await setup().sort("name");
    expect(h.showError).toHaveBeenCalledWith(
      "errors.accountChangedConcurrently"
    );
    expect(h.fileStore.reload).toBe(true);
  });
});
