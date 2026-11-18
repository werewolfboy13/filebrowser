// @vitest-environment jsdom
//
// The messages shown (by the error toast, the login page and the share
// page) for answers that are not failures of the app: 429 with
// Retry-After, 408, 413 and 423.
import { describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  (window as any).FileBrowser = { BaseURL: "" };
});
vi.mock("@/utils/auth", () => ({}));
vi.mock("@/stores/auth", () => ({}));

import { errorToast, statusMessage } from "@/utils/errors";
import { retryAfterSeconds, StatusError } from "@/api/utils";

const t = (k: string, v?: Record<string, unknown>) =>
  v ? `${k} ${JSON.stringify(v)}` : k;

describe("statusMessage", () => {
  it.each([
    [
      new StatusError("x", 429, undefined, 12),
      'errors.tooManyAttempts {"seconds":12}',
    ],
    [
      new StatusError("x", 429, undefined, 0),
      'errors.tooManyAttempts {"seconds":1}',
    ],
    [
      new StatusError("x", 429, undefined, 61),
      'errors.tooManyAttempts {"seconds":61}',
    ],
    [
      new StatusError("x", 429, undefined, 121),
      'errors.tooManyAttemptsMinutes {"minutes":3}',
    ],
    [new StatusError("x", 429), "errors.tooManyAttemptsLater"],
    // a day at most; past that (or a value too large to show) "a while"
    [
      new StatusError("x", 429, undefined, 86_400),
      'errors.tooManyAttemptsMinutes {"minutes":1440}',
    ],
    [
      new StatusError("x", 429, undefined, 86_401),
      "errors.tooManyAttemptsLater",
    ],
    [
      new StatusError("x", 429, undefined, Number("99999999999999999999999")),
      "errors.tooManyAttemptsLater",
    ],
    [new StatusError("x", 408), "errors.requestTimeout"],
    [new StatusError("x", 413), "errors.tooLarge"],
    [new StatusError("x", 423), "errors.locked"],
    [new StatusError("x", 403), "errors.forbidden"],
  ])("%o", (err, expected) => {
    expect(statusMessage(t, err)).toBe(expected);
  });

  it("leaves other errors alone", () => {
    expect(statusMessage(t, new StatusError("x", 500))).toBeNull();
    expect(statusMessage(t, new Error("x"))).toBeNull();
    expect(statusMessage(t, "a string")).toBeNull();
  });
});

// with the app's messages (en), as the error toast translates them
describe("the wait, in words", () => {
  it("one second, or several", async () => {
    const { default: i18n } = await import("@/i18n");
    const real = (k: string, v?: Record<string, unknown>, plural?: number) =>
      plural === undefined
        ? i18n.global.t(k, v ?? {})
        : i18n.global.t(k, v ?? {}, plural);
    const one = statusMessage(real, new StatusError("x", 429, undefined, 1));
    const five = statusMessage(real, new StatusError("x", 429, undefined, 5));
    console.log(JSON.stringify([one, five]));
    expect(one).toBe(
      "Too many attempts. Please wait 1 second before trying again."
    );
    expect(five).toBe(
      "Too many attempts. Please wait 5 seconds before trying again."
    );
  });
});

describe("retryAfterSeconds", () => {
  const res = (v?: string) =>
    new Response("", { status: 429, headers: v ? { "Retry-After": v } : {} });
  it("reads seconds", () => expect(retryAfterSeconds(res("42"))).toBe(42));
  it("reads an HTTP date", () => {
    const at = new Date(Date.now() + 10_000).toUTCString();
    const s = retryAfterSeconds(res(at))!;
    expect(s).toBeGreaterThanOrEqual(9);
    expect(s).toBeLessThanOrEqual(11);
  });
  it("is undefined when missing or invalid", () => {
    expect(retryAfterSeconds(res())).toBeUndefined();
    expect(retryAfterSeconds(res("soon"))).toBeUndefined();
  });
});

describe("errorToast", () => {
  it("a 429 from a current-password check says how long to wait, without a report link", () => {
    expect(
      errorToast(
        t,
        new StatusError("429 Too Many Requests\n", 429, undefined, 20),
        true
      )
    ).toEqual({
      message: 'errors.tooManyAttempts {"seconds":20}',
      isReport: false,
    });
  });
  // e.g. an upload refused at its end (403 on the final PATCH): not a bug
  it("a 403 says the user may not, without a report link", () => {
    expect(
      errorToast(t, { status: 403, message: "403\n" } as any, true)
    ).toEqual({ message: "errors.forbidden", isReport: false });
  });
  it("other errors keep their message and the report link", () => {
    expect(errorToast(t, new StatusError("boom", 500), true)).toEqual({
      message: "boom",
      isReport: true,
    });
    expect(errorToast(t, "plain", false)).toEqual({
      message: "plain",
      isReport: false,
    });
  });
});
