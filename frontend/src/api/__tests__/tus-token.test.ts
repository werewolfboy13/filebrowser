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
import {
  afterAll,
  beforeAll,
  beforeEach,
  describe,
  expect,
  it,
  vi,
} from "vitest";
import {
  startTusServer,
  useServerOrigin,
  type TusServer,
} from "./tus-loopback";

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

beforeAll(async () => {
  server = await startTusServer();
  useServerOrigin(server);
  tus = await import("@/api/tus");
});
afterAll(() => server.close());

beforeEach(() => {
  server.reset();
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

  it("a response asking for renewal (X-Renew-Token: true) renews the session", async () => {
    server.extraHeaders = {
      "X-Renew-Token": "true",
      "Access-Control-Expose-Headers": "X-Renew-Token",
    };
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
