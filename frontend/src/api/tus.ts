import * as tus from "tus-js-client";
import { baseURL, tusEndpoint, tusSettings, origin } from "@/utils/constants";
import { useAuthStore } from "@/stores/auth";
import { removePrefix } from "@/api/utils";
import { renew } from "@/utils/auth";

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
export class UploadError extends Error {
  constructor(
    message: string,
    public status: number,
    public method: string
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
      },
      // ...and renew it when the server says it is about to expire, as
      // fetchURL does for other API calls.
      onAfterResponse: async function (_req, res) {
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

        let message = "Upload failed";
        let status = 0;
        let method = "";
        if (error instanceof tus.DetailedError) {
          const res = error.originalResponse;
          message = res === null ? "000 No connection" : res.getBody();
          status = res?.getStatus() ?? 0;
          method = error.originalRequest?.getMethod() ?? "";
        }

        console.error(error);

        reject(new UploadError(message, status, method));
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
