// Whether a key event was typed into a text field (an input, textarea,
// select or contenteditable element such as the command shell), where
// page-wide shortcuts (Delete, Ctrl+A, Ctrl+V, Enter, arrows, Escape) must
// not act on files.
export function isTypingTarget(target: EventTarget | null): boolean {
  const el = target as HTMLElement | null;
  if (el == null || typeof el.tagName !== "string") return false;
  if (el.isContentEditable) return true;
  if (["INPUT", "TEXTAREA", "SELECT"].includes(el.tagName.toUpperCase())) {
    return true;
  }
  return (
    typeof el.closest === "function" &&
    el.closest(
      '[contenteditable=""], [contenteditable="true"], [contenteditable="plaintext-only"]'
    ) !== null
  );
}
