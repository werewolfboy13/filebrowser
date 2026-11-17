// @vitest-environment jsdom
//
// How the shared fetch wrapper (fetchURL) follows the session:
//
// - A session the server revoked (password, permission or scope change) gets
//   401. The renewal on X-Renew-Token used to run first: the server sent that
//   header with the 401, /api/renew answered 401 too, and the thrown renewal
//   error skipped the logout, so the tab stayed "logged in" and showed error
//   pages. Newer servers send no X-Renew-Token with a 401; both must log out.
// - The tab that changed its own password gets the new token in an
//   X-Auth-Token response header (the old one is revoked at once). It is
//   adopted like a renewal, and a request sent earlier with the old token
//   that comes back 401 afterwards must not log that tab out.
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
const h = vi.hoisted(() => ({ push: vi.fn() }));
vi.mock("@/router", () => ({ default: { push: h.push } }));
vi.mock("@/i18n", () => ({ detectLocale: () => "en", setLocale: vi.fn() }));
vi.mock("@/stores/upload", () => ({
  useUploadStore: () => ({ abort: vi.fn() }),
}));

import { createPinia, setActivePinia } from "pinia";
import { fetchURL } from "@/api/utils";
import { parseToken } from "@/utils/auth";
import { useAuthStore } from "@/stores/auth";

const b64url = (o: object) =>
  btoa(JSON.stringify(o))
    .replace(/=+$/, "")
    .replace(/\+/g, "-")
    .replace(/\//g, "_");
const jwt = (n: number) =>
  `${b64url({ alg: "HS256" })}.${b64url({
    exp: Math.floor(Date.now() / 1000) + 3600,
    user: { id: 2, locale: "en", perm: {}, n },
  })}.sig`;
const OLD = jwt(1);
const NEW = jwt(2);
const RENEWED = jwt(3);

type Handler = (url: string, init: RequestInit) => Promise<Response>;
let calls: string[];
function stubFetch(handler: Handler) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, init: RequestInit = {}) => {
      calls.push(`${init.method ?? "GET"} ${url}`);
      return handler(url, init);
    })
  );
}
const xAuth = (init: RequestInit) =>
  (init.headers as Record<string, string>)["X-Auth"];

const outcome = (p: Promise<unknown>) =>
  p.then(
    () => "resolved",
    (e: any) => `rejected: ${e.status}`
  );

beforeEach(() => {
  vi.unstubAllGlobals();
  setActivePinia(createPinia());
  h.push.mockClear();
  calls = [];
  parseToken(OLD);
});

describe("fetchURL and a revoked session", () => {
  for (const [server, headers] of [
    ["an older server (401 with X-Renew-Token)", { "X-Renew-Token": "true" }],
    ["a newer server (401 alone)", {}],
  ] as const) {
    it(`logs out and goes to the login page, with ${server}`, async () => {
      stubFetch(async (url) =>
        url === "/api/renew"
          ? new Response("401 Unauthorized\n", { status: 401 })
          : new Response("401 Unauthorized\n", { status: 401, headers })
      );
      const result = await outcome(fetchURL("/api/resources/", {}));
      console.log(
        "requests:",
        JSON.stringify(calls),
        "| outcome:",
        result,
        "| router.push:",
        JSON.stringify(h.push.mock.calls)
      );
      expect(result).toBe("rejected: 401");
      expect(h.push).toHaveBeenCalledWith({ path: "/login" });
      expect(useAuthStore().jwt).toBe("");
      expect(calls).toEqual(["GET /api/resources/"]); // no renewal attempt
    });
  }

  it("a successful response asking for renewal still renews", async () => {
    stubFetch(async (url) =>
      url === "/api/renew"
        ? new Response(RENEWED, { status: 200 })
        : new Response("{}", {
            status: 200,
            headers: { "X-Renew-Token": "true" },
          })
    );
    expect(await outcome(fetchURL("/api/resources/", {}))).toBe("resolved");
    expect(calls).toEqual(["GET /api/resources/", "POST /api/renew"]);
    expect(useAuthStore().jwt).toBe(RENEWED);
  });

  it("a renewal that fails for want of a connection does not fail the request", async () => {
    vi.spyOn(console, "warn").mockImplementation(() => {});
    stubFetch(async (url) => {
      if (url === "/api/renew") throw new TypeError("network down");
      return new Response("{}", {
        status: 200,
        headers: { "X-Renew-Token": "true" },
      });
    });
    expect(await outcome(fetchURL("/api/resources/", {}))).toBe("resolved");
    expect(useAuthStore().jwt).toBe(OLD);
    expect(h.push).not.toHaveBeenCalled();
  });
});

describe("fetchURL and a token handed out with a response (X-Auth-Token)", () => {
  it("own password change: stays logged in with the new token, without renewing", async () => {
    stubFetch(async (url) =>
      url === "/api/users/2"
        ? new Response("", {
            status: 200,
            headers: { "X-Auth-Token": NEW, "X-Renew-Token": "true" },
          })
        : new Response("unexpected", { status: 500 })
    );
    const result = await outcome(
      fetchURL("/api/users/2", { method: "PUT", body: "{}" })
    );
    expect(result).toBe("resolved");
    expect(calls).toEqual(["PUT /api/users/2"]);
    expect(useAuthStore().jwt).toBe(NEW);
    expect(localStorage.getItem("jwt")).toBe(NEW);
    expect(h.push).not.toHaveBeenCalled();
  });

  it("a late 401 for a request sent with the old token does not log out", async () => {
    let answerOld!: (r: Response) => void;
    stubFetch((url, init) => {
      if (url === "/api/resources/") {
        expect(xAuth(init)).toBe(OLD);
        // still on its way while the password is changed
        return new Promise<Response>((r) => (answerOld = r));
      }
      return Promise.resolve(
        new Response("", { status: 200, headers: { "X-Auth-Token": NEW } })
      );
    });
    const listing = outcome(fetchURL("/api/resources/", {}));
    await fetchURL("/api/users/2", { method: "PUT", body: "{}" });
    expect(useAuthStore().jwt).toBe(NEW);

    // the server revoked the old token: the earlier request comes back 401,
    // with or without X-Renew-Token depending on the server version
    answerOld(
      new Response("401 Unauthorized\n", {
        status: 401,
        headers: { "X-Renew-Token": "true" },
      })
    );
    expect(await listing).toBe("rejected: 401");
    console.log(
      "requests:",
      JSON.stringify(calls),
      "| router.push:",
      JSON.stringify(h.push.mock.calls)
    );
    expect(h.push).not.toHaveBeenCalled();
    expect(useAuthStore().jwt).toBe(NEW);
    expect(calls).not.toContain("POST /api/renew");
  });

  it("control: a 401 for the current token after the change logs out", async () => {
    stubFetch(async (url) =>
      url === "/api/users/2"
        ? new Response("", { status: 200, headers: { "X-Auth-Token": NEW } })
        : new Response("401 Unauthorized\n", { status: 401 })
    );
    await fetchURL("/api/users/2", { method: "PUT", body: "{}" });
    expect(await outcome(fetchURL("/api/resources/", {}))).toBe(
      "rejected: 401"
    );
    expect(h.push).toHaveBeenCalledWith({ path: "/login" });
  });
});

describe("fetchURL and Retry-After", () => {
  it("a 429 error carries the wait the server asks for", async () => {
    stubFetch(
      async () =>
        new Response("429 Too Many Requests\n", {
          status: 429,
          headers: { "Retry-After": "17" },
        })
    );
    const err = await fetchURL("/api/users/2", { method: "PUT" }).catch(
      (e) => e
    );
    expect(err.status).toBe(429);
    expect(err.retryAfter).toBe(17);
    expect(h.push).not.toHaveBeenCalled();
  });
});
