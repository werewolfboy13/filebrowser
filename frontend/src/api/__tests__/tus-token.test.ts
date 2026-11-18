// @vitest-environment jsdom
//
// tus uploads and the session token, on the real tus-js-client browser stack
// (XMLHttpRequest) against a loopback tus server (see tus-loopback.ts; no
// network).
//
// - An upload can outlive the token it started with: every request (create,
//   HEAD, PATCH, terminate) must carry the current token, and an
//   X-Renew-Token: true response must renew the session.
// - Each request must carry the token exactly once. tus-js-client applies
//   options.headers with setHeader and then calls onBeforeRequest; setting
//   X-Auth in both made XMLHttpRequest combine them into "tok, tok", which
//   the server rejects, so every upload from the web interface failed with
//   401. A model of setHeader as an overwrite cannot catch that; jsdom's
//   spec XMLHttpRequest combines repeated headers as browsers do.
import { beforeEach, describe, expect, it, vi } from "vitest";
import { loopbackPerTest, type TusServer } from "./tus-loopback";

vi.hoisted(() => {
  (window as any).FileBrowser = {
    BaseURL: "",
    StaticURL: "/static",
    TusSettings: { chunkSize: 4, retryCount: 1 },
  };
});
const TOKEN = "aaa.bbb.ccc";
const RENEWED = "ddd.eee.fff";
const auth = vi.hoisted(() => ({ jwt: "" }));
vi.mock("@/stores/auth", () => ({ useAuthStore: () => auth }));
const renew = vi.hoisted(() => vi.fn().mockResolvedValue(undefined));
vi.mock("@/utils/auth", () => ({ renew }));
// what the browser bundle gets (package.json "browser" field)
vi.mock("tus-js-client", async () =>
  vi.importActual("tus-js-client/lib.esm/browser/index.js")
);
vi.mock("@/api/utils", () => ({
  removePrefix: (u: string) => u.replace(/^\/files/, ""),
}));

let server: TusServer;
let tus: typeof import("@/api/tus");
const loopback = loopbackPerTest(() => import("@/api/tus"));

beforeEach(() => {
  ({ server, mod: tus } = loopback);
  renew.mockClear();
  auth.jwt = TOKEN;
});

const settle = (p: unknown) =>
  (p as Promise<unknown>).then(
    () => "resolved",
    (e: Error) => "rejected: " + e.message
  );

const send = (name = "big.bin", data = "0123456789") =>
  settle(tus.upload(`/files/${name}`, new Blob([data]), false, () => {}));

const requests = () =>
  server.seen.map((r) => `${r.method} ${r.xAuth} (${r.xAuthLines})`);

async function until(cond: () => boolean) {
  for (let i = 0; i < 200 && !cond(); i++) {
    await new Promise((r) => setTimeout(r, 10));
  }
}

describe("tus uploads and the session token (real tus-js-client, jsdom XHR)", () => {
  it("every request of an upload carries the token exactly once", async () => {
    const result = await send();
    console.log("requests:", JSON.stringify(requests()), "|", result);
    expect(result).toBe("resolved");
    expect(server.seen.map((r) => r.method)).toEqual([
      "POST",
      "PATCH",
      "PATCH",
      "PATCH",
    ]);
    for (const r of server.seen) {
      expect(r.xAuth).toBe(TOKEN);
      expect(r.xAuthLines).toBe(1);
    }
  });

  it("requests made after the session was renewed carry the new token", async () => {
    server.onRequest = (r) => {
      if (r.method === "POST") auth.jwt = RENEWED; // fetchURL -> renew -> parseToken
    };
    const result = await send();
    console.log("requests:", JSON.stringify(requests()));
    expect(result).toBe("resolved");
    expect(server.seen[0].xAuth).toBe(TOKEN);
    expect(server.seen.slice(1).map((r) => r.xAuth)).toEqual([
      RENEWED,
      RENEWED,
      RENEWED,
    ]);
  });

  it("the HEAD that resumes after a failed request carries the token once", async () => {
    server.once((r) =>
      r.method === "PATCH" ? { status: 500, body: "boom" } : undefined
    );
    const result = await send();
    console.log("requests:", JSON.stringify(requests()));
    expect(result).toBe("resolved");
    const head = server.seen.find((r) => r.method === "HEAD");
    expect(head?.xAuth).toBe(TOKEN);
    expect(head?.xAuthLines).toBe(1);
  });

  it("a chunk answered 423 (busy) is retried", async () => {
    server.once((r) =>
      r.method === "PATCH" ? { status: 423, body: "423 Locked" } : undefined
    );
    expect(await send()).toBe("resolved");
  });

  it("a create answered 409 is not retried and fails with status and method", async () => {
    server.once((r) =>
      r.method === "POST" ? { status: 409, body: "409 Conflict" } : undefined
    );
    vi.spyOn(console, "error").mockImplementation(() => {});
    const err = await (
      tus.upload(
        "/files/c.bin",
        new Blob(["x"]),
        false,
        () => {}
      ) as Promise<unknown>
    ).catch((e) => e);
    expect([err.status, err.method]).toEqual([409, "POST"]);
    expect(server.seen.map((r) => r.method)).toEqual(["POST"]);
  });

  it("the terminate request sent on abort carries the token once", async () => {
    server.onRequest = (r) => {
      if (r.method === "PATCH") {
        // abort while the first chunk is in flight
        server.onRequest = null;
        tus.abortAllUploads();
      }
    };
    const result = await send();
    await until(() => server.seen.some((r) => r.method === "DELETE"));
    console.log("requests:", JSON.stringify(requests()), "|", result);
    expect(result).toBe("rejected: Upload aborted");
    expect(server.seen.map((r) => r.method)).toEqual([
      "POST",
      "PATCH",
      "DELETE",
    ]);
    const del = server.seen.find((r) => r.method === "DELETE");
    expect(del?.xAuth).toBe(TOKEN);
    expect(del?.xAuthLines).toBe(1);
  });

  // The terminate DELETE needs the Delete permission: without it the server
  // answers 403. That was retried like a failed chunk, and the rejection of
  // abort()'s promise was left unhandled.
  it("a refused terminate is neither retried nor left unhandled", async () => {
    const unhandled: unknown[] = [];
    const onUnhandled = (e: unknown) => unhandled.push(e);
    process.on("unhandledRejection", onUnhandled);
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    try {
      for (let i = 0; i < 3; i++) {
        server.once((r) =>
          r.method === "DELETE"
            ? { status: 403, body: "403 Forbidden" }
            : undefined
        );
      }
      server.onRequest = (r) => {
        if (r.method === "PATCH") {
          server.onRequest = null;
          tus.abortAllUploads();
        }
      };
      expect(await send()).toBe("rejected: Upload aborted");
      await until(() => server.seen.some((r) => r.method === "DELETE"));
      await new Promise((r) => setTimeout(r, 100));
      console.log("requests:", JSON.stringify(requests()));
      expect(unhandled).toEqual([]);
      expect(server.seen.filter((r) => r.method === "DELETE")).toHaveLength(1);
    } finally {
      process.off("unhandledRejection", onUnhandled);
      warn.mockRestore();
    }
  });

  it("a response asking for renewal (X-Renew-Token: true) renews the session", async () => {
    server.extraHeaders = { "X-Renew-Token": "true" };
    expect(await send("small.bin", "abc")).toBe("resolved");
    expect(renew).toHaveBeenCalled();
    expect(renew).toHaveBeenCalledWith(TOKEN);
  });

  it("other responses do not renew", async () => {
    expect(await send("small.bin", "abc")).toBe("resolved");
    expect(renew).not.toHaveBeenCalled();
  });

  it("after logout (no token) requests are not stamped with an empty token", async () => {
    server.onRequest = (r) => {
      if (r.method === "POST") auth.jwt = "";
    };
    await send();
    console.log("requests:", JSON.stringify(requests()));
    for (const r of server.seen) expect(r.xAuth).toBe(TOKEN);
  });
});
