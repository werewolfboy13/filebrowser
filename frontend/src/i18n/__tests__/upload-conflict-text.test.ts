// The Replace/Skip question about an upload's conflict said that
// replacing a pending upload "fails if it is still running". It does not:
// the server locks an upload only while it handles each of its requests,
// so between two chunks Replace discards the other upload, running or not
// (its next chunk then gets 404). The question must say so.
import { readFileSync } from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";

// the file itself: importing it gives the i18n plugin's compiled messages
const en = JSON.parse(
  readFileSync(path.resolve(import.meta.dirname, "../en.json"), "utf8")
);

describe("prompts.uploadConflict", () => {
  const text: string = en.prompts.uploadConflict;

  it("does not claim that Replace fails on a running upload", () => {
    expect(text).not.toMatch(/fails/i);
  });

  it("says that Replace discards the pending upload even while it runs", () => {
    expect(text).toMatch(/discards that upload, even one still in progress/);
    expect(text).toContain("{name}");
  });
});
