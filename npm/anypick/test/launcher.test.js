"use strict";

const assert = require("node:assert/strict");
const { spawnSync } = require("node:child_process");
const test = require("node:test");
const path = require("node:path");

const launcher = path.resolve(__dirname, "..", "bin", "anypick.js");

function runWithLocalBinary(args) {
  return spawnSync(process.execPath, [launcher, ...args], {
    encoding: "utf8",
    env: { ...process.env, ANYPICK_BIN: process.execPath },
  });
}

test("forwards arguments and exit status to an overridden Go binary", () => {
  const result = runWithLocalBinary(["-e", "process.stdout.write(process.argv.slice(1).join('|')); process.exit(7)", "one", "two"]);
  assert.equal(result.stdout, "one|two");
  assert.equal(result.status, 7);
  assert.equal(result.stderr, "");
});
