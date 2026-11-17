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
  abort: vi.fn(),
  jwtAtAbort: null as string | null,
}));
vi.mock("@/router", () => ({ default: { push: h.push } }));
vi.mock("@/i18n", () => ({ detectLocale: () => "en", setLocale: vi.fn() }));
vi.mock("@/stores/upload", () => ({
  useUploadStore: () => ({ abort: h.abort }),
}));

import { createPinia, setActivePinia } from "pinia";
import { logout, parseToken } from "@/utils/auth";
import { useAuthStore } from "@/stores/auth";
import { useClipboardStore } from "@/stores/clipboard";
import { useFileStore } from "@/stores/file";
import { useLayoutStore } from "@/stores/layout";
import { fakeJwt, recordCookieWrites } from "./auth-session.shared";

describe("auth session over https", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    vi.clearAllMocks();
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
    h.abort.mockImplementation(() => (h.jwtAtAbort = useAuthStore().jwt));

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

    expect(h.abort).toHaveBeenCalledTimes(1);
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
});
