// The text editor's unsaved changes, kept when the session ends without the
// user asking (the server refused the token: revoked by a password,
// permission or scope change elsewhere, or expired; or the inactivity
// logout), so that they are not lost with the editor. Kept for the tab only
// (sessionStorage, gone with the tab), tagged with the user, per file (the
// latest text of each, all within MAX_KEPT_LENGTH, the oldest dropped
// first). Opening that file again offers to restore them. A deliberate
// logout, or another user's session starting on the tab, drops them.

const KEY = "editor-unsaved";
// Storage holds a few million characters per origin; the rest of the app
// needs some too. The texts kept add up to this at most; a longer text is
// not kept (it is not lost by this either: it was lost before this existed).
export const MAX_KEPT_LENGTH = 1_000_000;

// base: the fingerprint of the file's content as the editor last loaded or
// saved it, i.e. what the unsaved text was written over. If the file no
// longer has that content when it is opened again (someone saved it, or a
// scope change made the path name another file), the user is told so.
type UnsavedEdit = { path: string; content: string; base: string };
type Source = () => UnsavedEdit | null;
// base is missing in a text kept by an older version
type Edit = Omit<UnsavedEdit, "base"> & { base?: string };
// oldest first
interface Kept {
  user: number;
  username: string;
  edits: Edit[];
}

// The open editor: what it has not saved, or null.
let source: Source | null = null;

export function setUnsavedSource(next: Source) {
  source = next;
}

export function clearUnsavedSource(previous: Source) {
  if (source === previous) source = null;
}

// Called by logout() when the session ended without the user asking, while
// the user is still known. Never throws: the logout must go on.
// When the latest text of a file cannot be kept, an older one kept for it
// is dropped: it is not the user's latest changes.
export function keepUnsaved(user: IUser | null) {
  let path: string | undefined;
  try {
    const edit = source?.();
    if (!edit || !user) return;
    path = edit.path;
    if (edit.content.length > MAX_KEPT_LENGTH) {
      console.warn("the unsaved text is too long to be kept");
      dropUnsaved(path);
      return;
    }
    const kept = read();
    const edits = (kept && isFor(kept, user) ? kept.edits : []).filter(
      (e) => e.path !== edit.path
    );
    edits.push({ path: edit.path, content: edit.content, base: edit.base });
    // all of them within the same size: the oldest go first
    let total = edits.reduce((sum, e) => sum + e.content.length, 0);
    while (total > MAX_KEPT_LENGTH) total -= edits.shift()!.content.length;
    const next: Kept = { user: user.id, username: user.username, edits };
    sessionStorage.setItem(KEY, JSON.stringify(next));
  } catch (e) {
    console.warn("could not keep the unsaved text", e);
    if (path !== undefined) dropUnsaved(path);
  }
}

const isEdit = (e: any): e is Edit =>
  typeof e?.path === "string" &&
  typeof e.content === "string" &&
  (e.base === undefined || typeof e.base === "string");

function read(): Kept | null {
  try {
    const value = sessionStorage.getItem(KEY);
    const kept = value ? JSON.parse(value) : null;
    if (typeof kept?.user !== "number" || typeof kept.username !== "string") {
      return null;
    }
    // one file, as kept by the previous version
    const edits = Array.isArray(kept.edits) ? kept.edits : [kept];
    if (!edits.every(isEdit)) return null;
    return { user: kept.user, username: kept.username, edits };
  } catch {
    return null;
  }
}

const isFor = (kept: Kept, user: IUser | null) =>
  user !== null && kept.user === user.id && kept.username === user.username;

// path: only the kept text of that file
export function dropUnsaved(path?: string) {
  try {
    const kept = path === undefined ? null : read();
    const edits = kept?.edits.filter((e) => e.path !== path) ?? [];
    if (kept && edits.length === kept.edits.length) return;
    if (kept && edits.length > 0) {
      sessionStorage.setItem(KEY, JSON.stringify({ ...kept, edits }));
    } else {
      sessionStorage.removeItem(KEY);
    }
  } catch {
    // nothing kept
  }
}

// A session starts for this user: another user's text is dropped.
export function dropUnsavedOfOthers(user: IUser | null) {
  const kept = read();
  if (kept && !isFor(kept, user)) dropUnsaved();
}

// The kept text of this file, for this user, if any; base: see UnsavedEdit
// (missing in a text kept by an older version).
export function unsavedFor(
  user: IUser | null,
  path: string
): { content: string; base?: string } | null {
  dropUnsavedOfOthers(user);
  const edit = read()?.edits.find((e) => e.path === path);
  return edit ? { content: edit.content, base: edit.base } : null;
}

// A short fingerprint of a text (its length and a 32-bit FNV-1a hash), to
// tell whether a file still has the content it had, without keeping it.
export function fingerprint(text: string): string {
  let hash = 0x811c9dc5;
  for (let i = 0; i < text.length; i++) {
    hash ^= text.charCodeAt(i);
    hash = Math.imul(hash, 0x01000193);
  }
  return `${text.length}:${(hash >>> 0).toString(16)}`;
}
