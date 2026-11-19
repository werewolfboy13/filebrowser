// @vitest-environment jsdom
//
// The server's answers to a user's change of their own account:
// - 409 Conflict: an administrator changed the same account at the same
//   moment, and this change was not made. The form showed the raw
//   "409 Conflict"; it now says what happened and to try again.
// - 200 without X-Auth-Token (rare): the change was made and the tab's
//   token is revoked, but no new one came with the answer. The tab must
//   not end up stuck: the next request's 401 ends the session like any
//   forced logout (login page, back to this page afterwards), without a
//   raw "401 Unauthorized" toast on top of the login page.
// Real Profile.vue, api/users.ts, fetchURL and logout; fetch and the
// router are stand-ins.
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  (window as any).FileBrowser = {
    AuthMethod: "json",
    NoAuth: false,
    BaseURL: "",
    StaticURL: "/static",
    Name: "",
    LoginPage: true,
    LogoutPage: "/login",
    TusSettings: {},
  };
});
const h = vi.hoisted(() => ({ push: vi.fn() }));
vi.mock("@/router", () => ({
  default: {
    push: h.push,
    currentRoute: {
      value: {
        fullPath: "/settings/profile",
        matched: [{ meta: { requiresAuth: true } }],
      },
    },
  },
}));
vi.mock("@/stores/upload", () => ({
  useUploadStore: () => ({ abort: () => 0 }),
}));
vi.mock("@/components/settings/AceEditorTheme.vue", () => ({
  default: { render: () => null },
}));
vi.mock("@/components/settings/Languages.vue", () => ({
  default: { render: () => null },
}));
vi.mock("@/i18n", () => ({ detectLocale: () => "en", setLocale: vi.fn() }));
vi.mock("vue-i18n", async (orig) => ({
  ...(await orig<typeof import("vue-i18n")>()),
  useI18n: () => ({ t: (k: string) => k }),
}));

import { flushPromises } from "@vue/test-utils";
import enJson from "@/i18n/en.json?raw";
import { parseToken } from "@/utils/auth";
import { changePassword, mountProfile } from "./profile.shared";

const b64url = (o: object) =>
  btoa(JSON.stringify(o))
    .replace(/=+$/, "")
    .replace(/\+/g, "-")
    .replace(/\//g, "_");
const OLD = `${b64url({ alg: "HS256" })}.${b64url({
  exp: Math.floor(Date.now() / 1000) + 3600,
  user: { id: 7, username: "alice", locale: "en", perm: {} },
})}.sig`;

let answers: Response[];
let sent: { url: string; method: string; token: string }[];

beforeEach(() => {
  document.body.innerHTML = "";
  localStorage.clear();
  h.push.mockClear();
  sent = [];
  answers = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, init: RequestInit = {}) => {
      sent.push({
        url,
        method: init.method ?? "GET",
        token: (init.headers as Record<string, string>)["X-Auth"],
      });
      return answers.shift() ?? new Response("unexpected", { status: 500 });
    })
  );
});

async function profile() {
  const mounted = await mountProfile();
  parseToken(OLD);
  return mounted;
}

describe("the user's own account change", () => {
  it("409: says the account was changed at the same time, to try again", async () => {
    const { w, showError, showSuccess } = await profile();
    answers.push(new Response("409 Conflict\n", { status: 409 }));
    await changePassword(w, "old-pass");
    expect(sent.map((s) => `${s.method} ${s.url}`)).toEqual([
      "PUT /api/users/7",
    ]);
    expect(showSuccess).not.toHaveBeenCalled();
    console.log("toast:", JSON.stringify(showError.mock.calls));
    expect(showError).toHaveBeenCalledWith("errors.accountChangedConcurrently");
    expect(JSON.parse(enJson).errors.accountChangedConcurrently).toMatch(
      /changed .*at the same time.*try again/i
    );
    // still logged in, nothing else happened
    expect(h.push).not.toHaveBeenCalled();
  });

  it("409 on a settings update too", async () => {
    const { w, showError } = await profile();
    answers.push(new Response("409 Conflict\n", { status: 409 }));
    await w.find('input[name="singleClick"]').setValue(true);
    await w.findAll("form")[0].trigger("submit");
    await flushPromises();
    expect(showError).toHaveBeenCalledWith("errors.accountChangedConcurrently");
  });

  it("200 without X-Auth-Token: the next request's 401 is a normal forced logout", async () => {
    const { w, authStore, showError, showSuccess } = await profile();
    answers.push(new Response("", { status: 200 }));
    await changePassword(w, "old-pass");
    expect(showSuccess).toHaveBeenCalledTimes(1);
    expect(showError).not.toHaveBeenCalled();
    // the old token is still the tab's: nothing to adopt
    expect(authStore.jwt).toBe(OLD);
    expect(h.push).not.toHaveBeenCalled();

    // the next request goes out with it, and the server refuses it
    answers.push(new Response("401 Unauthorized\n", { status: 401 }));
    await w.find('input[name="singleClick"]').setValue(true);
    await w.findAll("form")[0].trigger("submit");
    await flushPromises();
    expect(sent.at(-1)).toMatchObject({ method: "PUT", token: OLD });
    console.log(
      "router.push:",
      JSON.stringify(h.push.mock.calls),
      "| toasts:",
      JSON.stringify(showError.mock.calls)
    );
    expect(h.push).toHaveBeenCalledWith({
      path: "/login",
      query: { redirect: "/settings/profile" },
    });
    expect(authStore.user).toBeNull();
    expect(authStore.jwt).toBe("");
    // the login page says enough: no raw "401 Unauthorized" toast
    expect(showError).not.toHaveBeenCalled();
  });
});
