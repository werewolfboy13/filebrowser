// @vitest-environment jsdom
//
// An admin saving ANOTHER user answered 409 (the target account was
// revoked while the request was at work; nothing was stored) showed the
// raw "409 Conflict" with a "Report issue" link. It now says the user was
// changed at the same time. Harness from user-edit-self.test.ts.
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
const h = vi.hoisted(() => ({ update: vi.fn(), get: vi.fn() }));
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
  useRoute: () => ({ path: "/settings/users/8", params: { id: "8" } }),
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
import { StatusError } from "@/api/utils";

// an admin saves user 8; the update fails with this; what is shown?
async function saveAnswered(error: Error) {
  const pinia = createPinia();
  setActivePinia(pinia);
  useAuthStore().user = {
    id: 7,
    username: "root",
    locale: "en",
    perm: { admin: true },
  } as any;
  h.get.mockResolvedValue({
    id: 8,
    username: "bob",
    locale: "en",
    perm: {},
    password: "",
  });
  h.update.mockRejectedValueOnce(error);
  const showError = vi.fn();
  const global = {
    plugins: [pinia],
    mocks: { $t: (k: string) => k },
    provide: { $showError: showError, $showSuccess: vi.fn() },
    stubs: { "router-link": true },
  };
  const page = mount(User, { attachTo: document.body, global });
  const prompts = mount(Prompts, { attachTo: document.body, global });
  await flushPromises();
  await page.find("form").trigger("submit");
  await nextTick();
  await prompts.find('input[type="password"]').setValue("my-current");
  await prompts
    .findAll("button")
    .find((b) => b.text() === "buttons.ok")!
    .trigger("click");
  await flushPromises();
  page.unmount();
  prompts.unmount();
  return showError;
}

describe("an admin editing another user", () => {
  it("a 409 says the user was changed at the same time", async () => {
    const showError = await saveAnswered(
      new StatusError("409 Conflict\n", 409)
    );
    expect(showError).toHaveBeenCalledWith("errors.userChangedConcurrently");
  });

  it("other errors are shown as they are", async () => {
    const error = new StatusError("500 Internal Server Error\n", 500);
    const showError = await saveAnswered(error);
    expect(showError).toHaveBeenCalledWith(error);
  });
});
