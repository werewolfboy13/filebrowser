// @vitest-environment jsdom
//
// Profile (JSON auth): after a password change, Profile.vue stored the NEW
// plaintext password in the Pinia auth store (visible in devtools and
// memory) and, because later updates spread the whole stored user into
// the request body, re-sent it with every unrelated settings update.
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
const update = vi.hoisted(() => vi.fn().mockResolvedValue(undefined));
vi.mock("@/utils/auth", () => ({ renew: vi.fn(), logout: vi.fn() }));
vi.mock("@/api", () => ({ users: { update } }));
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
import { changePassword, mountProfile, PROFILE_FIELDS } from "./profile.shared";

describe("Profile with JSON auth", () => {
  beforeEach(() => {
    document.body.innerHTML = "";
    update.mockClear();
  });

  it("does not keep the new password in the store or send it with later updates", async () => {
    const { w, authStore } = await mountProfile();
    await changePassword(w, "old-pass");

    await w.find('input[name="singleClick"]').setValue(true);
    await w.findAll("form")[0].trigger("submit");
    await flushPromises();

    const later = update.mock.calls[1];
    console.log(
      "store user.password:",
      JSON.stringify((authStore.user as any).password),
      "| later update body:",
      JSON.stringify(later[0])
    );
    expect((authStore.user as any).password).toBeUndefined();
    expect(later[0].password).toBeUndefined();
  });

  it("sends the password change with which=[password] and the current password, then clears the fields", async () => {
    const { w, showSuccess } = await mountProfile();
    await changePassword(w, "old-pass");

    expect(update).toHaveBeenCalledTimes(1);
    const [body, which, current] = update.mock.calls[0];
    expect(which).toEqual(["password"]);
    expect(current).toBe("old-pass");
    expect(body).toEqual({ id: 7, password: "N3w-Secret!" });
    expect(showSuccess).toHaveBeenCalledTimes(1);
    for (const name of ["password", "passwordConf", "current_password"]) {
      const el = w.find(`input[name="${name}"]`).element as HTMLInputElement;
      expect(el.value).toBe("");
    }
  });

  it("a password change without the current password is not sent", async () => {
    const { w } = await mountProfile();
    await changePassword(w);
    expect(update).not.toHaveBeenCalled();
  });

  it("settings updates send only the profile fields, without a current password", async () => {
    const { w } = await mountProfile();
    await w.find('input[name="hideDotfiles"]').setValue(true);
    await w.findAll("form")[0].trigger("submit");
    await flushPromises();

    const [body, which, current] = update.mock.calls[0];
    expect(which).toEqual(PROFILE_FIELDS);
    expect(current ?? null).toBeNull();
    expect(Object.keys(body).sort()).toEqual(["id", ...PROFILE_FIELDS].sort());
    expect(body.hideDotfiles).toBe(true);
  });
});
