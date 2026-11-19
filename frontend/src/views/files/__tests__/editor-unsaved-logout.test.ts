// @vitest-environment jsdom
//
// A save in the text editor answered 401 (the session revoked elsewhere: a
// password change on another device, an admin changing the user's
// permissions or scope) logs the tab out. That unmounted the editor and
// threw the unsaved text away, and after logging in again the user landed
// on /files/. Now the unsaved text is kept for the tab (sessionStorage),
// tagged with the user, the login page leads back to the file, and opening
// the file again offers to restore it: Restore or Discard, never silently.
// A deliberate logout, or another user logging in, drops it.
//
// Real Editor, Prompts, vue-router (memory history), api/files.ts, fetchURL
// and logout. Only fetch is mocked (the server's answers) and the toolbar
// animation.
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
  const { h, defineAsyncComponent } = await import("vue");
  // lazily, as Files.vue does (the editor imports this module in turn)
  const Editor = defineAsyncComponent(() => import("@/views/files/Editor.vue"));
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      {
        path: "/files/:path*",
        component: { render: () => h(Editor) },
        meta: { requiresAuth: true },
      },
      { path: "/login", component: { render: () => h("p", "login page") } },
    ],
  });
  return { default: router, router };
});
vi.mock("@/utils/buttons", () => ({
  default: { loading: vi.fn(), success: vi.fn(), done: vi.fn() },
}));

import { mount, type VueWrapper } from "@vue/test-utils";
import { createPinia, setActivePinia, type Pinia } from "pinia";
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
const token = (id: number, username: string) =>
  `${b64({ alg: "HS256" })}.${b64({
    user: {
      id,
      username,
      locale: "en",
      perm: { modify: true },
      aceEditorTheme: "",
    },
    exp: Math.floor(Date.now() / 1000) + 3600,
  })}.sig`;

const UNSAVED = "draft v1\n\nhalf an hour of unsaved work";
let putStatus = 401;
let wrapper: VueWrapper | null = null;
let pinia: Pinia;

const storedText = () =>
  [
    ...Object.values({ ...sessionStorage }),
    ...Object.values({ ...localStorage }),
  ]
    .map(String)
    .some((v) => v.includes("half an hour"));
const editorEl = () => document.getElementById("editor");
const promptNames = () => useLayoutStore().prompts.map((p) => p.prompt);

async function openFile(path = "/files/notes.md", content = "draft v1") {
  useFileStore().req = {
    name: path.split("/").pop(),
    path: path.slice("/files".length),
    type: "text",
    extension: ".md",
    content,
    isDir: false,
    items: [],
  } as any;
  await router.push(path);
  await vi.waitFor(
    () => {
      expect(editorEl()?.classList.contains("ace_editor")).toBe(true);
    },
    { timeout: 10_000 }
  );
  return ace.edit(editorEl()!);
}

// The suite also runs on builders short of CPU: the waits are given up to
// 10 s (and the tests 60 s).
const waitFor = (check: () => void) => vi.waitFor(check, { timeout: 10_000 });

beforeAll(async () => {
  // load the editor (and Ace) once, outside the tests' time limits
  await import("@/views/files/Editor.vue");
}, 120_000);

describe(
  "unsaved text in the editor across a forced logout",
  { timeout: 60_000 },
  () => {
    afterEach(() => {
      wrapper?.unmount();
      wrapper = null;
      sessionStorage.clear();
      localStorage.clear();
    });

    async function setup(user = { id: 1, username: "alice" }) {
      putStatus = 401;
      globalThis.fetch = vi.fn(async (_url: any, init?: any) => {
        if (init?.method === "PUT") {
          return new Response(putStatus === 200 ? "" : "401 Unauthorized", {
            status: putStatus,
          });
        }
        return new Response("{}", { status: 200 });
      }) as any;
      pinia = createPinia();
      setActivePinia(pinia);
      parseToken(token(user.id, user.username));
      await router.push("/login");
      await router.isReady();
      wrapper = mount(
        { render: () => [h(RouterView), h(Prompts)] },
        {
          attachTo: document.body,
          global: {
            plugins: [pinia, router, i18n],
            provide: { $showError: vi.fn(), $showSuccess: vi.fn() },
            stubs: { Breadcrumbs: true },
          },
        }
      );
    }

    async function saveAnswered401(editor: ace.Ace.Editor) {
      editor.setValue(UNSAVED);
      expect(editor.session.getUndoManager().isClean()).toBe(false);
      window.dispatchEvent(
        new KeyboardEvent("keydown", { key: "s", ctrlKey: true, bubbles: true })
      );
      await waitFor(() => {
        expect(router.currentRoute.value.path).toBe("/login");
        expect(editorEl()).toBeNull();
      });
    }

    it("keeps the text, leads back to the file, and offers to restore it", async () => {
      await setup();
      const editor = await openFile();
      await saveAnswered401(editor);

      expect(router.currentRoute.value.query.redirect).toBe("/files/notes.md");
      expect(storedText()).toBe(true);

      // the same user logs in again and is led back to the file
      parseToken(token(1, "alice"));
      const reopened = await openFile(
        router.currentRoute.value.query.redirect as string
      );
      await waitFor(() =>
        expect(promptNames()).toEqual(["restoreEditorChanges"])
      );
      // asked, not overwritten
      expect(reopened.getValue()).toBe("draft v1");
      // the safe answer (nothing is lost) has the default focus
      expect(document.activeElement?.textContent?.trim()).toBe("Restore");

      (document.activeElement as HTMLElement).click();
      await waitFor(() => expect(promptNames()).toEqual([]));
      expect(reopened.getValue()).toBe(UNSAVED);
      // restored as unsaved changes: closing asks, saving stores them
      expect(reopened.session.getUndoManager().isClean()).toBe(false);
      expect(storedText()).toBe(false);
    });

    it("Discard keeps the file's content and forgets the text", async () => {
      await setup();
      await saveAnswered401(await openFile());
      parseToken(token(1, "alice"));
      const reopened = await openFile();
      await waitFor(() =>
        expect(promptNames()).toEqual(["restoreEditorChanges"])
      );
      const discard = [...document.querySelectorAll("button")].find(
        (b) => b.textContent?.trim() === "Discard"
      )!;
      discard.click();
      await waitFor(() => expect(promptNames()).toEqual([]));
      expect(reopened.getValue()).toBe("draft v1");
      expect(storedText()).toBe(false);
    });

    it("Escape answers neither: the text is offered again next time", async () => {
      await setup();
      await saveAnswered401(await openFile());
      parseToken(token(1, "alice"));
      const reopened = await openFile();
      await waitFor(() =>
        expect(promptNames()).toEqual(["restoreEditorChanges"])
      );
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
      await waitFor(() => expect(promptNames()).toEqual([]));
      expect(reopened.getValue()).toBe("draft v1");
      expect(storedText()).toBe(true);
    });

    it("another user logging in on the tab never gets the text", async () => {
      await setup();
      await saveAnswered401(await openFile());
      expect(storedText()).toBe(true);

      parseToken(token(2, "bob"));
      // dropped as soon as the other user's session starts
      expect(storedText()).toBe(false);
      await openFile();
      await new Promise((r) => setTimeout(r, 0));
      expect(promptNames()).toEqual([]);
    });

    it("a deliberate logout keeps nothing, and drops what was kept", async () => {
      await setup();
      await saveAnswered401(await openFile());
      parseToken(token(1, "alice"));
      const editor = await openFile("/files/other.md", "other");
      await waitFor(() => expect(useAuthStore().user).not.toBeNull());
      editor.setValue("other, with half an hour of edits");
      // Sidebar's button: logout(clickEvent)
      logout(new MouseEvent("click") as any);
      await waitFor(() =>
        expect(router.currentRoute.value.path).toBe("/login")
      );
      expect(storedText()).toBe(false);
      expect(router.currentRoute.value.query.redirect).toBeUndefined();
    });

    it("the inactivity logout keeps the text too", async () => {
      await setup();
      const editor = await openFile();
      editor.setValue(UNSAVED);
      logout("inactivity", useAuthStore().jwt);
      await waitFor(() =>
        expect(router.currentRoute.value.path).toBe("/login")
      );
      expect(router.currentRoute.value.query).toEqual({
        "logout-reason": "inactivity",
        redirect: "/files/notes.md",
      });
      expect(storedText()).toBe(true);
    });

    // A save of the file while the question is unanswered (closed with
    // Escape or Cancel: "offered again next time", or still open) keeps the
    // text: it dropped it, without a word. Restore or Discard drops it.
    const ctrlS = () =>
      window.dispatchEvent(
        new KeyboardEvent("keydown", { key: "s", ctrlKey: true, bubbles: true })
      );
    const saved = async (count: number) =>
      waitFor(() =>
        expect(vi.mocked(buttons.success)).toHaveBeenCalledTimes(count)
      );

    it("a save after Escape keeps the text: it is offered again next time", async () => {
      await setup();
      await saveAnswered401(await openFile());
      parseToken(token(1, "alice"));
      const reopened = await openFile();
      await waitFor(() =>
        expect(promptNames()).toEqual(["restoreEditorChanges"])
      );
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
      await waitFor(() => expect(promptNames()).toEqual([]));
      putStatus = 200;
      vi.mocked(buttons.success).mockClear();
      reopened.setValue("saved now");
      ctrlS();
      await saved(1);
      expect(reopened.session.getUndoManager().isClean()).toBe(true);
      expect(storedText()).toBe(true);
    });

    it("Cancel on the changed-file question, an edit, Save: offered again, as changed", async () => {
      await setup();
      await saveAnswered401(await openFile("/files/notes.md", "draft v1"));
      parseToken(token(1, "alice"));
      const reopened = await openFile("/files/notes.md", "v2: bob's work");
      await waitFor(() =>
        expect(promptNames()).toEqual(["restoreEditorChanges"])
      );
      expect(document.activeElement?.textContent?.trim()).toBe("Cancel");
      (document.activeElement as HTMLElement).click();
      await waitFor(() => expect(promptNames()).toEqual([]));
      putStatus = 200;
      vi.mocked(buttons.success).mockClear();
      reopened.setValue("v2: bob's work (typo fixed)");
      ctrlS();
      await saved(1);
      expect(storedText()).toBe(true);

      await router.push("/login");
      await openFile("/files/notes.md", "v2: bob's work (typo fixed)");
      await waitFor(() =>
        expect(promptNames()).toEqual(["restoreEditorChanges"])
      );
      expect(question()).toMatch(/changed since you were logged out/);
    });

    it("Ctrl+S while the question is open saves the file and keeps the text", async () => {
      await setup();
      await saveAnswered401(await openFile("/files/notes.md", "draft v1"));
      parseToken(token(1, "alice"));
      const reopened = await openFile("/files/notes.md", "v2: bob's work");
      await waitFor(() =>
        expect(promptNames()).toEqual(["restoreEditorChanges"])
      );
      putStatus = 200;
      vi.mocked(buttons.success).mockClear();
      ctrlS();
      await saved(1);
      // the file's own content, unchanged, was saved
      const puts = vi
        .mocked(globalThis.fetch)
        .mock.calls.filter((c) => c[1]?.method === "PUT");
      expect(puts.at(-1)?.[1]?.body).toBe("v2: bob's work");
      expect(promptNames()).toEqual(["restoreEditorChanges"]);
      expect(storedText()).toBe(true);
      // Cancel still leaves it for next time
      const cancel = [...document.querySelectorAll("button")].find(
        (b) => b.textContent?.trim() === "Cancel"
      )!;
      cancel.click();
      await waitFor(() => expect(promptNames()).toEqual([]));
      expect(reopened.getValue()).toBe("v2: bob's work");
      expect(storedText()).toBe(true);
    });

    it("Restore, then Save: the text is saved and no longer kept", async () => {
      await setup();
      await saveAnswered401(await openFile());
      parseToken(token(1, "alice"));
      const reopened = await openFile();
      await waitFor(() =>
        expect(promptNames()).toEqual(["restoreEditorChanges"])
      );
      const restore = [...document.querySelectorAll("button")].find(
        (b) => b.textContent?.trim() === "Restore"
      )!;
      restore.click();
      await waitFor(() => expect(promptNames()).toEqual([]));
      putStatus = 200;
      vi.mocked(buttons.success).mockClear();
      ctrlS();
      await saved(1);
      expect(reopened.getValue()).toBe(UNSAVED);
      expect(storedText()).toBe(false);
    });

    it("Discard, then Save: the text is no longer kept", async () => {
      await setup();
      await saveAnswered401(await openFile());
      parseToken(token(1, "alice"));
      const reopened = await openFile();
      await waitFor(() =>
        expect(promptNames()).toEqual(["restoreEditorChanges"])
      );
      const discard = [...document.querySelectorAll("button")].find(
        (b) => b.textContent?.trim() === "Discard"
      )!;
      discard.click();
      await waitFor(() => expect(promptNames()).toEqual([]));
      expect(storedText()).toBe(false);
      putStatus = 200;
      vi.mocked(buttons.success).mockClear();
      reopened.setValue("saved now");
      ctrlS();
      await saved(1);
      expect(storedText()).toBe(false);
    });

    it("a full storage does not stop the logout", async () => {
      await setup();
      const editor = await openFile();
      const original = Storage.prototype.setItem;
      const setItem = vi
        .spyOn(Storage.prototype, "setItem")
        .mockImplementation(function (
          this: Storage,
          key: string,
          value: string
        ) {
          if (this === sessionStorage) {
            throw new DOMException("full", "QuotaExceededError");
          }
          original.call(this, key, value);
        });
      try {
        await saveAnswered401(editor);
      } finally {
        setItem.mockRestore();
      }
      expect(useAuthStore().user).toBeNull();
      expect(router.currentRoute.value.path).toBe("/login");
    });

    // The file at that path may have changed since the logout: someone saved
    // it, or a scope change (one cause of the forced logout) made the path
    // name another file. Restore, then Save, would replace that content.
    const SAME =
      "You were logged out before saving your changes to this file. Restore them in the editor (not saved until you save), or discard them?";
    const question = () =>
      document.querySelector(".card-content")?.textContent?.trim();

    it("says when the file changed since the logout, and the default focus leaves both alone", async () => {
      await setup();
      await saveAnswered401(await openFile("/files/notes.md", "draft v1"));
      parseToken(token(1, "alice"));
      const reopened = await openFile("/files/notes.md", "v2: bob's work");
      await waitFor(() =>
        expect(promptNames()).toEqual(["restoreEditorChanges"])
      );
      console.log("question:", question());
      expect(question()).not.toBe(SAME);
      expect(question()).toMatch(/changed since you were logged out/);
      expect(question()).toMatch(/replaces? its current content/);
      // neither Restore nor Discard has the default focus
      expect(document.activeElement?.textContent?.trim()).toBe("Cancel");
      (document.activeElement as HTMLElement).click();
      await waitFor(() => expect(promptNames()).toEqual([]));
      expect(reopened.getValue()).toBe("v2: bob's work");
      expect(reopened.session.getUndoManager().isClean()).toBe(true);
      expect(storedText()).toBe(true);
    });

    it("the user's own save before the logout is not a change", async () => {
      await setup();
      const editor = await openFile("/files/notes.md", "draft v1");
      putStatus = 200;
      editor.setValue("draft v1, saved");
      window.dispatchEvent(
        new KeyboardEvent("keydown", { key: "s", ctrlKey: true, bubbles: true })
      );
      await waitFor(() =>
        expect(editor.session.getUndoManager().isClean()).toBe(true)
      );
      putStatus = 401;
      await saveAnswered401(editor);
      parseToken(token(1, "alice"));
      await openFile("/files/notes.md", "draft v1, saved");
      await waitFor(() =>
        expect(promptNames()).toEqual(["restoreEditorChanges"])
      );
      expect(question()).toBe(SAME);
      expect(document.activeElement?.textContent?.trim()).toBe("Restore");
    });

    it("a kept text that does not say what it was written over counts as changed", async () => {
      await setup();
      sessionStorage.setItem(
        "editor-unsaved",
        JSON.stringify({
          user: 1,
          username: "alice",
          path: "/files/notes.md",
          content: UNSAVED,
        })
      );
      await openFile("/files/notes.md", "draft v1");
      await waitFor(() =>
        expect(promptNames()).toEqual(["restoreEditorChanges"])
      );
      expect(question()).toMatch(/changed since you were logged out/);
      expect(document.activeElement?.textContent?.trim()).toBe("Cancel");
    });
  }
);
