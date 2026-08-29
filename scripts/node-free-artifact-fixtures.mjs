import { mkdirSync, symlinkSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

export const FIXTURES = [
  {
    name: 'clean-helper-bundle',
    wantPass: true,
    build(root) {
      mkdirSync(join(root, 'Contents', 'Helpers'), { recursive: true });
      writeFileSync(join(root, 'Contents', 'Helpers', 'anypick-core'), 'elf');
      writeFileSync(join(root, 'Contents', 'Info.plist'), '<plist></plist>\n');
    },
  },
  {
    name: 'stale-plugin-host',
    wantPass: false,
    build(root) {
      mkdirSync(join(root, 'Contents', 'Resources', 'plugin-host'), { recursive: true });
      writeFileSync(
        join(root, 'Contents', 'Resources', 'plugin-host', 'plugin-host.mjs'),
        'export {}\n',
      );
    },
  },
  {
    name: 'forbidden-filename-node',
    wantPass: false,
    build(root) {
      mkdirSync(join(root, 'Helpers'), { recursive: true });
      writeFileSync(join(root, 'Helpers', 'node'), '#!/bin/sh\n');
    },
  },
  {
    name: 'forbidden-filename-npm',
    wantPass: false,
    build(root) {
      mkdirSync(join(root, 'Helpers'), { recursive: true });
      writeFileSync(join(root, 'Helpers', 'npm'), '#!/bin/sh\n');
    },
  },
  {
    name: 'forbidden-filename-pnpm',
    wantPass: false,
    build(root) {
      mkdirSync(join(root, 'Helpers'), { recursive: true });
      writeFileSync(join(root, 'Helpers', 'pnpm'), '#!/bin/sh\n');
    },
  },
  {
    name: 'forbidden-filename-tsx',
    wantPass: false,
    build(root) {
      mkdirSync(join(root, 'Helpers'), { recursive: true });
      writeFileSync(join(root, 'Helpers', 'tsx'), '#!/bin/sh\n');
    },
  },
  {
    name: 'node-shebang',
    wantPass: false,
    build(root) {
      writeFileSync(join(root, 'launch'), '#!/usr/bin/env node\nconsole.log(1)\n');
    },
  },
  {
    name: 'dev-path-early',
    wantPass: false,
    build(root) {
      writeFileSync(join(root, 'Info.plist'), 'helper=/Users/erik/workspace/lab/js/hotpug/go\n');
    },
  },
  {
    name: 'dev-path-after-4096',
    wantPass: false,
    build(root) {
      writeFileSync(
        join(root, 'notes.txt'),
        `${'x'.repeat(4100)}\n/Users/erik/workspace/lab/js/hotpug\n`,
      );
    },
  },
  {
    name: 'external-symlink',
    wantPass: false,
    build(root) {
      symlinkSync('/bin/sh', join(root, 'escape'));
    },
  },
  {
    name: 'dangling-symlink',
    wantPass: false,
    build(root) {
      symlinkSync(join(root, 'missing-target'), join(root, 'dangling'));
    },
  },
];
