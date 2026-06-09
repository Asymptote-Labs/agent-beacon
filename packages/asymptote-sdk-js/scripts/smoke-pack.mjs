import { execFileSync } from "node:child_process";
import { mkdtempSync, rmSync, unlinkSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const packageRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const tempRoot = mkdtempSync(join(tmpdir(), "asymptote-sdk-pack-"));
let tarballPath;

try {
  const packOutput = execFileSync("npm", ["pack", "--json"], {
    cwd: packageRoot,
    encoding: "utf8",
    stdio: ["ignore", "pipe", "inherit"],
  });
  const [packResult] = JSON.parse(packOutput);
  if (!packResult?.filename || !Array.isArray(packResult.files)) {
    throw new Error("npm pack did not return package file metadata");
  }

  const packedFiles = new Set(packResult.files.map(file => file.path));
  for (const expectedFile of ["README.md", "dist/index.js", "dist/index.d.ts", "package.json"]) {
    if (!packedFiles.has(expectedFile)) {
      throw new Error(`packed tarball is missing ${expectedFile}`);
    }
  }

  tarballPath = join(packageRoot, packResult.filename);
  execFileSync("npm", ["install", "--prefix", tempRoot, tarballPath], {
    stdio: "inherit",
  });
  execFileSync(
    process.execPath,
    [
      "--input-type=module",
      "--eval",
      "import { Observe, observe } from '@asymptote/sdk'; if (typeof Observe.initialize !== 'function' || typeof observe !== 'function') throw new Error('missing SDK exports');",
    ],
    {
      cwd: tempRoot,
      stdio: "inherit",
    },
  );
} finally {
  if (tarballPath) {
    try {
      unlinkSync(tarballPath);
    } catch {
      // Ignore cleanup errors; the smoke result should reflect package validity.
    }
  }
  rmSync(tempRoot, { force: true, recursive: true });
}
