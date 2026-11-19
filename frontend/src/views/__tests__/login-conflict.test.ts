// @vitest-environment jsdom
//
// The login page showed "Username already taken" for any 409. Only signup
// answers 409 for that; POST /api/login answers 409 (hook auth) when the
// account kept changing while the login stored what the hook returned, and
// the user should just try again. Real Login.vue; auth calls mocked.
import { afterEach, describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  (window as any).FileBrowser = {
    AuthMethod: "hook",
    BaseURL: "",
    StaticURL: "/static",
    Name: "",
    LoginPage: true,
    LogoutPage: "/login",
    Signup: true,
    ReCaptcha: false,
    TusSettings: { chunkSize: 1, retryCount: 0 },
  };
});
const h = vi.hoisted(() => ({ login: vi.fn(), signup: vi.fn() }));
vi.mock("@/utils/auth", () => ({
  renew: vi.fn(),
  logout: vi.fn(),
  login: h.login,
  signup: h.signup,
}));
vi.mock("vue-router", () => ({
  useRoute: () => ({ path: "/login", query: {} }),
  useRouter: () => ({ push: vi.fn().mockResolvedValue(undefined) }),
}));
vi.mock("vue-i18n", async (orig) => ({
  ...(await orig<typeof import("vue-i18n")>()),
  useI18n: () => ({ t: (k: string) => k }),
}));

import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { StatusError } from "@/api/utils";
import Login from "@/views/Login.vue";

let w: VueWrapper | null = null;
afterEach(() => {
  w?.unmount();
  w = null;
  vi.resetAllMocks();
});

async function submit(createAccount: boolean) {
  w = mount(Login, {
    global: {
      mocks: { $t: (k: string) => k },
      provide: { $showError: vi.fn() },
      directives: { focus: {} },
    },
  });
  if (createAccount) await w.find("p").trigger("click");
  const inputs = w.findAll("input:not([type=submit])");
  for (const input of inputs) await input.setValue("secret-alice");
  await w.find("form").trigger("submit");
  await flushPromises();
  return w.find(".wrong").text();
}

describe("the login page's answer to a 409", () => {
  it("login: the account changed at the same time, not 'username taken'", async () => {
    h.login.mockRejectedValueOnce(new StatusError("409 Conflict", 409));
    expect(await submit(false)).toBe("login.accountChangedConcurrently");
  });

  it("signup: the username is taken", async () => {
    h.signup.mockRejectedValueOnce(new StatusError("409 Conflict", 409));
    expect(await submit(true)).toBe("login.usernameTaken");
    expect(h.login).not.toHaveBeenCalled();
  });

  it("signup stored, then the login answered 409: not 'username taken'", async () => {
    h.signup.mockResolvedValueOnce(undefined);
    h.login.mockRejectedValueOnce(new StatusError("409 Conflict", 409));
    expect(await submit(true)).toBe("login.accountChangedConcurrently");
  });
});
