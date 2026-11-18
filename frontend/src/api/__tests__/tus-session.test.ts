// @vitest-environment jsdom
//
// A tus request answered 401 (the session revoked elsewhere, or expired)
// was retried like any other failure (HEAD, then POST after POST), and the
// upload then failed with a raw "401 Unauthorized" toast while the tab
// stayed logged in. Now a 401 for the token that is still the tab's
// current one is not retried and ends the session, as fetchURL does for
// other API calls. A 401 for a token replaced since (e.g. this tab changed
// its password meanwhile) is retried as before: the retry goes out with
// the new token. Real tus-js-client browser stack against a loopback server
// (see tus-loopback.ts; no network).
import { beforeEach, describe, expect, it, vi } from "vitest";
import { loopbackPerTest, type TusServer } from "./tus-loopback";

vi.hoisted(() => {
  (window as any).FileBrowser = {
    BaseURL: "",
    StaticURL: "/static",
    // retries on: 3 attempts, the first at once
    TusSettings: { chunkSize: 4, retryCount: 3 },
  };
});
const auth = vi.hoisted(() => ({ jwt: "old.tok.en" }));
const logout = vi.hoisted(() => vi.fn());
vi.mock("@/stores/auth", () => ({ useAuthStore: () => auth }));
vi.mock("@/utils/auth", () => ({ renew: vi.fn(), logout }));
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
  auth.jwt = "old.tok.en";
  logout.mockClear();
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
    (e: any) => `rejected: ${e.status}`
  );
const methods = () => server.seen.map((r) => r.method);
const unauthorized = (method: string) => () =>
  server.once((r) =>
    r.method === method
      ? { status: 401, body: "401 Unauthorized\n" }
      : undefined
  );

describe("tus requests answered 401", () => {
  it("a chunk refused for the current token: no retry, the session ends", async () => {
    unauthorized("PATCH")();
    const result = await send();
    console.log("requests:", JSON.stringify(methods()), "|", result);
    expect(result).toBe("rejected: 401");
    expect(methods()).toEqual(["POST", "PATCH"]);
    expect(logout).toHaveBeenCalledTimes(1);
    expect(logout).toHaveBeenCalledWith(undefined, "old.tok.en");
  });

  it("a create refused for the current token: no retry, the session ends", async () => {
    unauthorized("POST")();
    const result = await send();
    console.log("requests:", JSON.stringify(methods()), "|", result);
    expect(result).toBe("rejected: 401");
    expect(methods()).toEqual(["POST"]);
    expect(logout).toHaveBeenCalledWith(undefined, "old.tok.en");
  });

  it("a 401 for a token replaced since is retried with the new one", async () => {
    server.once((r) => {
      if (r.method !== "PATCH") return undefined;
      // this tab stored a new token while the chunk was on its way
      auth.jwt = "new.tok.en";
      return { status: 401, body: "401 Unauthorized\n" };
    });
    const result = await send();
    console.log("requests:", JSON.stringify(methods()), "|", result);
    expect(result).toBe("resolved");
    expect(logout).not.toHaveBeenCalled();
    expect(server.seen.at(-1)?.xAuth).toBe("new.tok.en");
  });
});
