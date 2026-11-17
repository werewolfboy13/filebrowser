// Test helper (jsdom): a tus server on the loopback interface, inside the
// test process, for driving the real tus-js-client browser stack (its
// XMLHttpRequest-based HTTP stack on jsdom's spec XMLHttpRequest) end to end.
// Nothing leaves the machine.
//
// jsdom's XMLHttpRequest follows the spec: calling setRequestHeader twice for
// the same header combines the values ("a, b"), as browsers do. The server
// records the X-Auth header of every request exactly as it arrives and, like
// http/auth.go's extractor, only takes a value with exactly two dots as a
// token; anything else is answered with 401.
//
// The page has to be same-origin with the server, or jsdom's XHR would add
// CORS preflights: call useServerOrigin() before importing the code under
// test, so that utils/constants reads the server's origin.
import http from "node:http";
import type { AddressInfo } from "node:net";

export interface SeenRequest {
  method: string;
  path: string;
  // the X-Auth header as the server received it (combined if repeated)
  xAuth: string | undefined;
  // how many X-Auth header lines arrived
  xAuthLines: number;
}

export interface Reply {
  status: number;
  headers?: Record<string, string>;
  body?: string;
}

// Returns a reply to use instead of the default handling, or undefined.
export type Override = (req: SeenRequest) => Reply | undefined;

export interface TusServer {
  origin: string;
  seen: SeenRequest[];
  // one-shot overrides, tried in order; the first that returns a reply is
  // used and removed
  once(override: Override): void;
  // called for every request after it is recorded (e.g. to change the
  // session token between requests)
  onRequest: ((req: SeenRequest) => void) | null;
  // headers added to every reply
  extraHeaders: Record<string, string>;
  reset(): void;
  close(): Promise<void>;
}

const isToken = (v: string | undefined) =>
  !!v && v !== "" && (v.match(/\./g) ?? []).length === 2;

export async function startTusServer(): Promise<TusServer> {
  const uploads = new Map<string, { length: number; offset: number }>();
  let overrides: Override[] = [];

  const state: TusServer = {
    origin: "",
    seen: [],
    once(o) {
      overrides.push(o);
    },
    onRequest: null,
    extraHeaders: {},
    reset() {
      state.seen.length = 0;
      overrides = [];
      uploads.clear();
      state.onRequest = null;
      state.extraHeaders = {};
    },
    close: () => new Promise<void>((r) => server.close(() => r())),
  };

  const server = http.createServer((req, res) => {
    const path = decodeURIComponent((req.url ?? "").split("?")[0]);
    const xAuthLines = req.rawHeaders.filter(
      (_v, i) => i % 2 === 0 && req.rawHeaders[i].toLowerCase() === "x-auth"
    ).length;
    const seen: SeenRequest = {
      method: req.method ?? "",
      path,
      xAuth: req.headers["x-auth"] as string | undefined,
      xAuthLines,
    };
    state.seen.push(seen);
    state.onRequest?.(seen);

    const chunks: Buffer[] = [];
    req.on("data", (c: Buffer) => chunks.push(c));
    req.on("end", () => {
      const reply = (r: Reply) => {
        res.statusCode = r.status;
        res.setHeader("Tus-Resumable", "1.0.0");
        for (const [k, v] of Object.entries({
          ...state.extraHeaders,
          ...r.headers,
        })) {
          res.setHeader(k, v);
        }
        res.end(r.body ?? "");
      };

      for (let i = 0; i < overrides.length; i++) {
        const r = overrides[i](seen);
        if (r) {
          overrides.splice(i, 1);
          return reply(r);
        }
      }

      if (!isToken(seen.xAuth)) {
        return reply({ status: 401, body: "401 Unauthorized\n" });
      }

      const key = path.replace(/^\/api\/tus/, "");
      const upload = uploads.get(key);
      switch (seen.method) {
        case "POST": {
          const length = Number(req.headers["upload-length"]);
          uploads.set(key, { length, offset: 0 });
          return reply({
            status: 201,
            headers: { Location: `/api/tus${encodeURI(key)}` },
          });
        }
        case "HEAD":
          if (!upload) return reply({ status: 404 });
          return reply({
            status: 200,
            headers: {
              "Upload-Offset": String(upload.offset),
              "Upload-Length": String(upload.length),
              "Cache-Control": "no-store",
            },
          });
        case "PATCH": {
          if (!upload) return reply({ status: 404 });
          const offset = Number(req.headers["upload-offset"]);
          if (offset !== upload.offset) return reply({ status: 409 });
          upload.offset += Buffer.concat(chunks).length;
          return reply({
            status: 204,
            headers: { "Upload-Offset": String(upload.offset) },
          });
        }
        case "DELETE":
          if (!upload) return reply({ status: 404 });
          uploads.delete(key);
          return reply({ status: 204 });
      }
      return reply({ status: 405 });
    });
  });

  await new Promise<void>((r) => server.listen(0, "127.0.0.1", r));
  const { port } = server.address() as AddressInfo;
  state.origin = `http://127.0.0.1:${port}`;
  return state;
}

// Point the jsdom page at the server's origin (vitest exposes the JSDOM
// instance as the global `jsdom`). reconfigure() moves the document, but
// jsdom's XMLHttpRequest takes its origin from the window's own _origin,
// which reconfigure() leaves alone; without updating it every request would
// be cross-origin and start with a CORS preflight. If a jsdom upgrade
// renames it, the tests fail on those preflights rather than pass silently.
export function useServerOrigin(server: TusServer) {
  const dom = (globalThis as any).jsdom;
  dom.reconfigure({ url: `${server.origin}/files/` });
  if (!("_origin" in dom.window)) {
    throw new Error("jsdom internals changed: window._origin is gone");
  }
  dom.window._origin = server.origin;
}
