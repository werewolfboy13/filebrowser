// @vitest-environment jsdom
//
// Video.js 10's skin listens for its keyboard shortcuts (Space, arrows to
// seek and change the volume, 0-9, m, f, c...) on the player container, so
// they only work while the player has focus. Opening a video preview left
// focus on the page, and the preview deliberately ignores the arrow keys for
// videos: nothing happened until the user clicked into the player. The
// viewer now focuses the player when it opens, unless the user is typing
// somewhere (e.g. the command shell) or a dialog is open.
//
// Real Video.js custom elements; nothing touches the network (jsdom does not
// fetch media, see video-viewer-no-third-party.test.ts).
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { useLayoutStore } from "@/stores/layout";

// jsdom gaps the player's store touches
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

let w: VueWrapper | null = null;

beforeEach(() => {
  setActivePinia(createPinia());
  document.body.innerHTML = "";
});
afterEach(() => {
  w?.unmount();
  w = null;
});

async function open() {
  const { default: VideoViewer } = await import("../VideoViewer.vue");
  w = mount(VideoViewer, {
    attachTo: document.body,
    props: { source: "/api/raw/movies/clip.mp4?inline=true" },
  });
  await flushPromises();
  await customElements.whenDefined("video-skin");
  for (let i = 0; i < 5; i++) await new Promise((r) => setTimeout(r, 10));
  const skin = document.querySelector("video-skin")!;
  const container = skin.shadowRoot!.querySelector(
    "media-container"
  ) as HTMLElement;
  return { skin, container };
}

function press(el: Element, key: string) {
  const ev = new KeyboardEvent("keydown", {
    key,
    bubbles: true,
    composed: true,
    cancelable: true,
  });
  el.dispatchEvent(ev);
  return ev;
}

describe("video preview keyboard focus", () => {
  it("focuses the player when it opens, so its shortcuts work at once", async () => {
    const { skin, container } = await open();
    console.log(
      "active:",
      document.activeElement?.tagName,
      "| in skin:",
      skin.shadowRoot!.activeElement?.tagName
    );
    expect(document.activeElement).toBe(skin);
    expect(skin.shadowRoot!.activeElement).toBe(container);

    // the skin's hotkeys take the key (and say so with preventDefault, which
    // Preview.vue's own key handler respects)
    expect(press(container, "ArrowRight").defaultPrevented).toBe(true);
    expect(press(container, " ").defaultPrevented).toBe(true);
    // keys the player has no shortcut for are left to the page (Preview:
    // Escape closes, Enter goes to the next file)
    expect(press(container, "Escape").defaultPrevented).toBe(false);
    expect(press(container, "Enter").defaultPrevented).toBe(false);
  });

  it("does not take focus from the command shell", async () => {
    const shell = document.createElement("pre");
    shell.setAttribute("contenteditable", "true"); // as Shell.vue renders it
    document.body.appendChild(shell);
    shell.focus();
    await open();
    expect(document.activeElement).toBe(shell);
  });

  it("does not take focus while a dialog is open", async () => {
    const button = document.createElement("button");
    document.body.appendChild(button);
    useLayoutStore().showHover("info");
    await open();
    expect(document.activeElement).not.toBe(
      document.querySelector("video-skin")
    );
  });
});

// A subtitle file the server cannot serve (e.g. 413: too large to convert)
// made the <track> fail silently.
describe("video preview subtitles", () => {
  it("reports a subtitle file that cannot be loaded", async () => {
    const { default: VideoViewer } = await import("../VideoViewer.vue");
    w = mount(VideoViewer, {
      attachTo: document.body,
      props: {
        source: "/api/raw/movies/clip.mp4?inline=true",
        subtitles: ["/api/subtitle/movies/clip.en.srt?inline=true"],
      },
    });
    await flushPromises();
    w.find("track").element.dispatchEvent(new Event("error"));
    expect(w.emitted("subtitleError")).toEqual([["clip.en"]]);
  });
});
