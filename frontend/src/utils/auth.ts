import { useAuthStore } from "@/stores/auth";
import { useClipboardStore } from "@/stores/clipboard";
import { useFileStore } from "@/stores/file";
import { useLayoutStore } from "@/stores/layout";
import { useUploadStore } from "@/stores/upload";
import router from "@/router";
import type { JwtPayload } from "jwt-decode";
import { jwtDecode } from "jwt-decode";
import { authMethod, baseURL, noAuth, logoutPage } from "./constants";
import {
  retryAfterSeconds,
  sessionMark,
  setSafeTimeout,
  startEpoch,
  StatusError,
} from "@/api/utils";

// The auth cookie lets the server authenticate plain GETs (downloads,
// previews). Over https, never let it travel over plain http.
function setAuthCookie(value: string, extra = "") {
  const secure = window.location.protocol === "https:" ? " Secure;" : "";
  document.cookie = `auth=${value};${extra} Path=/; SameSite=Strict;${secure}`;
}

export function parseToken(token: string) {
  // falsy or malformed jwt will throw InvalidTokenError
  const data = jwtDecode<JwtPayload & { user: IUser }>(token);
  // answers to renewals asked for before this token are for another one
  startEpoch(false);

  setAuthCookie(token);

  localStorage.setItem("jwt", token);

  const authStore = useAuthStore();
  authStore.jwt = token;
  authStore.setUser(data.user);

  // proxy auth with custom logout subject to unknown external timeout
  if (logoutPage !== "/login" && authMethod === "proxy") {
    console.warn("idle timeout disabled with proxy auth and custom logout");
    return;
  }

  const expiresAt = new Date(data.exp! * 1000);
  const timeout = expiresAt.getTime() - Date.now();
  authStore.setLogoutTimer(
    setSafeTimeout(() => {
      logout("inactivity");
    }, timeout)
  );
}

export async function validateLogin() {
  try {
    if (localStorage.getItem("jwt")) {
      await renew(<string>localStorage.getItem("jwt"));
    }
  } catch (error) {
    console.warn("Invalid JWT token in storage");
    throw error;
  }
}

export async function login(
  username: string,
  password: string,
  recaptcha: string
) {
  const data = { username, password, recaptcha };

  const res = await fetch(`${baseURL}/api/login`, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
    },
    body: JSON.stringify(data),
  });

  const body = await res.text();

  if (res.status === 200) {
    parseToken(body);
  } else {
    throw new StatusError(
      body || `${res.status} ${res.statusText}`,
      res.status,
      undefined,
      retryAfterSeconds(res)
    );
  }
}

// One renewal at a time per token: every response carrying X-Renew-Token
// (API calls, tus upload requests) asks for one, often concurrently.
let pendingRenew: { jwt: string; promise: Promise<void> } | null = null;

export function renew(jwt: string): Promise<void> {
  if (pendingRenew?.jwt === jwt) return pendingRenew.promise;

  const promise = requestRenew(jwt).finally(() => {
    if (pendingRenew?.promise === promise) pendingRenew = null;
  });
  pendingRenew = { jwt, promise };
  return promise;
}

async function requestRenew(jwt: string) {
  const { epoch } = sessionMark();
  const res = await fetch(`${baseURL}/api/renew`, {
    method: "POST",
    headers: {
      "X-Auth": jwt,
    },
  });

  const body = await res.text();

  // The tab logged out, or stored another token (a renewal, a token handed
  // out by a password change), while this was on its way: the answer is
  // about a token no longer in use. Applying it would log a logged-out tab
  // back in, or put back a token the change has revoked; its refusal does
  // not end the session that is in use.
  if (sessionMark().epoch !== epoch) return;

  if (res.status === 200) {
    parseToken(body);
  } else {
    throw new StatusError(
      body || `${res.status} ${res.statusText}`,
      res.status,
      undefined,
      retryAfterSeconds(res)
    );
  }
}

export async function signup(username: string, password: string) {
  const data = { username, password };

  const res = await fetch(`${baseURL}/api/signup`, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
    },
    body: JSON.stringify(data),
  });

  if (res.status !== 200) {
    const body = await res.text();
    throw new StatusError(
      body || `${res.status} ${res.statusText}`,
      res.status,
      undefined,
      retryAfterSeconds(res)
    );
  }
}

export function logout(reason?: string) {
  // answers to requests sent before this are for the session that ends here
  startEpoch(true);
  // Stop what still runs with this session (uploads; the pending ones would
  // otherwise start without a token) while the token is still set, then
  // drop per-session state so the next login on this tab starts clean.
  useUploadStore().abort();
  useClipboardStore().resetClipboard();
  useFileStore().clearFile();
  useLayoutStore().clearLayout();

  setAuthCookie("", " Max-Age=0;");

  const authStore = useAuthStore();
  // $reset() would forget the inactivity timer without stopping it, and it
  // would log out whoever logs in next on this tab.
  authStore.setLogoutTimer(null);
  authStore.clearUser();

  localStorage.setItem("jwt", "");
  if (noAuth) {
    window.location.reload();
  } else if (logoutPage !== "/login") {
    document.location.href = `${logoutPage}`;
  } else {
    if (typeof reason === "string" && reason.trim() !== "") {
      router.push({
        path: "/login",
        query: { "logout-reason": reason },
      });
    } else {
      router.push({
        path: "/login",
      });
    }
  }
}
