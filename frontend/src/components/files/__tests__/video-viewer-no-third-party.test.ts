// @vitest-environment jsdom
//
// The Video.js 10 player (custom elements from @videojs/html: player, skin,
// icons and the <media-i18n> locale packs) must not load anything from a
// third party at runtime: its scripts, styles, icons and translations have
// to come from the app bundle. Same check as the editor's
// (views/files/__tests__/editor-no-third-party.test.ts). Nothing here
// touches the network: jsdom does not fetch, and fetch/XHR/workers are
// stubbed by the guard.
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import {
  startNetworkGuard,
  type NetworkGuard,
} from "@/__tests__/network-guard";

let guard: NetworkGuard;

// jsdom gaps the player's store touches: media track lists that are event
// targets, and no-op playback methods
function emptyTrackList() {
  const list = new EventTarget() as EventTarget & Iterable<never>;
  Object.assign(list, { length: 0, [Symbol.iterator]: [][Symbol.iterator] });
  return list;
}
for (const prop of ["audioTracks", "videoTracks"]) {
  Object.defineProperty(HTMLMediaElement.prototype, prop, {
    configurable: true,
    get: emptyTrackList,
  });
}
HTMLMediaElement.prototype.load = () => {};
HTMLMediaElement.prototype.pause = () => {};

describe("video player network requests", () => {
  beforeEach(() => {
    document.body.innerHTML = "";
    guard = startNetworkGuard();
  });
  afterEach(() => guard.stop());

  for (const lang of ["en", "de", "ja"]) {
    it(`only requests same-origin resources (html lang=${lang})`, async () => {
      document.documentElement.lang = lang;
      const { default: VideoViewer } = await import("../VideoViewer.vue");
      const w = mount(VideoViewer, {
        attachTo: document.body,
        props: {
          source: "/api/raw/movies/clip.mp4?inline=true",
          subtitles: ["/api/subtitle/movies/clip.en.vtt"],
        },
      });
      await flushPromises();
      for (let i = 0; i < 10; i++) await new Promise((r) => setTimeout(r, 10));
      await customElements.whenDefined("video-skin");

      const thirdParty = guard.thirdParty();
      const skin = document.querySelector("video-skin");
      console.log(
        "requests:",
        JSON.stringify(guard.requests.filter((u) => !u.startsWith("data:"))),
        "| skin shadow children:",
        skin?.shadowRoot?.childElementCount ?? null,
        "| skin children:",
        skin?.childElementCount
      );
      expect(customElements.get("video-player")).toBeDefined();
      expect(guard.requests).toContain("/api/raw/movies/clip.mp4?inline=true");
      expect(thirdParty).toEqual([]);
      w.unmount();
    });
  }
});
