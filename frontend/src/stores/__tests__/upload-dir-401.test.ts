// @vitest-environment jsdom
//
// A folder in an upload is created with a plain XHR POST (api/files.ts
// postResources), which had no 401 handling, unlike fetchURL and tus: on a
// revoked session its 401 was shown as a raw "401 Unauthorized" toast and
// the session was not ended (an upload of folders only never logged out).
// Now, as there, a 401 for the token that is still the tab's ends the
// session (whose logout aborts the uploads and says how many were
// cancelled: no toast), and a 401 for a token replaced since does not.
//
// Real upload store and api/files.ts; XMLHttpRequest stubbed to answer 401.
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  (window as any).FileBrowser = {
    AuthMethod: "json",
    BaseURL: "",
    StaticURL: "/static",
    Name: "",
    LoginPage: true,
    LogoutPage: "/login",
    TusSettings: { chunkSize: 4, retryCount: 0 },
  };
});
const h = vi.hoisted(() => ({
  logout: vi.fn(),
  // what logout() does to the uploads (see utils/auth)
  onLogout: (() => {}) as () => void,
  showError: vi.fn(),
  onSend: (() => {}) as () => void,
}));
vi.mock("@/utils/auth", () => ({
  logout: (...args: unknown[]) => {
    h.logout(...args);
    h.onLogout();
  },
  renew: vi.fn(),
  parseToken: vi.fn(),
}));
vi.mock("@/utils/buttons", () => ({
  default: { loading: vi.fn(), success: vi.fn(), done: vi.fn() },
}));

import { flushPromises } from "@vue/test-utils";
import { createApp } from "vue";
import { createPinia, setActivePinia } from "pinia";
import { useAuthStore } from "@/stores/auth";
import { useUploadStore } from "@/stores/upload";

class Xhr401 {
  status = 0;
  responseText = "";
  upload: any = {};
  headers: Record<string, string> = {};
  onload: (() => void) | null = null;
  onloadend: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onabort: (() => void) | null = null;
  open() {}
  setRequestHeader(name: string, value: string) {
    this.headers[name] = value;
  }
  abort() {}
  send() {
    sent.push(this.headers["X-Auth"]);
    h.onSend();
    setTimeout(() => {
      this.status = 401;
      this.responseText = "401 Unauthorized\n";
      this.onload?.();
      this.onloadend?.();
    }, 0);
  }
}
let sent: string[] = [];

describe("a folder's creation answered 401", () => {
  beforeEach(() => {
    const app = createApp({});
    app.provide("$showError", h.showError);
    const pinia = createPinia();
    app.use(pinia);
    setActivePinia(pinia);
    h.logout.mockClear();
    h.showError.mockClear();
    h.onSend = () => {};
    h.onLogout = () => useUploadStore().abort();
    sent = [];
    (globalThis as any).XMLHttpRequest = Xhr401;
    useAuthStore().jwt = "revoked.tok.en";
  });

  it("for the current token: ends the session, without a raw toast", async () => {
    useUploadStore().upload("/files/docs/sub/", "sub", null, false, "dir");
    await vi.waitFor(() => expect(h.logout).toHaveBeenCalled());
    expect(sent).toEqual(["revoked.tok.en"]);
    expect(h.logout).toHaveBeenCalledTimes(1);
    expect(h.logout).toHaveBeenCalledWith(undefined, "revoked.tok.en");
    // the uploads were aborted with the session: the folder's error is not
    // shown on top of the login page's message
    await flushPromises();
    expect(h.showError).not.toHaveBeenCalled();
  });

  it("for a token replaced since: does not end the session", async () => {
    // e.g. this tab changed its own password while the request was out
    h.onSend = () => {
      useAuthStore().jwt = "new.tok.en";
    };
    useUploadStore().upload("/files/docs/sub/", "sub", null, false, "dir");
    await vi.waitFor(() => expect(h.showError).toHaveBeenCalled());
    expect(sent).toEqual(["revoked.tok.en"]);
    expect(h.logout).not.toHaveBeenCalled();
  });
});
