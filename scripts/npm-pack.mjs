#!/usr/bin/env node
// Build the npm packages from a GoReleaser dist directory:
//   @audd/cli                 the `audd` command (npm/cli), a small Node wrapper
//   @audd/cli-<os>-<cpu>      one per system, holding that system's binary
// The wrapper lists the system packages as optionalDependencies, so npm
// installs only the one that matches (the esbuild layout).
//
//   node scripts/npm-pack.mjs [--dist dist] [--out dist/npm] [--version X.Y.Z] [--no-pack]
//
// The version defaults to the one in dist/metadata.json. Tarballs land in
// --out; publish them with `npm publish <tarball>`, system packages first.
import { execFileSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = path.dirname(path.dirname(fileURLToPath(import.meta.url)));
const TEMPLATE = path.join(ROOT, "npm", "cli");

const GOOS = { linux: "linux", darwin: "darwin", windows: "win32" };
const GOARCH = { amd64: "x64", arm64: "arm64" };
const REQUIRED = [
  ["linux", "amd64"],
  ["linux", "arm64"],
  ["darwin", "amd64"],
  ["darwin", "arm64"],
  ["windows", "amd64"],
  ["windows", "arm64"],
];

/** npmPlatform maps a Go target to Node's process.platform/process.arch. */
export function npmPlatform(goos, goarch) {
  if (!GOOS[goos] || !GOARCH[goarch]) return null;
  return { os: GOOS[goos], cpu: GOARCH[goarch] };
}

function readJSON(p) {
  return JSON.parse(fs.readFileSync(p, "utf8"));
}

function writeJSON(p, v) {
  fs.writeFileSync(p, JSON.stringify(v, null, 2) + "\n");
}

function binaries(dist) {
  const file = path.join(dist, "artifacts.json");
  if (!fs.existsSync(file)) throw new Error(`${file} not found; run goreleaser first`);
  const root = path.dirname(path.resolve(dist));
  const found = new Map();
  for (const a of readJSON(file)) {
    if (a.type !== "Binary" || !npmPlatform(a.goos, a.goarch)) continue;
    found.set(`${a.goos}/${a.goarch}`, path.isAbsolute(a.path) ? a.path : path.join(root, a.path));
  }
  const missing = REQUIRED.map(([o, a]) => `${o}/${a}`).filter((k) => !found.has(k));
  if (missing.length) throw new Error(`no binary in the dist directory for: ${missing.join(", ")}`);
  return found;
}

/**
 * build writes the package directories under out (and, with pack, the
 * tarballs). Returns { version, packages: [{name, dir}], tarballs }.
 */
export function build({ dist, out, version, pack = true }) {
  version = (version || readJSON(path.join(dist, "metadata.json")).version || "").replace(/^v/, "");
  if (!/^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$/.test(version)) {
    throw new Error(`"${version}" is not a valid npm version`);
  }
  const bins = binaries(dist);
  const template = readJSON(path.join(TEMPLATE, "package.json"));
  fs.rmSync(out, { recursive: true, force: true });
  fs.mkdirSync(out, { recursive: true });

  const packages = [];
  const optional = {};
  for (const [goos, goarch] of REQUIRED) {
    const { os, cpu } = npmPlatform(goos, goarch);
    const name = `@audd/cli-${os}-${cpu}`;
    const dir = path.join(out, `cli-${os}-${cpu}`);
    const exe = goos === "windows" ? "audd.exe" : "audd";
    fs.mkdirSync(path.join(dir, "bin"), { recursive: true });
    fs.copyFileSync(bins.get(`${goos}/${goarch}`), path.join(dir, "bin", exe));
    fs.chmodSync(path.join(dir, "bin", exe), 0o755);
    fs.copyFileSync(path.join(ROOT, "LICENSE"), path.join(dir, "LICENSE"));
    fs.writeFileSync(
      path.join(dir, "README.md"),
      `# ${name}\n\nThe \`audd\` binary for ${os}/${cpu}. Install [@audd/cli](https://www.npmjs.com/package/@audd/cli) instead; it picks this package on matching systems.\n`,
    );
    writeJSON(path.join(dir, "package.json"), {
      name,
      version,
      description: `The audd binary for ${os}/${cpu}; install @audd/cli`,
      homepage: template.homepage,
      repository: template.repository,
      license: template.license,
      author: template.author,
      os: [os],
      cpu: [cpu],
      files: [`bin/${exe}`, "LICENSE", "README.md"],
      preferUnplugged: true,
    });
    optional[name] = version;
    packages.push({ name, dir });
  }

  const dir = path.join(out, "cli");
  fs.mkdirSync(path.join(dir, "bin"), { recursive: true });
  fs.copyFileSync(path.join(TEMPLATE, "bin", "audd.js"), path.join(dir, "bin", "audd.js"));
  fs.chmodSync(path.join(dir, "bin", "audd.js"), 0o755);
  fs.copyFileSync(path.join(TEMPLATE, "README.md"), path.join(dir, "README.md"));
  fs.copyFileSync(path.join(ROOT, "LICENSE"), path.join(dir, "LICENSE"));
  writeJSON(path.join(dir, "package.json"), { ...template, version, optionalDependencies: optional });
  packages.push({ name: template.name, dir });

  const tarballs = [];
  if (pack) {
    for (const p of packages) {
      const res = execFileSync("npm", ["pack", "--silent", "--pack-destination", out], {
        cwd: p.dir,
        encoding: "utf8",
        shell: process.platform === "win32",
      });
      const file = res.trim().split("\n").pop().trim();
      tarballs.push(path.join(out, file));
    }
  }
  return { version, packages, tarballs };
}

function parseArgs(argv) {
  const opts = { dist: path.join(ROOT, "dist"), pack: true };
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if (a === "--dist") opts.dist = argv[++i];
    else if (a === "--out") opts.out = argv[++i];
    else if (a === "--version") opts.version = argv[++i];
    else if (a === "--no-pack") opts.pack = false;
    else throw new Error(`unknown argument ${a}`);
  }
  opts.out = opts.out || path.join(opts.dist, "npm");
  return opts;
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const res = build(parseArgs(process.argv.slice(2)));
    for (const t of res.tarballs) console.log(t);
    if (!res.tarballs.length) for (const p of res.packages) console.log(p.dir);
  } catch (e) {
    console.error(`npm-pack: ${e.message}`);
    process.exit(1);
  }
}
