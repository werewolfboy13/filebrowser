import { useAuthStore } from "@/stores/auth";
import { renew, logout, parseToken } from "@/utils/auth";
import { baseURL } from "@/utils/constants";
import { encodePath } from "@/utils/url";

export class StatusError extends Error {
  constructor(
    message: any,
    public status?: number,
    public is_canceled?: boolean,
    // seconds to wait before trying again (Retry-After), when given
    public retryAfter?: number
  ) {
    super(message);
    this.name = "StatusError";
  }
}

// Where the session of this tab (its auth store) is at, so that an answer
// can tell whether it still speaks for the session it was asked in. Never
// reset, not even by logout.
// - epoch: a new one starts with every token stored and with logout. A
//   renewal is applied only in the epoch it was asked for in.
// - session: a new one starts with logout. A token handed out with an
//   answer (X-Auth-Token) is adopted only in the session the request was
//   sent in. Not only in its epoch: a renewal in between (a token that the
//   change handing out the new one has revoked) must not make it dropped.
interface SessionMark {
  epoch: number;
  session: number;
}
const sessionMarks = new WeakMap<object, SessionMark>();

function markOf(): SessionMark {
  const authStore = useAuthStore();
  let mark = sessionMarks.get(authStore);
  if (!mark) {
    mark = { epoch: 0, session: 0 };
    sessionMarks.set(authStore, mark);
  }
  return mark;
}

export function sessionMark(): SessionMark {
  return { ...markOf() };
}

// Called by parseToken (a token stored) and by logout (the session ended).
export function startEpoch(sessionEnded: boolean) {
  const mark = markOf();
  mark.epoch++;
  if (sessionEnded) mark.session++;
}

export async function fetchURL(
  url: string,
  opts: ApiOpts,
  auth = true
): Promise<Response> {
  const authStore = useAuthStore();

  opts = opts || {};
  opts.headers = opts.headers || {};

  const { headers, ...rest } = opts;
  // The token this request is sent with: its answer only speaks for the
  // session while that token is still the current one.
  const sentJwt = authStore.jwt;
  const sentSession = sessionMark().session;
  const isCurrent = () => authStore.jwt === sentJwt;
  let res;
  try {
    res = await fetch(`${baseURL}${url}`, {
      headers: {
        "X-Auth": sentJwt,
        ...headers,
      },
      ...rest,
    });
  } catch (e) {
    // Check if the error is an intentional cancellation
    if (e instanceof Error && e.name === "AbortError") {
      throw new StatusError("000 No connection", 0, true);
    }
    throw new StatusError("000 No connection", 0);
  }

  // A change to the user's own account that revokes their sessions (e.g. a
  // password change) hands this tab its new token with the answer. Adopt it
  // first, like a renewal; renewIfAsked then finds the token it was sent
  // with replaced and does not renew on top of it. Only from a successful
  // answer: the server sends it with nothing else.
  const handedOut = auth && res.ok ? res.headers.get("X-Auth-Token") : null;
  if (handedOut && sessionMark().session === sentSession) {
    try {
      parseToken(handedOut);
    } catch (e) {
      console.warn("ignoring a malformed X-Auth-Token", e);
    }
  }

  if (res.status < 200 || res.status > 299) {
    const body = await res.text();
    const error = new StatusError(
      body || `${res.status} ${res.statusText}`,
      res.status,
      undefined,
      retryAfterSeconds(res)
    );

    // The session is over (e.g. revoked by a password, permission or scope
    // change elsewhere): back to the login page. Unless this request went
    // out with a token that has been replaced since: then only that token
    // is gone, e.g. a request sent just before this tab changed its own
    // password, answered after the server revoked the old token. Older
    // servers send X-Renew-Token with such a 401; it is not acted on.
    if (auth && res.status == 401) {
      if (isCurrent()) logout(undefined, sentJwt);
      throw error;
    }

    await renewIfAsked(res);
    throw error;
  }

  await renewIfAsked(res);

  return res;

  async function renewIfAsked(response: Response) {
    if (!auth || response.headers.get("X-Renew-Token") !== "true") return;
    if (!isCurrent()) return;
    try {
      await renew(sentJwt);
    } catch (e) {
      // The token was refused: the session is over.
      if (e instanceof StatusError && e.status === 401) {
        if (isCurrent()) logout(undefined, sentJwt);
        throw new StatusError(e.message, 401);
      }
      // Otherwise (no connection) the request itself still succeeded; the
      // next response asking for renewal tries again.
      console.warn("could not renew the session", e);
    }
  }
}

// The wait a 429 (or 503) answer asks for, in whole seconds, from its
// Retry-After header (seconds, or an HTTP date).
export function retryAfterSeconds(res: Response): number | undefined {
  const value = res.headers.get("Retry-After")?.trim();
  if (!value) return undefined;
  if (/^\d+$/.test(value)) return Number(value);
  const date = Date.parse(value);
  if (Number.isNaN(date)) return undefined;
  return Math.max(0, Math.ceil((date - Date.now()) / 1000));
}

export async function fetchJSON<T>(url: string, opts?: any): Promise<T> {
  const res = await fetchURL(url, opts);

  if (res.status === 200) {
    return res.json() as Promise<T>;
  }

  throw new StatusError(`${res.status} ${res.statusText}`, res.status);
}

export function removePrefix(url: string): string {
  url = url.split("/").splice(2).join("/");

  if (url === "") url = "/";
  if (url[0] !== "/") url = "/" + url;
  return url;
}

export function createURL(endpoint: string, searchParams = {}): string {
  let prefix = baseURL;
  if (!prefix.endsWith("/")) {
    prefix = prefix + "/";
  }
  const url = new URL(prefix + encodePath(endpoint), origin);
  url.search = new URLSearchParams(searchParams).toString();

  return url.toString();
}

// setTimeout for delays longer than browsers allow (about 24.8 days; a
// longer delay fires at once): waits in steps of at most 24 hours. Returns
// a function that cancels it, whichever step it is at.
export function setSafeTimeout(
  callback: () => void,
  delay: number
): () => void {
  const MAX_DELAY = 86_400_000;
  let remaining = delay;
  let id: number;

  function scheduleNext() {
    if (remaining <= MAX_DELAY) {
      id = window.setTimeout(callback, remaining);
    } else {
      id = window.setTimeout(() => {
        remaining -= MAX_DELAY;
        scheduleNext();
      }, MAX_DELAY);
    }
  }

  scheduleNext();
  return () => window.clearTimeout(id);
}
