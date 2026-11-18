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
import { logout, parseToken, renew } from "@/utils/auth";
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

function deferred<T>() {
  let resolve!: (v: T) => void;
  const promise = new Promise<T>((r) => (resolve = r));
  return { promise, resolve };
}
async function until(cond: () => boolean) {
  for (let i = 0; i < 50 && !cond(); i++) {
    await new Promise((r) => setTimeout(r, 0));
  }
  expect(cond()).toBe(true);
}
const asksRenewal = () =>
  new Response("{}", { status: 200, headers: { "X-Renew-Token": "true" } });

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

  // The server hands a token out only with the 200 of the user's own PUT;
  // an error answer that carries one (a proxy, a bug) is not a session.
  it("a token on an error answer is not adopted", async () => {
    stubFetch(
      async () =>
        new Response("500 Internal Server Error\n", {
          status: 500,
          headers: { "X-Auth-Token": NEW },
        })
    );
    expect(
      await outcome(fetchURL("/api/users/2", { method: "PUT", body: "{}" }))
    ).toBe("rejected: 500");
    console.log(
      "token after a 500 carrying X-Auth-Token:",
      useAuthStore().jwt === NEW ? "NEW" : "OLD"
    );
    expect(useAuthStore().jwt).toBe(OLD);
    expect(localStorage.getItem("jwt")).toBe(OLD);
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

// A renewal, or a token handed out with an answer, speaks for the session
// as it was when the request went out. Each newly stored token and each
// logout starts a new epoch:
// - a renewal is applied only if the epoch is unchanged since it was asked
//   for (it would otherwise log a logged-out tab back in, or put back a
//   token that a password change has revoked);
// - a handed-out token (X-Auth-Token) is adopted unless the tab logged out
//   since the request was sent: it is newer than any renewal in between,
//   which the change that handed it out has revoked.
describe("fetchURL, renewals and handed-out tokens racing each other", () => {
  it("a renewal answered after logout does not log the tab back in", async () => {
    const renewAnswer = deferred<Response>();
    stubFetch((url) =>
      url === "/api/renew"
        ? renewAnswer.promise
        : Promise.resolve(new Response("{}"))
    );
    const pending = renew(OLD).catch(() => {});
    await until(() => calls.includes("POST /api/renew"));
    logout();
    renewAnswer.resolve(new Response(RENEWED, { status: 200 }));
    await pending;
    console.log(
      "after logout, late renewal:",
      JSON.stringify({
        jwt: useAuthStore().jwt === RENEWED ? "RENEWED" : useAuthStore().jwt,
        user: useAuthStore().user !== null,
        ls: localStorage.getItem("jwt") === RENEWED ? "RENEWED" : "",
      })
    );
    expect(useAuthStore().jwt).toBe("");
    expect(useAuthStore().user).toBeNull();
    expect(localStorage.getItem("jwt")).toBe("");
  });

  it("nor replaces the session of whoever logged in next on this tab", async () => {
    const renewAnswer = deferred<Response>();
    stubFetch((url) =>
      url === "/api/renew"
        ? renewAnswer.promise
        : Promise.resolve(new Response("{}"))
    );
    const pending = renew(OLD).catch(() => {});
    await until(() => calls.includes("POST /api/renew"));
    logout();
    parseToken(NEW); // the next login
    renewAnswer.resolve(new Response(RENEWED, { status: 200 }));
    await pending;
    expect(useAuthStore().jwt).toBe(NEW);
  });

  it("a renewal answered after a token was handed out does not replace it", async () => {
    const renewAnswer = deferred<Response>();
    stubFetch((url) => {
      if (url === "/api/renew") return renewAnswer.promise;
      if (url === "/api/users/2") {
        return Promise.resolve(
          new Response("", { status: 200, headers: { "X-Auth-Token": NEW } })
        );
      }
      return Promise.resolve(asksRenewal());
    });
    const listing = outcome(fetchURL("/api/resources/", {}));
    await until(() => calls.includes("POST /api/renew"));
    await fetchURL("/api/users/2", { method: "PUT", body: "{}" });
    renewAnswer.resolve(new Response(RENEWED, { status: 200 }));
    expect(await listing).toBe("resolved");
    console.log(
      "token after the late renewal:",
      useAuthStore().jwt === NEW ? "NEW" : "RENEWED (revoked)"
    );
    expect(useAuthStore().jwt).toBe(NEW);
    expect(localStorage.getItem("jwt")).toBe(NEW);
  });

  it("a token handed out after a renewal replaced the old one is adopted", async () => {
    const putAnswer = deferred<Response>();
    stubFetch((url) => {
      if (url === "/api/users/2") return putAnswer.promise;
      if (url === "/api/renew") {
        return Promise.resolve(new Response(RENEWED, { status: 200 }));
      }
      return Promise.resolve(asksRenewal());
    });
    const put = outcome(
      fetchURL("/api/users/2", { method: "PUT", body: "{}" })
    );
    await fetchURL("/api/resources/", {}); // renews while the PUT is out
    expect(useAuthStore().jwt).toBe(RENEWED);
    putAnswer.resolve(
      new Response("", { status: 200, headers: { "X-Auth-Token": NEW } })
    );
    expect(await put).toBe("resolved");
    console.log(
      "token after the PUT:",
      useAuthStore().jwt === NEW ? "NEW" : "RENEWED (NEW dropped)"
    );
    expect(useAuthStore().jwt).toBe(NEW);
  });

  it("a token handed out to a request sent before logout is not adopted", async () => {
    const putAnswer = deferred<Response>();
    stubFetch(() => putAnswer.promise);
    const put = outcome(
      fetchURL("/api/users/2", { method: "PUT", body: "{}" })
    );
    await until(() => calls.includes("PUT /api/users/2"));
    logout();
    putAnswer.resolve(
      new Response("", { status: 200, headers: { "X-Auth-Token": NEW } })
    );
    await put;
    expect(useAuthStore().jwt).toBe("");
    expect(localStorage.getItem("jwt")).toBe("");
  });

  it("a successful request whose renewal is refused after the token changed succeeds", async () => {
    const renewAnswer = deferred<Response>();
    stubFetch((url) => {
      if (url === "/api/renew") return renewAnswer.promise;
      if (url === "/api/users/2") {
        return Promise.resolve(
          new Response("", { status: 200, headers: { "X-Auth-Token": NEW } })
        );
      }
      return Promise.resolve(asksRenewal());
    });
    const save = outcome(fetchURL("/api/resources/a.txt", { method: "PUT" }));
    await until(() => calls.includes("POST /api/renew"));
    await fetchURL("/api/users/2", { method: "PUT", body: "{}" });
    // the password change revoked the token the renewal was asked for
    renewAnswer.resolve(new Response("401 Unauthorized\n", { status: 401 }));
    const result = await save;
    console.log(
      "save outcome:",
      result,
      "| logged out:",
      h.push.mock.calls.length > 0
    );
    expect(result).toBe("resolved");
    expect(h.push).not.toHaveBeenCalled();
    expect(useAuthStore().jwt).toBe(NEW);
  });
});

// Every tab shares the auth cookie and the stored token. After tab A
// changed the password (and stored its new token), tab B's next request
// is refused (its token is revoked) and tab B logs out: that must not
// clear what tab A stored, or tab A's previews and downloads (sent with
// the cookie) fail and reloading it lands on the login page.
describe("two tabs sharing the cookie and the stored token", () => {
  const cookieToken = () =>
    document.cookie
      .split("; ")
      .find((c) => c.startsWith("auth="))
      ?.slice("auth=".length) ?? "";
  const name = (t: string | null) =>
    t === NEW ? "NEW" : t === OLD ? "OLD" : JSON.stringify(t);

  it("the other tab's forced logout leaves the token this tab stored", async () => {
    const tabB = createPinia();
    const tabA = createPinia();
    setActivePinia(tabB);
    parseToken(OLD);
    setActivePinia(tabA);
    parseToken(OLD);
    stubFetch(async (url, init) => {
      if (url === "/api/users/2") {
        return new Response("", {
          status: 200,
          headers: { "X-Auth-Token": NEW },
        });
      }
      return xAuth(init) === NEW
        ? new Response("{}")
        : new Response("401 Unauthorized\n", { status: 401 });
    });

    await fetchURL("/api/users/2", { method: "PUT", body: "{}" }); // tab A
    setActivePinia(tabB);
    expect(await outcome(fetchURL("/api/resources/", {}))).toBe(
      "rejected: 401"
    );
    console.log(
      "after tab B's 401:",
      JSON.stringify({
        tabB: name(useAuthStore().jwt),
        cookie: name(cookieToken()),
        ls: name(localStorage.getItem("jwt")),
      })
    );
    // tab B is logged out...
    expect(useAuthStore().jwt).toBe("");
    expect(h.push).toHaveBeenCalledWith({ path: "/login" });
    // ...and what tab A stored is still there
    expect(localStorage.getItem("jwt")).toBe(NEW);
    expect(cookieToken()).toBe(NEW);
    setActivePinia(tabA);
    expect(useAuthStore().jwt).toBe(NEW);
  });

  it("a forced logout still clears them when they hold this tab's token", async () => {
    stubFetch(async () => new Response("401 Unauthorized\n", { status: 401 }));
    await outcome(fetchURL("/api/resources/", {}));
    expect(useAuthStore().jwt).toBe("");
    expect(localStorage.getItem("jwt")).toBe("");
    expect(cookieToken()).toBe("");
  });

  it("logging out on purpose clears them whatever they hold", () => {
    const tabB = createPinia();
    setActivePinia(tabB);
    parseToken(OLD);
    localStorage.setItem("jwt", NEW); // stored by another tab
    logout();
    expect(localStorage.getItem("jwt")).toBe("");
  });
});
