// Tells a drag that started on a listing row of this page (which moves or
// copies the selected items) apart from anything else dropped on the page:
// files dragged in from the desktop (an upload), or a row dragged over from
// another browser tab of File Browser, whose selection is not ours.
//
// dragstart puts a random per-drag token in the DataTransfer under a custom
// type; a drop only counts as a listing drag when it carries the token of
// the drag this page started and has not ended yet.

export const LISTING_DRAG_TYPE = "application/x-filebrowser-listing";

let activeToken: string | null = null;

function randomToken(): string {
  // crypto.randomUUID() needs a secure context; File Browser is often
  // served over plain http on a LAN, getRandomValues() works there too.
  const bytes = crypto.getRandomValues(new Uint8Array(16));
  return Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
}

export function startListingDrag(dataTransfer: DataTransfer | null) {
  activeToken = randomToken();
  // Firefox also needs some data set for the drag to start at all.
  dataTransfer?.setData(LISTING_DRAG_TYPE, activeToken);
}

export function endListingDrag() {
  activeToken = null;
}

// Whether a drop is the listing drag this page started.
export function isOwnListingDrag(dataTransfer: DataTransfer | null): boolean {
  if (activeToken === null || dataTransfer == null) return false;
  return dataTransfer.getData(LISTING_DRAG_TYPE) === activeToken;
}

// Whether a drag carries a listing row (from this tab or another one).
// Works during dragover too, where only the types can be read.
export function isListingDrag(dataTransfer: DataTransfer | null): boolean {
  return Array.from(dataTransfer?.types ?? []).includes(LISTING_DRAG_TYPE);
}
