// Node-style test (tus client and XMLHttpRequest are fakes; no network).
//
// "Abort" in the upload panel only cancelled tus uploads, and only one per
// path: uploads that fall back to a plain XHR POST (tus disabled or not
// applicable) kept running, and of two concurrent tus uploads to the same
// path only the later one was tracked.
import { beforeEach, describe, expect, it, vi } from "vitest";

const h = vi.hoisted(() => ({
  tusUploads: [] as any[],
  xhrs: [] as any[],
  useTus: true,
}));
vi.hoisted(() => {
  (globalThis as any).window = {
    FileBrowser: {
      BaseURL: "",
      TusSettings: { chunkSize: 1024, retryCount: 0 },
    },
    location: { origin: "https://fb.test", protocol: "https:" },
  };
  (globalThis as any).XMLHttpRequest = class {
    upload: any = {};
    onload: any;
    onerror: any;
    onabort: any;
    onloadend: any;
    aborted = false;
    constructor() {
      h.xhrs.push(this);
    }
    open() {}
    setRequestHeader() {}
    send() {}
    abort() {
      this.aborted = true;
      this.onabort?.();
      this.onloadend?.();
    }
  };
});
vi.mock("tus-js-client", () => ({
  isSupported: true,
  DetailedError: class extends Error {},
  Upload: class {
    options: any;
    aborted = false;
    constructor(_file: unknown, options: any) {
      this.options = options;
      h.tusUploads.push(this);
    }
    start() {}
    abort() {
      this.aborted = true;
    }
  },
}));
vi.mock("@/stores/auth", () => ({ useAuthStore: () => ({ jwt: "T" }) }));
vi.mock("@/stores/layout", () => ({ useLayoutStore: () => ({}) }));
vi.mock("@/utils/auth", () => ({ renew: vi.fn() }));
vi.mock("@/api/utils", () => ({
  removePrefix: (u: string) => u.replace(/^\/files/, ""),
  createURL: vi.fn(),
  fetchURL: vi.fn(),
  StatusError: class extends Error {},
}));
vi.mock("@/utils/encodings", () => ({
  isEncodableResponse: () => false,
  makeRawResource: vi.fn(),
}));

import * as tus from "@/api/tus";
import * as files from "@/api/files";

const settle = (p: Promise<unknown>) =>
  p.then(
    () => "resolved",
    (e: Error) => e.message
  );

describe("aborting uploads", () => {
  beforeEach(() => {
    h.tusUploads.length = 0;
    h.xhrs.length = 0;
  });

  it("aborts every tus upload, including two to the same path", async () => {
    const a = settle(
      tus.upload("/files/x.bin", new Blob(["a"]), false, null) as any
    );
    const b = settle(
      tus.upload("/files/x.bin", new Blob(["b"]), true, null) as any
    );
    tus.abortAllUploads();
    console.log(
      "tus aborted:",
      h.tusUploads.map((u) => u.aborted)
    );
    expect(h.tusUploads.map((u) => u.aborted)).toEqual([true, true]);
    expect(await a).toBe("Upload aborted");
    expect(await b).toBe("Upload aborted");
  });

  it("aborts uploads that use the plain XHR fallback", async () => {
    const blob = new Blob(["x"]);
    vi.spyOn(tus, "useTus").mockResolvedValue(false);
    const p = settle(files.post("/files/plain.bin", blob, false, () => {}));
    await new Promise((r) => setTimeout(r, 0)); // let post() open the XHR
    expect(h.xhrs).toHaveLength(1);

    files.abortResourceUploads();
    console.log("xhr aborted:", h.xhrs[0].aborted);
    expect(h.xhrs[0].aborted).toBe(true);
    expect(await p).toBe("Upload aborted");
  });
});
