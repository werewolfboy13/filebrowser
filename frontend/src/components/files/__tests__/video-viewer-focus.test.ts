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
//
// The suite also runs inside the Docker image build, on builders that may be
// short of CPU: the viewer (and Video.js) is loaded once, before the tests
// and outside their time limits, and the tests wait for conditions, not for
// fixed delays.
import {
  afterEach,
  beforeAll,
  beforeEach,
  describe,
  expect,
  it,
  vi,
} from "vitest";
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
let VideoViewer: (typeof import("../VideoViewer.vue"))["default"];

beforeAll(async () => {
  ({ default: VideoViewer } = await import("../VideoViewer.vue"));
  await customElements.whenDefined("video-skin");
}, 120_000);

beforeEach(() => {
  setActivePinia(createPinia());
  document.body.innerHTML = "";
});
afterEach(() => {
  w?.unmount();
  w = null;
});

// Mounts the viewer and waits until it has rendered and made its focus
// decision (on mount, after a microtask or two: past the next macrotask).
async function open() {
  w = mount(VideoViewer, {
    attachTo: document.body,
    props: { source: "/api/raw/movies/clip.mp4?inline=true" },
  });
  const skin = document.querySelector("video-skin")!;
  let container: HTMLElement | null = null;
  await vi.waitFor(
    () => {
      container = skin.shadowRoot?.querySelector("media-container") ?? null;
      expect(container).not.toBeNull();
    },
    { timeout: 20_000 }
  );
  await flushPromises();
  return { skin, container: container! as HTMLElement };
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

describe("video preview keyboard focus", { timeout: 30_000 }, () => {
  it("focuses the player when it opens, so its shortcuts work at once", async () => {
    const { skin, container } = await open();
    await vi.waitFor(() => expect(document.activeElement).toBe(skin));
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

  // Each guard on its own: a focused element outside the preview that is
  // not a text field (a sidebar button), and a text field inside it.
  function previewer() {
    const el = document.createElement("div");
    el.id = "previewer";
    document.body.appendChild(el);
    return el;
  }

  it("does not take focus from a control outside the preview", async () => {
    const sidebarButton = document.createElement("button");
    document.body.appendChild(sidebarButton);
    sidebarButton.focus();
    await open();
    expect(document.activeElement).toBe(sidebarButton);
  });

  it("does not take focus from a text field inside the preview", async () => {
    const field = document.createElement("input");
    previewer().appendChild(field);
    field.focus();
    await open();
    expect(document.activeElement).toBe(field);
  });

  it("control: takes it from a control of the preview (e.g. next file)", async () => {
    const next = document.createElement("button");
    previewer().appendChild(next);
    next.focus();
    const { skin } = await open();
    expect(document.activeElement).toBe(skin);
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
describe("video preview subtitles", { timeout: 30_000 }, () => {
  it("reports a subtitle file that cannot be loaded", async () => {
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
