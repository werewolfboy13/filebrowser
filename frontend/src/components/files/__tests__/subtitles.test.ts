import { afterEach, describe, expect, it, vi } from "vitest";
import { subLabel } from "@/components/files/subtitles";

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("subLabel", () => {
  it("uses the subtitle file name without its extension", () => {
    expect(
      subLabel("https://files.example/api/subtitle/movies/Film.en.vtt?k=1")
    ).toBe("Film.en");
  });

  it("decodes percent-encoded names", () => {
    expect(subLabel("https://files.example/api/subtitle/My%20Film.srt")).toBe(
      "My Film"
    );
  });

  it("resolves relative URLs against the page origin", () => {
    vi.stubGlobal("window", { location: { origin: "http://localhost" } });
    expect(subLabel("/api/subtitle/dir/Episode%201.vtt?auth=t")).toBe(
      "Episode 1"
    );
  });
});
