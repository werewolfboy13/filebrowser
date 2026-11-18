// Run after `vite build` (see the build script in package.json).
//
// The Go static handler serves a .js file only from its .js.gz sibling
// (http/static.go), so a chunk emitted without one answers 404 in the
// built image. vite-plugin-compression2 used to skip files whose gzip is
// not smaller (tiny re-export chunks), which broke the video player's
// locale loading. Fail the build if any .js file under dist lacks its .gz.
import { existsSync, readdirSync } from "node:fs";
import path from "node:path";

const dist = path.resolve(import.meta.dirname, "../dist");

function* files(dir) {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) yield* files(full);
    else yield full;
  }
}

const all = [...files(dist)];
const js = all.filter((f) => f.endsWith(".js"));
const missing = js.filter((f) => !existsSync(`${f}.gz`));

if (js.length === 0) {
  console.error(`check-dist-gzip: no .js files under ${dist}; build first`);
  process.exit(1);
}
if (missing.length > 0) {
  console.error(
    `check-dist-gzip: ${missing.length} of ${js.length} .js files have no .js.gz (they would answer 404):`
  );
  for (const f of missing) console.error(`  ${path.relative(dist, f)}`);
  process.exit(1);
}
console.log(`check-dist-gzip: all ${js.length} .js files have a .js.gz`);
