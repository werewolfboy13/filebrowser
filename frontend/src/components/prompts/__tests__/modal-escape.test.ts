// @vitest-environment jsdom
//
// Every dialog (BaseModal) added a window keydown listener at setup that
// swallows Escape (stopImmediatePropagation) and was never removed. After
// the first dialog, Escape no longer reached window listeners registered
// later (closing the editor or the preview, clearing the selection), and
// the leaked listeners emitted on unmounted dialogs, so later dialogs did
// not close on Escape either.
import { beforeEach, describe, expect, it, vi } from "vitest";

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

const escape = () =>
  document.body.dispatchEvent(
    new KeyboardEvent("keydown", { key: "Escape", bubbles: true })
  );

const listeners: Array<(e: KeyboardEvent) => void> = [];
const onPageEscape = (fn: () => void) => {
  const l = (e: KeyboardEvent) => e.key === "Escape" && fn();
  listeners.push(l);
  window.addEventListener("keydown", l);
};

function setup() {
  const pinia = createPinia();
  setActivePinia(pinia);
  const layout = useLayoutStore();
  const w = mount(Prompts, {
    attachTo: document.body,
    global: {
      plugins: [pinia],
      mocks: { $t: (k: string) => k },
      provide: { $showError: vi.fn() },
    },
  });
  return { layout, w };
}

describe("Escape and dialogs", () => {
  beforeEach(() => {
    document.body.innerHTML = "";
    listeners
      .splice(0)
      .forEach((l) => window.removeEventListener("keydown", l));
  });

  it("still closes a dialog, and still reaches the page, after an earlier dialog was closed", async () => {
    const { layout, w } = setup();
    layout.showHover("help");
    await nextTick();
    escape();
    await nextTick();
    expect(layout.prompts.length).toBe(0);

    // a view mounted later registers its own Escape handler (like Editor.vue)
    const laterView = vi.fn();
    onPageEscape(laterView);
    escape();

    layout.showHover("help");
    await nextTick();
    escape();
    await nextTick();

    console.log(
      "later view got Escape:",
      laterView.mock.calls.length,
      "| prompts open after Escape:",
      layout.prompts.length
    );
    expect(laterView).toHaveBeenCalledTimes(1); // only the one with no dialog
    expect(layout.prompts.length).toBe(0);
    w.unmount();
  });

  it("with a dialog open, Escape closes the dialog only, not the page under it", async () => {
    const { layout, w } = setup();
    const page = vi.fn(); // e.g. the editor, registered before the dialog
    onPageEscape(page);
    layout.showHover("help");
    layout.showHover("help");
    await nextTick();

    escape();
    await nextTick();
    expect(layout.prompts.length).toBe(1); // one dialog per Escape
    expect(page).not.toHaveBeenCalled();
    w.unmount();
  });
});
