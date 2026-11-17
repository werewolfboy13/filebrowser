import { useAuthStore } from "@/stores/auth";
import { renew, logout, parseToken } from "@/utils/auth";
import { baseURL } from "@/utils/constants";
import { encodePath } from "@/utils/url";

export class StatusError extends Error {
  constructor(
    message: any,
    public status?: number,
    public is_canceled?: boolean
  ) {
    super(message);
    this.name = "StatusError";
  }
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
  // first, like a renewal, and do not renew on top of it.
  const handedOut = auth ? res.headers.get("X-Auth-Token") : null;
  if (handedOut && isCurrent()) {
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
      res.status
    );

    // The session is over (e.g. revoked by a password, permission or scope
    // change elsewhere): back to the login page. Unless this request went
    // out with a token that has been replaced since: then only that token
    // is gone, e.g. a request sent just before this tab changed its own
    // password, answered after the server revoked the old token. Older
    // servers send X-Renew-Token with such a 401; it is not acted on.
    if (auth && res.status == 401) {
      if (isCurrent()) logout();
      throw error;
    }

    if (!handedOut) await renewIfAsked(res);
    throw error;
  }

  if (!handedOut) await renewIfAsked(res);

  return res;

  async function renewIfAsked(response: Response) {
    if (!auth || response.headers.get("X-Renew-Token") !== "true") return;
    if (!isCurrent()) return;
    try {
      await renew(sentJwt);
    } catch (e) {
      // The token was refused: the session is over.
      if (e instanceof StatusError && e.status === 401) {
        if (isCurrent()) logout();
        throw new StatusError(e.message, 401);
      }
      // Otherwise (no connection) the request itself still succeeded; the
      // next response asking for renewal tries again.
      console.warn("could not renew the session", e);
    }
  }
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

export function setSafeTimeout(callback: () => void, delay: number): number {
  const MAX_DELAY = 86_400_000;
  let remaining = delay;

  function scheduleNext(): number {
    if (remaining <= MAX_DELAY) {
      return window.setTimeout(callback, remaining);
    } else {
      return window.setTimeout(() => {
        remaining -= MAX_DELAY;
        scheduleNext();
      }, MAX_DELAY);
    }
  }

  return scheduleNext();
}
