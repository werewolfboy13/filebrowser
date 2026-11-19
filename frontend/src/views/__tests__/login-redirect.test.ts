// @vitest-environment jsdom
//
// After logging in, the login page goes to its `redirect` (the page a
// forced logout left). It went there with router.push({ path: redirect }),
// which keeps only the path: after a forced logout in the CSV editor
// (/files/t.csv?edit=true) the user landed on the table, not the editor.
// It now goes to the whole value, and only to a path of this app: a value
// that is not one ("https://host/x", "@host", "javascript:..."), or whose
// path has an empty segment once decoded ("//host/x", "/\host", "/%2Fx",
// "/files//x": no route
// matches it, and the catch-all route redirected it to itself until
// router.push failed with "Maximum call stack size exceeded"), leads to
// the default page instead. Real Login.vue and the app's route table and
// history.
import { afterEach, describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  (window as any).FileBrowser = {
    AuthMethod: "json",
    BaseURL: "",
    StaticURL: "/static",
    Name: "",
    LoginPage: true,
    LogoutPage: "/login",
    Signup: false,
    ReCaptcha: false,
    TusSettings: { chunkSize: 1, retryCount: 0 },
  };
  (window as any).matchMedia ??= () => ({
    matches: false,
    addListener() {},
    removeListener() {},
  });
});
const h = vi.hoisted(() => ({ login: vi.fn() }));
vi.mock("@/utils/auth", () => ({
  renew: vi.fn(),
  logout: vi.fn(),
  validateLogin: vi.fn(),
  signup: vi.fn(),
  login: h.login,
}));
vi.mock("vue-i18n", async (orig) => ({
  ...(await orig<typeof import("vue-i18n")>()),
  useI18n: () => ({ t: (k: string) => k }),
}));

import { mount, type VueWrapper } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import router from "@/router";
import Login from "@/views/Login.vue";
import { useAuthStore } from "@/stores/auth";

let w: VueWrapper | null = null;
afterEach(() => {
  w?.unmount();
  w = null;
});

const pinia = createPinia();
setActivePinia(pinia);
h.login.mockImplementation(async () => {
  useAuthStore().user = { id: 1, username: "alice", perm: {} } as any;
});

// logs in on the login page with this redirect; where does it lead?
async function loginWith(redirect: string | string[] | undefined) {
  useAuthStore().user = null;
  await router.push({
    path: "/login",
    query: redirect === undefined ? {} : { redirect },
  });
  w = mount(Login, {
    global: {
      plugins: [pinia, router],
      mocks: { $t: (k: string) => k },
      provide: { $showError: vi.fn() },
      directives: { focus: {} },
    },
  });
  const inputs = w.findAll("input");
  await inputs[0].setValue("alice");
  await inputs[1].setValue("secret");
  await w.find("form").trigger("submit");
  await vi.waitFor(
    () => expect(router.currentRoute.value.path).not.toBe("/login"),
    { timeout: 10_000 }
  );
  const landed = router.currentRoute.value.fullPath;
  w.unmount();
  w = null;
  return landed;
}

describe("the login page's redirect", { timeout: 60_000 }, () => {
  it("keeps the query of the page it leads back to", async () => {
    expect(await loginWith("/files/t.csv?edit=true")).toBe(
      "/files/t.csv?edit=true"
    );
    expect(await loginWith("/settings/profile")).toBe("/settings/profile");
    expect(await loginWith(undefined)).toBe("/files/");
  });

  it("leads only to a path of this app, else to the default page, without an error", async () => {
    const errors = vi.spyOn(console, "error");
    const warnings = vi.spyOn(console, "warn");
    const origin = window.location.origin;
    for (const value of [
      "//evil.example/x",
      "/\\evil.example/x",
      "https://evil.example/x",
      "@evil.example",
      "javascript:alert(1)",
      "/files//x",
      "///x",
      "/%2Fx",
      "/%2F%2Fevil.example/x",
      "/files/a%2F%2Fb",
      "/%5Cevil.example/x",
      ["/files/a", "/files/b"],
    ]) {
      expect(await loginWith(value), JSON.stringify(value)).toBe("/files/");
      expect(window.location.origin).toBe(origin);
    }
    expect(errors).not.toHaveBeenCalled();
    expect(warnings).not.toHaveBeenCalled();
  });
});
