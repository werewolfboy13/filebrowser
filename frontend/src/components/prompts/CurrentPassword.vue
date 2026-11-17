<template>
  <div class="card floating">
    <div class="card-title">
      <h2>{{ $t("prompts.currentPassword") }}</h2>
    </div>

    <div class="card-content">
      <p>
        {{ $t("prompts.currentPasswordMessage") }}
      </p>
      <input
        id="focus-prompt"
        class="input input--block"
        type="password"
        @keyup.enter="submit"
        v-model="password"
      />
    </div>

    <div class="card-action">
      <button
        class="button button--flat button--grey"
        @click="cancel"
        :aria-label="$t('buttons.cancel')"
        :title="$t('buttons.cancel')"
      >
        {{ $t("buttons.cancel") }}
      </button>
      <button
        @click="submit"
        class="button button--flat"
        type="submit"
        :aria-label="$t('buttons.ok')"
        :title="$t('buttons.ok')"
      >
        {{ $t("buttons.ok") }}
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref } from "vue";
import { useLayoutStore } from "@/stores/layout";
const layoutStore = useLayoutStore();

// The prompt this instance was created for (Prompts.vue gives every prompt
// shown its own instance).
const prompt = layoutStore.currentPrompt;

const password = ref("");

const submit = (event: Event) => {
  // never hand the password to a callback of another (earlier) prompt
  if (prompt == null || layoutStore.currentPrompt !== prompt) return;
  prompt.confirm(event, password.value);
};

const cancel = () => {
  layoutStore.closeHovers();
};
</script>
