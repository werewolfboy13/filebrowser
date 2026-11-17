// @vitest-environment jsdom
//
// BaseModal gives the element with id "focus-prompt" the default focus when
// a dialog opens. It used to search the whole document, so an element with
// that id elsewhere on the page (e.g. in a rendered markdown preview) took
// the focus instead of the dialog's button, and Enter activated it.
import { describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  (window as any).FileBrowser = {
    AuthMethod: "json",
    BaseURL: "",
    StaticURL: "/static",
    Name: "",
    LoginPage: true,
    LogoutPage: "/login",
    TusSettings: { chunkSize: 1, retryCount: 0 },
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

import { mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { nextTick } from "vue";
import Prompts from "@/components/prompts/Prompts.vue";
import { useLayoutStore } from "@/stores/layout";

describe("dialog default focus", () => {
  it("goes to the dialog's own #focus-prompt, not one elsewhere on the page", async () => {
    document.body.innerHTML =
      '<div id="preview"><a id="focus-prompt" href="https://attacker.example/">x</a></div>';
    const pinia = createPinia();
    setActivePinia(pinia);
    mount(Prompts, {
      attachTo: document.body,
      global: { plugins: [pinia], mocks: { $t: (k: string) => k } },
    });

    useLayoutStore().showHover({
      prompt: "discardEditorChanges",
      confirm: vi.fn(),
      saveAction: vi.fn(),
    });
    await nextTick();

    const focused = document.activeElement as HTMLElement;
    console.log("focused:", focused.outerHTML.slice(0, 80));
    expect(focused.closest("#preview")).toBeNull();
    expect(focused.closest("#modal-background")).not.toBeNull();
  });
});
