// @vitest-environment jsdom
//
// The text a forced logout keeps (utils/unsavedEdit) had one slot per tab:
// a kept text not answered yet (the restore question closed with Escape:
// "offered again next time") was dropped without a word by the next forced
// logout with another file's changes unsaved. Now a text is kept per file,
// all within the same total size (the oldest dropped first), still for one
// user only.
import { afterEach, describe, expect, it } from "vitest";
import {
  dropUnsaved,
  dropUnsavedOfOthers,
  keepUnsaved,
  MAX_KEPT_LENGTH,
  setUnsavedSource,
  unsavedFor,
} from "@/utils/unsavedEdit";

const alice = { id: 1, username: "alice" } as IUser;
const bob = { id: 2, username: "bob" } as IUser;

// a forced logout while this text of this file is unsaved
function forcedLogout(user: IUser, path: string, content: string) {
  setUnsavedSource(() => ({ path, content, base: "0:811c9dc5" }));
  keepUnsaved(user);
}
const text = (user: IUser, path: string) =>
  unsavedFor(user, path)?.content ?? null;

describe("kept unsaved text", () => {
  afterEach(() => sessionStorage.clear());

  it("a second forced logout with another file open keeps the first file's text", () => {
    forcedLogout(alice, "/files/a.md", "a.md: an hour of work");
    // alice logs in again, closes the question without answering, edits
    // b.md; forced logout #2
    forcedLogout(alice, "/files/b.md", "b.md: two lines");
    expect(text(alice, "/files/a.md")).toBe("a.md: an hour of work");
    expect(text(alice, "/files/b.md")).toBe("b.md: two lines");
    expect(unsavedFor(alice, "/files/a.md")?.base).toBe("0:811c9dc5");
  });

  it("a newer text of the same file replaces the older one", () => {
    forcedLogout(alice, "/files/a.md", "first");
    forcedLogout(alice, "/files/b.md", "b");
    forcedLogout(alice, "/files/a.md", "second");
    expect(text(alice, "/files/a.md")).toBe("second");
    expect(text(alice, "/files/b.md")).toBe("b");
  });

  it("stays within the same total size, dropping the oldest first", () => {
    const third = "x".repeat(Math.floor(MAX_KEPT_LENGTH / 3));
    forcedLogout(alice, "/files/1.md", third);
    forcedLogout(alice, "/files/2.md", third);
    forcedLogout(alice, "/files/3.md", third);
    expect(text(alice, "/files/1.md")).toBe(third);
    // the fourth does not fit with all three: the oldest goes
    forcedLogout(alice, "/files/4.md", third);
    expect(text(alice, "/files/1.md")).toBeNull();
    for (const n of [2, 3, 4])
      expect(text(alice, `/files/${n}.md`)).toBe(third);
    const stored = Object.values({ ...sessionStorage }).join("");
    expect(stored.length).toBeLessThan(MAX_KEPT_LENGTH + 1000);
    // one too long by itself is not kept, and drops nothing
    forcedLogout(alice, "/files/5.md", "y".repeat(MAX_KEPT_LENGTH + 1));
    expect(text(alice, "/files/5.md")).toBeNull();
    expect(text(alice, "/files/2.md")).toBe(third);
  });

  it("dropping one file's text keeps the others; dropping all drops all", () => {
    forcedLogout(alice, "/files/a.md", "a");
    forcedLogout(alice, "/files/b.md", "b");
    dropUnsaved("/files/a.md");
    expect(text(alice, "/files/a.md")).toBeNull();
    expect(text(alice, "/files/b.md")).toBe("b");
    dropUnsaved();
    expect(text(alice, "/files/b.md")).toBeNull();
  });

  it("is for its user only: another user's session drops all of it", () => {
    forcedLogout(alice, "/files/a.md", "a");
    forcedLogout(alice, "/files/b.md", "b");
    expect(text(bob, "/files/a.md")).toBeNull();
    expect(text(alice, "/files/b.md")).toBeNull();
    expect(sessionStorage.length).toBe(0);
    forcedLogout(alice, "/files/a.md", "a");
    dropUnsavedOfOthers(bob);
    expect(sessionStorage.length).toBe(0);
  });

  it("a text kept by the previous version (one file) is still offered", () => {
    sessionStorage.setItem(
      "editor-unsaved",
      JSON.stringify({
        user: 1,
        username: "alice",
        path: "/files/a.md",
        content: "old shape",
      })
    );
    expect(unsavedFor(alice, "/files/a.md")).toEqual({
      content: "old shape",
      base: undefined,
    });
    forcedLogout(alice, "/files/b.md", "b");
    expect(text(alice, "/files/a.md")).toBe("old shape");
    expect(text(alice, "/files/b.md")).toBe("b");
  });
});
