<template>
  <div id="modal-background" @click="backgroundClick">
    <div ref="modalContainer">
      <slot></slot>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from "vue";

const emit = defineEmits(["closed"]);

const modalContainer = ref(null);

onMounted(() => {
  // Look inside this dialog only: an element elsewhere on the page (e.g. in
  // a rendered file) must never receive the dialog's default focus.
  const container = modalContainer.value as HTMLElement | null;
  const element = container?.querySelector(
    "#focus-prompt"
  ) as HTMLElement | null;
  if (element) {
    element.focus();
  } else if (container) {
    container.focus();
  }
});

const backgroundClick = (event: Event) => {
  const target = event.target as HTMLElement;
  if (target.id == "modal-background") {
    emit("closed");
  }
};

window.addEventListener("keydown", (event) => {
  if (event.key === "Escape") {
    event.stopImmediatePropagation();
    emit("closed");
  }
});
</script>

<style scoped>
#modal-background {
  position: fixed;
  inset: 0;
  background-color: #00000096;
  display: flex;
  justify-content: center;
  align-items: center;
  z-index: 10000;
  animation: ease-in 150ms opacity-enter;
}

@keyframes opacity-enter {
  from {
    opacity: 0;
  }

  to {
    opacity: 1;
  }
}
</style>
