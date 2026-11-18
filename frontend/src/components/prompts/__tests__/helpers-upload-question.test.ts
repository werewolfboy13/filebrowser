// @vitest-environment jsdom
//
// Move, Copy and paste send their request with files.move/copy, and Upload
// and drag and drop queue their files with handleFiles, after waiting for
// the destination's listing (checkConflict). Both helpers close the prompt
// on top when called, meant to be the dialog that called them. An upload
// batch can push its Replace/Skip question on top meanwhile: it was closed
// without an answer, which skips every file still to be asked about. The
// helpers now leave that question open.
//
// Real files.move/copy and handleFiles, real layout and upload stores; the
// transport (fetchURL, tus) and button helpers are mocked. No network.
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  (window as any).FileBrowser = {
    AuthMethod: "json",
    BaseURL: "",
    StaticURL: "/static",
    TusSettings: { chunkSize: 1024, retryCount: 0 },
  };
});
const h = vi.hoisted(() => ({
  fetchURL: vi.fn(),
  showError: vi.fn(),
}));
vi.mock("@/api/utils", async (orig) => ({
  ...(await orig<typeof import("@/api/utils")>()),
  fetchURL: h.fetchURL,
}));
// every upload is refused: a file is at the destination
vi.mock("@/api/tus", () => ({
  useTus: async () => true,
  upload: () =>
    Promise.reject(
      Object.assign(new Error("409 Conflict\n"), {
        status: 409,
        method: "POST",
      })
    ),
  abortAllUploads: vi.fn(),
  UploadError: class extends Error {},
}));
vi.mock("@/utils/buttons", () => ({
  default: { loading: vi.fn(), success: vi.fn(), done: vi.fn() },
}));
vi.mock("@/utils/auth", () => ({ renew: vi.fn(), logout: vi.fn() }));

import { flushPromises } from "@vue/test-utils";
import { createApp } from "vue";
import { createPinia, setActivePinia } from "pinia";
import { files as api } from "@/api";
import { handleFiles } from "@/utils/upload";
import { useLayoutStore } from "@/stores/layout";
import { useUploadStore } from "@/stores/upload";

const settle = () => new Promise((r) => setTimeout(r, 0));

beforeEach(() => {
  const app = createApp({});
  app.provide("$showError", h.showError);
  const pinia = createPinia();
  app.use(pinia);
  setActivePinia(pinia);
  h.fetchURL.mockReset();
  h.fetchURL.mockResolvedValue(new Response(""));
  h.showError.mockReset();
});

// an upload batch asks about a conflict
async function uploadQuestion() {
  useUploadStore().upload(
    "/files/docs/a.pdf",
    "a.pdf",
    new File(["data"], "a.pdf"),
    false,
    "blob"
  );
  await settle();
  await flushPromises();
  expect(useLayoutStore().currentPromptName).toBe("replace");
}

const helpers: Array<[string, () => unknown]> = [
  ["move", () => api.move([{ from: "/files/docs/x", to: "/files/y/x" }])],
  ["copy", () => api.copy([{ from: "/files/docs/x", to: "/files/y/x" }])],
  ["handleFiles", () => handleFiles([], "/files/docs/")],
];

describe("the upload's Replace/Skip question", () => {
  for (const [name, call] of helpers) {
    it(`is left open by ${name}`, async () => {
      await uploadQuestion();
      await call();
      await flushPromises();
      const layout = useLayoutStore();
      console.log(
        `${name}: prompt:`,
        layout.currentPromptName ?? null,
        "| toasts:",
        h.showError.mock.calls.length
      );
      expect(layout.currentPromptName).toBe("replace");
      expect(h.showError).not.toHaveBeenCalled();
    });

    it(`control: ${name} still closes the dialog that called it`, async () => {
      const layout = useLayoutStore();
      layout.showHover(name === "handleFiles" ? "upload" : name);
      await call();
      expect(layout.prompts).toEqual([]);
    });
  }
});
