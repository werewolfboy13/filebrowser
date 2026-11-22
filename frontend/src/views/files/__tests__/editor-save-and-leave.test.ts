// @vitest-environment jsdom
//
// What Save, Close and Back write, and when they leave.
//
// "Edit as Text" on a CSV file (or a ?edit=true URL) mounts the Editor
// while the file is fetched again; its Ace editor is created only once the
// file has arrived. A save in that window (Ctrl+S, the Save button, or
// "Save changes" in the discard question that Close and Back asked although
// nothing was typed) sent a PUT without a body, and the server emptied the
// file. Now nothing is saved, and nothing asked, until the editor has its
// text; a file the user emptied on purpose is still saved empty.
//
// Real Files.vue, Editor, Prompts, header actions, router (memory history),
// api/files.ts and logout; only fetch (the server's answers), the toolbar
// animation and the header bar's frame are mocked.
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
  // __esModule: defineAsyncComponent unwraps .default only for a module
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
import { h } from "vue";
import { RouterView } from "vue-router";
import ace from "ace-builds";
import router from "@/router";
import i18n from "@/i18n";
import Prompts from "@/components/prompts/Prompts.vue";
import buttons from "@/utils/buttons";
import { parseToken } from "@/utils/auth";
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
  extension: "." + name.split(".").pop(),
  content,
  isDir: false,
  size: content.length,
  modified: "2026-11-18T10:00:00Z",
});
let RESOURCES: Record<string, any> = {};
const reset = () => {
  RESOURCES = {
    "/api/resources/": {
      name: "",
      path: "/",
      isDir: true,
      type: "",
      items: [],
    },
    "/api/resources/a.txt": file("a.txt", "the content of a"),
    "/api/resources/b.txt": file("b.txt", "the content of b"),
    "/api/resources/a.csv": file("a.csv", "a1,a2"),
    "/api/resources/b.csv": file("b.csv", "b1,b2"),
  };
};

let wrapper: VueWrapper | null = null;
// body: null when the PUT carried no body at all
let puts: { url: string; body: string | null }[] = [];
// the status the server answers a PUT with
let putStatus = 200;
// a request of this method and path waits for release()
const hold = { method: "GET", path: "", release: () => {} };
const showError = vi.fn();
// The suite also runs on builders short of CPU: the waits are given up to
// 10 s (and the tests 60 s).
const waitFor = (check: () => void) => vi.waitFor(check, { timeout: 10_000 });
const promptNames = () => useLayoutStore().prompts.map((p) => p.prompt);
const editorEl = () => document.getElementById("editor");
const isAce = () => editorEl()?.classList.contains("ace_editor") === true;
const button = (label: string) =>
  [...document.querySelectorAll("button")].find(
    (b) => b.textContent?.trim() === label
  )!;
// "Save changes" in the discard question, once it is shown
const saveChanges = async () => {
  await waitFor(() => expect(button("Save changes")).toBeDefined());
  button("Save changes").click();
};
const saveButton = () => document.getElementById("save-button");
const editorMounted = () =>
  document.getElementById("editor-container") !== null;
const currentPath = () => router.currentRoute.value.fullPath;

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
    const method = init?.method ?? "GET";
    if (method === "PUT") {
      // recorded when sent, before any answer
      puts.push({
        url: path,
        body: init.body === undefined ? null : String(init.body),
      });
    }
    if (path === hold.path && method === hold.method) {
      await new Promise<void>((resolve, reject) => {
        hold.release = resolve;
        init?.signal?.addEventListener("abort", () =>
          reject(Object.assign(new Error("aborted"), { name: "AbortError" }))
        );
      });
    }
    if (method === "PUT") {
      if (putStatus !== 200) {
        return new Response("500 Internal Server Error", { status: putStatus });
      }
      // what the server stores: a PUT without a body empties the file
      const body = puts[puts.length - 1].body;
      RESOURCES[path] = { ...RESOURCES[path], content: body ?? "" };
      return new Response("", { status: 200 });
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
        provide: { $showError: showError, $showSuccess: vi.fn() },
        stubs: {
          Breadcrumbs: true,
          // its actions (the real ones) without the bar around them
          HeaderBar: {
            render(this: any) {
              return h("header", this.$slots.default?.());
            },
          },
        },
      },
    }
  );
}

const ctrlS = () =>
  window.dispatchEvent(
    new KeyboardEvent("keydown", { key: "s", ctrlKey: true, bubbles: true })
  );
// a real Escape key press (the Editor checks event.code)
const escape = () =>
  window.dispatchEvent(
    new KeyboardEvent("keydown", {
      key: "Escape",
      code: "Escape",
      bubbles: true,
    })
  );

// "Edit as Text" on the CSV file shown in the preview, its answer held:
// the Editor is mounted, the file still loading.
async function editAsTextLoading(name: string, from = "a.csv") {
  await open("/files/" + from);
  await waitFor(() =>
    expect(document.getElementById("preview")).not.toBe(null)
  );
  hold.path = "/api/resources/" + name;
  const going = router.push(`/files/${name}?edit=true`);
  await waitFor(() => {
    expect(useLayoutStore().loading).toBe(true);
    expect(editorMounted()).toBe(true);
  });
  return going;
}

describe("Save, Close and Back in the editor", { timeout: 60_000 }, () => {
  afterEach(async () => {
    hold.path = "";
    hold.method = "GET";
    hold.release();
    putStatus = 200;
    wrapper?.unmount();
    wrapper = null;
    sessionStorage.clear();
    vi.mocked(buttons.success).mockClear();
    showError.mockClear();
    await router.push("/login");
  });

  it("no Save button and no save by Ctrl+S while 'Edit as Text' loads", async () => {
    reset();
    const going = await editAsTextLoading("a.csv");
    // the PUT, if any, is sent synchronously: recorded by now
    ctrlS();
    expect(puts).toEqual([]);
    expect(saveButton()).toBe(null);

    hold.path = "";
    hold.release();
    await going;
    await shownEditor("a1,a2");
    expect(saveButton()).not.toBe(null);
    expect(puts).toEqual([]);
    expect(RESOURCES["/api/resources/a.csv"].content).toBe("a1,a2");
  });

  it("Close while 'Edit as Text' loads asks nothing and writes nothing", async () => {
    reset();
    await editAsTextLoading("a.csv");
    escape();
    const asked = promptNames();
    // what the old code then did: "Save changes", a PUT without a body
    if (asked.includes("discardEditorChanges")) await saveChanges();
    expect({ asked, puts }).toEqual({ asked: [], puts: [] });
    // closed: the folder
    await waitFor(() => expect(currentPath()).toBe("/files/"));
    expect(RESOURCES["/api/resources/a.csv"].content).toBe("a1,a2");
  });

  it("Back while 'Edit as Text' loads asks nothing and writes nothing", async () => {
    reset();
    await editAsTextLoading("a.csv");
    void router.back();
    await waitFor(() =>
      expect(promptNames().length > 0 || currentPath() === "/files/a.csv").toBe(
        true
      )
    );
    const asked = promptNames();
    if (asked.includes("discardEditorChanges")) await saveChanges();
    expect({ asked, puts }).toEqual({ asked: [], puts: [] });
    expect(currentPath()).toBe("/files/a.csv");
    expect(RESOURCES["/api/resources/a.csv"].content).toBe("a1,a2");
  });

  it("Ctrl+S during a jump to another CSV file's ?edit=true writes nothing", async () => {
    reset();
    const going = await editAsTextLoading("b.csv", "a.csv");
    ctrlS();
    expect(puts).toEqual([]);

    hold.path = "";
    hold.release();
    await going;
    await shownEditor("b1,b2");
    expect(puts).toEqual([]);
    expect(RESOURCES["/api/resources/b.csv"].content).toBe("b1,b2");
  });

  it("a file the user emptied is saved empty, on purpose", async () => {
    reset();
    await open("/files/a.txt");
    const editor = await shownEditor("the content of a");
    editor.setValue("");
    ctrlS();
    await waitFor(() => expect(vi.mocked(buttons.success)).toHaveBeenCalled());
    // a PUT with an empty body, not one without a body
    expect(puts).toEqual([{ url: "/api/resources/a.txt", body: "" }]);
    expect(RESOURCES["/api/resources/a.txt"].content).toBe("");
    // and saved: Close leaves without asking
    escape();
    expect(promptNames()).toEqual([]);
    await waitFor(() => expect(currentPath()).toBe("/files/"));
  });
});
