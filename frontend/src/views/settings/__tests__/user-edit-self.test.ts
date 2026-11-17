// @vitest-environment jsdom
//
// Settings > Users > (yourself): saving copied the edited user, including
// a new plaintext password, into the auth store, which then kept it in
// memory/devtools. Also checks what the save sends: which=["all"] with
// the current password (JSON auth requires it for "all").
import { describe, expect, it, vi } from "vitest";

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
const h = vi.hoisted(() => ({
  // record the body as it is serialised at call time, like the real API
  update: vi.fn(async (user: any, which: string[], current: string | null) => {
    h.sent.push([JSON.parse(JSON.stringify(user)), which, current]);
  }),
  sent: [] as any[],
  get: vi.fn(),
}));
vi.mock("@/utils/auth", () => ({ renew: vi.fn(), logout: vi.fn() }));
vi.mock("@/api", () => ({
  users: { update: h.update, get: h.get },
  settings: { get: vi.fn().mockResolvedValue({ authMethod: "json" }) },
}));
vi.mock("@/components/settings/UserForm.vue", () => ({
  default: { render: () => null },
}));
vi.mock("@/views/Errors.vue", () => ({ default: { render: () => null } }));
vi.mock("@/i18n", () => ({ detectLocale: () => "en", setLocale: vi.fn() }));
vi.mock("vue-router", () => ({
  useRoute: () => ({ path: "/settings/users/7", params: { id: "7" } }),
  useRouter: () => ({ push: vi.fn() }),
}));
vi.mock("vue-i18n", async (orig) => ({
  ...(await orig<typeof import("vue-i18n")>()),
  useI18n: () => ({ t: (k: string) => k }),
}));

import { flushPromises, mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { nextTick } from "vue";
import User from "@/views/settings/User.vue";
import Prompts from "@/components/prompts/Prompts.vue";
import { useAuthStore } from "@/stores/auth";

describe("an admin editing their own user", () => {
  it("does not put the new password into the auth store", async () => {
    const pinia = createPinia();
    setActivePinia(pinia);
    const me = { id: 7, username: "root", locale: "en", perm: { admin: true } };
    useAuthStore().user = { ...me } as any;
    h.get.mockResolvedValue({ ...me, password: "" });

    const global = {
      plugins: [pinia],
      mocks: { $t: (k: string) => k },
      provide: { $showError: vi.fn(), $showSuccess: vi.fn() },
      stubs: { "router-link": true },
    };
    const page = mount(User, { attachTo: document.body, global });
    const prompts = mount(Prompts, { attachTo: document.body, global });
    await flushPromises();

    // the admin types a new password into the form
    (page.vm as any).$.setupState.user.password = "N3w-Secret!";
    await page.find("form").trigger("submit");
    await nextTick();
    await prompts.find('input[type="password"]').setValue("my-current");
    await prompts
      .findAll("button")
      .find((b) => b.text() === "buttons.ok")!
      .trigger("click");
    await flushPromises();

    const [body, which, current] = h.sent[0];
    console.log(
      "which:",
      JSON.stringify(which),
      "| current_password sent:",
      current !== null,
      "| store password:",
      JSON.stringify((useAuthStore().user as any).password)
    );
    expect(which).toEqual(["all"]);
    expect(current).toBe("my-current");
    expect(body.password).toBe("N3w-Secret!");
    expect((useAuthStore().user as any).password).toBeUndefined();
  });
});
