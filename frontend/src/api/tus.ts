import * as tus from "tus-js-client";
import { baseURL, tusEndpoint, tusSettings, origin } from "@/utils/constants";
import { useAuthStore } from "@/stores/auth";
import { removePrefix } from "@/api/utils";
import { logout, renew } from "@/utils/auth";

const RETRY_BASE_DELAY = 1000;
const RETRY_MAX_DELAY = 20000;
// Retries of a 423 Locked answer when retries are turned off otherwise.
const LOCKED_RETRY_DELAYS = [500, 1000, 2000, 4000, 8000];
// Every upload in flight. A set, not a map by path: two uploads to the
// same path (e.g. a retry with overwrite) must both be abortable.
const CURRENT_UPLOADS = new Set<tus.Upload>();

// A failed upload: the answer's body, with its status (0 when the server
// could not be reached) and the method of the request that failed, so that
// callers can tell e.g. a conflict on create (POST) from one on a chunk.
// recreated: the failed POST created the upload again (tus-js-client does
// so when resuming finds it gone) after this upload's final PATCH went out
// and got no definitive answer: no answer, a 5xx (e.g. a proxy's 502 or
// 504) or 423. Only then can the upload be gone because that PATCH
// completed it. Not after a definitive refusal of it (any other 4xx), nor
// when the upload was lost before all of its data was sent (e.g. a server
// restart): a file at the destination is then not this upload's.
// final: the failed request was the final PATCH, on which the server puts
// the file in place (a 409 then: a file appeared at the destination).
export class UploadError extends Error {
  constructor(
    message: string,
    public status: number,
    public method: string,
    public recreated = false,
    public final = false
  ) {
    super(message);
    this.name = "UploadError";
  }
}

export async function upload(
  filePath: string,
  content: ApiContent = "",
  overwrite = false,
  onupload: any
) {
  if (!tusSettings) {
    // Shouldn't happen as we check for tus support before calling this function
    throw new Error("Tus.io settings are not defined");
  }

  filePath = removePrefix(filePath);
  const resourcePath = `${tusEndpoint}${filePath}?override=${overwrite}`;

  const authStore = useAuthStore();
  // Used when the session has ended (logout) while the upload still runs,
  // so that its last requests (e.g. the terminate on abort) are not sent
  // with an empty token.
  const startJwt = authStore.jwt;

  // Exit early because of typescript, tus content can't be a string
  if (content === "") {
    return false;
  }
  const retryDelays = computeRetryDelays(tusSettings);
  // whether a PATCH is the final one, which completes the upload
  const isFinalPatch = (req: tus.HttpRequest) =>
    req.getMethod() === "PATCH" &&
    content instanceof Blob &&
    Number(req.getHeader("Upload-Offset")) +
      (tusSettings.chunkSize || Infinity) >=
      content.size;
  // the last PATCH sent was the final one, and got no definitive answer
  // (see UploadError.recreated)
  let finalPatchUnanswered = false;
  // A 401 for the token that is still this tab's: the session is over (e.g.
  // revoked by a password, permission or scope change elsewhere). Not for a
  // token replaced since the request went out (e.g. this tab changed its own
  // password meanwhile): a retry goes out with the new one.
  const refusedCurrentToken = (err: Error | tus.DetailedError) =>
    err instanceof tus.DetailedError &&
    err.originalResponse?.getStatus() === 401 &&
    !!authStore.jwt &&
    err.originalRequest?.getHeader("X-Auth") === authStore.jwt;
  return new Promise<void | string>((resolve, reject) => {
    const upload = new tus.Upload(content, {
      endpoint: `${origin}${baseURL}${resourcePath}`,
      chunkSize: tusSettings.chunkSize,
      // With retries turned off, still retry what the server asks to be
      // retried (423, see onShouldRetry).
      retryDelays: retryDelays ?? LOCKED_RETRY_DELAYS,
      parallelUploads: 1,
      storeFingerprintForResuming: false,
      // An upload can outlive the token it started with: send the current
      // one with every request (create, HEAD, PATCH, terminate)...
      //
      // Set X-Auth here only, never also in `headers`: tus-js-client applies
      // `headers` with setHeader and then calls onBeforeRequest, and
      // XMLHttpRequest combines a header set twice into "tok, tok", which the
      // server rejects with 401.
      onBeforeRequest: function (req) {
        req.setHeader("X-Auth", authStore.jwt || startJwt);
        if (req.getMethod() === "PATCH") {
          finalPatchUnanswered = isFinalPatch(req);
        }
      },
      // ...and renew it when the server says it is about to expire, as
      // fetchURL does for other API calls.
      onAfterResponse: async function (req, res) {
        // a 4xx other than 423 (busy): the final PATCH was refused, and
        // stored nothing (no answer at all never comes here)
        const status = res.getStatus();
        if (
          req.getMethod() === "PATCH" &&
          status >= 400 &&
          status < 500 &&
          status !== 423
        ) {
          finalPatchUnanswered = false;
        }
        if (res.getHeader("X-Renew-Token") === "true" && authStore.jwt) {
          await renew(authStore.jwt).catch((e) =>
            console.warn("could not renew the session during an upload", e)
          );
        }
      },
      onShouldRetry: function (err) {
        const status = err.originalResponse
          ? err.originalResponse.getStatus()
          : 0;

        // Do not retry for file conflict, nor a refusal (403: e.g. the
        // final PATCH, or creating the upload again after it, asked to
        // replace a file the user may no longer modify): asking again is
        // refused again.
        if (status === 409 || status === 403) {
          return false;
        }

        // asking again with the same token is refused again
        if (refusedCurrentToken(err)) return false;

        // Cancelling (terminate) is only worth repeating when the server
        // could not answer it or the upload was busy; a refusal (e.g. 403
        // without the Delete permission) stays a refusal.
        if (err.originalRequest?.getMethod() === "DELETE") {
          if (status === 423) return true;
          return retryDelays !== undefined && (status === 0 || status >= 500);
        }

        // 423: another request for this upload is still being handled
        // (e.g. a chunk sent again while the server still writes the first
        // attempt). It is over shortly; always try again.
        if (status === 423) {
          return true;
        }

        return retryDelays !== undefined;
      },
      onError: function (error: Error | tus.DetailedError) {
        CURRENT_UPLOADS.delete(upload);

        if (error.message === "Upload aborted") {
          return reject(error);
        }

        // back to the login page, as for other API calls; that aborts the
        // other uploads and says how many were cancelled
        if (refusedCurrentToken(error)) {
          logout(undefined, authStore.jwt);
        }

        let message = "Upload failed";
        let status = 0;
        let method = "";
        let final = false;
        if (error instanceof tus.DetailedError) {
          const res = error.originalResponse;
          message = res === null ? "000 No connection" : res.getBody();
          status = res?.getStatus() ?? 0;
          method = error.originalRequest?.getMethod() ?? "";
          final =
            !!error.originalRequest && isFinalPatch(error.originalRequest);
        }

        console.error(error);

        reject(
          new UploadError(
            message,
            status,
            method,
            method === "POST" && finalPatchUnanswered,
            final
          )
        );
      },
      onProgress: function (bytesUploaded) {
        if (typeof onupload === "function") {
          onupload({ loaded: bytesUploaded });
        }
      },
      onSuccess: function () {
        CURRENT_UPLOADS.delete(upload);
        resolve();
      },
    });
    CURRENT_UPLOADS.add(upload);
    upload.start();
  });
}

function computeRetryDelays(tusSettings: TusSettings): number[] | undefined {
  if (!tusSettings.retryCount || tusSettings.retryCount < 1) {
    // Disable retries altogether
    return undefined;
  }
  // The tus client expects our retries as an array with computed backoffs
  // E.g.: [0, 3000, 5000, 10000, 20000]
  const retryDelays = [];
  let delay = 0;

  for (let i = 0; i < tusSettings.retryCount; i++) {
    retryDelays.push(Math.min(delay, RETRY_MAX_DELAY));
    delay =
      delay === 0 ? RETRY_BASE_DELAY : Math.min(delay * 2, RETRY_MAX_DELAY);
  }

  return retryDelays;
}

export async function useTus(content: ApiContent) {
  return isTusSupported() && content instanceof Blob;
}

function isTusSupported() {
  return tus.isSupported === true;
}

export function abortAllUploads() {
  for (const upload of [...CURRENT_UPLOADS]) {
    CURRENT_UPLOADS.delete(upload);
    upload
      .abort(true)
      .catch((e) =>
        console.warn("could not cancel the upload on the server", e)
      );
    upload.options!.onError!(new Error("Upload aborted"));
  }
}
