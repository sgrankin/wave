// Build the browser bundle into dist/: bundle the editor entry to an ES module
// and copy index.html. `node esbuild.mjs` for a one-shot build; pass --watch to
// rebuild on change. Serve dist/ as the web root (waved -webroot web/dist).

import * as esbuild from "esbuild";
import { cp, mkdir } from "node:fs/promises";

const watch = process.argv.includes("--watch");

const options = {
  entryPoints: ["src/editor/main.ts"],
  bundle: true,
  format: "esm",
  target: "es2022",
  outfile: "dist/main.js",
  sourcemap: true,
  logLevel: "info",
};

await mkdir("dist", { recursive: true });
await cp("index.html", "dist/index.html");

if (watch) {
  const ctx = await esbuild.context(options);
  await ctx.watch();
  console.log("watching…");
} else {
  await esbuild.build(options);
}
