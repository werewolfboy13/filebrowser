// @vitest-environment jsdom
//
// The dialogs that can destroy something (overwrite a file, delete, discard
// changes) give the default focus to the safe answer (Skip, Cancel). Their
// tabindex values put the destructive button first in the tab order:
// Shift+Tab from Skip, or the first Tab when the focus stayed elsewhere
// (e.g. in a text field the user is typing in), landed on Replace or Delete.
// The safe answer now comes first.
//
// jsdom does not move the focus on Tab, so the order is computed as the
// browser does: positive tabindex values ascending, then document order
// (https://html.spec.whatwg.org/#sequential-focus-navigation).
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  (window as any).FileBrowser = {
    AuthMethod: "json",
    BaseURL: "",
    StaticURL: "/static",
    TusSettings: { chunkSize: 1024, retryCount: 0 },
  };
});
vi.mock("@/utils/auth", () => ({ renew: vi.fn(), logout: vi.fn() }));
vi.mock("@/api", () => ({ files: {}, share: {}, users: {}, pub: {} }));
vi.mock("@/utils/upload", () => ({
  checkConflict: vi.fn(),
  handleFiles: vi.fn(),
}));
vi.mock("vue-i18n", async (orig) => ({
  ...(await orig<typeof import("vue-i18n")>()),
  useI18n: () => ({ t: (k: string) => k }),
}));

import { mount, type VueWrapper } from "@vue/test-utils";
import { createPinia, setActivePinia, type Pinia } from "pinia";
import { nextTick } from "vue";
import Prompts from "@/components/prompts/Prompts.vue";
import { useLayoutStore } from "@/stores/layout";
import { useFileStore } from "@/stores/file";

let pinia: Pinia;
let wrapper: VueWrapper | null = null;
beforeEach(() => {
  pinia = createPinia();
  setActivePinia(pinia);
});
afterEach(() => {
  wrapper?.unmount();
  wrapper = null;
  document.body.innerHTML = "";
});

// the dialog's buttons, in the order Tab reaches them
function tabOrder(): string[] {
  const buttons = [
    ...document.querySelectorAll<HTMLButtonElement>("#modal-background button"),
  ].filter((b) => b.tabIndex >= 0);
  const positive = buttons
    .filter((b) => b.tabIndex > 0)
    .sort((a, b) => a.tabIndex - b.tabIndex); // stable: document order
  const rest = buttons.filter((b) => b.tabIndex === 0);
  return [...positive, ...rest].map((b) => b.textContent!.trim());
}

async function show(prompt: PopupProps | string) {
  wrapper = mount(Prompts, {
    attachTo: document.body,
    global: {
      plugins: [pinia],
      mocks: { $t: (k: string) => k, $route: { path: "/files/a.txt" } },
      provide: { $showError: vi.fn() },
    },
  });
  useLayoutStore().showHover(prompt as any);
  await nextTick();
  const order = tabOrder();
  console.log(
    `${typeof prompt === "string" ? prompt : prompt.prompt}:`,
    JSON.stringify(order),
    "| focused:",
    document.activeElement?.textContent?.trim()
  );
  return order;
}

describe("the safe answer comes first in the tab order", () => {
  it("Replace/Skip: Skip before Replace", async () => {
    const order = await show({ prompt: "replace", confirm: vi.fn() } as any);
    expect(order).toEqual(["prompts.skip", "buttons.replace"]);
  });

  it("Delete: Cancel before Delete", async () => {
    useFileStore().req = { isDir: false, name: "a.txt" } as any;
    const order = await show({ prompt: "delete", confirm: vi.fn() } as any);
    expect(order).toEqual(["buttons.cancel", "buttons.delete"]);
  });

  it("delete a share: Cancel before Delete", async () => {
    const order = await show({
      prompt: "share-delete",
      confirm: vi.fn(),
    } as any);
    expect(order).toEqual(["buttons.cancel", "buttons.delete"]);
  });

  it("discard editor changes: Cancel, then Save, then Discard", async () => {
    const order = await show({
      prompt: "discardEditorChanges",
      confirm: vi.fn(),
      saveAction: vi.fn(),
    } as any);
    expect(order).toEqual([
      "buttons.cancel",
      "buttons.saveChanges",
      "buttons.discardChanges",
    ]);
  });
});
