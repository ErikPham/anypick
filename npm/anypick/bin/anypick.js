#!/usr/bin/env node

"use strict";

const crypto = require("node:crypto");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const { spawn, spawnSync } = require("node:child_process");
const https = require("node:https");

const packageInfo = require("../package.json");
const releaseRepository = process.env.ANYPICK_RELEASE_REPOSITORY || "outof0/anypick";
const releaseBase = `https://github.com/${releaseRepository}/releases/download/v${packageInfo.version}`;

function targetForHost() {
  const architectures = {
    x64: "amd64",
    arm64: "arm64",
  };
  const goarch = architectures[process.arch];
  if (!goarch) {
    throw new Error(`AnyPick does not publish an npm binary for ${process.platform}/${process.arch}.`);
  }
  const platforms = {
    darwin: "darwin",
    linux: "linux",
    win32: "windows",
  };
  const goos = platforms[process.platform];
  if (!goos) {
    throw new Error(`AnyPick does not publish an npm binary for ${process.platform}.`);
  }
  return `${goos}-${goarch}`;
}

function cacheRoot() {
  if (process.env.ANYPICK_CACHE_DIR) {
    return path.resolve(process.env.ANYPICK_CACHE_DIR);
  }
  if (process.env.XDG_CACHE_HOME && process.platform !== "win32") {
    return path.join(process.env.XDG_CACHE_HOME, "anypick");
  }
  if (process.platform === "win32") {
    return path.join(process.env.LOCALAPPDATA || path.join(os.homedir(), "AppData", "Local"), "AnyPick");
  }
  return path.join(os.homedir(), ".cache", "anypick");
}

function isExecutable(file) {
  try {
    const mode = fs.statSync(file).mode;
    fs.accessSync(file, process.platform === "win32" ? fs.constants.F_OK : fs.constants.X_OK);
    return fs.statSync(file).isFile() && (process.platform === "win32" || (mode & 0o111) !== 0);
  } catch {
    return false;
  }
}

function request(urlString, redirects = 0) {
  if (redirects > 5) {
    return Promise.reject(new Error("too many redirects while downloading the AnyPick CLI"));
  }
  const url = new URL(urlString);
  if (url.protocol !== "https:") {
    return Promise.reject(new Error(`refusing non-HTTPS release URL: ${url.protocol}`));
  }
  return new Promise((resolve, reject) => {
    const requestHandle = https.get(url, (response) => {
      const location = response.headers.location;
      if (location && response.statusCode >= 300 && response.statusCode < 400) {
        response.resume();
        request(new URL(location, url).toString(), redirects + 1).then(resolve, reject);
        return;
      }
      if (response.statusCode !== 200) {
        response.resume();
        reject(new Error(`download failed with HTTP ${response.statusCode} for ${url}`));
        return;
      }
      const chunks = [];
      response.on("data", (chunk) => chunks.push(chunk));
      response.on("end", () => resolve(Buffer.concat(chunks)));
      response.on("error", reject);
    });
    requestHandle.on("error", reject);
  });
}

function run(command, args) {
  const result = spawnSync(command, args, {
    encoding: "utf8",
    stdio: ["ignore", "pipe", "pipe"],
    windowsHide: true,
  });
  if (result.error) {
    throw result.error;
  }
  if (result.status !== 0) {
    const detail = (result.stderr || result.stdout || "").trim();
    throw new Error(`${command} failed${detail ? `: ${detail}` : ""}`);
  }
}

function powershellLiteral(value) {
  return `'${value.replaceAll("'", "''")}'`;
}

function extractArchive(archive, destination, isZip) {
  try {
    run("tar", isZip ? ["-xf", archive, "-C", destination] : ["-xzf", archive, "-C", destination]);
    return;
  } catch (tarError) {
    if (!isZip || process.platform !== "win32") {
      throw tarError;
    }
    run("powershell.exe", [
      "-NoLogo",
      "-NoProfile",
      "-NonInteractive",
      "-Command",
      `Expand-Archive -LiteralPath ${powershellLiteral(archive)} -DestinationPath ${powershellLiteral(destination)} -Force`,
    ]);
  }
}

function findFile(root, fileName) {
  for (const entry of fs.readdirSync(root, { withFileTypes: true })) {
    const candidate = path.join(root, entry.name);
    if (entry.isFile() && entry.name === fileName) {
      return candidate;
    }
    if (entry.isDirectory()) {
      const nested = findFile(candidate, fileName);
      if (nested) {
        return nested;
      }
    }
  }
  return undefined;
}

function expectedSha256(checksums, asset) {
  const line = checksums
    .toString("utf8")
    .split(/\r?\n/)
    .map((item) => item.trim())
    .find((item) => item.endsWith(`  ${asset}`) || item.endsWith(` *${asset}`));
  const match = line && line.match(/^([a-fA-F0-9]{64})\s+\*?\S+$/);
  if (!match) {
    throw new Error(`release checksums do not contain ${asset}`);
  }
  return match[1].toLowerCase();
}

async function downloadBinary(target, destination) {
  const executable = process.platform === "win32" ? "anypick.exe" : "anypick";
  const isZip = process.platform === "win32";
  const archive = `anypick-${packageInfo.version}-${target}${isZip ? ".zip" : ".tar.gz"}`;
  const archiveBytes = await request(`${releaseBase}/${archive}`);
  const checksums = await request(`${releaseBase}/SHA256SUMS.txt`);
  const actual = crypto.createHash("sha256").update(archiveBytes).digest("hex");
  const expected = expectedSha256(checksums, archive);
  if (actual !== expected) {
    throw new Error(`checksum mismatch for ${archive}: expected ${expected}, got ${actual}`);
  }

  const temporaryRoot = fs.mkdtempSync(path.join(cacheRoot(), `.download-${process.pid}-`));
  const archivePath = path.join(temporaryRoot, archive);
  const extractedRoot = path.join(temporaryRoot, "extracted");
  try {
    fs.mkdirSync(extractedRoot, { recursive: true });
    fs.writeFileSync(archivePath, archiveBytes, { mode: 0o600 });
    extractArchive(archivePath, extractedRoot, isZip);
    const extractedBinary = findFile(extractedRoot, executable);
    if (!extractedBinary) {
      throw new Error(`release archive ${archive} did not contain ${executable}`);
    }
    const temporaryBinary = `${destination}.tmp-${process.pid}`;
    fs.copyFileSync(extractedBinary, temporaryBinary);
    if (process.platform !== "win32") {
      fs.chmodSync(temporaryBinary, 0o755);
    }
    try {
      fs.renameSync(temporaryBinary, destination);
    } catch (error) {
      if (isExecutable(destination)) {
        fs.rmSync(temporaryBinary, { force: true });
      } else {
        throw error;
      }
    }
  } finally {
    fs.rmSync(temporaryRoot, { recursive: true, force: true });
  }
}

async function resolveBinary() {
  if (process.env.ANYPICK_BIN) {
    const override = path.resolve(process.env.ANYPICK_BIN);
    if (!isExecutable(override)) {
      throw new Error(`ANYPICK_BIN is not an executable file: ${override}`);
    }
    return override;
  }
  const target = targetForHost();
  const executable = process.platform === "win32" ? "anypick.exe" : "anypick";
  const destination = path.join(cacheRoot(), packageInfo.version, target, executable);
  if (isExecutable(destination)) {
    return destination;
  }
  fs.mkdirSync(path.dirname(destination), { recursive: true, mode: 0o700 });
  try {
    await downloadBinary(target, destination);
  } catch (error) {
    throw new Error(
      `could not install AnyPick ${packageInfo.version} for ${target}: ${error.message}\n` +
        `The release asset is ${releaseBase}/${`anypick-${packageInfo.version}-${target}${process.platform === "win32" ? ".zip" : ".tar.gz"}`}.`,
    );
  }
  return destination;
}

async function main() {
  const binary = await resolveBinary();
  const child = spawn(binary, process.argv.slice(2), {
    stdio: "inherit",
    windowsHide: false,
  });
  for (const signal of ["SIGINT", "SIGTERM", "SIGHUP"]) {
    process.on(signal, () => child.kill(signal));
  }
  child.on("error", (error) => {
    console.error(`failed to start AnyPick: ${error.message}`);
    process.exitCode = 1;
  });
  child.on("exit", (code, signal) => {
    if (signal && process.platform !== "win32") {
      process.kill(process.pid, signal);
      return;
    }
    process.exitCode = code === null ? 1 : code;
  });
}

main().catch((error) => {
  console.error(`AnyPick: ${error.message}`);
  process.exitCode = 1;
});
