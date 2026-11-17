// @vitest-environment jsdom
//
// Settings > Share management reported "share deleted" and dropped the row
// even when DELETE /api/share/<hash> failed, because api.remove() was not
// awaited inside the try: a failed revocation of a public link looked done.
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  (window as any).FileBrowser = {
    AuthMethod: "json",
    BaseURL: "",
    StaticURL: "/static",
    Name: "",
    LoginPage: true,
    LogoutPage: "/login",
    TusSettings: { chunkSize: 1, retryCount: 0 },
  };
});

const h = vi.hoisted(() => ({ list: vi.fn(), remove: vi.fn() }));

vi.mock("@/utils/auth", () => ({ renew: vi.fn(), logout: vi.fn() }));
vi.mock("@/api", () => ({
  share: {
    list: h.list,
    remove: h.remove,
    getShareURL: (s: any) => `http://fb.test/share/${s.hash}`,
  },
  users: { getAll: vi.fn().mockResolvedValue([]) },
}));
const tfn = (k: string) => k;
vi.mock("vue-i18n", async (orig) => ({
  ...(await orig<typeof import("vue-i18n")>()),
  useI18n: () => ({ t: tfn }),
}));

import { flushPromises, mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import dayjs from "dayjs";
import relativeTime from "dayjs/plugin/relativeTime";
import Shares from "@/views/settings/Shares.vue";
import Prompts from "@/components/prompts/Prompts.vue";
import { useAuthStore } from "@/stores/auth";
dayjs.extend(relativeTime);

async function deleteShare() {
  const pinia = createPinia();
  setActivePinia(pinia);
  useAuthStore().user = { id: 1, perm: { admin: false } } as any;
  h.list.mockResolvedValue([
    { hash: "AbCdEf", path: "/secret.pdf", expire: 0, userID: 1 },
  ]);
  const showSuccess = vi.fn();
  const showError = vi.fn();
  const global = {
    plugins: [pinia],
    mocks: { $t: tfn },
    provide: { $showError: showError, $showSuccess: showSuccess },
  };
  const page = mount(Shares, { global, attachTo: document.body });
  const prompts = mount(Prompts, { global, attachTo: document.body });
  await flushPromises();
  expect(page.text()).toContain("/secret.pdf");

  await page.find("button.action").trigger("click"); // trash icon
  await flushPromises();
  await prompts.find("button.button--red").trigger("click"); // "Delete"
  await flushPromises();
  return { page, showSuccess, showError };
}

describe("share deletion in Settings > Shares", () => {
  beforeEach(() => {
    document.body.innerHTML = "";
    vi.clearAllMocks();
  });

  it("does not report success or hide the row when the DELETE fails", async () => {
    // (the extra catch only keeps an unawaited rejection from failing the run)
    const failed = Promise.reject(new Error("500 Internal Server Error"));
    failed.catch(() => {});
    h.remove.mockReturnValue(failed);

    const { page, showSuccess, showError } = await deleteShare();
    console.log(
      "success toasts:",
      showSuccess.mock.calls.length,
      "| error toasts:",
      showError.mock.calls.length,
      "| row still listed:",
      page.text().includes("/secret.pdf")
    );
    expect(h.remove).toHaveBeenCalledWith("AbCdEf");
    expect(showSuccess).not.toHaveBeenCalled();
    expect(showError).toHaveBeenCalledTimes(1);
    expect(page.text()).toContain("/secret.pdf");
  });

  it("control: a successful DELETE removes the row and reports success", async () => {
    h.remove.mockResolvedValue(undefined);
    const { page, showSuccess, showError } = await deleteShare();
    expect(showSuccess).toHaveBeenCalledTimes(1);
    expect(showError).not.toHaveBeenCalled();
    expect(page.text()).not.toContain("/secret.pdf");
  });
});
