// @vitest-environment jsdom
//
// After too many wrong passwords the server answers 429 with Retry-After
// (login, and the password of a share). The share page replaced its
// password form with the "internal error" page, with no way to try again
// and no word on when; the login page showed a raw "429 Too Many Requests"
// toast. Both now keep their form and say how long to wait.
import { afterEach, describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  (window as any).FileBrowser = {
    AuthMethod: "json",
    BaseURL: "",
    StaticURL: "/static",
    Name: "",
    LoginPage: true,
    LogoutPage: "/login",
    EnableThumbs: false,
    ReCaptcha: "",
    Signup: false,
    TusSettings: { chunkSize: 1, retryCount: 0 },
  };
});
const h = vi.hoisted(() => ({
  fetch: vi.fn(),
  login: vi.fn(),
  showError: vi.fn(),
}));
vi.mock("@/utils/auth", () => ({
  renew: vi.fn(),
  logout: vi.fn(),
  login: h.login,
  signup: vi.fn(),
}));
vi.mock("@/api", () => ({
  pub: { fetch: h.fetch, getDownloadURL: () => "" },
}));
vi.mock("vue-router", () => ({
  useRoute: () => ({
    path: "/share/abc",
    params: { path: ["abc"] },
    query: {},
  }),
  useRouter: () => ({ push: vi.fn() }),
}));
// echo the parameters, so the wait shown can be checked
const t = (k: string, v?: Record<string, unknown>) =>
  v ? `${k} ${JSON.stringify(v)}` : k;
vi.mock("vue-i18n", async (orig) => ({
  ...(await orig<typeof import("vue-i18n")>()),
  useI18n: () => ({ t }),
}));

import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { StatusError } from "@/api/utils";
import Share from "@/views/Share.vue";
import Login from "@/views/Login.vue";

let w: VueWrapper | null = null;
afterEach(() => {
  w?.unmount();
  w = null;
  document.body.innerHTML = "";
  vi.clearAllMocks();
});

const global = {
  mocks: { $t: t },
  provide: { $showError: h.showError, $showSuccess: vi.fn() },
  directives: { focus: {} },
  stubs: {
    HeaderBar: true,
    Breadcrumbs: true,
    QrcodeVue: true,
    Item: true,
    Action: true,
  },
};

async function sharePasswordAttempt(second: StatusError) {
  setActivePinia(createPinia());
  h.fetch
    .mockRejectedValueOnce(new StatusError("401 Unauthorized", 401))
    .mockRejectedValueOnce(second);
  w = mount(Share, { attachTo: document.body, global });
  await flushPromises();
  expect(w.find('input[type="password"]').exists()).toBe(true);
  await w.find('input[type="password"]').setValue("a-guess");
  await w.find("#password button").trigger("click");
  await flushPromises();
  return w;
}

describe("share page after too many password attempts", () => {
  it("keeps the password form and says how long to wait", async () => {
    const w = await sharePasswordAttempt(
      new StatusError("429 Too Many Requests\n", 429, undefined, 30)
    );
    console.log(
      "password form shown:",
      w.find('input[type="password"]').exists(),
      "| text:",
      JSON.stringify(w.text())
    );
    expect(w.find('input[type="password"]').exists()).toBe(true);
    expect(w.text()).toContain('errors.tooManyAttempts {"seconds":30}');
    expect(w.text()).not.toContain("errors.internal");
    expect(w.text()).not.toContain("login.wrongCredentials");
  });

  it("without Retry-After it still keeps the form", async () => {
    const w = await sharePasswordAttempt(
      new StatusError("429 Too Many Requests\n", 429)
    );
    expect(w.find('input[type="password"]').exists()).toBe(true);
    expect(w.text()).toContain("errors.tooManyAttemptsLater");
  });

  it("control: a wrong password says so", async () => {
    const w = await sharePasswordAttempt(
      new StatusError("401 Unauthorized", 401)
    );
    expect(w.text()).toContain("login.wrongCredentials");
  });
});

describe("login after too many attempts", () => {
  async function attempt(err: StatusError) {
    setActivePinia(createPinia());
    h.login.mockRejectedValueOnce(err);
    w = mount(Login, { attachTo: document.body, global });
    await w.find('input[type="text"]').setValue("bob");
    await w.find('input[type="password"]').setValue("guess");
    await w.find("form").trigger("submit");
    await flushPromises();
    return w;
  }

  it("shows how long to wait on the form, not a raw toast", async () => {
    const w = await attempt(
      new StatusError("429 Too Many Requests\n", 429, undefined, 300)
    );
    console.log("login text:", JSON.stringify(w.find(".wrong").text()));
    expect(w.find(".wrong").text()).toBe(
      'errors.tooManyAttemptsMinutes {"minutes":5}'
    );
    expect(h.showError).not.toHaveBeenCalled();
  });

  it("control: wrong credentials", async () => {
    const w = await attempt(new StatusError("403 Forbidden", 403));
    expect(w.find(".wrong").text()).toBe("login.wrongCredentials");
  });
});
