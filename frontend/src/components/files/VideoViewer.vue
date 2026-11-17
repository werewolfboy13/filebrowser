<template>
  <!--
    This component is not called VideoPlayer: that would collide with the
    <video-player> custom element below, which vue-tsc would then resolve as
    a recursive reference to this component.

    <media-i18n> without a lang attribute follows the nearest ancestor lang,
    i.e. <html lang>, which the app sets to the user's locale.
  -->
  <media-i18n>
    <video-player default-playback-rates="0.5 1 1.5 2 2.5 3">
      <!--
        The packaged skin supplies the controls, keyboard shortcuts (seek,
        volume, 0-9 to jump, speed, captions, fullscreen) and touch gestures
        (double tap to seek) that the videojs-hotkeys and videojs-mobile-ui
        plugins used to add.
      -->
      <video-skin ref="skinElement" class="video-max">
        <video
          ref="videoElement"
          :src="source"
          :autoplay="autoplay"
          preload="auto"
          playsinline
        >
          <track
            kind="subtitles"
            v-for="(sub, index) in subtitles"
            :key="sub"
            :src="sub"
            :label="subLabel(sub)"
            :default="index === 0"
            @error="emit('subtitleError', subLabel(sub))"
          />
          <p>
            Sorry, your browser doesn't support embedded videos, but don't
            worry, you can <a :href="source">download it</a>
            and watch it with your favorite video player!
          </p>
        </video>
      </video-skin>
    </video-player>
  </media-i18n>
</template>

<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref } from "vue";
import { useLayoutStore } from "@/stores/layout";
import { isTypingTarget } from "@/utils/keyboard";
import "@videojs/html/i18n";
import "@videojs/html/video/player";
import "@videojs/html/video/skin";
import { subLabel } from "./subtitles";

// A subtitle file could not be loaded (e.g. one too large for the server
// to convert: 413). A failed <track> is otherwise silent.
const emit = defineEmits<{ subtitleError: [name: string] }>();

withDefaults(
  defineProps<{
    source: string;
    subtitles?: string[];
    autoplay?: boolean;
  }>(),
  {
    subtitles: () => [],
    autoplay: false,
  }
);

const videoElement = ref<HTMLVideoElement | null>(null);
const skinElement = ref<HTMLElement | null>(null);
const layoutStore = useLayoutStore();

// The skin's keyboard shortcuts (Space, arrows, 0-9, m, f, c...) listen on
// the player container, so they only work while the player has focus; the
// preview leaves the arrow keys to them for videos. Focus the player when
// the preview opens (the viewer is created anew for every file), unless the
// user is typing (e.g. in the command shell) or a dialog is open. Keys the
// player has no shortcut for (Escape, Enter) still reach the preview.
onMounted(async () => {
  await customElements.whenDefined("video-skin");
  await nextTick();
  if (layoutStore.currentPrompt !== null) return;
  const active = document.activeElement;
  if (isTypingTarget(active)) return;
  // a control of the preview (e.g. the next/previous buttons) may hand
  // focus on; anything else outside it keeps it
  if (
    active !== null &&
    active !== document.body &&
    active.closest("#previewer") === null
  ) {
    return;
  }
  const container = skinElement.value?.shadowRoot?.querySelector(
    "media-container"
  ) as HTMLElement | null | undefined;
  container?.focus({ preventScroll: true });
});

onBeforeUnmount(() => {
  // Stop playback and abort the download right away instead of waiting for
  // the detached element to be garbage collected (what video.js 8's
  // dispose() used to do for us).
  const video = videoElement.value;
  if (video) {
    video.pause();
    video.removeAttribute("src");
    video.load();
  }
});
</script>

<style scoped>
.video-max {
  display: block;
  width: 100%;
  height: 100%;
}

video-skin:not(:defined) {
  visibility: hidden;
}
</style>
