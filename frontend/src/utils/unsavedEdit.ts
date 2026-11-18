// The text editor's unsaved changes, kept when the session ends without the
// user asking (the server refused the token: revoked by a password,
// permission or scope change elsewhere, or expired; or the inactivity
// logout), so that they are not lost with the editor. Kept for the tab only
// (sessionStorage, gone with the tab), tagged with the user, one file at a
// time. Opening that file again offers to restore them. A deliberate logout,
// or another user's session starting on the tab, drops them.

const KEY = "editor-unsaved";
// Storage holds a few million characters per origin; the rest of the app
// needs some too. A longer text is not kept (it is not lost by this either:
// it was lost before this existed).
export const MAX_KEPT_LENGTH = 1_000_000;

type UnsavedEdit = { path: string; content: string };
type Source = () => UnsavedEdit | null;
interface Kept extends UnsavedEdit {
  user: number;
  username: string;
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
export function keepUnsaved(user: IUser | null) {
  try {
    const edit = source?.();
    if (!edit || !user) return;
    if (edit.content.length > MAX_KEPT_LENGTH) {
      console.warn("the unsaved text is too long to be kept");
      return;
    }
    const kept: Kept = {
      user: user.id,
      username: user.username,
      path: edit.path,
      content: edit.content,
    };
    sessionStorage.setItem(KEY, JSON.stringify(kept));
  } catch (e) {
    console.warn("could not keep the unsaved text", e);
  }
}

function read(): Kept | null {
  try {
    const value = sessionStorage.getItem(KEY);
    const kept = value ? JSON.parse(value) : null;
    if (
      typeof kept?.user !== "number" ||
      typeof kept.username !== "string" ||
      typeof kept.path !== "string" ||
      typeof kept.content !== "string"
    ) {
      return null;
    }
    return kept;
  } catch {
    return null;
  }
}

const isFor = (kept: Kept, user: IUser | null) =>
  user !== null && kept.user === user.id && kept.username === user.username;

// path: only if the kept text is that file's
export function dropUnsaved(path?: string) {
  try {
    if (path !== undefined && read()?.path !== path) return;
    sessionStorage.removeItem(KEY);
  } catch {
    // nothing kept
  }
}

// A session starts for this user: another user's text is dropped.
export function dropUnsavedOfOthers(user: IUser | null) {
  const kept = read();
  if (kept && !isFor(kept, user)) dropUnsaved();
}

// The kept text of this file, for this user, if any.
export function unsavedFor(user: IUser | null, path: string): string | null {
  dropUnsavedOfOthers(user);
  const kept = read();
  return kept && kept.path === path ? kept.content : null;
}
