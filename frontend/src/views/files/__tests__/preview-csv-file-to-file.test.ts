// @vitest-environment jsdom
//
// A route change straight to a CSV file from another file shown in the
// preview (a pick from the back button's history menu, history.go(-2)) kept
// the same Preview, and the preview reads the CSV text when the route
// changes, before the new file has arrived: the table showed the previous
// CSV file's rows under the new file's name (or nothing, coming from an
// image). Now a CSV file gets a preview of its own. Images, audio and video
// keep theirs across prev/next (its folder listing, its player state).
// Back from "Edit as Text" to the same CSV file's table showed the rows from
// before the save: the preview now follows the file the store gets.
//
// Real Files.vue, Preview, Editor, router (memory history) and api/files.ts;
// only fetch (the server's answers), the EPUB reader, the toolbar animation
// and the table (which shows the text it is given) are mocked.
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
vi.mock("vue-reader", () => ({ VueReader: { render: () => null } }));
vi.mock("@/utils/buttons", () => ({
  default: { loading: vi.fn(), success: vi.fn(), done: vi.fn() },
}));
vi.mock("@/components/files/ExtendedImage.vue", async () => {
  const { h } = await import("vue");
  return { default: { render: () => h("img", { id: "image" }) } };
});
vi.mock("@/components/files/CsvViewer.vue", async () => {
  const { h } = await import("vue");
  return {
    default: {
      props: ["content", "error"],
      render(this: any) {
        return h("pre", { id: "csv" }, String(this.content));
      },
    },
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
import { useAuthStore } from "@/stores/auth";
import { useFileStore } from "@/stores/file";
import { useLayoutStore } from "@/stores/layout";

const file = (name: string, content: string, type = "text") => ({
  name,
  path: "/" + name,
  url: "/files/" + name,
  type,
  extension: "." + name.split(".").pop(),
  content,
  isDir: false,
  size: content.length,
  modified: "2026-11-18T10:00:00Z",
});
const FILES = [
  file("a.csv", "a1,a2"),
  file("b.csv", "b1,b2"),
  file("c.png", "", "image"),
  file("d.png", "", "image"),
];
const RESOURCES: Record<string, object> = {
  "/api/resources/": {
    name: "",
    path: "/",
    url: "/files/",
    isDir: true,
    type: "",
    items: FILES,
  },
};
const reset = () => {
  for (const f of FILES) RESOURCES["/api/resources" + f.path] = f;
};
reset();

let wrapper: VueWrapper | null = null;
// The suite also runs on builders short of CPU: the waits are given up to
// 10 s (and the tests 60 s).
const waitFor = (check: () => void) => vi.waitFor(check, { timeout: 10_000 });
const table = () => document.getElementById("csv")?.textContent;
const editorEl = () => document.getElementById("editor");
const isAce = () => editorEl()?.classList.contains("ace_editor") === true;

beforeAll(async () => {
  // load the editor (and Ace) once, outside the tests' time limits
  await import("@/views/files/Editor.vue");
}, 120_000);

async function open(path: string) {
  globalThis.fetch = vi.fn(async (url: any, init?: any) => {
    const path = String(url).split("?")[0];
    if (init?.method === "PUT") {
      RESOURCES[path] = { ...RESOURCES[path], content: String(init.body) };
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
  useAuthStore().user = { id: 1, perm: {} } as any;
  await router.push(path);
  await router.isReady();
  wrapper = mount(
    { render: () => h(RouterView) },
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

// the new file is shown (the store has it, nothing loads any more)
const arrived = (name: string) =>
  waitFor(() => {
    expect(useFileStore().req?.name).toBe(name);
    expect(useLayoutStore().loading).toBe(false);
  });

describe(
  "a route change straight to a CSV file from another previewed file",
  { timeout: 60_000 },
  () => {
    afterEach(async () => {
      wrapper?.unmount();
      wrapper = null;
      reset();
      await router.push("/login");
    });

    it("from another CSV file: the table shows the new file's rows", async () => {
      await open("/files/a.csv");
      await waitFor(() => expect(table()).toBe("a1,a2"));

      // e.g. the back button's history menu: /files/a.csv -> /files/b.csv
      await router.push("/files/b.csv");
      await arrived("b.csv");
      await waitFor(() => expect(table()).toBe("b1,b2"));
    });

    it("from an image: the table shows the CSV file's rows", async () => {
      await open("/files/c.png");
      await waitFor(() =>
        expect(document.getElementById("image")).not.toBe(null)
      );

      await router.push("/files/a.csv");
      await arrived("a.csv");
      await waitFor(() => expect(table()).toBe("a1,a2"));
    });

    it("an image keeps its preview on the way to the next one", async () => {
      await open("/files/c.png");
      await waitFor(() =>
        expect(document.getElementById("image")).not.toBe(null)
      );
      // the component instance shown
      const preview = () =>
        wrapper!.findComponent({ name: "Preview" }).vm.$.uid;
      const first = preview();

      // the Next button, the right arrow key
      await router.replace("/files/d.png");
      await arrived("d.png");
      await waitFor(() =>
        expect(document.getElementById("image")).not.toBe(null)
      );
      expect(preview()).toBe(first);
    });
    it("Back from 'Edit as Text' after a save: the table shows the saved rows", async () => {
      await open("/files/a.csv");
      await waitFor(() => expect(table()).toBe("a1,a2"));

      await router.push("/files/a.csv?edit=true");
      await waitFor(() => {
        expect(isAce()).toBe(true);
        expect(ace.edit(editorEl()!).getValue()).toBe("a1,a2");
      });
      ace.edit(editorEl()!).setValue("a1,a2\nnew,row");
      window.dispatchEvent(
        new KeyboardEvent("keydown", { key: "s", ctrlKey: true, bubbles: true })
      );
      await waitFor(() =>
        expect(
          (RESOURCES["/api/resources/a.csv"] as { content: string }).content
        ).toBe("a1,a2\nnew,row")
      );

      router.back();
      await waitFor(() =>
        expect(router.currentRoute.value.fullPath).toBe("/files/a.csv")
      );
      await arrived("a.csv");
      await waitFor(() => expect(table()).toBe("a1,a2\nnew,row"));
    });
  }
);
