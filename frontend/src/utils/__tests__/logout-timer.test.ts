// @vitest-environment jsdom
//
// parseToken() arms an inactivity timer that logs out when the token
// expires. logout() reset the auth store without clearing it, so after
// logging in again on the same tab the old session's timer still fired and
// logged the new session out. Timers longer than setTimeout's limit (about
// 24.8 days; setSafeTimeout re-arms every 24 h) could not be cleared at all,
// on logout or on renewal, because only the first timer id was kept.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  (window as any).FileBrowser = {
    AuthMethod: "json",
    BaseURL: "",
    StaticURL: "/static",
    LoginPage: true,
    LogoutPage: "/login",
    NoAuth: false,
  };
});
const h = vi.hoisted(() => ({ push: vi.fn(), abort: vi.fn() }));
vi.mock("@/router", () => ({ default: { push: h.push } }));
vi.mock("@/i18n", () => ({ detectLocale: () => "en", setLocale: vi.fn() }));
vi.mock("@/stores/upload", () => ({
  useUploadStore: () => ({ abort: h.abort }),
}));

import { createPinia, setActivePinia } from "pinia";
import { logout, parseToken } from "@/utils/auth";
import { useAuthStore } from "@/stores/auth";

const b64url = (o: object) =>
  btoa(JSON.stringify(o))
    .replace(/=+$/, "")
    .replace(/\+/g, "-")
    .replace(/\//g, "_");
const jwt = (expInSeconds: number, n: number) =>
  `${b64url({ alg: "HS256" })}.${b64url({
    exp: Math.floor(Date.now() / 1000) + expInSeconds,
    user: { id: 1, locale: "en", perm: {}, n },
  })}.sig`;

const MIN = 60 * 1000;
const HOUR = 60 * MIN;
const DAY = 24 * HOUR;

describe("inactivity timer across logout and login", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    setActivePinia(createPinia());
    h.push.mockClear();
  });
  afterEach(() => vi.useRealTimers());

  it("the old session's timer does not log out the new session", () => {
    parseToken(jwt(3600, 1)); // first session, expires in 1 h
    vi.advanceTimersByTime(30 * MIN);
    logout(); // user logs out after 30 min
    h.push.mockClear();
    parseToken(jwt(7200, 2)); // logs in again: new token valid for 2 h
    vi.advanceTimersByTime(31 * MIN); // 1 h after the FIRST login
    console.log(
      "router.push after re-login:",
      JSON.stringify(h.push.mock.calls),
      "| user:",
      useAuthStore().user?.id ?? null
    );
    expect(h.push).not.toHaveBeenCalled();
    expect(useAuthStore().user).not.toBeNull();
  });

  it("the same holds for tokens valid for more than 24 hours", () => {
    parseToken(jwt(3 * 24 * 3600, 1)); // expires in 3 days
    vi.advanceTimersByTime(DAY + HOUR); // past the first re-arm
    logout();
    h.push.mockClear();
    parseToken(jwt(10 * 24 * 3600, 2)); // new session, 10 days
    vi.advanceTimersByTime(2 * DAY); // past the old token's expiry
    expect(h.push).not.toHaveBeenCalled();
  });

  it("renewing a token valid for more than 24 hours replaces its timer", () => {
    parseToken(jwt(2 * 24 * 3600, 1)); // expires in 2 days
    vi.advanceTimersByTime(DAY + HOUR); // past the first re-arm
    parseToken(jwt(10 * 24 * 3600, 2)); // renewed: 10 more days
    vi.advanceTimersByTime(DAY); // past the first token's expiry
    expect(h.push).not.toHaveBeenCalled();
  });

  it("control: the current session's timer still logs out at expiry", () => {
    parseToken(jwt(2 * 24 * 3600, 1));
    vi.advanceTimersByTime(2 * DAY + MIN);
    expect(h.push).toHaveBeenCalledWith({
      path: "/login",
      query: { "logout-reason": "inactivity" },
    });
    expect(localStorage.getItem("jwt")).toBe("");
  });

  // The stored token is shared by every tab: an idle tab whose token
  // expires must not clear the newer token an active tab stored.
  it("at expiry, a newer token stored by another tab is left alone", () => {
    parseToken(jwt(3600, 1));
    const other = jwt(7200, 2);
    localStorage.setItem("jwt", other); // another tab renewed
    vi.advanceTimersByTime(HOUR + MIN);
    expect(h.push).toHaveBeenCalled(); // this tab is logged out...
    expect(useAuthStore().jwt).toBe("");
    expect(localStorage.getItem("jwt")).toBe(other); // ...the other is not
  });
});
