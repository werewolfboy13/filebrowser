// @vitest-environment jsdom
//
// A route change from one text file straight to another (a pick from the
// back button's history menu, history.go(-2), two quick Backs that beat the
// listing fetch) kept the same Editor: Files.vue rendered it without a key.
// The page showed a blank editor under the new file's name, and Save (or
// Ctrl+S) wrote the previous file's text into the new file, with the success
// tick. The editor's unsaved-text state (the restore question, keptPending,
// what a forced logout keeps) went to the wrong file as well. Now each file
// gets an Editor of its own, and an Editor only ever saves and keeps the
// file it was opened for.
//
// Real Files.vue, Editor, Prompts, router (memory history), api/files.ts and
// logout; only fetch (the server's answers) and the toolbar animation are
// mocked.
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  (window as any).FileBrowser = {
    AuthMethod: "json",
    BaseURL: "",
    StaticURL: "/static",
    Name: "",
    DisableExternal: true,
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

vi.mock("@/router", async () => {
  const { createRouter, createMemoryHistory } = await import("vue-router");
  const { h } = await import("vue");
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      {
        path: "/files/:path*",
        // lazily: Files.vue imports this module in turn (through
        // utils/auth), which must get this router, the one the test drives
        component: () => import("@/views/Files.vue"),
        meta: { requiresAuth: true },
      },
      { path: "/login", component: { render: () => h("p", "login") } },
    ],
  });
  return { default: router, router };
});
vi.mock("@/utils/buttons", () => ({
  default: { loading: vi.fn(), success: vi.fn(), done: vi.fn() },
}));
vi.mock("@/views/files/Preview.vue", async () => {
  const { h } = await import("vue");
  return { default: { render: () => h("p", { id: "preview" }, "preview") } };
});
vi.mock("@/views/files/FileListing.vue", async () => {
  const { h } = await import("vue");
  return { default: { render: () => h("p", { id: "listing" }, "listing") } };
});

import { mount, type VueWrapper } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { h } from "vue";
import { RouterView } from "vue-router";
import ace from "ace-builds";
import router from "@/router";
import i18n from "@/i18n";
import Prompts from "@/components/prompts/Prompts.vue";
import buttons from "@/utils/buttons";
import { logout, parseToken } from "@/utils/auth";
import { useAuthStore } from "@/stores/auth";
import { useFileStore } from "@/stores/file";
import { useLayoutStore } from "@/stores/layout";

const b64 = (o: object) =>
  btoa(JSON.stringify(o))
    .replace(/=+$/, "")
    .replace(/\+/g, "-")
    .replace(/\//g, "_");
const token = `${b64({ alg: "HS256" })}.${b64({
  user: {
    id: 1,
    username: "alice",
    locale: "en",
    perm: { modify: true },
    aceEditorTheme: "",
  },
  exp: Math.floor(Date.now() / 1000) + 3600,
})}.sig`;

const file = (name: string, content: string) => ({
  name,
  path: "/" + name,
  type: "text",
  extension: ".txt",
  content,
  isDir: false,
  size: content.length,
  modified: "2026-11-18T10:00:00Z",
});
const RESOURCES: Record<string, object> = {
  "/api/resources/a.txt": file("a.txt", "the content of a"),
  "/api/resources/b.txt": file("b.txt", "the content of b"),
};

let wrapper: VueWrapper | null = null;
let puts: { url: string; body: string }[] = [];
// a GET of this path waits for release(): the new file still loading
const hold = { path: "", release: () => {} };
// The suite also runs on builders short of CPU: the waits are given up to
// 10 s (and the tests 60 s).
const waitFor = (check: () => void) => vi.waitFor(check, { timeout: 10_000 });
const promptNames = () => useLayoutStore().prompts.map((p) => p.prompt);
const editorEl = () => document.getElementById("editor");
const isAce = () => editorEl()?.classList.contains("ace_editor") === true;
const kept = () =>
  JSON.parse(sessionStorage.getItem("editor-unsaved") ?? "null")?.edits ?? null;
const button = (label: string) =>
  [...document.querySelectorAll("button")].find(
    (b) => b.textContent?.trim() === label
  )!;

// The editor the page shows, once it shows the file's content.
async function shownEditor(content: string) {
  await waitFor(() => {
    expect(isAce()).toBe(true);
    expect(ace.edit(editorEl()!).getValue()).toBe(content);
  });
  return ace.edit(editorEl()!);
}

beforeAll(async () => {
  // load the editor (and Ace) once, outside the tests' time limits
  await import("@/views/files/Editor.vue");
}, 120_000);

async function open(path: string) {
  puts = [];
  globalThis.fetch = vi.fn(async (url: any, init?: any) => {
    const path = String(url).split("?")[0];
    if (init?.method === "PUT") {
      puts.push({ url: path, body: String(init.body) });
      return new Response("", { status: 200 });
    }
    if (path === hold.path) {
      await new Promise<void>((resolve, reject) => {
        hold.release = resolve;
        init?.signal?.addEventListener("abort", () =>
          reject(Object.assign(new Error("aborted"), { name: "AbortError" }))
        );
      });
    }
    const body = RESOURCES[path];
    return body
      ? new Response(JSON.stringify(body), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        })
      : new Response("404 Not Found", { status: 404 });
  }) as any;
  const pinia = createPinia();
  setActivePinia(pinia);
  parseToken(token);
  await router.push(path);
  await router.isReady();
  wrapper = mount(
    { render: () => [h(RouterView), h(Prompts)] },
    {
      attachTo: document.body,
      global: {
        plugins: [pinia, router, i18n],
        provide: { $showError: vi.fn(), $showSuccess: vi.fn() },
        stubs: { Breadcrumbs: true, HeaderBar: true, Action: true },
      },
    }
  );
}

const ctrlS = () =>
  window.dispatchEvent(
    new KeyboardEvent("keydown", { key: "s", ctrlKey: true, bubbles: true })
  );

describe(
  "a route change from one text file straight to another",
  { timeout: 60_000 },
  () => {
    afterEach(async () => {
      hold.path = "";
      hold.release();
      wrapper?.unmount();
      wrapper = null;
      sessionStorage.clear();
      vi.mocked(buttons.success).mockClear();
      await router.push("/login");
    });

    it("shows the new file in a working editor, and Save writes only its own text", async () => {
      await open("/files/b.txt");
      await shownEditor("the content of b");

      // e.g. the back button's history menu: /files/b.txt -> /files/a.txt
      await router.push("/files/a.txt");
      await shownEditor("the content of a");

      ctrlS();
      await waitFor(() =>
        expect(vi.mocked(buttons.success)).toHaveBeenCalled()
      );
      expect(puts).toEqual([
        { url: "/api/resources/a.txt", body: "the content of a" },
      ]);
    });

    it("a save while the new file loads writes the file the editor was opened for", async () => {
      await open("/files/b.txt");
      await shownEditor("the content of b");

      hold.path = "/api/resources/a.txt";
      const going = router.push("/files/a.txt");
      await waitFor(() => expect(useLayoutStore().loading).toBe(true));
      ctrlS();
      await waitFor(() =>
        expect(vi.mocked(buttons.success)).toHaveBeenCalled()
      );
      // never b's text into a.txt
      expect(puts).toEqual([
        { url: "/api/resources/b.txt", body: "the content of b" },
      ]);

      hold.path = "";
      hold.release();
      await going;
      await shownEditor("the content of a");
    });

    it("offers the new file's kept text; Restore and Save store it, b's stays kept", async () => {
      sessionStorage.setItem(
        "editor-unsaved",
        JSON.stringify({
          user: 1,
          username: "alice",
          edits: [
            { path: "/files/b.txt", content: "kept for b", base: "x" },
            { path: "/files/a.txt", content: "kept for a", base: "y" },
          ],
        })
      );
      await open("/files/b.txt");
      await waitFor(() =>
        expect(promptNames()).toEqual(["restoreEditorChanges"])
      );
      // Escape: b's text is offered again next time (keptPending)
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
      await waitFor(() => expect(promptNames()).toEqual([]));

      await router.push("/files/a.txt");
      // a's kept text is offered for a
      await waitFor(() => {
        expect(useFileStore().req?.name).toBe("a.txt");
        expect(promptNames()).toEqual(["restoreEditorChanges"]);
      });
      button("Restore").click();
      await waitFor(() => expect(promptNames()).toEqual([]));
      expect(ace.edit(editorEl()!).getValue()).toBe("kept for a");

      ctrlS();
      await waitFor(() =>
        expect(vi.mocked(buttons.success)).toHaveBeenCalled()
      );
      expect(puts).toEqual([
        { url: "/api/resources/a.txt", body: "kept for a" },
      ]);
      // a's text is stored and no longer kept; b's is still to be answered
      expect(kept()).toEqual([
        { path: "/files/b.txt", content: "kept for b", base: "x" },
      ]);
    });

    it("a forced logout keeps the new file's edits under its own path", async () => {
      await open("/files/b.txt");
      await shownEditor("the content of b");
      await router.push("/files/a.txt");
      const editor = await shownEditor("the content of a");

      editor.setValue("unsaved edits of a");
      logout("inactivity", useAuthStore().jwt);
      await waitFor(() =>
        expect(router.currentRoute.value.path).toBe("/login")
      );
      expect(router.currentRoute.value.query.redirect).toBe("/files/a.txt");
      expect(kept()).toEqual([
        {
          path: "/files/a.txt",
          content: "unsaved edits of a",
          base: expect.any(String),
        },
      ]);
    });

    it("a forced logout while the new file loads never keeps the old text under the new path", async () => {
      await open("/files/b.txt");
      const editor = await shownEditor("the content of b");
      editor.setValue("unsaved edits of b");

      hold.path = "/api/resources/a.txt";
      void router.push("/files/a.txt");
      // the editor asks about b's unsaved edits first
      await waitFor(() =>
        expect(promptNames()).toEqual(["discardEditorChanges"])
      );
      button("Discard").click();
      await waitFor(() => {
        expect(router.currentRoute.value.path).toBe("/files/a.txt");
        expect(useLayoutStore().loading).toBe(true);
      });

      logout("inactivity", useAuthStore().jwt);
      await waitFor(() =>
        expect(router.currentRoute.value.path).toBe("/login")
      );
      const paths = (kept() ?? []).map((e: any) => e.path);
      expect(paths).not.toContain("/files/a.txt");
    });

    it("a save while the new file loads leaves the new file's kept text alone", async () => {
      sessionStorage.setItem(
        "editor-unsaved",
        JSON.stringify({
          user: 1,
          username: "alice",
          edits: [{ path: "/files/a.txt", content: "kept for a", base: "y" }],
        })
      );
      await open("/files/b.txt");
      await shownEditor("the content of b");

      hold.path = "/api/resources/a.txt";
      const going = router.push("/files/a.txt");
      await waitFor(() => expect(useLayoutStore().loading).toBe(true));
      // b's save drops what was kept of b, never of a (the route by now)
      ctrlS();
      await waitFor(() =>
        expect(vi.mocked(buttons.success)).toHaveBeenCalled()
      );
      expect(puts).toEqual([
        { url: "/api/resources/b.txt", body: "the content of b" },
      ]);
      expect(kept()).toEqual([
        { path: "/files/a.txt", content: "kept for a", base: "y" },
      ]);

      hold.path = "";
      hold.release();
      await going;
      // and a's kept text is offered for a
      await waitFor(() =>
        expect(promptNames()).toEqual(["restoreEditorChanges"])
      );
    });
  }
);
