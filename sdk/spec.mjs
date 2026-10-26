// Writes the spec the SDK generators read, from the one the panel exports
// (web/src/api/openapi.json, kept equal to the code by CI): the same operations and schemas,
// with two changes.
//   - The paths lose their /api/v1. The panel's API page and the docs give the API address
//     with it (https://<panel>/<secret path>/api/v1), and that is what a user pastes as the
//     SDK's base URL; left in the paths, it would be sent twice.
//   - info.version is the panel version the SDK is built for.
// Usage: node sdk/spec.mjs <version> <output file>
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const prefix = "/api/v1";
// A panel release: 0.5.2.0, or 0.4.5 for the older tags with three numbers.
const release = /^\d+\.\d+\.\d+(\.\d+)?$/;

const [version, out] = process.argv.slice(2);
if (!release.test(version ?? "") || !out) {
  console.error("usage: node sdk/spec.mjs <version, like 0.5.2.0> <output file>");
  process.exit(2);
}

const spec = JSON.parse(readFileSync(resolve(root, "web/src/api/openapi.json"), "utf8"));
const paths = {};
for (const [path, item] of Object.entries(spec.paths)) {
  // A path outside the prefix would get a wrong address from the SDKs: stop, do not guess.
  if (!path.startsWith(`${prefix}/`)) {
    console.error(`${path} is not under ${prefix}: the SDKs' base URL is the API address, so every path must be`);
    process.exit(1);
  }
  paths[path.slice(prefix.length)] = item;
}
spec.paths = paths;
spec.info.version = version;

mkdirSync(dirname(resolve(out)), { recursive: true });
writeFileSync(resolve(out), `${JSON.stringify(spec, null, 2)}\n`);
console.log(`${Object.keys(paths).length} paths for ${version} -> ${out}`);
