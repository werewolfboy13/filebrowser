// @vitest-environment jsdom
//
// The conflict dialog's "Resume previous transfer" replaced every
// conflicting file that is smaller in the destination, "as we suppose
// that their transfer has been interrupted". With this server an
// interrupted upload leaves nothing at the destination (it is committed
// only when complete), so such a file is a complete one, e.g. a newer,
// shorter version. The option still does the same, under a label that
// says so; the old key is gone, so the other locales, whose translations
// still make the old claim, show the English text instead.
import { afterEach, describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  (window as any).FileBrowser = { StaticURL: "/static", Name: "" };
});

import { mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import i18n from "@/i18n";
import ResolveConflict from "@/components/prompts/ResolveConflict.vue";
import { useLayoutStore } from "@/stores/layout";

const LABEL = "Replace the files that are smaller in the destination";

function open() {
  const pinia = createPinia();
  setActivePinia(pinia);
  const confirm = vi.fn();
  const conflict = [
    { index: 0, name: "a.txt", origin: { size: 9 }, dest: { size: 5 } },
    { index: 1, name: "b.txt", origin: { size: 5 }, dest: { size: 9 } },
  ].map((c) => ({
    ...c,
    checked: ["origin"],
    isSmallerOnServer: c.origin.size > c.dest.size,
  }));
  useLayoutStore().showHover({
    prompt: "resolve-conflict",
    props: { conflict, isUploadAction: true },
    confirm,
  });
  const wrapper = mount(ResolveConflict, {
    global: { plugins: [pinia, i18n] },
  });
  const button = wrapper
    .findAll("button")
    .find((b) => b.find("i").text() === "replay")!;
  return { wrapper, button, confirm };
}

describe("the conflict dialog's option for smaller files", () => {
  afterEach(() => {
    i18n.global.locale.value = "en";
  });

  it("says what it does", () => {
    i18n.global.locale.value = "en";
    const { button } = open();
    const text = button.text();
    console.log("en:", JSON.stringify(text));
    expect(text).toContain(LABEL);
    expect(text).toContain(
      "Replace each conflicting file that is smaller in the destination"
    );
    expect(text).not.toMatch(/resume|interrupt/i);
  });

  it("other locales fall back to the English text, not the old claim", () => {
    for (const locale of ["fr", "de", "pt-br", "zh-cn"]) {
      i18n.global.locale.value = locale;
      const { button } = open();
      expect(button.text(), locale).toContain(LABEL);
    }
  });

  it("still replaces the smaller ones in the destination and skips the rest", async () => {
    i18n.global.locale.value = "en";
    const { button, confirm } = open();
    await button.trigger("click");
    const result = confirm.mock.calls[0][1].map((c: any) => [
      c.name,
      c.checked,
    ]);
    expect(result).toEqual([
      ["a.txt", ["origin"]],
      ["b.txt", ["dest"]],
    ]);
  });
});
