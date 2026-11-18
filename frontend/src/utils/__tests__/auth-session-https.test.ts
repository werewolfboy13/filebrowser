// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://fb.test/files/" }
//
// Session handling in utils/auth.ts:
// - the auth cookie (accepted by the server on GET routes) is marked
//   Secure when the app is served over https, so it is never sent over
//   plain http;
// - logout stops what still runs with the session (uploads, whose pending
//   files would otherwise start without a token) and drops per-session
//   state (file clipboard, current listing and selection, open prompts
//   with their callbacks), so the next user to log in on this tab does
//   not inherit them.
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  (window as any).FileBrowser = {
    AuthMethod: "json",
    BaseURL: "",
    StaticURL: "/static",
    LoginPage: true,
    LogoutPage: "/login",
    NoAuth: false,
  };
});
const h = vi.hoisted(() => ({
  push: vi.fn(),
  abortAllUploads: vi.fn(),
  abortResourceUploads: vi.fn(),
  post: vi.fn(),
  jwtAtAbort: null as string | null,
}));
const { currentRoute } = vi.hoisted(() => ({
  // the page the session ends on: a forced logout leads back to it
  currentRoute: {
    value: {
      fullPath: "/files/docs/",
      matched: [{ meta: { requiresAuth: true } }],
    },
  },
}));
vi.mock("@/router", () => ({
  default: { push: h.push, currentRoute: currentRoute },
}));
vi.mock("@/i18n", () => ({ detectLocale: () => "en", setLocale: vi.fn() }));
// The real upload store, over mocked transports: logout() runs its abort().
vi.mock("@/api", () => ({
  files: { post: h.post, abortResourceUploads: h.abortResourceUploads },
}));
vi.mock("@/api/tus", () => ({ abortAllUploads: h.abortAllUploads }));
vi.mock("@/utils/buttons", () => ({
  default: { loading: vi.fn(), success: vi.fn(), done: vi.fn() },
}));

import { createApp } from "vue";
import { createPinia, setActivePinia } from "pinia";
import { logout, parseToken } from "@/utils/auth";
import { useUploadStore } from "@/stores/upload";
import { useAuthStore } from "@/stores/auth";
import { useClipboardStore } from "@/stores/clipboard";
import { useFileStore } from "@/stores/file";
import { useLayoutStore } from "@/stores/layout";
import { fakeJwt, recordCookieWrites } from "./auth-session.shared";

describe("auth session over https", () => {
  beforeEach(() => {
    // the upload store injects $showError: give the pinia an app
    const app = createApp({});
    app.provide("$showError", vi.fn());
    const pinia = createPinia();
    app.use(pinia);
    setActivePinia(pinia);
    vi.clearAllMocks();
    h.post.mockResolvedValue(undefined);
  });

  it("marks the auth cookie Secure, when setting and when clearing it", () => {
    const writes = recordCookieWrites();
    parseToken(fakeJwt());
    logout();
    console.log("cookie writes:", JSON.stringify(writes));
    expect(writes).toHaveLength(2);
    for (const w of writes) {
      expect(w).toMatch(/;\s*Secure/i);
      expect(w).toMatch(/SameSite=Strict/);
    }
  });

  it("logout aborts uploads (while the token is still set) and clears session state", () => {
    recordCookieWrites();
    const auth = useAuthStore();
    parseToken(fakeJwt());
    const jwt = auth.jwt;
    h.abortAllUploads.mockImplementation(
      () => (h.jwtAtAbort = useAuthStore().jwt)
    );

    useClipboardStore().$patch({
      key: "x",
      path: "/files/a/",
      items: [{ from: "/files/a/f", name: "f" } as any],
    });
    const files = useFileStore();
    files.req = { url: "/files/a/", items: [] } as any;
    files.selected = [0];
    useLayoutStore().showHover({
      prompt: "resolve-conflict",
      confirm: vi.fn(),
    });

    logout("inactivity");

    expect(h.abortAllUploads).toHaveBeenCalledTimes(1);
    expect(h.abortResourceUploads).toHaveBeenCalledTimes(1);
    expect(h.jwtAtAbort).toBe(jwt);
    expect(useClipboardStore().items).toEqual([]);
    expect(useFileStore().req).toBeNull();
    expect(useFileStore().selected).toEqual([]);
    expect(useLayoutStore().prompts).toEqual([]);
    expect(auth.user).toBeNull();
    expect(auth.jwt).toBe("");
    expect(h.push).toHaveBeenCalledWith({
      path: "/login",
      query: { "logout-reason": "inactivity" },
    });
  });
  // logout() aborts uploads. abort() stopped the queue (lastUpload =
  // Infinity) and only the settling of the last active upload restored it,
  // so with nothing uploading at logout the queue stayed stopped: after
  // logging in again on the same tab (no page reload) the first file of the
  // next upload was dropped and the upload reported as done.
  it("after logout and a new login, every file of the next upload is sent", async () => {
    recordCookieWrites();
    parseToken(fakeJwt());
    logout(); // nothing was uploading
    parseToken(fakeJwt()); // logs in again on the same tab

    // upload.handleFiles() queues each file of a selection in turn
    for (const name of ["a.pdf", "b.pdf", "c.pdf"]) {
      useUploadStore().upload(
        `/files/${name}`,
        name,
        new File(["data"], name),
        false,
        "blob"
      );
    }
    await new Promise((r) => setTimeout(r, 0));

    const sent = h.post.mock.calls.map((c: unknown[]) => c[0]);
    console.log("api.post paths:", JSON.stringify(sent));
    expect(sent).toEqual(["/files/a.pdf", "/files/b.pdf", "/files/c.pdf"]);
  });
});
