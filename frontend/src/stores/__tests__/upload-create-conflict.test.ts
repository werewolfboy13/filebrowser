// @vitest-environment jsdom
//
// A tus upload is created with POST. Without override the server answers
// 409 when the destination exists, and also while an unfinished upload to
// the same path is pending (http/tus_handlers.go keeps it for 3 minutes,
// with its temporary file hidden from listings). The conflict dialog only
// knows the listing, so it never offered a choice: the upload ended in a
// bare "409 Conflict" toast, and trying again within those minutes gave
// the same. Now the user is asked to replace or skip that file.
//
// Replacing is the user's explicit choice: it overwrites the file, or
// starts the pending upload over (discarding what it had sent, and making
// it fail if it is still running somewhere). Nothing is retried with
// override without that choice.
//
// Real upload, layout and file stores and the real Prompts/Replace
// dialog; the transport (api.post) is mocked. No network.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  (window as any).FileBrowser = {
    AuthMethod: "json",
    BaseURL: "",
    StaticURL: "/static",
    TusSettings: { chunkSize: 1024, retryCount: 0 },
  };
});
const h = vi.hoisted(() => ({
  post: vi.fn(),
  showError: vi.fn(),
}));
vi.mock("@/api", () => ({
  files: { post: h.post, abortResourceUploads: vi.fn() },
}));
vi.mock("@/api/tus", () => ({ abortAllUploads: vi.fn() }));
vi.mock("@/utils/buttons", () => ({
  default: { loading: vi.fn(), success: vi.fn(), done: vi.fn() },
}));
vi.mock("@/utils/auth", () => ({ renew: vi.fn(), logout: vi.fn() }));
const tfn = (k: string, p?: any) => (p ? `${k}${JSON.stringify(p)}` : k);

import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { createApp } from "vue";
import { createPinia, setActivePinia } from "pinia";
import { useUploadStore } from "@/stores/upload";
import { useLayoutStore } from "@/stores/layout";
import Replace from "@/components/prompts/Replace.vue";

const conflictOn = (method: string) =>
  Object.assign(new Error("409 Conflict\n"), { status: 409, method });

const settle = () => new Promise((r) => setTimeout(r, 0));

function queue(name: string, overwrite = false) {
  const file = new File(["data"], name);
  useUploadStore().upload(`/files/${name}`, name, file, overwrite, "blob");
  return file;
}

let wrapper: VueWrapper | null = null;

beforeEach(() => {
  const app = createApp({});
  app.provide("$showError", h.showError);
  const pinia = createPinia();
  app.use(pinia);
  setActivePinia(pinia);
  h.post.mockReset();
  h.showError.mockReset();
});
afterEach(() => {
  wrapper?.unmount();
  wrapper = null;
});

describe("a conflict reported when the upload is created", () => {
  it("asks to replace or skip instead of failing with a bare 409", async () => {
    h.post.mockRejectedValueOnce(conflictOn("POST"));
    queue("a.pdf");
    await settle();

    const layout = useLayoutStore();
    console.log(
      "toasts:",
      h.showError.mock.calls.length,
      "| prompt:",
      layout.currentPromptName,
      JSON.stringify(layout.currentPrompt?.props)
    );
    expect(h.showError).not.toHaveBeenCalled();
    expect(layout.currentPromptName).toBe("replace");
    expect(layout.currentPrompt?.props).toEqual({
      messageKey: "prompts.uploadConflict",
      messageParams: { name: "a.pdf" },
    });
    expect(h.post).toHaveBeenCalledTimes(1); // nothing retried on its own
  });

  it("Replace uploads the same file again, with override", async () => {
    h.post.mockRejectedValueOnce(conflictOn("POST"));
    h.post.mockResolvedValueOnce(undefined);
    const file = queue("a.pdf");
    await settle();

    const layout = useLayoutStore();
    layout.currentPrompt!.confirm();
    await settle();
    expect(layout.prompts).toEqual([]);
    expect(h.post).toHaveBeenCalledTimes(2);
    const [path, content, overwrite] = h.post.mock.calls[1];
    expect([path, content, overwrite]).toEqual(["/files/a.pdf", file, true]);
  });

  it("Skip (or Cancel, or Escape) leaves it", async () => {
    h.post.mockRejectedValueOnce(conflictOn("POST"));
    queue("a.pdf");
    await settle();
    useLayoutStore().closeHovers();
    await settle();
    expect(h.post).toHaveBeenCalledTimes(1);
    expect(h.showError).not.toHaveBeenCalled();
  });

  it("several conflicts are asked about one after another", async () => {
    h.post
      .mockRejectedValueOnce(conflictOn("POST"))
      .mockRejectedValueOnce(conflictOn("POST"))
      .mockResolvedValue(undefined);
    queue("a.pdf");
    queue("b.pdf");
    await settle();
    const layout = useLayoutStore();
    expect(layout.prompts).toHaveLength(1);
    expect(layout.currentPrompt?.props.messageParams).toEqual({
      name: "a.pdf",
    });
    layout.closeHovers(); // skip a.pdf
    expect(layout.currentPrompt?.props.messageParams).toEqual({
      name: "b.pdf",
    });
    layout.currentPrompt!.confirm(); // replace b.pdf
    await settle();
    expect(h.post.mock.calls.map((c) => [c[0], c[2]])).toEqual([
      ["/files/a.pdf", false],
      ["/files/b.pdf", false],
      ["/files/b.pdf", true],
    ]);
  });

  it("after abort (or logout) the question goes away and Replace does nothing", async () => {
    h.post.mockRejectedValueOnce(conflictOn("POST"));
    queue("a.pdf");
    await settle();
    const layout = useLayoutStore();
    const prompt = layout.currentPrompt!;
    useUploadStore().abort();
    expect(layout.prompts).toEqual([]);
    prompt.confirm(); // a click that was already on its way
    await settle();
    expect(h.post).toHaveBeenCalledTimes(1);
  });

  it("a conflict on a chunk (PATCH) is an error, not a question", async () => {
    h.post.mockRejectedValueOnce(conflictOn("PATCH"));
    queue("a.pdf");
    await settle();
    expect(useLayoutStore().prompts).toEqual([]);
    expect(h.showError).toHaveBeenCalledTimes(1);
  });

  it("a conflict while already replacing is an error, not a question again", async () => {
    h.post.mockRejectedValueOnce(conflictOn("POST"));
    queue("a.pdf", true);
    await settle();
    expect(useLayoutStore().prompts).toEqual([]);
    expect(h.showError).toHaveBeenCalledTimes(1);
  });
});

describe("the Replace dialog", () => {
  async function show(confirm = vi.fn()) {
    const layout = useLayoutStore();
    layout.showHover({
      prompt: "replace",
      props: {
        messageKey: "prompts.uploadConflict",
        messageParams: { name: "a.pdf" },
      },
      confirm,
    });
    wrapper = mount(Replace, {
      attachTo: document.body,
      global: { mocks: { $t: tfn } },
    });
    await flushPromises();
    return { layout, confirm };
  }

  it("shows the message for the file, and Replace confirms", async () => {
    const { confirm } = await show();
    expect(wrapper!.text()).toContain('prompts.uploadConflict{"name":"a.pdf"}');
    await wrapper!.find("#focus-prompt").trigger("click");
    expect(confirm).toHaveBeenCalledTimes(1);
  });

  it("Skip closes it without confirming", async () => {
    const { layout, confirm } = await show();
    const skip = wrapper!
      .findAll("button")
      .find((b) => b.text() === "prompts.skip")!;
    await skip.trigger("click");
    expect(confirm).not.toHaveBeenCalled();
    expect(layout.prompts).toEqual([]);
  });
});
