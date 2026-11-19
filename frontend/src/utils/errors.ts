// Messages for the answers the server gives to a request that was fine
// but cannot be served right now, or not in this form. They are shown
// instead of the raw "429 Too Many Requests" and the like.
//
// - 429: too many failed password attempts (login, share password, the
//   current password asked for by account changes); Retry-After says how
//   long to wait.
// - 408: a request body (e.g. the login form) did not arrive in time.
// - 413: a request or the file it asks for is too large to process.
// - 423: an upload is busy with another request.
// - 403: the user may not do this (the server says no more than that).

// plural: the count that picks the singular or plural form of a message
// ("1 second" | "{seconds} seconds")
type Translate = (
  key: string,
  values?: Record<string, unknown>,
  plural?: number
) => string;

// The longest wait shown as such (the server asks for minutes); past it,
// or for a value too large to be one, "a while".
const MAX_WAIT_SHOWN = 24 * 60 * 60;

export function tooManyAttemptsMessage(
  t: Translate,
  retryAfter?: number
): string {
  if (retryAfter === undefined || !(retryAfter <= MAX_WAIT_SHOWN)) {
    return t("errors.tooManyAttemptsLater");
  }
  if (retryAfter > 90) {
    return t("errors.tooManyAttemptsMinutes", {
      minutes: Math.ceil(retryAfter / 60),
    });
  }
  const seconds = Math.max(1, retryAfter);
  return t("errors.tooManyAttempts", { seconds }, seconds);
}

// The message for an error with one of the statuses above, or null.
export function statusMessage(t: Translate, error: unknown): string | null {
  const { status, retryAfter } = (error ?? {}) as {
    status?: number;
    retryAfter?: number;
  };
  switch (status) {
    case 429:
      return tooManyAttemptsMessage(t, retryAfter);
    case 408:
      return t("errors.requestTimeout");
    case 413:
      return t("errors.tooLarge");
    case 423:
      return t("errors.locked");
    case 403:
      return t("errors.forbidden");
  }
  return null;
}

// What the error toast shows for an error: the message above when there is
// one (with no "report issue" link: these are not bugs), its own message
// otherwise. Used for every $showError, e.g. by the profile and user forms
// and the current-password dialog, whose password checks answer 429 too.
export function errorToast(
  t: Translate,
  error: Error | string,
  canReport: boolean
): { message: string; isReport: boolean } {
  const known = statusMessage(t, error);
  if (known !== null) return { message: known, isReport: false };
  return {
    message: (error as Error).message || String(error),
    isReport: canReport,
  };
}

// What the error toast shows for an error answering a change of the user's
// own account (profile, password, or an admin editing their own user), or
// null for none:
// - 409: an administrator changed the same account at the same moment, and
//   this change was not made;
// - 401 once the session has ended (fetchURL logged out: the token was
//   revoked, e.g. by an earlier password change whose answer brought no
//   new token): the login page says enough.
export function ownAccountChangeError(
  t: Translate,
  error: unknown,
  loggedIn: boolean
): Error | string | null {
  const status = (error as { status?: number } | null)?.status;
  if (status === 409) return t("errors.accountChangedConcurrently");
  if (status === 401 && !loggedIn) return null;
  return error instanceof Error ? error : String(error);
}
