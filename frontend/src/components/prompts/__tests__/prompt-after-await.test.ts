// @vitest-environment jsdom
//
// New folder, New file, Rename and Delete (from the file preview) wait for
// their request, then confirmed and closed the prompt on top of the stack.
// An upload batch can push its Replace/Skip question on top meanwhile (a
// conflict reported by the server): New folder and Delete then ran the
// question's Replace, which sends the conflicting file again with
// override, an overwrite nobody chose; all four closed the question
// without an answer, which skips every file still to be asked about.
// Each dialog now confirms only its own prompt, and only while it is the
// one shown, and closes only its own prompt.
//
// Real Prompts.vue (with the real dialogs), layout, file and upload
// stores; the API, router and button helpers are mocked. No network.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  (window as any).FileBrowser = {
    AuthMethod: "json",
    BaseURL: "",
    StaticURL: "/static",
    TusSettings: { chunkSize: 1024, retryCount: 0 },
  };
  // jsdom has no layout (CreateFilePath scrolls its path into view)
  Element.prototype.scrollIntoView = () => {};
});

const h = vi.hoisted(() => ({
  // requests of the dialog under test, answered by the test
  pending: [] as Array<() => void>,
  // uploads sent: [path, overwrite]
  uploads: [] as Array<[string, boolean]>,
  fetch: vi.fn(),
  showError: vi.fn(),
  push: vi.fn(),
}));
const deferred = () =>
  new Promise<void>((resolve) => h.pending.push(() => resolve()));

vi.mock("@/api", () => ({
  files: {
    // an upload (from the upload store) carries the file; anything else
    // is the dialog's own request
    post: (url: string, content?: unknown, overwrite = false) => {
      if (!(content instanceof File)) return deferred();
      h.uploads.push([url, overwrite]);
      if (overwrite) return Promise.resolve();
      return Promise.reject(
        Object.assign(new Error("409 Conflict\n"), {
          status: 409,
          method: "POST",
        })
      );
    },
    remove: () => deferred(),
    move: () => deferred(),
    fetch: h.fetch,
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
vi.mock("vue-router", () => ({
  useRoute: () => ({ path: "/files/docs/" }),
  useRouter: () => ({ push: h.push }),
}));
const tfn = (k: string, p?: any) => (p ? `${k}${JSON.stringify(p)}` : k);
vi.mock("vue-i18n", async (orig) => ({
  ...(await orig<typeof import("vue-i18n")>()),
  useI18n: () => ({ t: tfn }),
}));

import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { createPinia, setActivePinia, type Pinia } from "pinia";
import Prompts from "@/components/prompts/Prompts.vue";
import { useLayoutStore } from "@/stores/layout";
import { useFileStore } from "@/stores/file";
import { useUploadStore } from "@/stores/upload";

const settle = () => new Promise((r) => setTimeout(r, 0));

let pinia: Pinia;
let wrapper: VueWrapper | null = null;

function mountPrompts() {
  wrapper = mount(Prompts, {
    attachTo: document.body,
    global: {
      plugins: [pinia],
      mocks: {
        $t: tfn,
        $route: { path: "/files/docs/" },
        $router: { push: h.push },
      },
      provide: { $showError: h.showError, $showSuccess: vi.fn() },
    },
  });
}

const button = (text: string) => {
  const b = wrapper!.findAll("button").find((b) => b.text() === text);
  if (!b) throw new Error(`no button "${text}" in: ${wrapper!.text()}`);
  return b;
};

// the listing of /files/docs/, with one file selected
function listing() {
  const files = useFileStore();
  files.isFiles = true;
  files.req = {
    url: "/files/docs/",
    path: "/docs/",
    name: "docs",
    isDir: true,
    items: [
      {
        name: "old.txt",
        url: "/files/docs/old.txt",
        path: "/docs/old.txt",
        isDir: false,
        size: 1,
        index: 0,
      },
    ],
  } as any;
  files.selected = [0];
}

// the file preview of /files/docs/old.txt
function preview() {
  const files = useFileStore();
  files.isFiles = true;
  files.req = {
    url: "/files/docs/old.txt",
    path: "/docs/old.txt",
    name: "old.txt",
    isDir: false,
  } as any;
}

// An upload batch reports a conflict: its question goes on top.
async function uploadConflict() {
  const layout = useLayoutStore();
  const below = layout.currentPrompt;
  useUploadStore().upload(
    "/files/docs/a.pdf",
    "a.pdf",
    new File(["data"], "a.pdf"),
    false,
    "blob"
  );
  await settle();
  await flushPromises();
  expect(layout.currentPromptName).toBe("replace");
  expect(layout.prompts[layout.prompts.length - 2]).toBe(below);
}

// the dialog's pending request is answered
async function answer() {
  expect(h.pending).toHaveLength(1);
  h.pending.shift()!();
  await settle();
  await flushPromises();
}

function expectQuestionUntouched(label: string) {
  const layout = useLayoutStore();
  console.log(
    `${label}: prompts left:`,
    JSON.stringify(layout.prompts.map((p) => p.prompt)),
    "| uploads sent [path, overwrite]:",
    JSON.stringify(h.uploads),
    "| toasts:",
    h.showError.mock.calls.length
  );
  // neither confirmed (no upload with overwrite) nor closed (no toast
  // saying the remaining files were skipped)...
  expect(h.uploads).toEqual([["/files/docs/a.pdf", false]]);
  expect(h.showError).not.toHaveBeenCalled();
  // ...it is still asked, and the dialog that finished is gone
  expect(layout.prompts.map((p) => p.prompt)).toEqual(["replace"]);
}

beforeEach(() => {
  pinia = createPinia();
  setActivePinia(pinia);
  h.pending.length = 0;
  h.uploads.length = 0;
  h.fetch.mockReset();
  h.showError.mockReset();
  h.push.mockReset();
});
afterEach(() => {
  wrapper?.unmount();
  wrapper = null;
  document.body.innerHTML = "";
});

describe("New folder", () => {
  async function create(confirm: any = undefined) {
    listing();
    h.fetch.mockResolvedValue({
      url: "/files/docs/",
      path: "/docs/",
      name: "docs",
      isDir: true,
      items: [{ name: "new", isDir: true, size: 0 }],
    });
    mountPrompts();
    useLayoutStore().showHover(
      confirm ? { prompt: "newDir", confirm } : "newDir"
    );
    await flushPromises();
    await wrapper!.find("input").setValue("new");
    await button("buttons.create").trigger("click");
  }

  it("does not confirm or close the upload question shown meanwhile", async () => {
    await create();
    await uploadConflict();
    await answer();
    expectQuestionUntouched("new folder");
    // the folder was still created and the listing updated
    expect(h.fetch).toHaveBeenCalledWith("/files/docs/");
    expect(useFileStore().req?.items.map((i) => i.name)).toEqual(["new"]);
  });

  it("its own confirm does not run while the question is on top", async () => {
    const confirm = vi.fn();
    await create(confirm);
    await uploadConflict();
    await answer();
    expectQuestionUntouched("new folder with a confirm");
    expect(confirm).not.toHaveBeenCalled();
  });

  it("control: without the question, it confirms its own prompt and closes", async () => {
    const confirm = vi.fn();
    await create(confirm);
    await answer();
    expect(confirm).toHaveBeenCalledWith("/files/docs/new/");
    expect(useLayoutStore().prompts).toEqual([]);
  });
});

describe("Delete, from the file preview", () => {
  async function remove(confirm: any) {
    preview();
    mountPrompts();
    useLayoutStore().showHover({ prompt: "delete", confirm });
    await flushPromises();
    await button("buttons.delete").trigger("click");
  }

  it("does not confirm or close the upload question shown meanwhile", async () => {
    const confirm = vi.fn();
    await remove(confirm);
    await uploadConflict();
    await answer();
    expectQuestionUntouched("delete");
    expect(confirm).not.toHaveBeenCalled();
  });

  it("control: without the question, it confirms its own prompt and closes", async () => {
    const confirm = vi.fn();
    await remove(confirm);
    await answer();
    expect(confirm).toHaveBeenCalledTimes(1);
    expect(useLayoutStore().prompts).toEqual([]);
  });
});

describe("New file", () => {
  it("does not close the upload question shown meanwhile", async () => {
    listing();
    mountPrompts();
    useLayoutStore().showHover("newFile");
    await flushPromises();
    await wrapper!.find("input").setValue("new.txt");
    await button("buttons.create").trigger("click");
    await uploadConflict();
    await answer();
    expectQuestionUntouched("new file");
    expect(h.push).toHaveBeenCalledWith({ path: "/files/docs/new.txt" });
  });
});

describe("Rename, in the listing", () => {
  it("does not close the upload question shown meanwhile", async () => {
    listing();
    mountPrompts();
    useLayoutStore().showHover("rename");
    await flushPromises();
    await wrapper!.find("input").setValue("renamed.txt");
    await button("buttons.rename").trigger("click");
    await uploadConflict();
    await answer();
    expectQuestionUntouched("rename");
    expect(useFileStore().preselect).toBe("/docs/renamed.txt");
    expect(useFileStore().reload).toBe(true);
  });

  it("control: without the question, it closes", async () => {
    listing();
    mountPrompts();
    useLayoutStore().showHover("rename");
    await flushPromises();
    await wrapper!.find("input").setValue("renamed.txt");
    await button("buttons.rename").trigger("click");
    await answer();
    expect(useLayoutStore().prompts).toEqual([]);
  });
});
