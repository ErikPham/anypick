import { mkdtempSync, rmSync } from 'node:fs';
import { spawnSync } from 'node:child_process';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';
import assert from 'node:assert/strict';

import { inspectNodeFreeArtifact } from './check-node-free-artifact.mjs';
import { FIXTURES } from './node-free-artifact-fixtures.mjs';

const repoRoot = dirname(dirname(fileURLToPath(import.meta.url)));
const shellScanner = join(repoRoot, 'src/macos/scripts/check-node-free-app.sh');

function materialize(build) {
  const root = mkdtempSync(join(tmpdir(), 'anypick-node-free-'));
  build(root);
  return root;
}

function runShell(root) {
  return spawnSync('bash', [shellScanner, root], { encoding: 'utf8' });
}

for (const fixture of FIXTURES) {
  test(`JS scanner: ${fixture.name}`, () => {
    const root = materialize(fixture.build);
    try {
      const violations = inspectNodeFreeArtifact(root);
      if (fixture.wantPass) {
        assert.deepEqual(violations, []);
      } else {
        assert.notEqual(violations.length, 0, `${fixture.name} was accepted by the JS scanner`);
      }
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });

  test(`shell scanner: ${fixture.name}`, () => {
    const root = materialize(fixture.build);
    try {
      const result = runShell(root);
      if (fixture.wantPass) {
        assert.equal(result.status, 0, result.stderr);
      } else {
        assert.notEqual(
          result.status,
          0,
          `${fixture.name} was accepted by the production shell scanner\n${result.stdout}${result.stderr}`,
        );
      }
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });
}

test('JS and shell scanners agree on every shared fixture', () => {
  const disagreements = [];
  for (const fixture of FIXTURES) {
    const root = materialize(fixture.build);
    try {
      const jsFail = inspectNodeFreeArtifact(root).length > 0;
      const shellFail = runShell(root).status !== 0;
      if (jsFail !== shellFail) {
        disagreements.push(
          `${fixture.name}: js=${jsFail ? 'fail' : 'pass'} shell=${shellFail ? 'fail' : 'pass'}`,
        );
      }
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  }
  assert.deepEqual(disagreements, []);
});
