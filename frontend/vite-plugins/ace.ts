// Serve Ace's dynamically loaded modules (modes, themes, snippets,
// extensions, keybindings and syntax-check workers) from File Browser's own
// static path instead of a third-party CDN.
//
// Ace loads these files at runtime with <script src> (modes, themes,
// snippets, extensions) and `new Worker(url)` (workers), resolving names
// against `basePath` (see src/utils/ace.ts). The files are copied from the
// installed ace-builds package, so they always match the bundled ace core.
//
// In a build they are emitted as gzip files only: the Go server serves every
// "/static/<path>.js" request from "<path>.js.gz" in the embedded dist and
// never reads the plain .js (http/static.go), so shipping both would just
// double their size in the binary.
import fs from "node:fs";
import path from "node:path";
import { gzipSync } from "node:zlib";
import type { Plugin } from "vite";

// Directory under the static URL (and under dist) that holds the files.
export const ACE_DIR = "ace";

export const aceSourceDir = path.resolve(
  import.meta.dirname,
  "../node_modules/ace-builds/src-min-noconflict"
);

// Every module ace can load on demand, as paths relative to aceSourceDir
// ("mode-python.js", "snippets/python.js", ...). ace.js itself is left out:
// the core is bundled into the app.
export function aceAssetFiles(dir = aceSourceDir): string[] {
  const files: string[] = [];
  const walk = (rel: string) => {
    for (const entry of fs.readdirSync(path.join(dir, rel), {
      withFileTypes: true,
    })) {
      const relPath = rel ? `${rel}/${entry.name}` : entry.name;
      if (entry.isDirectory()) {
        walk(relPath);
      } else if (entry.name.endsWith(".js") && relPath !== "ace.js") {
        files.push(relPath);
      }
    }
  };
  walk("");
  return files.sort();
}

export function selfHostAce(): Plugin {
  let files: Set<string> | null = null;
  const knownFiles = () => (files ??= new Set(aceAssetFiles()));

  return {
    name: "filebrowser:self-host-ace",

    // Dev server: StaticURL is "" there, so ace asks for "/ace/<file>".
    configureServer(server) {
      server.middlewares.use(`/${ACE_DIR}`, (req, res, next) => {
        const name = decodeURIComponent((req.url ?? "").split("?")[0]).replace(
          /^\/+/,
          ""
        );
        if (!knownFiles().has(name)) return next();
        res.setHeader("Content-Type", "application/javascript; charset=utf-8");
        res.end(fs.readFileSync(path.join(aceSourceDir, name)));
      });
    },

    generateBundle() {
      for (const name of knownFiles()) {
        this.emitFile({
          type: "asset",
          fileName: `${ACE_DIR}/${name}.gz`,
          source: gzipSync(fs.readFileSync(path.join(aceSourceDir, name)), {
            level: 9,
          }),
        });
      }
    },
  };
}
