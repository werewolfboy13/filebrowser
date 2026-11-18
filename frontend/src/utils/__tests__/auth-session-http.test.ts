// @vitest-environment jsdom
// @vitest-environment-options { "url": "http://fb.test/files/" }
//
// Over plain http the auth cookie cannot be Secure (the browser would
// drop it), so it is set without it.
import { describe, expect, it, vi } from "vitest";

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
  default: { push: vi.fn(), currentRoute: currentRoute },
}));
vi.mock("@/i18n", () => ({ detectLocale: () => "en", setLocale: vi.fn() }));
vi.mock("@/stores/upload", () => ({
  useUploadStore: () => ({ abort: vi.fn() }),
}));

import { createPinia, setActivePinia } from "pinia";
import { parseToken } from "@/utils/auth";
import { fakeJwt, recordCookieWrites } from "./auth-session.shared";

describe("auth session over http", () => {
  it("does not mark the auth cookie Secure", () => {
    setActivePinia(createPinia());
    const writes = recordCookieWrites();
    parseToken(fakeJwt());
    expect(writes).toHaveLength(1);
    expect(writes[0]).not.toMatch(/Secure/i);
    expect(writes[0]).toMatch(/^auth=.+; Path=\/; SameSite=Strict;/);
  });
});
