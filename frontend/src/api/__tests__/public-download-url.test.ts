// Node-style test (no DOM, no network).
//
// The public share page built download URLs from raw item paths, so names
// containing '#', '?', '%' or ',' downloaded the wrong path (for '#', the
// parent folder, which an unprotected share serves as a zip) or failed,
// and the token of a password-protected share ended up in the URL fragment
// and was never sent.
import { beforeEach, describe, expect, it, vi } from "vitest";

const opened: string[] = vi.hoisted(() => []);
vi.hoisted(() => {
  (globalThis as any).window = {
    FileBrowser: {
      BaseURL: "",
      StaticURL: "/static",
      Name: "",
      TusSettings: {},
    },
    location: { origin: "https://fb.test" },
    open: (u: string) => opened.push(u),
  };
});
vi.mock("@/utils/auth", () => ({ renew: vi.fn(), logout: vi.fn() }));
vi.mock("@/stores/auth", () => ({ useAuthStore: () => ({ jwt: "" }) }));

import { download } from "@/api/pub";

// What the server sees: Go's r.URL.Path for a single file; for files= it
// takes Query().Get("files"), splits on ',' and QueryUnescapes each entry
// with '+' kept (http/raw.go parseQueryFiles).
function serverView(raw: string) {
  const u = new URL(raw, "https://fb.test");
  const files = u.searchParams.get("files");
  return {
    path: decodeURIComponent(u.pathname),
    token: u.searchParams.get("token"),
    algo: u.searchParams.get("algo"),
    files:
      files === null
        ? null
        : files.split(",").map((n) => {
            try {
              return decodeURIComponent(n.replace(/\+/g, "%2B"));
            } catch {
              return "<400 bad escape>";
            }
          }),
  };
}

describe("public share download URLs", () => {
  beforeEach(() => {
    opened.length = 0;
  });

  it("single file whose name contains '#' keeps its path and the token", () => {
    download(null as any, "HaSh", "TOK/EN+1=", "/photos/#1 best.jpg");
    const v = serverView(opened[0]);
    console.log("opened:", opened[0], "| server sees:", JSON.stringify(v));
    expect(v.path).toBe("/api/public/dl/HaSh/photos/#1 best.jpg");
    expect(v.token).toBe("TOK/EN+1=");
  });

  it("single file whose name contains '?' or '%41'", () => {
    download(null as any, "HaSh", "", "/q?.txt");
    download(null as any, "HaSh", "", "/100%41.txt");
    expect(serverView(opened[0]).path).toBe("/api/public/dl/HaSh/q?.txt");
    expect(serverView(opened[1]).path).toBe("/api/public/dl/HaSh/100%41.txt");
  });

  it("multi-file download keeps names containing ',', '%' and '+' intact", () => {
    download("zip" as any, "HaSh", "T", "/a,b.txt", "/50%25 off.txt", "/c+d");
    const v = serverView(opened[0]);
    console.log("files= as the server splits it:", JSON.stringify(v.files));
    expect(v.files).toEqual(["/a,b.txt", "/50%25 off.txt", "/c+d"]);
    expect(v.algo).toBe("zip");
    expect(v.token).toBe("T");
  });

  it("control: plain names are unchanged", () => {
    download(null as any, "HaSh", "", "/docs/report.pdf");
    expect(serverView(opened[0]).path).toBe(
      "/api/public/dl/HaSh/docs/report.pdf"
    );
  });
});
