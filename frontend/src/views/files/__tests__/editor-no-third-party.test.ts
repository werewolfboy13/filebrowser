// @vitest-environment jsdom
//
// Opening a file in the text editor must not make the browser load anything
// from a third party: Ace's modes, themes, snippets, extensions and
// syntax-check workers have to come from File Browser's own static path, with
// or without the "disable external" branding option. They used to be
// injected as <script src="https://cdn.jsdelivr.net/..."> into the app
// origin, with no integrity check.
//
// jsdom does not fetch scripts, so this test plays the File Browser server:
// when ace appends a same-origin <script> under /static/ace/, the test checks
// that the build ships that file (vite-plugins/ace.ts), runs the matching
// file from ace-builds and fires its load event. Nothing touches the network.
import fs from "node:fs";
import path from "node:path";
import {
  afterEach,
  beforeEach,
  describe,
  expect,
  it,
  onTestFinished,
  vi,
} from "vitest";

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

const route = vi.hoisted(() => ({ path: "/files/notes/script.py" }));

vi.mock("@/utils/auth", () => ({ renew: vi.fn(), logout: vi.fn() }));
vi.mock("@/api", () => ({ files: { put: vi.fn() } }));
vi.mock("vue-router", () => ({
  useRoute: () => ({ path: route.path, query: {} }),
  useRouter: () => ({ push: vi.fn() }),
  onBeforeRouteUpdate: vi.fn(),
}));
vi.mock("vue-i18n", async (orig) => ({
  ...(await orig<typeof import("vue-i18n")>()),
  useI18n: () => ({ t: (k: string) => k }),
}));

import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import ace from "ace-builds";
import Editor from "@/views/files/Editor.vue";
import { useAuthStore } from "@/stores/auth";
import { useFileStore } from "@/stores/file";
import { aceAssetFiles, aceSourceDir } from "../../../../vite-plugins/ace";
import {
  startNetworkGuard,
  type NetworkGuard,
} from "@/__tests__/network-guard";

const shipped = new Set(aceAssetFiles());
const STATIC_ACE = `${window.location.origin}/static/ace/`;

let guard: NetworkGuard;
let served: string[];
let wrapper: VueWrapper | null = null;

// Play the server for /static/ace/*.js: run the shipped file, then fire load.
function serveAceScript(el: Element) {
  if (!(el instanceof HTMLScriptElement) || !el.src.startsWith(STATIC_ACE)) {
    return;
  }
  const name = el.src.slice(STATIC_ACE.length);
  if (!shipped.has(name)) return; // a 404: never loads
  served.push(name);
  queueMicrotask(() => {
    (0, eval)(fs.readFileSync(path.join(aceSourceDir, name), "utf8"));
    el.onload?.(new Event("load"));
  });
}

async function openFile(name: string, content: string, theme: string) {
  route.path = `/files/notes/${name}`;
  const pinia = createPinia();
  setActivePinia(pinia);
  useAuthStore().user = {
    id: 1,
    perm: { modify: true },
    aceEditorTheme: theme,
  } as any;
  useFileStore().req = {
    name,
    path: `/notes/${name}`,
    type: "text",
    content,
    isDir: false,
    items: [],
  } as any;

  wrapper = mount(Editor, {
    attachTo: document.body,
    global: {
      plugins: [pinia],
      mocks: { $t: (k: string) => k },
      provide: { $showError: vi.fn() },
      stubs: { Breadcrumbs: true },
    },
  });
  await flushPromises();
  return ace.edit(document.getElementById("editor")!);
}

// waits for the condition (no fixed delay: the suite also runs on builders
// short of CPU)
const waitFor = (cond: () => boolean) =>
  vi.waitFor(() => expect(cond()).toBe(true), { timeout: 10_000 });

describe("text editor network requests", () => {
  beforeEach(() => {
    document.body.innerHTML = "";
    document.head.querySelectorAll("script").forEach((s) => s.remove());
    served = [];
    guard = startNetworkGuard(serveAceScript);
  });

  afterEach(() => {
    wrapper?.unmount();
    wrapper = null;
    guard.stop();
  });

  it("loads the language mode, theme and snippets from the app's static path", async () => {
    const editor = await openFile("script.py", "print('hi')\n", "monokai");
    const modeId = () => (editor.session as any).$modeId;
    await waitFor(
      () =>
        modeId() === "ace/mode/python" &&
        served.includes("theme-monokai.js") &&
        served.includes("snippets/python.js")
    );

    console.log(
      "requests:",
      JSON.stringify(guard.requests.filter((u) => !u.startsWith("data:")))
    );
    expect(guard.thirdParty()).toEqual([]);
    expect(modeId()).toBe("ace/mode/python");
    expect(editor.getTheme()).toBe("ace/theme/monokai");
    expect(served).toEqual(
      expect.arrayContaining([
        "mode-python.js",
        "theme-monokai.js",
        "snippets/python.js",
      ])
    );
  });

  it("starts syntax-check workers from a same-origin URL, not a blob", async () => {
    const createObjectURL = vi
      .spyOn(URL, "createObjectURL")
      .mockReturnValue("blob:nope");
    onTestFinished(() => createObjectURL.mockRestore());

    const editor = await openFile("app.js", "let a = ;\n", "");
    await waitFor(() => guard.workers.length > 0);

    console.log("workers:", JSON.stringify(guard.workers));
    expect(guard.thirdParty()).toEqual([]);
    expect((editor.session as any).$modeId).toBe("ace/mode/javascript");
    expect(guard.workers).toEqual(["/static/ace/worker-javascript.js"]);
    expect(shipped.has("worker-javascript.js")).toBe(true);
    expect(createObjectURL).not.toHaveBeenCalled();
  });

  it("ships every mode, theme and worker ace can ask for", async () => {
    const { modes } = await import("ace-builds/src-noconflict/ext-modelist");
    const { themes } = await import("ace-builds/src-noconflict/ext-themelist");
    const missing = [
      ...modes.map((m: { name: string }) => `mode-${m.name}.js`),
      ...themes.map((t: { name: string }) => `theme-${t.name}.js`),
      "ext-searchbox.js",
      "ext-prompt.js",
      ...[
        "javascript",
        "json",
        "css",
        "html",
        "xml",
        "yaml",
        "php",
        "lua",
        "coffee",
        "xquery",
      ].map((w) => `worker-${w}.js`),
    ].filter((f) => !shipped.has(f));
    expect(missing).toEqual([]);
    expect(shipped.has("ace.js")).toBe(false); // bundled into the app
  });
});
