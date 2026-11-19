// @vitest-environment jsdom
//
// The end of an upload batch sets fileStore.reload, and Files.vue then
// reloaded the current resource, with layoutStore.loading set meanwhile.
// With a text file open, Editor.vue swapped its <form id="editor"> for the
// spinner and back while the Editor and its Ace instance were kept: the
// editor went blank, the unsaved edits stayed in a detached Ace instance
// (Ctrl+S still saved them), and an open restore question was closed
// unanswered. A file shown in the editor or the preview has no listing to
// refresh: the reload is now skipped there (the listing is fetched anew
// when the file is closed), and nothing on the page is disturbed.
//
// Real Files.vue, Editor, Prompts, router (memory history), api/files.ts;
// fetch mocked.
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
  const Files = (await import("@/views/Files.vue")).default;
  const { h } = await import("vue");
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/files/:path*", component: Files, meta: { requiresAuth: true } },
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
  return {
    __esModule: true,
    default: { render: () => h("p", { id: "preview" }, "preview") },
  };
});
vi.mock("@/views/files/FileListing.vue", async () => {
  const { h } = await import("vue");
  return { default: { render: () => h("p", { id: "listing" }, "listing") } };
});

import { mount, type VueWrapper } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { h, watch } from "vue";
import { RouterView } from "vue-router";
import ace from "ace-builds";
import router from "@/router";
import i18n from "@/i18n";
import Prompts from "@/components/prompts/Prompts.vue";
import { parseToken } from "@/utils/auth";
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

const RESOURCES: Record<string, object> = {
  "/api/resources/notes.md": {
    name: "notes.md",
    path: "/notes.md",
    type: "text",
    extension: ".md",
    content: "file content",
    isDir: false,
    size: 12,
    modified: "2026-11-18T10:00:00Z",
  },
  "/api/resources/photo.jpg": {
    name: "photo.jpg",
    path: "/photo.jpg",
    type: "image",
    extension: ".jpg",
    isDir: false,
    size: 1200,
    modified: "2026-11-18T10:00:00Z",
  },
  "/api/resources/": {
    name: "",
    path: "/",
    type: "",
    isDir: true,
    items: [],
    numDirs: 0,
    numFiles: 0,
    sorting: { by: "name", asc: true },
  },
};

let wrapper: VueWrapper | null = null;
let gets: string[] = [];
let puts: string[] = [];
const waitFor = (check: () => void) => vi.waitFor(check, { timeout: 10_000 });

beforeAll(async () => {
  await import("@/views/files/Editor.vue");
}, 120_000);

async function open(path: string) {
  gets = [];
  puts = [];
  globalThis.fetch = vi.fn(async (url: any, init?: any) => {
    if (init?.method === "PUT") {
      puts.push(String(init.body));
      return new Response("", { status: 200 });
    }
    const path = String(url).split("?")[0];
    gets.push(path);
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

// layoutStore.loading set at any point from now on
function watchLoading() {
  const seen = { loading: false };
  watch(
    () => useLayoutStore().loading,
    (loading) => {
      if (loading) seen.loading = true;
    },
    { flush: "sync" }
  );
  return seen;
}

describe("a listing reload while a file is open", { timeout: 60_000 }, () => {
  afterEach(async () => {
    wrapper?.unmount();
    wrapper = null;
    sessionStorage.clear();
    await router.push("/login");
  });

  it("leaves the editor, its unsaved edits and an open restore question alone", async () => {
    // a kept text for the file, as a forced logout leaves it
    sessionStorage.setItem(
      "editor-unsaved",
      JSON.stringify({
        user: 1,
        username: "alice",
        path: "/files/notes.md",
        content: "kept text",
      })
    );
    await open("/files/notes.md");
    await waitFor(() =>
      expect(
        document.getElementById("editor")?.classList.contains("ace_editor")
      ).toBe(true)
    );
    const editor = ace.edit(document.getElementById("editor")!);
    await waitFor(() =>
      expect(useLayoutStore().prompts.map((p) => p.prompt)).toEqual([
        "restoreEditorChanges",
      ])
    );
    editor.setValue("my unsaved edits");
    const fetchesBefore = gets.length;
    const seen = watchLoading();

    // an upload batch ends
    useFileStore().reload = true;
    await waitFor(() => expect(useFileStore().reload).toBe(false));

    expect(seen.loading).toBe(false);
    expect(gets.length).toBe(fetchesBefore);
    expect(useLayoutStore().prompts.map((p) => p.prompt)).toEqual([
      "restoreEditorChanges",
    ]);
    expect(document.body.contains(editor.container)).toBe(true);
    expect(document.getElementById("editor")).toBe(editor.container);
    expect(editor.getValue()).toBe("my unsaved edits");

    // the next batch end is not lost either: back in the listing (the
    // edits saved, the question answered), it reloads
    editor.session.getUndoManager().markClean();
    useLayoutStore().closeHovers();
    await router.push("/files/");
    await waitFor(() =>
      expect(document.getElementById("listing")).not.toBeNull()
    );
    const listingFetches = gets.length;
    useFileStore().reload = true;
    await waitFor(() => expect(gets.length).toBe(listingFetches + 1));
    expect(gets.at(-1)).toBe("/api/resources/");
  });

  it("leaves a file in the preview alone", async () => {
    await open("/files/photo.jpg");
    await waitFor(() =>
      expect(document.getElementById("preview")).not.toBeNull()
    );
    const shown = document.getElementById("preview");
    const fetchesBefore = gets.length;
    const seen = watchLoading();
    useFileStore().reload = true;
    await waitFor(() => expect(useFileStore().reload).toBe(false));
    expect(seen.loading).toBe(false);
    expect(gets.length).toBe(fetchesBefore);
    expect(document.getElementById("preview")).toBe(shown);
  });
});
