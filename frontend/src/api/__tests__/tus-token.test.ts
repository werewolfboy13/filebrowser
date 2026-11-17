// Node-style test (the tus client is mocked; no network).
//
// The tus client was given the JWT once, as a static header, when an
// upload started. Token renewals made by the app during the upload never
// reached later PATCH requests, and the X-Renew-Token header on tus
// responses was ignored, so an upload that outlived the token it started
// with failed part-way with 401.
import { beforeEach, describe, expect, it, vi } from "vitest";

const captured = vi.hoisted(() => ({ options: null as any }));
vi.hoisted(() => {
  (globalThis as any).window = {
    FileBrowser: {
      BaseURL: "",
      TusSettings: { chunkSize: 1024, retryCount: 1 },
    },
    location: { origin: "https://fb.test" },
  };
});
vi.mock("tus-js-client", () => ({
  isSupported: true,
  DetailedError: class extends Error {},
  Upload: class {
    options: any;
    constructor(_file: unknown, options: any) {
      this.options = options;
      captured.options = options;
    }
    start() {}
    abort() {}
  },
}));
const auth = vi.hoisted(() => ({ jwt: "TOKEN-AT-START" }));
vi.mock("@/stores/auth", () => ({ useAuthStore: () => auth }));
const renew = vi.hoisted(() => vi.fn().mockResolvedValue(undefined));
vi.mock("@/utils/auth", () => ({ renew }));
vi.mock("@/api/utils", () => ({
  removePrefix: (u: string) => u.replace(/^\/files/, ""),
}));

import { upload } from "@/api/tus";

// the headers a request made now would carry
function nextRequestHeaders() {
  const headers: Record<string, string> = { ...captured.options.headers };
  captured.options.onBeforeRequest?.({
    setHeader: (k: string, v: string) => (headers[k] = v),
  });
  return headers;
}

describe("tus uploads follow token renewals", () => {
  beforeEach(() => {
    auth.jwt = "TOKEN-AT-START";
    renew.mockClear();
    void upload("/files/big.iso", new Blob(["x"]) as any, false, () => {});
  });

  it("requests made after the session was renewed carry the new token", () => {
    expect(nextRequestHeaders()["X-Auth"]).toBe("TOKEN-AT-START");
    auth.jwt = "RENEWED-TOKEN"; // e.g. fetchURL -> renew -> parseToken
    const headers = nextRequestHeaders();
    console.log("X-Auth on a later tus request:", headers["X-Auth"]);
    expect(headers["X-Auth"]).toBe("RENEWED-TOKEN");
  });

  it("a tus response asking for renewal (X-Renew-Token: true) renews the session", async () => {
    const res = {
      getHeader: (k: string) => (k === "X-Renew-Token" ? "true" : undefined),
    };
    await captured.options.onAfterResponse?.({}, res);
    expect(renew).toHaveBeenCalledTimes(1);
    expect(renew).toHaveBeenCalledWith("TOKEN-AT-START");
  });

  it("other responses do not renew", async () => {
    await captured.options.onAfterResponse?.({}, { getHeader: () => null });
    expect(renew).not.toHaveBeenCalled();
  });

  it("after logout (no token) requests are not stamped with an empty token", () => {
    auth.jwt = "";
    expect(nextRequestHeaders()["X-Auth"]).toBe("TOKEN-AT-START");
  });
});
