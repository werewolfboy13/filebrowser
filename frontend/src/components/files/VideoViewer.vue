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
      <video-skin class="video-max">
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
import { onBeforeUnmount, ref } from "vue";
import "@videojs/html/i18n";
import "@videojs/html/video/player";
import "@videojs/html/video/skin";
import { subLabel } from "./subtitles";

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
