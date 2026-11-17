import ace from "ace-builds";
import { staticURL } from "./constants";

// Where Ace loads its on-demand modules (modes, themes, snippets, extensions
// and syntax-check workers) from: File Browser's own static path, filled at
// build time by vite-plugins/ace.ts. Never a third-party CDN.
export const aceBasePath = `${staticURL}/ace/`;

export function configureAce() {
  ace.config.set("basePath", aceBasePath);
  // Start workers straight from their same-origin URL. Ace's default wraps
  // them in a blob: worker that calls importScripts(url), which would need
  // blob: in a Content-Security-Policy's worker-src.
  ace.config.set("loadWorkerFromBlob", false);
}
