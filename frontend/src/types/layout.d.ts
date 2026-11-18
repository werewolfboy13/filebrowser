interface PopupProps {
  // Unique per showHover() call, set by the layout store. Prompts.vue keys
  // the prompt component with it, so every prompt shown gets its own
  // component instance (and its own captured confirm/props).
  id?: number;
  prompt: string;
  confirm?: any;
  action?: PopupAction;
  saveAction?: () => void;
  props?: any;
  close?: (() => Promise<string>) | null;
  // set when the user answers the prompt itself (its buttons, Escape, a
  // click beside it), as opposed to its being closed from outside (e.g. on
  // navigation)
  answered?: boolean;
}

type PopupAction = (e: Event) => void;
