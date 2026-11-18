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
  fetch: vi.fn(),
  showError: vi.fn(),
}));
vi.mock("@/api", () => ({
  files: { post: h.post, fetch: h.fetch, abortResourceUploads: vi.fn() },
  share: {},
  users: {},
  pub: {},
}));
vi.mock("@/utils/upload", () => ({
  checkConflict: vi.fn(),
  handleFiles: vi.fn(),
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
import { useFileStore } from "@/stores/file";
import { watch } from "vue";
import Replace from "@/components/prompts/Replace.vue";
import Prompts from "@/components/prompts/Prompts.vue";

const conflictOn = (method: string) =>
  Object.assign(new Error("409 Conflict\n"), { status: 409, method });

const settle = () => new Promise((r) => setTimeout(r, 0));

function queue(name: string, overwrite = false) {
  const file = new File(["data"], name);
  useUploadStore().upload(`/files/${name}`, name, file, overwrite, "blob");
  return file;
}

// what the question's Skip button does (see Replace.vue)
function skipQuestion() {
  const layout = useLayoutStore();
  layout.currentPrompt!.answered = true;
  layout.closeHovers();
}

let wrapper: VueWrapper | null = null;

beforeEach(() => {
  const app = createApp({});
  app.provide("$showError", h.showError);
  const pinia = createPinia();
  app.use(pinia);
  setActivePinia(pinia);
  h.post.mockReset();
  h.fetch.mockReset();
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

  // When the queue drains the store asks the listing to reload, and the
  // listing (Files.vue fetchData) closes the open prompt when it does: the
  // question vanished a moment after it appeared, a silent Skip. The batch
  // is not over while a question is open.
  it("the question stays until answered, and the listing reloads after", async () => {
    const layout = useLayoutStore();
    const files = useFileStore();
    // what Files.vue does on reload
    let reloads = 0;
    watch(
      () => files.reload,
      (reload) => {
        if (!reload) return;
        reloads++;
        files.reload = false;
        layout.closeHovers();
      },
      { flush: "sync" }
    );
    h.post.mockRejectedValueOnce(conflictOn("POST"));
    queue("a.pdf");
    await settle();
    await settle();
    console.log("prompts after the queue drained:", layout.prompts.length);
    expect(layout.currentPromptName).toBe("replace");
    expect(reloads).toBe(0);

    skipQuestion();
    await settle();
    expect(reloads).toBe(1);
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
    expect(useFileStore().reload).toBe(true); // the batch finished
  });

  it("Skip (or Cancel, or Escape) leaves it", async () => {
    h.post.mockRejectedValueOnce(conflictOn("POST"));
    queue("a.pdf");
    await settle();
    skipQuestion();
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
    skipQuestion(); // a.pdf
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

  // In a folder upload, a/x.txt and b/x.txt must be told apart.
  it("names the file by its path within the folder uploaded", async () => {
    h.post.mockRejectedValueOnce(conflictOn("POST"));
    const { handleFiles } =
      await vi.importActual<typeof import("@/utils/upload")>("@/utils/upload");
    const file = new File(["x"], "x.txt");
    handleFiles(
      [{ file, name: "x.txt", size: 1, isDir: false, fullPath: "b/x.txt" }],
      "/files/up/"
    );
    await settle();
    const layout = useLayoutStore();
    console.log("question:", JSON.stringify(layout.currentPrompt?.props));
    expect(h.post.mock.calls[0][0]).toBe("/files/up/b/x.txt");
    expect(layout.currentPrompt?.props.messageParams).toEqual({
      name: "b/x.txt",
    });
  });

  it("a conflict on a chunk (PATCH) is an error, not a question", async () => {
    h.post.mockRejectedValueOnce(conflictOn("PATCH"));
    queue("a.pdf");
    await settle();
    expect(useLayoutStore().prompts).toEqual([]);
    expect(h.showError).toHaveBeenCalledTimes(1);
  });

  // The final PATCH is refused with 409 when a file appeared at the
  // destination during the upload: the same question as on create (and as
  // the plain POST upload asks), not a raw "409 Conflict" toast.
  it("a conflict on the final PATCH asks to replace or skip", async () => {
    h.post.mockRejectedValueOnce(
      Object.assign(conflictOn("PATCH"), { final: true })
    );
    h.post.mockResolvedValueOnce(undefined);
    const file = queue("a.pdf");
    await settle();
    const layout = useLayoutStore();
    console.log(
      "final PATCH 409: toasts:",
      h.showError.mock.calls.length,
      "| prompt:",
      layout.currentPromptName ?? null
    );
    expect(h.showError).not.toHaveBeenCalled();
    expect(layout.currentPromptName).toBe("replace");
    layout.currentPrompt!.confirm(); // Replace: sent again, with override
    await settle();
    expect(h.post.mock.calls[1].slice(0, 3)).toEqual([
      "/files/a.pdf",
      file,
      true,
    ]);
  });

  it("a conflict while already replacing is an error, not a question again", async () => {
    h.post.mockRejectedValueOnce(conflictOn("POST"));
    queue("a.pdf", true);
    await settle();
    expect(useLayoutStore().prompts).toEqual([]);
    expect(h.showError).toHaveBeenCalledTimes(1);
  });
});

// Abort starts the queue over. It used to do so only once the last aborted
// upload had settled, and that dropped the files queued in the meantime;
// an aborted upload can settle late (a plain POST reads the whole file
// first when the page is not served over http(s)).
// When the answer to the final PATCH is lost (a proxy's 502, a network
// blip) after the server stored the file, tus resumes: HEAD finds the
// upload gone (it was completed), so tus creates it again, and that is
// refused with 409 because the file is now there. That 409 is about the
// file this upload stored: it must not be asked about as a conflict.
// The question can be closed from outside it: a route change closes the
// open prompt twice (Layout's route watcher and Files' fetchData each call
// closeHovers), and closing a question showed the next one at once, so
// one Back press skipped two files without a word. Closed from outside,
// the question now skips every file still to be asked about, with one
// toast saying how many. Skip, Escape and a click beside the question
// still skip just that file.
describe("closing the question from outside it, e.g. by navigating away", () => {
  const names = () => useLayoutStore().currentPrompt?.props?.messageParams;
  async function threeConflicts() {
    h.post
      .mockRejectedValueOnce(conflictOn("POST"))
      .mockRejectedValueOnce(conflictOn("POST"))
      .mockRejectedValueOnce(conflictOn("POST"));
    queue("a.pdf");
    queue("b.pdf");
    queue("c.pdf");
    await settle();
  }

  it("skips every file still to be asked about, with one toast saying so", async () => {
    await threeConflicts();
    const layout = useLayoutStore();
    expect(names()).toEqual({ name: "a.pdf" });
    // one route change
    layout.closeHovers();
    layout.closeHovers();
    await settle();
    console.log(
      "after one route change: question:",
      JSON.stringify(names() ?? null),
      "| toasts:",
      JSON.stringify(h.showError.mock.calls)
    );
    expect(layout.prompts).toEqual([]);
    expect(h.showError).toHaveBeenCalledTimes(1);
    const [message, report] = h.showError.mock.calls[0];
    expect(message).toMatch(/^3 files were not uploaded/);
    expect(report).toBe(false);
    expect(h.post).toHaveBeenCalledTimes(3); // nothing replaced
    expect(useFileStore().reload).toBe(true); // the batch finished
  });

  it("Skip, Escape and a click beside the question skip just that file", async () => {
    wrapper = mount(Prompts, {
      attachTo: document.body,
      global: { mocks: { $t: tfn } },
    });
    await threeConflicts();
    await flushPromises();
    const skip = wrapper
      .findAll("button")
      .find((b) => b.text() === "prompts.skip")!;
    await skip.trigger("click");
    await flushPromises();
    expect(names()).toEqual({ name: "b.pdf" });
    window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
    await flushPromises();
    expect(names()).toEqual({ name: "c.pdf" });
    await wrapper.find("#modal-background").trigger("click");
    await flushPromises();
    expect(useLayoutStore().prompts).toEqual([]);
    expect(h.showError).not.toHaveBeenCalled();
    expect(h.post).toHaveBeenCalledTimes(3);
  });
});

describe("a conflict when tus creates the upload again", () => {
  const recreateConflict = () =>
    Object.assign(conflictOn("POST"), { recreated: true });
  const listing = (size: number) => ({
    isDir: true,
    items: [
      { name: "other.txt", size: 4, isDir: false },
      { name: "a b.pdf", size, isDir: false },
    ],
  });
  const queueAB = () => {
    const file = new File(["data"], "a b.pdf"); // 4 bytes
    useUploadStore().upload(
      "/files/docs/a%20b.pdf",
      "a b.pdf",
      file,
      false,
      "blob"
    );
  };

  it("is not asked about when the file at the destination has its size", async () => {
    h.post.mockRejectedValueOnce(recreateConflict());
    h.fetch.mockResolvedValueOnce(listing(4));
    queueAB();
    await settle();
    await settle();
    const layout = useLayoutStore();
    const store = useUploadStore();
    console.log(
      "re-create 409, same size there: prompt:",
      layout.currentPromptName ?? null,
      "| listing fetched:",
      JSON.stringify(h.fetch.mock.calls.map((c) => c[0]))
    );
    expect(h.fetch).toHaveBeenCalledWith("/files/docs/");
    expect(layout.prompts).toEqual([]);
    expect(h.showError).not.toHaveBeenCalled();
    expect(useFileStore().reload).toBe(true); // the batch finished
    expect(store.pendingUploadCount).toBe(0);
  });

  it("is asked about when the file there has another size", async () => {
    h.post.mockRejectedValueOnce(recreateConflict());
    h.fetch.mockResolvedValueOnce(listing(5));
    queueAB();
    await settle();
    await settle();
    expect(useLayoutStore().currentPromptName).toBe("replace");
  });

  it("is asked about when the destination cannot be listed", async () => {
    h.post.mockRejectedValueOnce(recreateConflict());
    h.fetch.mockRejectedValueOnce(new Error("000 No connection"));
    queueAB();
    await settle();
    await settle();
    expect(useLayoutStore().currentPromptName).toBe("replace");
  });

  // A file of the same size may be another file: only the re-create
  // (after this upload's data was sent) is checked.
  it("control: a conflict on first creating the upload is asked about without looking", async () => {
    h.post.mockRejectedValueOnce(conflictOn("POST"));
    h.fetch.mockResolvedValue(listing(4));
    queueAB();
    await settle();
    await settle();
    expect(h.fetch).not.toHaveBeenCalled();
    expect(useLayoutStore().currentPromptName).toBe("replace");
  });
});

describe("abort and a late-settling upload", () => {
  it("a file queued after abort is uploaded, and the late settlement changes nothing", async () => {
    let settleBig!: (e: Error) => void;
    let finishNext!: () => void;
    h.post
      .mockImplementationOnce(
        () => new Promise((_, reject) => (settleBig = reject))
      )
      .mockImplementationOnce(
        () => new Promise<void>((resolve) => (finishNext = resolve))
      );
    queue("big.iso");
    await settle();
    const store = useUploadStore();
    store.abort(); // the transport is mocked: big.iso settles later
    const next = queue("next.pdf");
    await settle();
    settleBig(new Error("Upload aborted"));
    await settle();
    console.log(
      "posts:",
      JSON.stringify(h.post.mock.calls.map((c) => c[0])),
      "| pending after:",
      store.pendingUploadCount
    );
    expect(h.post.mock.calls.map((c) => c[0])).toEqual([
      "/files/big.iso",
      "/files/next.pdf",
    ]);
    expect(store.pendingUploadCount).toBe(1);
    expect(store.totalBytes).toBe(next.size);
    expect(useFileStore().reload).toBe(false);

    finishNext();
    await settle();
    expect(store.pendingUploadCount).toBe(0);
    expect(useFileStore().reload).toBe(true); // the new batch finished
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
