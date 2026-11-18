<template>
  <div class="card floating">
    <div class="card-content">
      <p>
        {{ $t("prompts.restoreEditorChanges") }}
      </p>
    </div>
    <div class="card-action">
      <button
        id="focus-prompt"
        class="button button--flat button--blue"
        @click="restore"
        :aria-label="$t('buttons.restore')"
        :title="$t('buttons.restore')"
        tabindex="1"
      >
        {{ $t("buttons.restore") }}
      </button>
      <button
        class="button button--flat button--red"
        @click="discard"
        :aria-label="$t('buttons.discardChanges')"
        :title="$t('buttons.discardChanges')"
        tabindex="2"
      >
        {{ $t("buttons.discardChanges") }}
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
// Offers to put back the editor's unsaved changes kept by a forced logout.
// confirm() restores them, action() discards them. Restore has the default
// focus: it loses nothing (the file is not saved until the user saves),
// while Discard drops the kept text for good. Escape or a click beside the
// dialog answers neither, and the text is offered again next time.
import { useLayoutStore } from "@/stores/layout";

const layoutStore = useLayoutStore();
// The prompt this instance was created for (Prompts.vue gives every prompt
// shown its own instance): never answer another one.
const prompt = layoutStore.currentPrompt;

const restore = (event: Event) => {
  if (prompt == null || layoutStore.currentPrompt !== prompt) return;
  prompt.confirm?.(event);
};

const discard = (event: Event) => {
  if (prompt == null || layoutStore.currentPrompt !== prompt) return;
  prompt.action?.(event);
};
</script>
