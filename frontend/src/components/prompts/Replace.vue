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
        ref="skipButton"
        class="button button--flat button--grey"
        @click="skip"
        :aria-label="$t('prompts.skip')"
        :title="$t('prompts.skip')"
        tabindex="2"
      >
        {{ $t("prompts.skip") }}
      </button>
      <button
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
//
// The question can appear at any moment (e.g. a conflict reported during an
// upload batch, while the user types elsewhere), and the next one where the
// one just answered was. So the default focus is on Skip, not Replace (a
// focused button is clicked by Enter), and not taken from a text field the
// user is typing in; and the second click of a double-click is not taken as
// a click on the next question's Replace.
import { onMounted, ref } from "vue";
import { useLayoutStore } from "@/stores/layout";
import { isTypingTarget } from "@/utils/keyboard";

const layoutStore = useLayoutStore();
const skipButton = ref<HTMLButtonElement | null>(null);

onMounted(() => {
  if (!isTypingTarget(document.activeElement)) skipButton.value?.focus();
});
// The prompt this instance was created for (Prompts.vue gives every prompt
// shown its own instance): never confirm another one.
const prompt = layoutStore.currentPrompt;

const replace = (event: Event) => {
  if (prompt == null || layoutStore.currentPrompt !== prompt) return;
  if ((event as MouseEvent).detail > 1) return;
  prompt.confirm?.(event);
};

const skip = () => {
  if (prompt == null || layoutStore.currentPrompt !== prompt) return;
  prompt.answered = true;
  layoutStore.closeHovers();
};
</script>
