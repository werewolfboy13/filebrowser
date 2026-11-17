// Shared setup for the Profile.vue tests (the auth method is fixed per test
// file, since constants are read once at import).
import { vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";

export async function mountProfile() {
  const Profile = (await import("@/views/settings/Profile.vue")).default;
  const { useAuthStore } = await import("@/stores/auth");
  const pinia = createPinia();
  setActivePinia(pinia);
  const authStore = useAuthStore();
  authStore.user = {
    id: 7,
    username: "alice",
    locale: "en",
    lockPassword: false,
    hideDotfiles: false,
    singleClick: false,
    redirectAfterCopyMove: false,
    dateFormat: false,
    aceEditorTheme: "",
    viewMode: "list",
    sorting: { by: "name", asc: true },
    perm: { admin: false },
    scope: "/home/alice",
  } as any;
  const showSuccess = vi.fn();
  const showError = vi.fn();
  const w = mount(Profile, {
    attachTo: document.body,
    global: {
      plugins: [pinia],
      mocks: { $t: (k: string) => k },
      provide: { $showError: showError, $showSuccess: showSuccess },
    },
  });
  await flushPromises();
  return { w, authStore, showSuccess, showError };
}

export async function changePassword(w: any, current?: string) {
  await w.find('input[name="password"]').setValue("N3w-Secret!");
  await w.find('input[name="passwordConf"]').setValue("N3w-Secret!");
  if (current !== undefined) {
    await w.find('input[name="current_password"]').setValue(current);
  }
  await w.findAll("form")[1].trigger("submit");
  await flushPromises();
}

export const PROFILE_FIELDS = [
  "locale",
  "hideDotfiles",
  "singleClick",
  "redirectAfterCopyMove",
  "dateFormat",
  "aceEditorTheme",
];
