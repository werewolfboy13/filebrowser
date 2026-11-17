// @vitest-environment jsdom
//
// Profile with a non-JSON auth method (e.g. "hook"): the current-password
// field is hidden, but it was still required, so "Update password" did
// nothing at all (no request, no message).
import { describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  (window as any).FileBrowser = {
    AuthMethod: "hook",
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

import { changePassword, mountProfile } from "./profile.shared";

describe("Profile with hook auth", () => {
  it("submits a password change although the current-password field is hidden", async () => {
    const { w, authStore } = await mountProfile();
    const hasCurrentField = w.find('input[name="current_password"]').exists();
    await changePassword(w);

    console.log(
      "current-password field shown:",
      hasCurrentField,
      "| update calls:",
      update.mock.calls.length
    );
    expect(hasCurrentField).toBe(false);
    expect(update).toHaveBeenCalledTimes(1);
    const [body, which, current] = update.mock.calls[0];
    expect(which).toEqual(["password"]);
    expect(current ?? null).toBeNull(); // no current_password field sent
    expect(body.password).toBe("N3w-Secret!");
    expect((authStore.user as any).password).toBeUndefined();
  });
});
