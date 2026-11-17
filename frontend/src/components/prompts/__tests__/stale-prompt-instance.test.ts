// @vitest-environment jsdom
//
// Prompts.vue renders the current prompt inside <keep-alive>. Without a
// per-prompt key, a prompt shown again while another prompt stays open
// underneath (the conflict dialog stacked on Copy, Move or Upload) reused
// the cached instance of the earlier one, whose setup had captured the
// earlier prompt's conflict list and confirm callback. After Cancel and a
// retry, "Overwrite all" then ran the FIRST operation: Copy/Move overwrote
// files in the destination the user had backed out of, and Upload
// overwrote with the first file selection and never uploaded the second.
//
// Real Prompts/BaseModal/ResolveConflict/Copy/Move/Upload/FileList/
// CurrentPassword and real Pinia stores; only the API, upload helpers and
// auth are mocked. Nothing touches the network.
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  (window as any).FileBrowser = {
    AuthMethod: "json",
    BaseURL: "",
    StaticURL: "/static",
    Name: "",
    DisableExternal: false,
    NoAuth: false,
    LoginPage: true,
    LogoutPage: "/login",
    EnableExec: false,
    EnableThumbs: true,
    ResizePreview: true,
    TusSettings: { chunkSize: 1, retryCount: 0 },
  };
});

const h = vi.hoisted(() => ({
  copy: vi.fn().mockResolvedValue(undefined),
  move: vi.fn().mockResolvedValue(undefined),
  checkConflict: vi.fn(),
  handleFiles: vi.fn(),
}));

vi.mock("@/utils/auth", () => ({ renew: vi.fn(), logout: vi.fn() }));
vi.mock("@/api", () => ({
  files: { copy: h.copy, move: h.move, fetch: vi.fn() },
  share: {},
  users: {},
  pub: {},
}));
vi.mock("@/utils/upload", () => ({
  checkConflict: h.checkConflict,
  handleFiles: h.handleFiles,
}));
vi.mock("@/utils/buttons", () => ({
  default: { loading: vi.fn(), done: vi.fn(), success: vi.fn() },
}));
vi.mock("vue-router", () => ({
  useRoute: () => ({ path: "/files/src/" }),
  useRouter: () => ({ push: vi.fn() }),
}));
const tfn = (k: string, p?: any) => (p ? `${k}${JSON.stringify(p)}` : k);
vi.mock("vue-i18n", async (orig) => ({
  ...(await orig<typeof import("vue-i18n")>()),
  useI18n: () => ({ t: tfn }),
}));

import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { createPinia, setActivePinia, type Pinia } from "pinia";
import { nextTick } from "vue";
import Prompts from "@/components/prompts/Prompts.vue";
import { useLayoutStore } from "@/stores/layout";
import { useFileStore } from "@/stores/file";
import { useAuthStore } from "@/stores/auth";

const conflict = (index: number, name: string): ConflictingResource => ({
  index,
  name,
  origin: { size: 1, lastModified: undefined },
  dest: { size: 2, lastModified: undefined },
  checked: ["origin"],
  isSmallerOnServer: false,
});

let pinia: Pinia;
function mountPrompts() {
  return mount(Prompts, {
    attachTo: document.body,
    global: {
      plugins: [pinia],
      mocks: {
        $t: tfn,
        $route: { path: "/files/src/" },
        $router: { push: vi.fn() },
      },
      provide: { $showError: vi.fn(), $showSuccess: vi.fn() },
    },
  });
}

const buttonByText = (w: VueWrapper, text: string) => {
  const b = w.findAll("button").find((b) => b.text().includes(text));
  if (!b) throw new Error(`no button "${text}" in: ${w.text()}`);
  return b;
};

function listing() {
  const fileStore = useFileStore();
  useAuthStore().user = {
    id: 1,
    perm: { create: true, rename: true },
    singleClick: false,
    redirectAfterCopyMove: false,
  } as any;
  fileStore.req = {
    url: "/files/src/",
    isDir: true,
    items: [
      {
        name: "report.txt",
        url: "/files/src/report.txt",
        isDir: false,
        size: 1,
        modified: "",
      },
      { name: "old", url: "/files/src/old/", isDir: true, size: 0 },
      { name: "new", url: "/files/src/new/", isDir: true, size: 0 },
    ],
  } as any;
  fileStore.selected = [0];
  // every destination reports report.txt as already present
  h.checkConflict.mockImplementation(async (_items: any[], dest: string) => [
    conflict(0, `${dest}report.txt`),
  ]);
}

describe("prompt instances are not reused across prompts", () => {
  beforeEach(() => {
    document.body.innerHTML = "";
    pinia = createPinia();
    setActivePinia(pinia);
    vi.clearAllMocks();
  });

  it("a second conflict dialog stacked on another prompt shows and confirms its own conflicts", async () => {
    const layout = useLayoutStore();
    const w = mountPrompts();

    layout.showHover("help"); // any prompt that stays open underneath
    const confirmA = vi.fn();
    layout.showHover({
      prompt: "resolve-conflict",
      props: { conflict: [conflict(0, "/A/one.txt")] },
      confirm: confirmA,
    });
    await nextTick();
    expect(w.text()).toContain('"count":1');

    await buttonByText(w, "buttons.cancel").trigger("click");
    await nextTick();
    expect(layout.currentPromptName).toBe("help");

    const confirmB = vi.fn();
    layout.showHover({
      prompt: "resolve-conflict",
      props: {
        conflict: [conflict(0, "/B/one.txt"), conflict(1, "/B/two.txt")],
      },
      confirm: confirmB,
    });
    await nextTick();
    const rendered = w.text();
    await buttonByText(w, "buttons.overrideAll").trigger("click");

    console.log(
      "rendered:",
      rendered.match(/"count":\d+/)?.[0],
      "| confirmA calls:",
      confirmA.mock.calls.length,
      "| confirmB calls:",
      confirmB.mock.calls.length
    );
    expect(rendered).toContain('"count":2');
    expect(confirmA).not.toHaveBeenCalled();
    expect(confirmB).toHaveBeenCalledTimes(1);
    expect(confirmB.mock.calls[0][1].map((c: any) => c.name)).toEqual([
      "/B/one.txt",
      "/B/two.txt",
    ]);
  });

  for (const op of ["copy", "move"] as const) {
    it(`${op}: Overwrite all after Cancel and a new destination writes to the new destination`, async () => {
      listing();
      const layout = useLayoutStore();
      const w = mountPrompts();
      layout.showHover(op);
      await nextTick();

      // 1st attempt: "old" -> conflict -> Cancel
      await w.find('li[data-url="/files/src/old/"]').trigger("click");
      await buttonByText(w, `buttons.${op}`).trigger("click");
      await flushPromises();
      expect(layout.currentPromptName).toBe("resolve-conflict");
      await buttonByText(w, "buttons.cancel").trigger("click");
      await nextTick();
      expect(layout.currentPromptName).toBe(op);

      // 2nd attempt: "new" instead -> conflict -> Overwrite all
      await w.find('li[data-url="/files/src/old/"]').trigger("click"); // unselect
      await w.find('li[data-url="/files/src/new/"]').trigger("click");
      await buttonByText(w, `buttons.${op}`).trigger("click");
      await flushPromises();
      expect(h.checkConflict).toHaveBeenLastCalledWith(
        expect.anything(),
        "/files/src/new/",
        true
      );
      await buttonByText(w, "buttons.overrideAll").trigger("click");
      await flushPromises();

      const api = op === "copy" ? h.copy : h.move;
      expect(api).toHaveBeenCalledTimes(1);
      const [items] = api.mock.calls[0];
      console.log(
        `api.${op} items:`,
        JSON.stringify(items.map((i: any) => [i.to, i.overwrite]))
      );
      expect(items).toHaveLength(1);
      expect(items[0].to).toBe("/files/src/new/report.txt");
      expect(items[0].overwrite).toBe(true);
    });
  }

  it("upload: Overwrite all after Cancel and a new file selection uploads the new selection", async () => {
    listing();
    const layout = useLayoutStore();
    const w = mountPrompts();

    // Upload.vue opens a file picker with input.click(); play the user
    const pick = async (names: string[]) => {
      const click = vi
        .spyOn(HTMLInputElement.prototype, "click")
        .mockImplementationOnce(function (this: HTMLInputElement) {
          Object.defineProperty(this, "files", {
            value: names.map((n) => new File(["x"], n)),
          });
          this.dispatchEvent(new Event("change"));
        });
      await w.find(".card-action .action").trigger("click"); // "File"
      await flushPromises();
      click.mockRestore();
    };

    layout.showHover("upload");
    await nextTick();

    await pick(["a.txt"]);
    expect(layout.currentPromptName).toBe("resolve-conflict");
    await buttonByText(w, "buttons.cancel").trigger("click");
    await nextTick();
    expect(layout.currentPromptName).toBe("upload");

    await pick(["b.txt"]);
    expect(layout.currentPromptName).toBe("resolve-conflict");
    await buttonByText(w, "buttons.overrideAll").trigger("click");
    await flushPromises();

    const uploaded = h.handleFiles.mock.calls.map(([files, path]) => [
      files.map((f: any) => [f.name, !!f.overwrite]),
      path,
    ]);
    console.log("handleFiles calls:", JSON.stringify(uploaded));
    expect(uploaded).toEqual([[[["b.txt", true]], "/files/src/"]]);
  });

  it("current-password: a second prompt starts empty and confirms its own action", async () => {
    const layout = useLayoutStore();
    const w = mountPrompts();

    layout.showHover("help");
    const confirmA = vi.fn();
    layout.showHover({ prompt: "current-password", confirm: confirmA });
    await nextTick();
    await w.find('input[type="password"]').setValue("typed-for-A");
    await buttonByText(w, "buttons.cancel").trigger("click");
    await nextTick();

    const confirmB = vi.fn();
    layout.showHover({ prompt: "current-password", confirm: confirmB });
    await nextTick();
    const field = (w.find('input[type="password"]').element as HTMLInputElement)
      .value;
    await buttonByText(w, "buttons.ok").trigger("click");

    console.log(
      "field on 2nd prompt:",
      JSON.stringify(field),
      "| confirmA:",
      confirmA.mock.calls.length,
      "| confirmB:",
      confirmB.mock.calls.length
    );
    expect(field).toBe("");
    expect(confirmA).not.toHaveBeenCalled();
    expect(confirmB).toHaveBeenCalledTimes(1);
  });

  it("control: Copy keeps its chosen destination while the conflict dialog is on top", async () => {
    listing();
    const layout = useLayoutStore();
    const w = mountPrompts();
    layout.showHover("copy");
    await nextTick();
    await w.find('li[data-url="/files/src/new/"]').trigger("click");
    await buttonByText(w, "buttons.copy").trigger("click");
    await flushPromises();
    await buttonByText(w, "buttons.cancel").trigger("click");
    await nextTick();
    // the same Copy instance is back, with "new" still selected
    expect(
      w.find('li[data-url="/files/src/new/"]').attributes("aria-selected")
    ).toBe("true");
  });
});
