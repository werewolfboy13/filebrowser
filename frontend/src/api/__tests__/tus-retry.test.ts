// @vitest-environment jsdom
//
// The server answers 423 Locked to a tus request for an upload that another
// request is still working on (http/tus_handlers.go: tusAcquire), e.g. a
// chunk that is retried while the server still writes the first attempt.
// That is meant to be retried. Here the administrator has turned retries
// off (retryCount 0), which also turned off the retry of 423: the upload
// failed. Real tus-js-client browser stack against a loopback server (see
// tus-loopback.ts; no network).
import { beforeEach, describe, expect, it, vi } from "vitest";
import { loopbackPerTest, type TusServer } from "./tus-loopback";

vi.hoisted(() => {
  (window as any).FileBrowser = {
    BaseURL: "",
    StaticURL: "/static",
    TusSettings: { chunkSize: 4, retryCount: 0 },
  };
});
const auth = vi.hoisted(() => ({ jwt: "aaa.bbb.ccc" }));
vi.mock("@/stores/auth", () => ({ useAuthStore: () => auth }));
vi.mock("@/utils/auth", () => ({ renew: vi.fn() }));
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
  vi.spyOn(console, "error").mockImplementation(() => {});
});

const send = () =>
  (
    tus.upload(
      "/files/big.bin",
      new Blob(["0123456789"]),
      false,
      () => {}
    ) as Promise<unknown>
  ).then(
    () => "resolved",
    (e: Error) => "rejected: " + e.message.trim()
  );
const methods = () => server.seen.map((r) => r.method);
const locked = (method: string) => () =>
  server.once((r) =>
    r.method === method ? { status: 423, body: "423 Locked\n" } : undefined
  );

describe("tus uploads with retries turned off", () => {
  it("a create answered 423 is retried", async () => {
    locked("POST")();
    const result = await send();
    console.log("requests:", JSON.stringify(methods()), "|", result);
    expect(result).toBe("resolved");
  });

  it("a chunk answered 423 is retried", async () => {
    locked("PATCH")();
    const result = await send();
    console.log("requests:", JSON.stringify(methods()), "|", result);
    expect(result).toBe("resolved");
    expect(methods()).toContain("HEAD");
  });

  it("repeated 423s are retried until the request goes through", async () => {
    for (let i = 0; i < 2; i++) locked("POST")();
    const err = await (
      tus.upload(
        "/files/b.bin",
        new Blob(["x"]),
        false,
        () => {}
      ) as Promise<unknown>
    ).catch((e) => e);
    // two 423s, then the third attempt goes through
    expect(methods().slice(0, 3)).toEqual(["POST", "POST", "POST"]);
    expect(err).toBeUndefined();
  }, 10_000);

  it("other failures are still not retried, and keep their status", async () => {
    server.once((r) =>
      r.method === "PATCH" ? { status: 500, body: "boom" } : undefined
    );
    const err = await (
      tus.upload(
        "/files/big.bin",
        new Blob(["0123456789"]),
        false,
        () => {}
      ) as Promise<unknown>
    ).catch((e) => e);
    expect(err.message).toBe("boom");
    expect(err.status).toBe(500);
    expect(err.method).toBe("PATCH");
    expect(methods()).toEqual(["POST", "PATCH"]);
  });
});
