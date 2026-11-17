import { beforeEach, describe, expect, it } from "vitest";
import {
  mapActions,
  mapState,
  mapWritableState,
  setActivePinia,
  storeToRefs,
  type Pinia,
} from "pinia";
import { createApp } from "vue";
import type { Router } from "vue-router";
import createPinia from "@/stores";
import { useClipboardStore } from "@/stores/clipboard";
import { useLayoutStore } from "@/stores/layout";

// Exercises the pinia APIs the app relies on, including the Options API
// helpers used by the components that are excluded from type checking.
const router = { currentRoute: { value: {} } } as unknown as Router;

let pinia: Pinia;

beforeEach(() => {
  pinia = createPinia(router);
  // pinia only applies plugins once it is installed in an app, as main.ts does
  createApp({}).use(pinia);
  setActivePinia(pinia);
});

describe("pinia stores", () => {
  it("injects the router into every store through the plugin", () => {
    const layout = useLayoutStore() as unknown as { router: Router };
    expect(layout.router).toBe(router);
  });

  it("runs options-store getters, actions and $reset", () => {
    const layout = useLayoutStore();
    expect(layout.currentPromptName).toBeUndefined();
    layout.showHover("help");
    expect(layout.currentPromptName).toBe("help");

    const clipboard = useClipboardStore();
    clipboard.key = "x";
    clipboard.items.push({ from: "/a", name: "a" } as ClipItem);
    clipboard.resetClipboard();
    expect(clipboard.key).toBe("");
    expect(clipboard.items).toEqual([]);
  });

  it("keeps storeToRefs refs in sync with the store", () => {
    const layout = useLayoutStore();
    const { loading } = storeToRefs(layout);
    loading.value = true;
    expect(layout.loading).toBe(true);
  });

  it("supports mapState, mapWritableState and mapActions on this.$pinia", () => {
    const computed = {
      ...mapState(useLayoutStore, ["loading", "currentPromptName"]),
      ...mapWritableState(useClipboardStore, ["key"]),
    };
    const methods = mapActions(useLayoutStore, ["showHover"]);
    const vm = { $pinia: pinia };

    methods.showHover.call(vm as never, "info");
    expect(computed.currentPromptName.call(vm as never)).toBe("info");
    expect(computed.loading.call(vm as never)).toBe(false);

    computed.key.set.call(vm as never, "copy");
    expect(useClipboardStore().key).toBe("copy");
    expect(computed.key.get.call(vm as never)).toBe("copy");
  });
});
