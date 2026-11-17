import path from "node:path";
import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";
import VueI18nPlugin from "@intlify/unplugin-vue-i18n/vite";
import legacy from "@vitejs/plugin-legacy";
import { compression } from "vite-plugin-compression2";
import { selfHostAce } from "./vite-plugins/ace.ts";

// Custom elements from @videojs/html used in templates (VideoPlayer.vue).
// Vue must not try to resolve them as Vue components.
const videoJsElements = new Set(["media-i18n", "video-player", "video-skin"]);

const plugins = [
  vue({
    template: {
      compilerOptions: {
        isCustomElement: (tag) => videoJsElements.has(tag),
      },
    },
  }),
  VueI18nPlugin({
    include: [path.resolve(import.meta.dirname, "./src/i18n/**/*.json")],
  }),
  legacy({
    // defaults already drop IE support
    targets: ["defaults"],
  }),
  compression({ include: /\.js$/, deleteOriginalAssets: false }),
  selfHostAce(),
];

const resolve = {
  alias: {
    // vue: "@vue/compat",
    "@/": `${path.resolve(import.meta.dirname, "src")}/`,
  },
};

// https://vitejs.dev/config/
export default defineConfig(({ command }) => {
  if (command === "serve") {
    return {
      plugins,
      resolve,
      server: {
        proxy: {
          "/api/command": {
            target: "ws://127.0.0.1:8080",
            ws: true,
          },
          "/api": "http://127.0.0.1:8080",
        },
      },
    };
  } else {
    // command === 'build'
    return {
      plugins,
      resolve,
      base: "",
      build: {
        rollupOptions: {
          input: {
            index: path.resolve(import.meta.dirname, "./public/index.html"),
          },
          output: {
            manualChunks: (id) => {
              // bundle dayjs files in a single chunk
              // this avoids having small files for each locale
              if (id.includes("dayjs/")) {
                return "dayjs";
                // bundle i18n in a separate chunk, but leave the video
                // player's locale packs to load on demand with the player
              } else if (id.includes("i18n/") && !id.includes("@videojs/")) {
                return "i18n";
              }
            },
          },
        },
      },
      experimental: {
        renderBuiltUrl(filename, { hostType }) {
          if (hostType === "js") {
            return { runtime: `window.__prependStaticUrl("${filename}")` };
          } else if (hostType === "html") {
            return `[{[ .StaticURL ]}]/${filename}`;
          } else {
            return { relative: true };
          }
        },
      },
    };
  }
});
