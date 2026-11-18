// @vitest-environment jsdom
//
// renew() is called for every response that carries X-Renew-Token (API
// calls, and now tus upload requests). Concurrent calls for the same token
// share one POST /api/renew instead of each issuing their own.
import { describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  (window as any).FileBrowser = {
    AuthMethod: "json",
    BaseURL: "",
    StaticURL: "/static",
    LoginPage: true,
    LogoutPage: "/login",
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

import { createPinia, setActivePinia } from "pinia";
import { renew } from "@/utils/auth";
import { useAuthStore } from "@/stores/auth";

const b64url = (o: object) =>
  btoa(JSON.stringify(o))
    .replace(/=+$/, "")
    .replace(/\+/g, "-")
    .replace(/\//g, "_");
// fixed expiry, so the same n always gives the same token
const exp = Math.floor(Date.now() / 1000) + 3600;
const jwt = (n: number) =>
  `${b64url({ alg: "HS256" })}.${b64url({
    exp,
    user: { id: 1, locale: "en", n },
  })}.sig`;

describe("renew()", () => {
  it("shares one request between concurrent calls for the same token", async () => {
    setActivePinia(createPinia());
    let n = 0;
    const fetchSpy = vi.fn(async () => new Response(jwt(++n), { status: 200 }));
    vi.stubGlobal("fetch", fetchSpy);

    await Promise.all([renew("OLD"), renew("OLD"), renew("OLD")]);
    expect(fetchSpy).toHaveBeenCalledTimes(1);
    expect(useAuthStore().jwt).toBe(jwt(1));

    // a later renewal is a new request
    await renew(useAuthStore().jwt);
    expect(fetchSpy).toHaveBeenCalledTimes(2);
    vi.unstubAllGlobals();
  });

  it("a failed renewal is reported to every caller and not cached", async () => {
    setActivePinia(createPinia());
    const fetchSpy = vi.fn(
      async () => new Response("expired", { status: 401 })
    );
    vi.stubGlobal("fetch", fetchSpy);
    const results = await Promise.allSettled([renew("BAD"), renew("BAD")]);
    expect(results.map((r) => r.status)).toEqual(["rejected", "rejected"]);
    expect(fetchSpy).toHaveBeenCalledTimes(1);
    await expect(renew("BAD")).rejects.toThrow();
    expect(fetchSpy).toHaveBeenCalledTimes(2);
    vi.unstubAllGlobals();
  });
});
