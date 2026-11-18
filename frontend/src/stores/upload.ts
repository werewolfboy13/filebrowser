import { defineStore } from "pinia";
import { useFileStore } from "./file";
import { useLayoutStore } from "./layout";
import { files as api } from "@/api";
import buttons from "@/utils/buttons";
import { computed, inject, markRaw, ref } from "vue";
import * as tus from "@/api/tus";
import i18n from "@/i18n";

// TODO: make this into a user setting
const UPLOADS_LIMIT = 5;

const beforeUnload = (event: Event) => {
  event.preventDefault();
  // To remove >> is deprecated
  // event.returnValue = "";
};

export const useUploadStore = defineStore("upload", () => {
  const $showError = inject<IToastError>("$showError")!;

  let progressInterval: number | null = null;

  //
  // STATE
  //

  const allUploads = ref<Upload[]>([]);
  const activeUploads = ref<Set<Upload>>(new Set());
  const lastUpload = ref<number>(-1);
  const totalBytes = ref<number>(0);
  const sentBytes = ref<number>(0);

  //
  // ACTIONS
  //

  const upload = (
    path: string,
    name: string,
    file: File | null,
    overwrite: boolean,
    type: ResourceType
  ) => {
    if (!hasActiveUploads() && !hasPendingUploads()) {
      window.addEventListener("beforeunload", beforeUnload);
      buttons.loading("upload");
    }

    const upload: Upload = {
      path,
      name,
      file,
      overwrite,
      type,
      totalBytes: file?.size || 1,
      sentBytes: 0,
      // Stores rapidly changing sent bytes value without causing component re-renders
      rawProgress: markRaw({
        sentBytes: 0,
      }),
    };

    totalBytes.value += upload.totalBytes;
    allUploads.value.push(upload);

    processUploads();
  };

  // Returns how many uploads it cancelled: running, queued, or waiting for
  // the user's answer to a conflict.
  const abort = (): number => {
    const cancelled =
      pendingUploadCount.value + conflicts.length + (asking !== null ? 1 : 0);
    // bumps the generation: what settles from now on for an upload started
    // before is ignored (see processUploads)
    dropConflicts();
    tus.abortAllUploads();
    api.abortResourceUploads();
    // Start the queue over at once. The aborted uploads can settle later,
    // and files queued meanwhile (e.g. after logging in again) belong to
    // the next batch: waiting for them dropped those files.
    window.removeEventListener("beforeunload", beforeUnload);
    reset();
    return cancelled;
  };

  //
  // GETTERS
  //

  const pendingUploadCount = computed(
    () =>
      allUploads.value.length -
      (lastUpload.value + 1) +
      activeUploads.value.size
  );

  //
  // PRIVATE FUNCTIONS
  //

  const hasActiveUploads = () => activeUploads.value.size > 0;

  const hasPendingUploads = () =>
    allUploads.value.length > lastUpload.value + 1;

  const isActiveUploadsOnLimit = () => activeUploads.value.size < UPLOADS_LIMIT;

  const processUploads = async () => {
    // The batch is not over while the user is asked about a conflict: its
    // end reloads the listing, which closes any open prompt.
    if (!hasActiveUploads() && !hasPendingUploads() && !hasConflicts()) {
      const fileStore = useFileStore();
      window.removeEventListener("beforeunload", beforeUnload);
      buttons.success("upload");
      reset();
      fileStore.reload = true;
    }

    if (isActiveUploadsOnLimit() && hasPendingUploads()) {
      if (progressInterval === null) {
        // Update the state in a fixed time interval. Guard on the handle, not
        // on the active count: this runs again every time the queue drains to
        // zero with work still pending, and reassigning would leak the timer.
        progressInterval = window.setInterval(syncState, 1000);
      }

      const upload = nextUpload();
      const started = generation;
      let succeeded = true;
      let conflict = false;

      if (upload.type === "dir") {
        await api.post(upload.path).catch((err) => {
          succeeded = false;
          $showError(err);
        });
      } else {
        const onUpload = (event: ProgressEvent) => {
          upload.rawProgress.sentBytes = event.loaded;
        };

        await api
          .post(upload.path, upload.file!, upload.overwrite, onUpload)
          .catch(async (err) => {
            succeeded = false;
            if (err.message === "Upload aborted") return;
            // aborted meanwhile (e.g. by a logout, which says so): no error
            // of its own, e.g. the 401 that ended the session
            if (started !== generation) return;
            if (isCreateConflict(err) && !upload.overwrite && upload.file) {
              if (err.recreated && (await isStored(upload.path, upload.file))) {
                succeeded = true;
                return;
              }
              conflict = true;
              return;
            }
            $showError(err);
          });
      }

      // aborted meanwhile: the queue has started over without this upload
      if (started !== generation) return;
      if (conflict) askReplace(upload, upload.file!);
      finishUpload(upload, succeeded);
    }
  };

  const nextUpload = (): Upload => {
    lastUpload.value++;

    const upload = allUploads.value[lastUpload.value];
    activeUploads.value.add(upload);

    return upload;
  };

  const finishUpload = (upload: Upload, succeeded: boolean) => {
    if (succeeded) {
      sentBytes.value += upload.totalBytes - upload.sentBytes;
      upload.sentBytes = upload.totalBytes;
    } else {
      // Credit only what actually reached the server and drop the rest from the
      // total. Counting a failed upload as fully sent makes the bar report 100%
      // while nothing was written, which reads as success.
      sentBytes.value += upload.rawProgress.sentBytes - upload.sentBytes;
      totalBytes.value -= upload.totalBytes - upload.rawProgress.sentBytes;
      upload.sentBytes = upload.rawProgress.sentBytes;
    }
    upload.file = null;

    activeUploads.value.delete(upload);
    processUploads();
  };

  const syncState = () => {
    for (const upload of activeUploads.value) {
      sentBytes.value += upload.rawProgress.sentBytes - upload.sentBytes;
      upload.sentBytes = upload.rawProgress.sentBytes;
    }
  };

  //
  // CONFLICTS REPORTED ON CREATE
  //
  // The conflict dialog checks the listing before an upload starts. The
  // server can still refuse to create the upload (409 without override):
  // the file appeared meanwhile, or an unfinished upload to the same path
  // is pending (see tusPostHandler; its temporary file is not listed).
  // Ask, one file at a time, whether to replace it or skip it. Replacing
  // overwrites the file, or starts the pending upload over; it is only
  // ever done on the user's say-so.

  type Conflict = Pick<Upload, "path" | "name" | "type"> & { file: File };
  let conflicts: Conflict[] = [];
  // the question being shown, if any
  let asking: PopupProps | null = null;
  // bumped by abort(): answers to questions asked before it, and uploads
  // started before it, are ignored
  let generation = 0;

  const hasConflicts = () => asking !== null || conflicts.length > 0;

  // on create, or on the final PATCH (a file appeared meanwhile; the plain
  // POST upload reports that on its POST); a 409 on another PATCH is not
  const isCreateConflict = (err: any) =>
    err?.status === 409 && (err?.method === "POST" || err?.final === true);

  // A 409 when tus created the upload again (see UploadError.recreated)
  // can be about the file this upload stored: the answer to its final
  // PATCH was lost, tus resumed, found the upload gone because it was
  // completed, and created it anew. A file of its name and size at the
  // destination is taken to be that file. Only for such a re-create: on
  // first creating the upload, or creating it again before its final
  // PATCH went out (or after that was refused), a file of the same size
  // there is another file.
  // The listing of the folder, not the file itself, whose information
  // includes the content of a text file.
  const isStored = async (path: string, file: File) => {
    try {
      const slash = path.lastIndexOf("/");
      const name = decodeURIComponent(path.slice(slash + 1));
      const folder = await api.fetch(path.slice(0, slash + 1));
      return (folder.items ?? []).some(
        (item) => !item.isDir && item.name === name && item.size === file.size
      );
    } catch {
      return false; // ask
    }
  };

  const askReplace = (upload: Upload, file: File) => {
    conflicts.push({
      path: upload.path,
      name: upload.name,
      type: upload.type,
      file,
    });
    askNext();
  };

  const askNext = () => {
    if (asking !== null) return;
    const conflict = conflicts.shift();
    if (!conflict) return;

    const layoutStore = useLayoutStore();
    const asked = generation;
    layoutStore.showHover({
      prompt: "replace",
      props: {
        messageKey: "prompts.uploadConflict",
        messageParams: { name: conflict.name },
      },
      confirm: () => {
        // queue it first: closing finishes the batch if nothing is queued
        if (asked === generation) {
          upload(
            conflict.path,
            conflict.name,
            conflict.file,
            true,
            conflict.type
          );
        }
        shown.answered = true;
        layoutStore.closeHovers();
      },
      // runs however the question is closed: answered (Replace, Skip,
      // Escape, a click beside it), or from outside it
      close: async () => {
        if (asked !== generation) return "";
        asking = null;
        if (shown.answered) {
          askNext();
        } else {
          // Closed without an answer, e.g. by a route change, which closes
          // the open prompt twice (Layout and Files each do) and would
          // silently skip the next question too: skip every file still to
          // be asked about, and say so.
          skipAll();
        }
        // the last question answered: finish the batch (unless Replace
        // queued an upload, which finishes it in turn)
        processUploads();
        return "";
      },
    });
    // the question as shown (the callbacks above run only once it is)
    const shown = layoutStore.prompts[layoutStore.prompts.length - 1];
    asking = shown;
  };

  const skipAll = () => {
    const count = 1 + conflicts.length;
    conflicts = [];
    $showError(
      i18n.global.t("prompts.uploadConflictsSkipped", { count }, count),
      false
    );
  };

  const dropConflicts = () => {
    generation++;
    conflicts = [];
    if (asking !== null) {
      const layoutStore = useLayoutStore();
      const shown = asking;
      layoutStore.prompts = layoutStore.prompts.filter((p) => p !== shown);
      asking = null;
    }
  };

  const reset = () => {
    if (progressInterval !== null) {
      clearInterval(progressInterval);
      progressInterval = null;
    }

    allUploads.value = [];
    activeUploads.value = new Set();
    lastUpload.value = -1;
    totalBytes.value = 0;
    sentBytes.value = 0;
  };

  return {
    // STATE
    activeUploads,
    totalBytes,
    sentBytes,

    // ACTIONS
    upload,
    abort,

    // GETTERS
    pendingUploadCount,
  };
});
