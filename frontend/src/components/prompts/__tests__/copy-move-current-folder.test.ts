// The Copy and Move prompts compare the folder being viewed with the chosen
// destination (which always ends in "/"). At a folder URL without the
// trailing slash, copying into the current folder was not recognised: it
// was sent without rename (the server refuses to copy a file onto itself)
// and the listing was not reloaded; Move was not disabled for the current
// folder.
import { beforeEach, describe, expect, it, vi } from "vitest";
import CopyPrompt from "@/components/prompts/Copy.vue";
import MovePrompt from "@/components/prompts/Move.vue";
import { files as api } from "@/api";
import { checkConflict } from "@/utils/upload";

vi.mock("@/api", () => ({
  files: {
    copy: vi.fn().mockResolvedValue(undefined),
    move: vi.fn().mockResolvedValue(undefined),
  },
}));
vi.mock("@/api/utils", () => ({
  removePrefix: (value: string) => value.replace(/^\/files/, ""),
}));
vi.mock("@/utils/buttons", () => ({
  default: { loading: vi.fn(), success: vi.fn(), done: vi.fn() },
}));
vi.mock("@/utils/upload", () => ({ checkConflict: vi.fn() }));

function makeContext(routePath: string) {
  return {
    selected: [0],
    req: {
      items: [
        {
          url: "/files/docs/file.txt",
          name: "file.txt",
          size: 12,
          isDir: false,
          modified: "2026-06-04T00:00:00Z",
        },
      ],
    },
    dest: "/files/docs/",
    user: { redirectAfterCopyMove: false },
    $route: { path: routePath },
    $router: { push: vi.fn() },
    reload: false,
    preselect: "",
    showHover: vi.fn(),
    closeHovers: vi.fn(),
    $showError: vi.fn(),
  };
}

describe("copy into the folder being viewed", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(checkConflict).mockResolvedValue([]);
  });

  for (const path of ["/files/docs/", "/files/docs"]) {
    it(`at ${path} copies with rename and reloads the listing`, async () => {
      const context = makeContext(path);
      await (CopyPrompt as any).methods.copy.call(context, {
        preventDefault: vi.fn(),
      });
      expect(vi.mocked(api.copy).mock.calls[0][0][0]).toMatchObject({
        to: "/files/docs/file.txt",
        rename: true,
      });
      expect(context.reload).toBe(true);
    });
  }

  for (const path of ["/files/docs/", "/files/docs"]) {
    it(`Move is disabled for the current folder at ${path}`, () => {
      const isCurrentFolder = (MovePrompt as any).computed.isCurrentFolder;
      expect(isCurrentFolder.call(makeContext(path))).toBe(true);
    });
  }
});
