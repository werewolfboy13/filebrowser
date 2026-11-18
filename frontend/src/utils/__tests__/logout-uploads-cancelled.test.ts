// @vitest-environment jsdom
//
// logout() aborts the uploads still running or queued (they would go on
// without a session). It did so silently: after a forced logout (the
// session revoked elsewhere, or expired) the login page gave no reason,
// and the user could take the batch for finished. Now the login page says
// how many uploads were cancelled. An upload that settles after the abort
// (e.g. its request refused with 401) shows no error of its own either:
// the raw "401 Unauthorized" toast is gone.
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  (window as any).FileBrowser = {
    AuthMethod: "json",
    BaseURL: "",
    StaticURL: "/static",
    Name: "",
    LoginPage: true,
    LogoutPage: "/login",
    NoAuth: false,
  };
});
const h = vi.hoisted(() => ({
  push: vi.fn(),
  post: vi.fn(),
  showError: vi.fn(),
  currentRoute: {
    value: {
      fullPath: "/files/docs/",
      matched: [{ meta: { requiresAuth: true } }],
    },
  },
}));
vi.mock("@/router", () => ({
  default: { push: h.push, currentRoute: h.currentRoute },
}));
// The real upload store, over a mocked transport whose uploads never
// settle on their own.
vi.mock("@/api", () => ({
  files: { post: h.post, abortResourceUploads: vi.fn() },
}));
vi.mock("@/api/tus", () => ({ abortAllUploads: vi.fn() }));
vi.mock("@/utils/buttons", () => ({
  default: { loading: vi.fn(), success: vi.fn(), done: vi.fn() },
}));

import { createApp } from "vue";
import { mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { createMemoryHistory, createRouter } from "vue-router";
import i18n from "@/i18n";
import Login from "@/views/Login.vue";
import { StatusError } from "@/api/utils";
import { logout, parseToken } from "@/utils/auth";
import { useAuthStore } from "@/stores/auth";
import { useUploadStore } from "@/stores/upload";
import { fakeJwt } from "./auth-session.shared";

const settle: Array<(e: unknown) => void> = [];

function queue(count: number) {
  for (let i = 0; i < count; i++) {
    useUploadStore().upload(
      `/files/docs/f${i}.bin`,
      `f${i}.bin`,
      new File(["data"], `f${i}.bin`),
      false,
      "blob"
    );
  }
}

async function loginPage(query: Record<string, string>) {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: "/login", component: Login }],
  });
  await router.push({ path: "/login", query });
  await router.isReady();
  const wrapper = mount(Login, {
    global: {
      plugins: [router, i18n],
      provide: { $showError: vi.fn() },
    },
  });
  const message = wrapper.find(".logout-message");
  return message.exists() ? message.text() : null;
}

describe("uploads cancelled by a logout", () => {
  beforeEach(() => {
    const app = createApp({});
    app.provide("$showError", h.showError);
    const pinia = createPinia();
    app.use(pinia);
    setActivePinia(pinia);
    vi.clearAllMocks();
    settle.length = 0;
    h.post.mockImplementation(
      () => new Promise((_resolve, reject) => settle.push(reject))
    );
    i18n.global.locale.value = "en";
  });

  it("a forced logout says how many uploads it cancelled", async () => {
    parseToken(fakeJwt());
    queue(7); // 5 running (the limit), 2 queued
    await new Promise((r) => setTimeout(r, 0));
    expect(h.post).toHaveBeenCalledTimes(5);

    logout(undefined, useAuthStore().jwt);
    console.log("router.push:", JSON.stringify(h.push.mock.calls));
    expect(h.push).toHaveBeenCalledWith({
      path: "/login",
      query: {
        "logout-reason": "uploadsCancelled",
        "cancelled-uploads": "7",
        redirect: "/files/docs/",
      },
    });

    // the running uploads settle afterwards, refused: no toast of their own
    for (const reject of settle) {
      reject(new StatusError("401 Unauthorized\n", 401));
    }
    await new Promise((r) => setTimeout(r, 0));
    expect(h.showError).not.toHaveBeenCalled();
  });

  it("a folder being created when the logout comes shows no error either", async () => {
    parseToken(fakeJwt());
    useUploadStore().upload("/files/docs/sub/", "sub", null, false, "dir");
    await new Promise((r) => setTimeout(r, 0));
    expect(h.post).toHaveBeenCalledTimes(1);
    logout(undefined, useAuthStore().jwt);
    settle[0](new StatusError("401 Unauthorized\n", 401));
    await new Promise((r) => setTimeout(r, 0));
    expect(h.showError).not.toHaveBeenCalled();
  });

  it("so does a deliberate logout", () => {
    parseToken(fakeJwt());
    queue(2);
    logout(new MouseEvent("click") as any);
    expect(h.push).toHaveBeenCalledWith({
      path: "/login",
      query: { "logout-reason": "uploadsCancelled", "cancelled-uploads": "2" },
    });
  });

  it("control: no uploads, no such reason", () => {
    parseToken(fakeJwt());
    logout("inactivity", useAuthStore().jwt);
    expect(h.push).toHaveBeenCalledWith({
      path: "/login",
      query: { "logout-reason": "inactivity", redirect: "/files/docs/" },
    });
  });

  it("the login page shows it, translated", async () => {
    expect(
      await loginPage({
        "logout-reason": "uploadsCancelled",
        "cancelled-uploads": "7",
      })
    ).toBe(
      "You have been logged out: 7 uploads were cancelled before they finished."
    );
    expect(
      await loginPage({
        "logout-reason": "uploadsCancelled",
        "cancelled-uploads": "1",
      })
    ).toBe(
      "You have been logged out: 1 upload was cancelled before it finished."
    );
    // a count that is not a positive whole number: no message
    for (const count of ["0", "-2", "1.5", "x", ""]) {
      expect(
        await loginPage({
          "logout-reason": "uploadsCancelled",
          "cancelled-uploads": count,
        })
      ).toBeNull();
    }
  });
});
