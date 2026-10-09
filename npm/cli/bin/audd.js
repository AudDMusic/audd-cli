#!/usr/bin/env node
// Runs the audd binary from the @audd/cli-<os>-<cpu> package that npm
// installed for this system.
"use strict";

const { spawn } = require("child_process");

const pkg = `@audd/cli-${process.platform}-${process.arch}`;
const exe = process.platform === "win32" ? "audd.exe" : "audd";

let bin;
try {
  bin = require.resolve(`${pkg}/bin/${exe}`);
} catch (e) {
  const supported = ["darwin-arm64", "darwin-x64", "linux-arm64", "linux-x64", "win32-arm64", "win32-x64"];
  if (!supported.includes(`${process.platform}-${process.arch}`)) {
    console.error(`audd: there is no build for ${process.platform}/${process.arch}.`);
    console.error("Install from source with: go install github.com/AudDMusic/audd-cli/cmd/audd@latest");
  } else {
    console.error(`audd: the package ${pkg} is missing. It holds the audd binary for this system.`);
    console.error("npm skips it when optional dependencies are turned off (--omit=optional or --no-optional).");
    console.error("Reinstall with optional dependencies, for example: npm install -g @audd/cli");
  }
  process.exit(1);
}

const child = spawn(bin, process.argv.slice(2), { stdio: "inherit" });

// Ctrl-C reaches audd from the terminal directly, so audd can stop a batch
// cleanly; this wrapper only waits for it. Other signals are passed on.
process.on("SIGINT", () => {});
for (const sig of ["SIGTERM", "SIGHUP"]) {
  process.on(sig, () => {
    try {
      child.kill(sig);
    } catch (e) {
      // audd has already exited.
    }
  });
}

child.on("error", (err) => {
  console.error(`audd: cannot run ${bin}: ${err.message}`);
  process.exit(1);
});

child.on("exit", (code, signal) => {
  if (signal) {
    process.removeAllListeners(signal);
    process.kill(process.pid, signal);
    return;
  }
  process.exit(code === null ? 1 : code);
});
