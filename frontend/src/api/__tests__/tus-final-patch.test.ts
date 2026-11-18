// @vitest-environment jsdom
//
// How an upload ends when the final PATCH (the one that completes it and
// has the server put the file in place, http/tus_handlers.go) does not
// simply succeed. Real tus-js-client browser stack against a loopback tus
// server (see tus-loopback.ts: 127.0.0.1 only, closed after each test; no
// network), which here plays the server's answers:
//
// - 403: the upload asked to replace the file, but the user may no longer
//   modify it. The server forgets the upload (HEAD then answers 404), and
//   creating it again is refused the same way.
import { beforeEach, describe, expect, it, vi } from "vitest";
import { loopbackPerTest, type TusServer } from "./tus-loopback";

vi.hoisted(() => {
  (window as any).FileBrowser = {
    BaseURL: "",
    StaticURL: "/static",
    // 10 bytes in chunks of 4: the third PATCH is the final one
    TusSettings: { chunkSize: 4, retryCount: 2 },
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

const methods = () => server.seen.map((r) => r.method);

// answers the n-th PATCH (1-based) with the reply, once
function onPatch(n: number, status: number) {
  let patches = 0;
  server.once((r) =>
    r.method === "PATCH" && ++patches === n
      ? { status, body: `${status}\n` }
      : undefined
  );
}
function answer(method: string, status: number, times = 1) {
  for (let i = 0; i < times; i++) {
    server.once((r) =>
      r.method === method ? { status, body: `${status}\n` } : undefined
    );
  }
}

// the first POST creates the upload; the next ones (creating it again
// after a resume) are answered with the status
function refuseRecreate(status: number, times = 1) {
  let posts = 0;
  server.onRequest = (r) => {
    if (r.method === "POST") posts++;
  };
  for (let i = 0; i < times; i++) {
    server.once((r) =>
      r.method === "POST" && posts > 1
        ? { status, body: `${status}\n` }
        : undefined
    );
  }
}

async function send(): Promise<any> {
  return (
    tus.upload(
      "/files/f.bin",
      new Blob(["0123456789"]),
      false,
      () => {}
    ) as Promise<unknown>
  ).then(
    () => "resolved",
    (e) => e
  );
}

describe("a refusal (403) of the final PATCH or of creating the upload again", () => {
  it("a final PATCH answered 403 is not retried", async () => {
    onPatch(3, 403);
    answer("HEAD", 404, 2);
    refuseRecreate(403, 2); // only reached if retried
    const err = await send();
    console.log(
      "final PATCH 403:",
      err.status,
      err.method,
      "| requests:",
      methods().join(" ")
    );
    expect([err.status, err.method]).toEqual([403, "PATCH"]);
    expect(methods()).toEqual(["POST", "PATCH", "PATCH", "PATCH"]);
  });

  it("creating it again answered 403 is not retried", async () => {
    onPatch(3, 500); // e.g. the answer was lost: tus resumes
    answer("HEAD", 404, 2);
    refuseRecreate(403, 2);
    const err = await send();
    console.log(
      "re-create 403:",
      err.status,
      err.method,
      "| requests:",
      methods().join(" ")
    );
    expect([err.status, err.method]).toEqual([403, "POST"]);
    expect(methods()).toEqual([
      "POST",
      "PATCH",
      "PATCH",
      "PATCH",
      "HEAD",
      "POST",
    ]);
  });
});
