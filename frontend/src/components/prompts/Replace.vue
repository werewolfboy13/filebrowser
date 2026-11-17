<template>
  <div class="card floating">
    <div class="card-title">
      <h2>{{ $t("prompts.replace") }}</h2>
    </div>

    <div class="card-content">
      <p>
        {{
          prompt?.props?.messageKey
            ? $t(prompt.props.messageKey, prompt.props.messageParams ?? {})
            : $t("prompts.replaceMessage")
        }}
      </p>
    </div>

    <div class="card-action">
      <button
        class="button button--flat button--grey"
        @click="skip"
        :aria-label="$t('prompts.skip')"
        :title="$t('prompts.skip')"
        tabindex="2"
      >
        {{ $t("prompts.skip") }}
      </button>
      <button
        id="focus-prompt"
        class="button button--flat button--red"
        @click="replace"
        :aria-label="$t('buttons.replace')"
        :title="$t('buttons.replace')"
        tabindex="1"
      >
        {{ $t("buttons.replace") }}
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
// Asks whether to replace something at the destination or to skip it.
// props: { messageKey?, messageParams? } for the question; confirm() runs on
// Replace. Skip, Cancel and Escape just close it (the prompt's close
// callback, if any, runs then).
import { useLayoutStore } from "@/stores/layout";

const layoutStore = useLayoutStore();
// The prompt this instance was created for (Prompts.vue gives every prompt
// shown its own instance): never confirm another one.
const prompt = layoutStore.currentPrompt;

const replace = (event: Event) => {
  if (prompt == null || layoutStore.currentPrompt !== prompt) return;
  prompt.confirm?.(event);
};

const skip = () => {
  if (prompt == null || layoutStore.currentPrompt !== prompt) return;
  layoutStore.closeHovers();
};
</script>
