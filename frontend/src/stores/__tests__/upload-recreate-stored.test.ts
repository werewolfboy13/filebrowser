// @vitest-environment jsdom
//
// When tus creates an upload again (resuming found it gone) and that is
// refused with 409, a file of the upload's name and size at the
// destination was taken to be this upload, stored by a final PATCH whose
// answer was lost, and the upload counted as done. But the upload is also
// created again when its registration was lost before all of its data
// was sent (a server restart, a laptop asleep): another file of the same
// size that appeared there meanwhile was then reported as this upload,
// although its data was never stored. The check is now made only when
// this upload's final PATCH went out and got no definitive answer (no
// answer, a 5xx, or 423); otherwise the user is asked, as for any other
// conflict.
//
// Real upload, layout and file stores, real tus.ts and the tus-js-client
// browser stack against a loopback tus server (see
// api/__tests__/tus-loopback.ts: 127.0.0.1 only, closed after each test;
// no network). Only the listing of the destination is mocked: it has a
// file of the upload's name and size.
import { beforeEach, describe, expect, it, vi } from "vitest";
import { loopbackPerTest, type TusServer } from "@/api/__tests__/tus-loopback";

vi.hoisted(() => {
  (window as any).FileBrowser = {
    AuthMethod: "json",
    BaseURL: "",
    StaticURL: "/static",
    // 10 bytes in chunks of 4: the third PATCH is the final one
    TusSettings: { chunkSize: 4, retryCount: 2 },
  };
});
const h = vi.hoisted(() => ({
  fetch: vi.fn(),
  showError: vi.fn(),
}));
const auth = vi.hoisted(() => ({ jwt: "aaa.bbb.ccc" }));
vi.mock("@/stores/auth", () => ({ useAuthStore: () => auth }));
vi.mock("@/utils/auth", () => ({ renew: vi.fn(), logout: vi.fn() }));
vi.mock("tus-js-client", async () =>
  vi.importActual("tus-js-client/lib.esm/browser/index.js")
);
vi.mock("@/api/utils", () => ({
  removePrefix: (u: string) => u.replace(/^\/files/, ""),
}));
// uploads go through the real tus.ts (imported when called: the module
// is loaded anew for each test, for that test's server)
vi.mock("@/api", async () => {
  return {
    files: {
      post: async (url: string, content: Blob, overwrite: boolean, up: any) =>
        (await import("@/api/tus")).upload(url, content, overwrite, up),
      fetch: h.fetch,
      abortResourceUploads: vi.fn(),
    },
    share: {},
    users: {},
    pub: {},
  };
});
vi.mock("@/utils/upload", () => ({
  checkConflict: vi.fn(),
  handleFiles: vi.fn(),
}));
vi.mock("@/utils/buttons", () => ({
  default: { loading: vi.fn(), success: vi.fn(), done: vi.fn() },
}));

let server: TusServer;
let mods: Awaited<ReturnType<typeof load>>;
async function load() {
  const [vue, pinia, upload, layout, file] = await Promise.all([
    import("vue"),
    import("pinia"),
    import("@/stores/upload"),
    import("@/stores/layout"),
    import("@/stores/file"),
  ]);
  return { vue, pinia, upload, layout, file };
}
const loopback = loopbackPerTest(load);

beforeEach(() => {
  ({ server, mod: mods } = loopback);
  vi.spyOn(console, "error").mockImplementation(() => {});
  h.fetch.mockReset();
  h.showError.mockReset();
  // the destination has a file of the upload's name and size
  h.fetch.mockResolvedValue({
    isDir: true,
    items: [{ name: "f.bin", size: 10, isDir: false }],
  });
  const app = mods.vue.createApp({});
  app.provide("$showError", h.showError);
  const pinia = mods.pinia.createPinia();
  app.use(pinia);
  mods.pinia.setActivePinia(pinia);
  count = {};
  server.onRequest = (r) => {
    count[r.method] = (count[r.method] ?? 0) + 1;
  };
});

const methods = () => server.seen.map((r) => r.method).join(" ");

// requests of each method received so far, the current one included
let count: Record<string, number> = {};
// answers the n-th PATCH (1-based) with the status
function onPatch(n: number, status: number) {
  server.once((r) =>
    r.method === "PATCH" && count.PATCH === n
      ? { status, body: `${status}\n` }
      : undefined
  );
}
function answer(method: string, status: number) {
  server.once((r) =>
    r.method === method ? { status, body: `${status}\n` } : undefined
  );
}
// the first POST creates the upload; creating it again is answered 409
// (the file of the same name and size is there)
function refuseRecreate() {
  server.once((r) =>
    r.method === "POST" && count.POST > 1
      ? { status: 409, body: "409 Conflict\n" }
      : undefined
  );
}

// uploads f.bin (10 bytes) and waits for the outcome
async function upload(label: string) {
  const layout = mods.layout.useLayoutStore();
  const files = mods.file.useFileStore();
  mods.upload
    .useUploadStore()
    .upload(
      "/files/f.bin",
      "f.bin",
      new File(["0123456789"], "f.bin"),
      false,
      "blob"
    );
  await vi.waitFor(
    () => {
      if (
        layout.currentPromptName == null &&
        !files.reload &&
        h.showError.mock.calls.length === 0
      )
        throw new Error("not settled yet");
    },
    { timeout: 5000, interval: 10 }
  );
  const toasts = h.showError.mock.calls.length;
  const outcome = {
    asked: layout.currentPromptName === "replace",
    // the batch finished without a question or an error
    done: files.reload && layout.currentPromptName == null && toasts === 0,
    toasts,
  };
  console.log(`${label}:`, JSON.stringify(outcome), "|", methods());
  return outcome;
}

describe("creating the upload again is refused, a file of its size is there", () => {
  it("registration lost before the final chunk: the user is asked", async () => {
    onPatch(2, 404);
    answer("HEAD", 404);
    refuseRecreate();
    const outcome = await upload("PATCH 2 404, HEAD 404, POST 409");
    expect(methods()).toBe("POST PATCH PATCH HEAD POST");
    expect(outcome).toEqual({ asked: true, done: false, toasts: 0 });
  });

  it("final PATCH refused 404: the user is asked", async () => {
    onPatch(3, 404);
    answer("HEAD", 404);
    refuseRecreate();
    const outcome = await upload("final PATCH 404, HEAD 404, POST 409");
    expect(methods()).toBe("POST PATCH PATCH PATCH HEAD POST");
    expect(outcome).toEqual({ asked: true, done: false, toasts: 0 });
  });

  // The answer to the final PATCH may be lost after the server stored
  // the file (a proxy's 502 or 504): the file there is taken to be this
  // upload's.
  for (const status of [502, 504, 500]) {
    it(`final PATCH answered ${status}: counted as stored`, async () => {
      onPatch(3, status);
      answer("HEAD", 404);
      refuseRecreate();
      const outcome = await upload(`final PATCH ${status}, HEAD 404, POST 409`);
      expect(methods()).toBe("POST PATCH PATCH PATCH HEAD POST");
      expect(outcome).toEqual({ asked: false, done: true, toasts: 0 });
    });
  }

  it("final PATCH answered 423, then HEAD 404: counted as stored", async () => {
    onPatch(3, 423);
    answer("HEAD", 404);
    refuseRecreate();
    const outcome = await upload("final PATCH 423, HEAD 404, POST 409");
    expect(methods()).toBe("POST PATCH PATCH PATCH HEAD POST");
    expect(outcome).toEqual({ asked: false, done: true, toasts: 0 });
  });

  // a later definitive refusal of another PATCH: the earlier final PATCH
  // stored nothing (the server took the upload back to an earlier offset)
  it("final PATCH 502, resumed from an earlier offset, then lost: the user is asked", async () => {
    onPatch(3, 502);
    // resuming: the server has only the first chunk
    server.once((r) =>
      r.method === "HEAD"
        ? {
            status: 200,
            headers: {
              "Upload-Offset": "4",
              "Upload-Length": "10",
              "Cache-Control": "no-store",
            },
          }
        : undefined
    );
    onPatch(4, 404); // the chunk at offset 4
    answer("HEAD", 404);
    refuseRecreate();
    const outcome = await upload("final PATCH 502, HEAD offset 4, PATCH 404");
    expect(methods()).toBe("POST PATCH PATCH PATCH HEAD PATCH HEAD POST");
    expect(outcome).toEqual({ asked: true, done: false, toasts: 0 });
  });
});

describe("control: a final PATCH refused outright is never counted as stored", () => {
  it("409 (a file appeared): the user is asked", async () => {
    onPatch(3, 409);
    const outcome = await upload("final PATCH 409");
    expect(outcome).toEqual({ asked: true, done: false, toasts: 0 });
    expect(h.fetch).not.toHaveBeenCalled();
  });

  it("403: an error", async () => {
    onPatch(3, 403);
    const outcome = await upload("final PATCH 403");
    expect(outcome).toEqual({ asked: false, done: false, toasts: 1 });
    expect(h.fetch).not.toHaveBeenCalled();
  });
});
