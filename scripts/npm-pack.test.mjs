// Tests for npm-pack.mjs. Run: node --test scripts/
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { test } from "node:test";

import { build, npmPlatform } from "./npm-pack.mjs";

const TARGETS = [
  ["linux", "amd64"],
  ["linux", "arm64"],
  ["darwin", "amd64"],
  ["darwin", "arm64"],
  ["windows", "amd64"],
  ["windows", "arm64"],
];

function fakeDist(root) {
  const dist = path.join(root, "dist");
  const artifacts = [];
  for (const [goos, goarch] of TARGETS) {
    const dir = path.join(dist, `audd_${goos}_${goarch}`);
    fs.mkdirSync(dir, { recursive: true });
    const name = goos === "windows" ? "audd.exe" : "audd";
    const p = path.join(dir, name);
    fs.writeFileSync(p, `binary for ${goos}/${goarch}`);
    artifacts.push({ name, path: path.relative(root, p), goos, goarch, type: "Binary" });
  }
  artifacts.push({ name: "checksums.txt", path: "dist/checksums.txt", type: "Checksum" });
  fs.writeFileSync(path.join(dist, "artifacts.json"), JSON.stringify(artifacts));
  fs.writeFileSync(path.join(dist, "metadata.json"), JSON.stringify({ version: "1.2.3" }));
  return dist;
}

test("npmPlatform maps Go targets to Node names", () => {
  assert.deepEqual(npmPlatform("linux", "amd64"), { os: "linux", cpu: "x64" });
  assert.deepEqual(npmPlatform("windows", "arm64"), { os: "win32", cpu: "arm64" });
  assert.deepEqual(npmPlatform("darwin", "arm64"), { os: "darwin", cpu: "arm64" });
  assert.equal(npmPlatform("plan9", "amd64"), null);
});

test("build writes the wrapper and one package per platform", () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "audd-npm-"));
  try {
    const dist = fakeDist(root);
    const out = path.join(root, "npm");
    const res = build({ dist, out, pack: false });
    assert.equal(res.version, "1.2.3");

    const names = res.packages.map((p) => p.name).sort();
    assert.deepEqual(names, [
      "@audd/cli",
      "@audd/cli-darwin-arm64",
      "@audd/cli-darwin-x64",
      "@audd/cli-linux-arm64",
      "@audd/cli-linux-x64",
      "@audd/cli-win32-arm64",
      "@audd/cli-win32-x64",
    ]);

    const wrapper = JSON.parse(fs.readFileSync(path.join(out, "cli", "package.json"), "utf8"));
    assert.equal(wrapper.version, "1.2.3");
    assert.deepEqual(wrapper.bin, { audd: "bin/audd.js" });
    assert.equal(wrapper.license, "MIT");
    assert.equal(Object.keys(wrapper.optionalDependencies).length, 6);
    for (const v of Object.values(wrapper.optionalDependencies)) assert.equal(v, "1.2.3");
    assert.ok(fs.existsSync(path.join(out, "cli", "bin", "audd.js")));
    assert.ok(fs.existsSync(path.join(out, "cli", "LICENSE")));
    assert.ok(fs.existsSync(path.join(out, "cli", "README.md")));

    const win = JSON.parse(fs.readFileSync(path.join(out, "cli-win32-x64", "package.json"), "utf8"));
    assert.deepEqual(win.os, ["win32"]);
    assert.deepEqual(win.cpu, ["x64"]);
    assert.equal(win.version, "1.2.3");
    assert.equal(win.bin, undefined, "platform packages must not install their own audd command");
    assert.equal(
      fs.readFileSync(path.join(out, "cli-win32-x64", "bin", "audd.exe"), "utf8"),
      "binary for windows/amd64",
    );
    if (process.platform !== "win32") {
      const mode = fs.statSync(path.join(out, "cli-linux-x64", "bin", "audd")).mode;
      assert.ok(mode & 0o111, mode.toString(8));
    }
  } finally {
    fs.rmSync(root, { recursive: true, force: true });
  }
});

test("build takes an explicit version and packs tarballs", () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "audd-npm-"));
  try {
    const dist = fakeDist(root);
    const out = path.join(root, "npm");
    const res = build({ dist, out, version: "v2.0.0-rc.1", pack: true });
    assert.equal(res.version, "2.0.0-rc.1");
    assert.equal(res.tarballs.length, 7);
    for (const t of res.tarballs) assert.ok(fs.existsSync(t), t);
    const list = execFileSync("tar", ["-tzf", res.tarballs.find((t) => t.includes("audd-cli-linux-x64"))], {
      encoding: "utf8",
    });
    assert.match(list, /package\/bin\/audd/);
  } finally {
    fs.rmSync(root, { recursive: true, force: true });
  }
});

test("build fails when a platform binary is missing", () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "audd-npm-"));
  try {
    const dist = fakeDist(root);
    const artifacts = JSON.parse(fs.readFileSync(path.join(dist, "artifacts.json"), "utf8"));
    fs.writeFileSync(
      path.join(dist, "artifacts.json"),
      JSON.stringify(artifacts.filter((a) => a.goos !== "darwin")),
    );
    assert.throws(() => build({ dist, out: path.join(root, "npm"), pack: false }), /darwin/);
  } finally {
    fs.rmSync(root, { recursive: true, force: true });
  }
});
